package reconciler

import (
	"errors"
	"maps"
	"reflect"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestSpecOfReadsTheJSONNames is the premise the field map rests on: the engine reads a
// spec it knows nothing about through its JSON form, so the names it sees are exactly the
// names a user writes in YAML and registry.KeyField.Spec names.
func TestSpecOfReadsTheJSONNames(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ObjectTypes = []string{"dcim.device"}
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	spec, err := specOf(obj)
	if err != nil {
		t.Fatalf("specOf() = %v", err)
	}

	want := []string{"color", "endpointRef", "name", "objectTypes", "parentRef", "slug"}
	if got := slices.Sorted(maps.Keys(spec)); !slices.Equal(got, want) {
		t.Fatalf("spec fields = %v, want %v", got, want)
	}
}

func TestSpecFieldsDesired(t *testing.T) {
	tests := []struct {
		name         string
		spec         specFields
		descriptor   registry.Descriptor
		wantPayload  netbox.Object
		wantDeclared []string
		wantResolved []string
		wantRefs     []string
		wantErr      error
	}{
		{
			name: "scalars are written under their api names",
			spec: specFields{"name": "Managed", "slug": "managed", "weight": float64(500)},
			wantPayload: netbox.Object{
				"name": "Managed", "slug": "managed", "weight": float64(500),
			},
			wantDeclared: []string{"name", "slug", "weight"},
			wantResolved: []string{"name", "slug", "weight"},
		},
		{
			name:         "the engine's own spec fields are never sent",
			spec:         specFields{"endpointRef": "homelab", "onConflict": "Adopt", "slug": "managed"},
			wantPayload:  netbox.Object{"slug": "managed"},
			wantDeclared: []string{"slug"},
			wantResolved: []string{"slug"},
		},
		{
			name:         "an unset field is left to netbox rather than nulled",
			spec:         specFields{"slug": "managed", "color": nil},
			wantPayload:  netbox.Object{"slug": "managed"},
			wantDeclared: []string{"slug"},
			wantResolved: []string{"slug"},
		},
		{
			name: "a list is written but is not something a filter can match",
			spec: specFields{"objectTypes": []any{"dcim.device"}},
			wantPayload: netbox.Object{
				"object_types": []any{"dcim.device"},
			},
			wantDeclared: []string{"objectTypes"},
		},
		{
			// The M1 contract from NBO-009: declared, accepted, left out, and reported.
			name:         "a reference is reported rather than serialised",
			spec:         specFields{"slug": "managed", "parentRef": map[string]any{"name": "europe"}},
			wantPayload:  netbox.Object{"slug": "managed"},
			wantDeclared: []string{"parentRef", "slug"},
			wantResolved: []string{"slug"},
			wantRefs:     []string{"parentRef"},
		},
		{
			name:       "a polymorphic reference is reported too",
			spec:       specFields{"slug": "managed", "scope": map[string]any{"name": "home"}},
			descriptor: scopedDescriptor(),
			// scope_type and scope_id are one reference written from one spec field, so the
			// pair is declared on the generic FK rather than in the field map.
			wantPayload:  netbox.Object{"slug": "managed"},
			wantDeclared: []string{"scope", "slug"},
			wantResolved: []string{"slug"},
			wantRefs:     []string{"scope"},
		},
		{
			name:    "a spec field with no mapping is an error, not a dropped field",
			spec:    specFields{"slug": "managed", "unmapped": "surprise"},
			wantErr: errUnmappedField,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := tc.descriptor
			if descriptor.GVK.Empty() {
				descriptor = fakeDescriptor()
			}

			payload, state, refs, err := tc.spec.desired(descriptor)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("desired() = %v, want %v", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("desired() = %v, want no error", err)
			}

			if !reflect.DeepEqual(payload, tc.wantPayload) {
				t.Errorf("payload = %v, want %v", payload, tc.wantPayload)
			}

			if !slices.Equal(state.Declared, tc.wantDeclared) {
				t.Errorf("declared = %v, want %v", state.Declared, tc.wantDeclared)
			}

			if !slices.Equal(state.Resolved, tc.wantResolved) {
				t.Errorf("resolved = %v, want %v", state.Resolved, tc.wantResolved)
			}

			if !slices.Equal(refs, tc.wantRefs) && len(refs)+len(tc.wantRefs) > 0 {
				t.Errorf("refs = %v, want %v", refs, tc.wantRefs)
			}
		})
	}
}

func TestSpecFieldsParams(t *testing.T) {
	spec := specFields{"name": "DNS", "slug": "managed", "vid": float64(4094)}

	tests := []struct {
		name    string
		key     registry.NaturalKey
		want    netbox.Params
		wantErr error
	}{
		{
			name: "an exact match is the bare filter",
			key:  registry.NaturalKey{Fields: []registry.KeyField{{Filter: "slug", Spec: "slug"}}},
			want: netbox.Params{"slug": "managed"},
		},
		{
			name: "a case-insensitive match carries the modifier",
			key: registry.NaturalKey{Fields: []registry.KeyField{
				{Filter: "name", Spec: "name", Lookup: registry.LookupIExact},
			}},
			want: netbox.Params{"name__ie": "DNS"},
		},
		{
			name: "a number is rendered without a fraction netbox would reject",
			key:  registry.NaturalKey{Fields: []registry.KeyField{{Filter: "vid", Spec: "vid"}}},
			want: netbox.Params{"vid": "4094"},
		},
		{
			name: "a null pin is sent rather than omitted",
			key: registry.NaturalKey{
				Fields:     []registry.KeyField{{Filter: "slug", Spec: "slug"}},
				NullFields: []registry.NullField{{Filter: "parent_id", Spec: "parentRef"}},
			},
			want: netbox.Params{"slug": "managed", "parent_id__isnull": "true"},
		},
		{
			name:    "a filter with no value is refused rather than omitted",
			key:     registry.NaturalKey{Fields: []registry.KeyField{{Filter: "rd", Spec: "rd"}}},
			wantErr: errUnfilterable,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			params, err := spec.params(tc.key)

			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("params() = %v, want %v", err, tc.wantErr)
				}

				return
			}

			if err != nil {
				t.Fatalf("params() = %v, want no error", err)
			}

			if !reflect.DeepEqual(params, tc.want) {
				t.Errorf("params = %v, want %v", params, tc.want)
			}
		})
	}
}

func TestFilterValue(t *testing.T) {
	tests := []struct {
		name   string
		value  any
		want   string
		wantOK bool
	}{
		{name: "string", value: "managed", want: "managed", wantOK: true},
		{name: "empty string is not an identity", value: ""},
		{name: "integral number", value: float64(4094), want: "4094", wantOK: true},
		{name: "decimal number", value: float64(1.5), want: "1.5", wantOK: true},
		{name: "bool", value: true, want: "true", wantOK: true},
		{name: "list", value: []any{"dcim.device"}},
		{name: "reference", value: map[string]any{"name": "europe"}},
		{name: "unset", value: nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := filterValue(tc.value)

			if got != tc.want || ok != tc.wantOK {
				t.Fatalf("filterValue(%v) = %q, %v; want %q, %v", tc.value, got, ok, tc.want, tc.wantOK)
			}
		})
	}
}

// TestFieldRulesFromDescriptor checks the translation Drift depends on. A class the engine
// fails to pass through is a comparison that never converges, which is a PATCH loop rather
// than an error (docs/concepts/drift.md).
func TestFieldRulesFromDescriptor(t *testing.T) {
	d := scopedDescriptor()
	d.M2M = []string{"tags"}
	d.Arrays = []string{"vid_ranges"}

	rules := fieldRules(d)

	if !rules.M2M["tags"] || !rules.ObjectTypeLists["object_types"] || !rules.Arrays["vid_ranges"] {
		t.Errorf("field classes = %+v, want tags m2m, object_types an object-type list, vid_ranges an array", rules)
	}

	want := []netbox.GenericFK{{TypeField: "scope_type", IDField: "scope_id"}}
	if !reflect.DeepEqual(rules.GenericFKs, want) {
		t.Errorf("generic FKs = %v, want %v", rules.GenericFKs, want)
	}
}

// TestEnvelopeFieldsAreDerived guards the reflection: the engine excludes the envelope's
// fields from every payload by reading them off the struct, so a field added to the
// envelope cannot leak into NetBox as an unknown column.
func TestEnvelopeFieldsAreDerived(t *testing.T) {
	for _, name := range []string{"endpointRef", "onConflict", "deletionPolicy"} {
		if !envelopeFields[name] {
			t.Errorf("envelopeFields is missing %q", name)
		}
	}

	if len(envelopeFields) != reflect.TypeFor[netboxv1alpha1.NetBoxObjectSpec]().NumField() {
		t.Errorf("envelopeFields = %v, want one entry per field of NetBoxObjectSpec", envelopeFields)
	}
}

// scopedDescriptor is the fake kind with a polymorphic scope, like ipam.Prefix since
// NetBox 4.2 (docs/decisions/0003-ownership-and-references.md rule 2).
func scopedDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.GenericFKs = []registry.GenericFKSpec{{
		TypeField:    "scope_type",
		IDField:      "scope_id",
		AllowedTypes: []string{"dcim.site", "dcim.region"},
		Spec:         "scope",
	}}
	d.ContainmentRef = "scope"

	return d
}
