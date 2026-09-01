package registry

import (
	"errors"
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestValidateGenericFKList covers the to-many shape's boot check. Every row is a way the
// shape would be half-declared, and each is silent at runtime if it is not caught here: a list
// written under no field name reaches no payload, and NetBox answers a body it does not
// recognise with 201 rather than an error.
func TestValidateGenericFKList(t *testing.T) {
	for _, tc := range []struct {
		name    string
		list    *GenericFKList
		cached  []string
		wantErr error
	}{
		{
			name: "a well-formed to-many pair",
			list: &GenericFKList{APIField: "a_terminations", TypeKey: "object_type", IDKey: "object_id"},
		},
		{
			name:    "no api field",
			list:    &GenericFKList{TypeKey: "object_type", IDKey: "object_id"},
			wantErr: ErrInvalidGenericFKList,
		},
		{
			name:    "no type key",
			list:    &GenericFKList{APIField: "a_terminations", IDKey: "object_id"},
			wantErr: ErrInvalidGenericFKList,
		},
		{
			name:    "no id key",
			list:    &GenericFKList{APIField: "a_terminations", TypeKey: "object_type"},
			wantErr: ErrInvalidGenericFKList,
		},
		{
			// The caches a cable's terminations do have -- `_device`, `_rack`, `_location`,
			// `_site` -- are columns of dcim.CableTermination and not of the kind carrying the
			// list, so there is nowhere for the engine to read or write them.
			name:    "a to-many pair declaring caches",
			list:    &GenericFKList{APIField: "a_terminations", TypeKey: "object_type", IDKey: "object_id"},
			cached:  []string{"_device"},
			wantErr: ErrInvalidGenericFKList,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := tagDescriptor()
			d.ReadOnly = append(d.ReadOnly, "_device")
			d.GenericFKs = []GenericFKSpec{{
				TypeField: "termination_a_type", IDField: "termination_a_id",
				Spec: "aTerminations", AllowedTypes: []string{"dcim.interface"},
				Members: []GenericFKMember{{Spec: "interfaceRef", Target: testGVK("NetBoxInterface")}},
				List:    tc.list, Cached: tc.cached,
			}}

			err := d.Validate()
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}

				return
			}

			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Validate() = %v, want %v", err, tc.wantErr)
			}
		})
	}
}

// TestGenericFKListAPIFieldMayNotAlsoBeAField is the collision a to-one pair is already
// checked for, on the one name a to-many pair actually writes.
//
// Two renderings of one NetBox field is the failure: the Field entry would send the spec value
// verbatim and applyGenericFKList would send the resolved list, and which of them survives
// depends on map iteration order.
func TestGenericFKListAPIFieldMayNotAlsoBeAField(t *testing.T) {
	d := tagDescriptor()
	d.Fields = append(d.Fields, Field{Spec: "rawTerminations", API: "a_terminations", Class: ClassArray})
	d.GenericFKs = []GenericFKSpec{{
		TypeField: "termination_a_type", IDField: "termination_a_id",
		Spec: "aTerminations", AllowedTypes: []string{"dcim.interface"},
		Members: []GenericFKMember{{Spec: "interfaceRef", Target: testGVK("NetBoxInterface")}},
		List:    &GenericFKList{APIField: "a_terminations", TypeKey: "object_type", IDKey: "object_id"},
	}}

	if err := d.Validate(); !errors.Is(err, ErrGenericFKNotSpecField) {
		t.Fatalf("Validate() = %v, want ErrGenericFKNotSpecField", err)
	}
}

// TestCableDescriptorStatesItsIdentity pins the facts about dcim.Cable that nothing else in
// this repository can derive, on the shipped Descriptor rather than a fixture.
//
// The kind has **no `meta.constraints` at all** (docs/netbox-schema.md -> dcim.Cable), so its
// identity is the four CableFilterSet parameters below and nothing weaker. Each of them is
// checked against NetBox rather than assumed, because django-filter answers an unrecognised
// parameter with the unfiltered set (#206) -- and this kind adopts what its lookup returns.
func TestCableDescriptorStatesItsIdentity(t *testing.T) {
	cable, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCable"))
	if !ok {
		t.Fatal("no descriptor registered for NetBoxCable")
	}

	if cable.Endpoint != "dcim/cables" || cable.ObjectType != "dcim.cable" {
		t.Errorf("endpoint/objectType = %s/%s, want dcim/cables and dcim.cable",
			cable.Endpoint, cable.ObjectType)
	}

	if len(cable.NaturalKeys) != 1 {
		t.Fatalf("NetBoxCable declares %d natural-key candidates, want exactly 1: there is "+
			"nothing weaker to fall back to on a model with no constraints", len(cable.NaturalKeys))
	}

	want := []string{
		CableTerminationATypeField, CableTerminationAIDField,
		CableTerminationBTypeField, CableTerminationBIDField,
	}

	got := make([]string, 0, len(cable.NaturalKeys[0].Fields))
	for _, field := range cable.NaturalKeys[0].Fields {
		got = append(got, field.Filter)

		// The filter and the spec name have to be the same string: applyGenericFKList files the
		// representative element under the pair's TypeField and IDField, and a KeyField whose
		// Spec named anything else would render no value and make the candidate inapplicable
		// forever.
		if field.Spec != field.Filter {
			t.Errorf("natural key filters on %s and reads %s; the two must be the same name",
				field.Filter, field.Spec)
		}
	}

	if !slices.Equal(got, want) {
		t.Errorf("natural key = %v, want %v", got, want)
	}
}

// TestCableTerminationUnionOffersEveryCabledModel keeps the two independent statements about
// the union honest, and both directions matter.
//
// `AllowedTypes` is what NetBox accepts in the column, derived from the nine models mixing in
// `dcim.CabledObjectModel`. `Members` is what the CRD offers. Here they coincide, unlike
// tenancy.ContactAssignment's 25-against-11 -- so the assertion is equality, and a member added
// without its allowed type (or the reverse) fails.
func TestCableTerminationUnionOffersEveryCabledModel(t *testing.T) {
	cable, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCable"))
	if !ok {
		t.Fatal("no descriptor registered for NetBoxCable")
	}

	want := []string{
		"circuits.circuittermination", "dcim.consoleport", "dcim.consoleserverport",
		"dcim.frontport", "dcim.interface", "dcim.powerfeed", "dcim.poweroutlet",
		"dcim.powerport", "dcim.rearport",
	}

	for _, pair := range cable.GenericFKs {
		if !slices.Equal(pair.AllowedTypes, want) {
			t.Errorf("%s allows %v, want %v", pair.Spec, pair.AllowedTypes, want)
		}

		if len(pair.Members) != len(want) {
			t.Errorf("%s offers %d members for %d allowed types; every CabledObjectModel "+
				"subclass has a typed alias", pair.Spec, len(pair.Members), len(want))
		}

		// The nested keys are GenericObjectSerializer's, not dcim.CableTermination's columns.
		// Writing `termination_type` inside an element would be dropped by NetBox.
		if pair.List.TypeKey != "object_type" || pair.List.IDKey != "object_id" {
			t.Errorf("%s writes elements as %s/%s, want object_type/object_id",
				pair.Spec, pair.List.TypeKey, pair.List.IDKey)
		}
	}
}

// TestCableBundleKeysOnAColumnUniqueName is the identity of the far end, and the reason it is
// allowed to be `name` where tenancy.Contact's is a Conflict waiting to happen.
//
// `name CharField REQ UNIQUE len=100` (docs/netbox-schema.md -> dcim.CableBundle): the database
// refuses the second row, so the key is as strong as any OrganizationalModel's `slug`.
func TestCableBundleKeysOnAColumnUniqueName(t *testing.T) {
	bundle, ok := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxCableBundle"))
	if !ok {
		t.Fatal("no descriptor registered for NetBoxCableBundle")
	}

	if bundle.Endpoint != "dcim/cable-bundles" {
		t.Errorf("endpoint = %s, want dcim/cable-bundles", bundle.Endpoint)
	}

	if len(bundle.NaturalKeys) != 1 || len(bundle.NaturalKeys[0].Fields) != 1 ||
		bundle.NaturalKeys[0].Fields[0].Filter != "name" {
		t.Errorf("natural keys = %+v, want one candidate on `name`", bundle.NaturalKeys)
	}

	// A PrimaryModel has no `slug`, so a candidate on one would render no value and never be
	// applicable.
	if _, mapped := bundle.FieldFor("slug"); mapped {
		t.Error("NetBoxCableBundle maps `slug`, which dcim.CableBundle does not have")
	}
}
