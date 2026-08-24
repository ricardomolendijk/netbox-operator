package registry

import (
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

func TestKeyFieldParam(t *testing.T) {
	tests := []struct {
		name  string
		field KeyField
		want  string
	}{
		{
			name:  "exact match is the bare filter",
			field: KeyField{Filter: "slug", Spec: "slug"},
			want:  "slug",
		},
		{
			name:  "case-insensitive match carries the modifier",
			field: KeyField{Filter: "name", Spec: "name", Lookup: LookupIExact},
			want:  "name__ie",
		},
		{
			name:  "foreign keys filter on the id column",
			field: KeyField{Filter: "site_id", Spec: "siteRef"},
			want:  "site_id",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.field.Param(); got != tc.want {
				t.Fatalf("Param() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNullPinRendersPerColumnClass is the registry half of #206: a pin declares its
// column's filter class and the client turns that into whichever parameter NetBox
// registers. The strings themselves, and the NetBox source behind each, are pinned in
// internal/netbox (TestNullPinSpellingPerColumnType); this checks that a declaration
// reaches them.
func TestNullPinRendersPerColumnClass(t *testing.T) {
	tests := map[string]struct {
		field NullField
		want  netbox.Params
	}{
		"foreign key": {
			field: NullField{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef},
			want:  netbox.Params{"vrf_id": "null"},
		},
		"char column": {
			field: NullField{Filter: "rd", Spec: "rd", Column: NullColumnChar},
			want:  netbox.Params{"rd": "null"},
		},
		"numeric column": {
			field: NullField{Filter: "scope_id", Spec: "scope", Column: NullColumnNumeric},
			want:  netbox.Params{"scope_id__empty": "true"},
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := netbox.Params{}.Null(tc.field.Filter, netbox.NullColumn(tc.field.Column))
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Null(%q, %q) = %v, want %v", tc.field.Filter, tc.field.Column, got, tc.want)
			}
		})
	}
}

// TestNullPinWithNoColumnClassIsRejected keeps the zero value from becoming a default. Every
// spelling is wrong for some column, so a pin that does not say which class it targets has
// to fail the boot rather than pick one.
func TestNullPinWithNoColumnClassIsRejected(t *testing.T) {
	key := NaturalKey{
		Fields: []KeyField{{Filter: "name", Spec: "name"}},
		// Deliberately no Column: this fixture proves the zero value is not a default.
		// Do not "fix" it by declaring one.
		NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef"}},
	}
	if err := key.Validate(); !errors.Is(err, ErrUnknownNullColumn) {
		t.Fatalf("Validate() = %v, want ErrUnknownNullColumn", err)
	}
}

// params is every query parameter a candidate sends, value-matched fields first.
//
// A value-matched field is its parameter name; a null pin is the full `param=value` pair,
// rendered by the client that actually sends it. Both halves matter. A ref pin's parameter
// is the bare column name -- `?parent_id=null` -- so without the value a pin and a value
// match are the same string and this test could not tell "no parent" from "some parent".
// And going through netbox.Params means these expectations are the wire itself rather than
// a second opinion about it, which is how #206 stayed invisible.
func params(k NaturalKey) []string {
	out := make([]string, 0, len(k.Fields)+len(k.NullFields))

	for _, field := range k.Fields {
		out = append(out, field.Param())
	}

	for _, field := range k.NullFields {
		pin := netbox.Params{}.Null(field.Filter, netbox.NullColumn(field.Column))
		for param, value := range pin {
			out = append(out, param+"="+value)
		}
	}

	return out
}

func TestDescriptorCandidates(t *testing.T) {
	tests := []struct {
		name       string
		descriptor Descriptor
		state      SpecState
		want       [][]string
	}{
		{
			name:       "VRF with an rd tries rd before name",
			descriptor: vrfDescriptor(),
			state:      SpecState{Declared: []string{"name", "rd"}, Resolved: []string{"name", "rd"}},
			want:       [][]string{{"rd"}, {"name"}},
		},
		{
			name:       "VRF without an rd keys on name alone",
			descriptor: vrfDescriptor(),
			state:      SpecState{Declared: []string{"name"}, Resolved: []string{"name"}},
			want:       [][]string{{"name"}},
		},
		{
			name:       "child Region keys on its parent, never on the null variant",
			descriptor: regionDescriptor(),
			state:      SpecState{Declared: []string{"name", "parentRef"}, Resolved: []string{"name", "parentRef"}},
			want:       [][]string{{"parent_id", "name"}},
		},
		{
			name:       "top-level Region pins parent_id to null",
			descriptor: regionDescriptor(),
			state:      SpecState{Declared: []string{"name"}, Resolved: []string{"name"}},
			want:       [][]string{{"name", "parent_id=null"}},
		},
		{
			name:       "Region with an unresolved parent has no candidate at all",
			descriptor: regionDescriptor(),
			state:      SpecState{Declared: []string{"name", "parentRef"}, Resolved: []string{"name"}},
			want:       nil,
		},
		{
			name:       "Device with a tenant matches the name case-insensitively",
			descriptor: deviceDescriptor(),
			state: SpecState{
				Declared: []string{"name", "siteRef", "tenantRef"},
				Resolved: []string{"name", "siteRef", "tenantRef"},
			},
			want: [][]string{{"name__ie", "site_id", "tenant_id"}},
		},
		{
			name:       "Device without a tenant pins tenant_id to null",
			descriptor: deviceDescriptor(),
			state:      SpecState{Declared: []string{"name", "siteRef"}, Resolved: []string{"name", "siteRef"}},
			want:       [][]string{{"name__ie", "site_id", "tenant_id=null"}},
		},
		{
			name:       "assigned address in a VRF disambiguates on the assignment first",
			descriptor: ipAddressDescriptor(),
			state: SpecState{
				Declared: []string{"address", "vrfRef", "assignedObject"},
				Resolved: []string{"address", "vrfRef", "assignedObject"},
			},
			want: [][]string{
				{"address", "vrf_id", "assigned_object_type", "assigned_object_id"},
				{"address", "vrf_id"},
			},
		},
		{
			name:       "assigned global address pins vrf_id to null in every candidate",
			descriptor: ipAddressDescriptor(),
			state: SpecState{
				Declared: []string{"address", "assignedObject"},
				Resolved: []string{"address", "assignedObject"},
			},
			want: [][]string{
				{"address", "assigned_object_type", "assigned_object_id", "vrf_id=null"},
				{"address", "vrf_id=null"},
			},
		},
		{
			name:       "unassigned address in a VRF never matches across VRFs",
			descriptor: ipAddressDescriptor(),
			state:      SpecState{Declared: []string{"address", "vrfRef"}, Resolved: []string{"address", "vrfRef"}},
			want:       [][]string{{"address", "vrf_id"}},
		},
		{
			name:       "unassigned global address",
			descriptor: ipAddressDescriptor(),
			state:      SpecState{Declared: []string{"address"}, Resolved: []string{"address"}},
			want:       [][]string{{"address", "vrf_id=null"}},
		},
		{
			name:       "nested group with no parent constraint keys on slug regardless",
			descriptor: tenantGroupDescriptor(),
			state: SpecState{
				Declared: []string{"name", "slug", "parentRef"},
				Resolved: []string{"name", "slug", "parentRef"},
			},
			want: [][]string{{"slug"}},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			candidates := tc.descriptor.Candidates(tc.state)

			if len(candidates) != len(tc.want) {
				t.Fatalf("Candidates() returned %d candidates, want %d", len(candidates), len(tc.want))
			}

			for i, candidate := range candidates {
				if got := params(candidate); !slices.Equal(got, tc.want[i]) {
					t.Fatalf("candidate %d params = %v, want %v", i, got, tc.want[i])
				}
			}
		})
	}
}

func TestNaturalKeyValidate(t *testing.T) {
	tests := []struct {
		name    string
		key     NaturalKey
		wantErr error
	}{
		{
			name: "value field and a null pin",
			key: NaturalKey{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},
		{
			name:    "no value field",
			key:     NaturalKey{NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}}},
			wantErr: ErrNoKeyFields,
		},
		{
			name:    "field with no filter",
			key:     NaturalKey{Fields: []KeyField{{Spec: "name"}}},
			wantErr: ErrEmptyFilter,
		},
		{
			name:    "field with no spec name",
			key:     NaturalKey{Fields: []KeyField{{Filter: "name"}}},
			wantErr: ErrEmptyFilter,
		},
		{
			name:    "substring lookup",
			key:     NaturalKey{Fields: []KeyField{{Filter: "name", Spec: "name", Lookup: "ic"}}},
			wantErr: ErrUnknownLookup,
		},
		{
			name: "null pin with no spec name",
			key: NaturalKey{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id"}},
			},
			wantErr: ErrEmptyFilter,
		},
		{
			name: "filter both matched and pinned",
			key: NaturalKey{
				Fields:     []KeyField{{Filter: "vrf_id", Spec: "vrfRef"}},
				NullFields: []NullField{{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef}},
			},
			wantErr: ErrNullFieldConflict,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.key.Validate()

			if tc.wantErr == nil && err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}
