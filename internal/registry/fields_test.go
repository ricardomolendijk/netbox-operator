package registry

import (
	"errors"
	"testing"
)

func TestDescriptorFieldFor(t *testing.T) {
	device := deviceDescriptor()

	tests := []struct {
		name    string
		spec    string
		wantAPI string
		wantRef bool
		wantOK  bool
	}{
		{name: "scalar", spec: "name", wantAPI: "name", wantOK: true},
		{name: "reference drops the Ref suffix", spec: "siteRef", wantAPI: "site", wantRef: true, wantOK: true},
		{
			// The case a camelCase-to-snake_case convention gets wrong, and the reason
			// the mapping is a table.
			name: "acronym in the middle of a name", spec: "primaryIP4Ref",
			wantAPI: "primary_ip4", wantRef: true, wantOK: true,
		},
		{name: "unmapped", spec: "comments"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			field, ok := device.FieldFor(tc.spec)

			if ok != tc.wantOK {
				t.Fatalf("FieldFor(%q) ok = %v, want %v", tc.spec, ok, tc.wantOK)
			}

			if field.API != tc.wantAPI || field.Ref != tc.wantRef {
				t.Fatalf("FieldFor(%q) = %+v, want api %q ref %v", tc.spec, field, tc.wantAPI, tc.wantRef)
			}
		})
	}
}

func TestDescriptorGenericFKFor(t *testing.T) {
	address := ipAddressDescriptor()

	generic, ok := address.GenericFKFor("assignedObject")
	if !ok {
		t.Fatal("GenericFKFor(assignedObject) = false, want true")
	}

	if generic.TypeField != "assigned_object_type" || generic.IDField != "assigned_object_id" {
		t.Fatalf("GenericFKFor(assignedObject) = %+v", generic)
	}

	if _, ok := address.GenericFKFor("vrfRef"); ok {
		t.Fatal("GenericFKFor(vrfRef) = true, want false: an ordinary reference is not a polymorphic pair")
	}
}

// TestDescriptorValidateFieldMap is the boot-time half of the spec-to-API bridge: every
// way a field map can be wrong, and every way something else can name a field it does not
// declare. Each of these would otherwise surface as a payload with a field NetBox ignores.
func TestDescriptorValidateFieldMap(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*Descriptor)
		wantErr error
	}{
		{
			name:    "no field map at all",
			mutate:  func(d *Descriptor) { d.Fields = nil },
			wantErr: ErrNoFields,
		},
		{
			name:    "entry with no api name",
			mutate:  func(d *Descriptor) { d.Fields = append(d.Fields, Field{Spec: "comments"}) },
			wantErr: ErrEmptyField,
		},
		{
			name:    "entry with no spec name",
			mutate:  func(d *Descriptor) { d.Fields = append(d.Fields, Field{API: "comments"}) },
			wantErr: ErrEmptyField,
		},
		{
			name:    "two entries for one spec field",
			mutate:  func(d *Descriptor) { d.Fields = append(d.Fields, Field{Spec: "name", API: "display_name"}) },
			wantErr: ErrDuplicateSpecField,
		},
		{
			name:    "two spec fields writing one api field",
			mutate:  func(d *Descriptor) { d.Fields = append(d.Fields, Field{Spec: "title", API: "name"}) },
			wantErr: ErrDuplicateAPIField,
		},
		{
			name: "spec field mapped onto a read-only column",
			// Writing a read-only column silently no-ops, so this is a PATCH loop rather
			// than an error unless it is caught at boot.
			mutate:  func(d *Descriptor) { d.Fields = append(d.Fields, Field{Spec: "displayName", API: "display"}) },
			wantErr: ErrFieldReadOnly,
		},
		{
			name: "non-reference field declaring a target kind",
			// Almost always a forgotten `Ref: true`. Left alone, the resolver ignores the
			// field and the engine writes the reference to NetBox verbatim.
			mutate: func(d *Descriptor) {
				d.Fields = append(d.Fields, Field{
					Spec: "tenant", API: "tenant", Target: testGVK("NetBoxTenant"),
				})
			},
			wantErr: ErrTargetNotRef,
		},
		{
			name: "natural key matches on an undeclared spec field",
			mutate: func(d *Descriptor) {
				d.NaturalKeys = append(d.NaturalKeys, NaturalKey{Fields: []KeyField{{Filter: "rd", Spec: "rd"}}})
			},
			wantErr: ErrUnknownSpecField,
		},
		{
			name: "null pin names an undeclared spec field",
			mutate: func(d *Descriptor) {
				d.NaturalKeys[0].NullFields = []NullField{{Filter: "tenant_id", Spec: "tenantRef"}}
			},
			wantErr: ErrUnknownSpecField,
		},
		{
			name:    "containment ref names an undeclared spec field",
			mutate:  func(d *Descriptor) { d.ContainmentRef = "siteRef" },
			wantErr: ErrUnknownSpecField,
		},
		{
			name:    "containment ref names a scalar",
			mutate:  func(d *Descriptor) { d.ContainmentRef = "name" },
			wantErr: ErrContainmentNotRef,
		},
		{
			name: "containment ref names a polymorphic pair",
			mutate: func(d *Descriptor) {
				d.GenericFKs = []GenericFKSpec{{
					TypeField: "scope_type", IDField: "scope_id",
					AllowedTypes: []string{"dcim.site"}, Spec: "scopeRef",
				}}
				d.ContainmentRef = "scopeRef"
			},
		},
		{
			name: "deferred field no reference writes",
			mutate: func(d *Descriptor) {
				d.Deferred = []DeferredField{{APIField: "primaryIp4", Mode: DeferAlways}}
			},
			wantErr: ErrDeferredNotRef,
		},
		{
			name: "deferred field that is a scalar",
			mutate: func(d *Descriptor) {
				d.Deferred = []DeferredField{{APIField: "color", Mode: DeferAlways}}
			},
			wantErr: ErrDeferredNotRef,
		},
		{
			name: "generic FK with no spec field behind it",
			mutate: func(d *Descriptor) {
				d.GenericFKs = []GenericFKSpec{{
					TypeField: "scope_type", IDField: "scope_id", AllowedTypes: []string{"dcim.site"},
				}}
			},
			wantErr: ErrGenericFKNotSpecField,
		},
		{
			name: "generic FK spec field is also an ordinary field",
			mutate: func(d *Descriptor) {
				d.Fields = append(d.Fields, Field{Spec: "scopeRef", API: "scope", Ref: true})
				d.GenericFKs = []GenericFKSpec{{
					TypeField: "scope_type", IDField: "scope_id",
					AllowedTypes: []string{"dcim.site"}, Spec: "scopeRef",
				}}
			},
			wantErr: ErrGenericFKNotSpecField,
		},
		{
			name: "generic FK column is also an ordinary field",
			mutate: func(d *Descriptor) {
				d.Fields = append(d.Fields, Field{Spec: "scopeType", API: "scope_type"})
				d.GenericFKs = []GenericFKSpec{{
					TypeField: "scope_type", IDField: "scope_id",
					AllowedTypes: []string{"dcim.site"}, Spec: "scopeRef",
				}}
			},
			wantErr: ErrGenericFKNotSpecField,
		},
		{
			name:    "array field that is also many-to-many",
			mutate:  func(d *Descriptor) { d.Arrays, d.M2M = []string{"object_types"}, []string{"object_types"} },
			wantErr: ErrFieldClassConflict,
		},
		{
			name:    "empty array field name",
			mutate:  func(d *Descriptor) { d.Arrays = []string{""} },
			wantErr: ErrEmptyField,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d := tagDescriptor()
			tc.mutate(&d)

			err := d.Validate()

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
