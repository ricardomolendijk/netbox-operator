package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestRackDescriptorsAreRegisteredAndValid is the boot check for NBO-051's five kinds.
func TestRackDescriptorsAreRegisteredAndValid(t *testing.T) {
	for _, tc := range []struct {
		kind       string
		endpoint   string
		objectType string
	}{
		{"NetBoxRackRole", "dcim/rack-roles", "dcim.rackrole"},
		{"NetBoxRackType", "dcim/rack-types", "dcim.racktype"},
		{"NetBoxRackGroup", "dcim/rack-groups", "dcim.rackgroup"},
		{"NetBoxRack", "dcim/racks", "dcim.rack"},
		{"NetBoxRackReservation", "dcim/rack-reservations", "dcim.rackreservation"},
	} {
		t.Run(tc.kind, func(t *testing.T) {
			gvk := netboxv1alpha1.GroupVersion.WithKind(tc.kind)

			d, ok := Get(gvk)
			if !ok {
				t.Fatalf("Get(%s) found no descriptor; the init() did not run", gvk)
			}

			if err := d.Validate(); err != nil {
				t.Fatalf("Validate() = %v, want nil", err)
			}

			// The endpoint is looked up in docs/netbox-schema.md's endpoint map, never
			// derived: `dcim/rack-types` is not the pluralisation of `dcim.RackType`, and
			// `dcim/rack-groups` is not `dcim/rackgroups`.
			if d.Endpoint != tc.endpoint {
				t.Errorf("Endpoint = %q, want %q (docs/netbox-schema.md, endpoint map)",
					d.Endpoint, tc.endpoint)
			}

			if d.ObjectType != tc.objectType {
				t.Errorf("ObjectType = %q, want %q", d.ObjectType, tc.objectType)
			}

			if d.Scope != apiextensionsv1.NamespaceScoped {
				t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
			}

			if d.UpdateStrategy != UpdatePatch {
				t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
			}

			// All five carry both mixins, so all five are stamped in full: RackRole and
			// RackGroup are OrganizationalModels, and the other three are PrimaryModels
			// directly or through RackBase (docs/netbox-schema.md, bases).
			if !d.Taggable || !d.CustomFieldable {
				t.Errorf("Taggable/CustomFieldable = %v/%v, want both: the model mixes in "+
					"TagsMixin and CustomFieldsMixin", d.Taggable, d.CustomFieldable)
			}

			// Racks are configuration, not allocated state: nothing here frees a resource
			// when it is deleted, which is what #176 reserved Retain for.
			if d.RetainOnDelete {
				t.Errorf("RetainOnDelete = true; a rack is configuration a manifest " +
					"recreates (#176, docs/concepts/deletion.md)")
			}
		})
	}
}

// TestRackNaturalKeysComeFromTheConstraints is NBO-051's central claim, and the five kinds
// disagree in four different ways over one app and one source file:
//
//   - dcim.RackRole and dcim.RackGroup declare **no** meta.constraints, so the key is the base
//     class's column-unique `slug` -- the dcim.Manufacturer derivation, not a nested group's.
//   - dcim.RackType has two unconditional constraints, both starting at `manufacturer`, so it
//     has two candidates and no pin: the dcim.DeviceType shape exactly.
//   - dcim.Rack has two constraints and both are keyed on the *optional* `location`, so the
//     null-pinned `(site, name)` variant is a convention rather than a constraint.
//   - dcim.RackReservation has no constraints and no column-unique at all, and its ordering
//     names only `created`, so its key is pure convention over its two required columns.
func TestRackNaturalKeysComeFromTheConstraints(t *testing.T) {
	tests := map[string]struct {
		kind string
		want []NaturalKey
	}{
		"a rack role is keyed on its column-unique slug alone": {
			kind: "NetBoxRackRole",
			want: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		},
		"a rack group is too, because it is an OrganizationalModel and not a nested group": {
			kind: "NetBoxRackGroup",
			want: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		},
		"a rack type is keyed by slug then model, both under its manufacturer": {
			kind: "NetBoxRackType",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "slug", Spec: "slug"},
				}},
				{Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "model", Spec: "model"},
				}},
			},
		},
		"a rack pins location_id when it names no location": {
			kind: "NetBoxRack",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "location_id", Spec: "locationRef"},
					{Filter: "name", Spec: "name"},
				}},
				{
					Fields: []KeyField{
						{Filter: "site_id", Spec: "siteRef"},
						{Filter: "name", Spec: "name"},
					},
					NullFields: []NullField{
						{Filter: "location_id", Spec: "locationRef", Column: NullColumnRef},
					},
				},
				{Fields: []KeyField{
					{Filter: "location_id", Spec: "locationRef"},
					{Filter: "facility_id", Spec: "facilityID"},
				}},
			},
		},
		"a reservation is keyed on its rack and its required description": {
			kind: "NetBoxRackReservation",
			want: []NaturalKey{
				{Fields: []KeyField{
					{Filter: "rack_id", Spec: "rackRef"},
					{Filter: "description", Spec: "description"},
				}},
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			if !reflect.DeepEqual(d.NaturalKeys, tc.want) {
				t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, tc.want)
			}
		})
	}
}

// TestRackCandidatesByState is the table NBO-051's acceptance criteria ask for: location set,
// location unset, and location declared but not yet resolved.
//
// The `want: nil` row is the one worth having. A rack whose `locationRef` names a
// NetBoxLocation that has not reconciled yet must **not** fall through to the `(site, name)`
// variant -- that candidate would find a location-less rack of the same name in the same site
// and adopt it, and the follow-up PATCH would move somebody else's rack into a room. With
// nothing applicable the engine waits, which is the correct outcome (NBO-015).
func TestRackCandidatesByState(t *testing.T) {
	tests := map[string]struct {
		kind  string
		state SpecState
		want  [][]string
	}{
		"a rack in a location is keyed on the constraint": {
			kind: "NetBoxRack",
			state: SpecState{
				Declared: []string{"name", "siteRef", "locationRef"},
				Resolved: []string{"name", "siteRef", "locationRef"},
			},
			want: [][]string{{"location_id", "name"}},
		},
		"a rack in a location that also has a facility id keeps the second constraint": {
			kind: "NetBoxRack",
			state: SpecState{
				Declared: []string{"name", "siteRef", "locationRef", "facilityID"},
				Resolved: []string{"name", "siteRef", "locationRef", "facilityID"},
			},
			want: [][]string{{"location_id", "name"}, {"location_id", "facility_id"}},
		},
		"a rack with no location falls back to the null-pinned site convention": {
			kind: "NetBoxRack",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: [][]string{{"site_id", "name", "location_id=null"}},
		},
		"a location-less rack with a facility id gets no facility candidate": {
			kind: "NetBoxRack",
			state: SpecState{
				Declared: []string{"name", "siteRef", "facilityID"},
				Resolved: []string{"name", "siteRef", "facilityID"},
			},
			want: [][]string{{"site_id", "name", "location_id=null"}},
		},
		"a rack whose location has not been created yet has no candidate": {
			kind: "NetBoxRack",
			state: SpecState{
				Declared: []string{"name", "siteRef", "locationRef"},
				Resolved: []string{"name", "siteRef"},
			},
			want: nil,
		},
		"a rack whose site has not been created yet has no candidate either": {
			kind: "NetBoxRack",
			state: SpecState{
				Declared: []string{"name", "siteRef"},
				Resolved: []string{"name"},
			},
			want: nil,
		},
		"a rack type with no manufacturer has no candidate at all": {
			kind: "NetBoxRackType",
			state: SpecState{
				Declared: []string{"slug", "model"}, Resolved: []string{"slug", "model"},
			},
			want: nil,
		},
		"a reservation whose rack has not been created yet has no candidate": {
			kind: "NetBoxRackReservation",
			state: SpecState{
				Declared: []string{"rackRef", "description"},
				Resolved: []string{"description"},
			},
			want: nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(tc.kind))

			var got [][]string
			for _, key := range d.Candidates(tc.state) {
				got = append(got, params(key))
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Candidates(%+v) = %v, want %v", tc.state, got, tc.want)
			}
		})
	}
}

// TestOnlyTheReservationHasAContainmentParent is the cascade reading of NBO-051, and it
// contradicts the ticket, which asks for an owner reference from `NetBoxRack.siteRef`.
//
// `dcim.Rack.site` is `on_delete=PROTECT` and `dcim.Rack.location` is `on_delete=SET_NULL`
// (docs/netbox-schema.md -> dcim.Rack), so neither qualifies: an owner reference on a
// PROTECTed foreign key promises a cluster-side cascade NetBox refuses to perform, which
// deletes the CR and leaves the row, and a SET_NULL one deletes the CR over a column NetBox
// merely cleared (registry.ErrContainmentNotCascade,
// docs/decisions/0003-ownership-and-references.md rule 4).
//
// `dcim.RackReservation.rack` is the one `CASCADE` in the whole hierarchy, so it is the one
// containment parent.
func TestOnlyTheReservationHasAContainmentParent(t *testing.T) {
	reservation, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxRackReservation"))

	if reservation.ContainmentRef != "rackRef" {
		t.Errorf("NetBoxRackReservation ContainmentRef = %q, want %q: `rack` is REQ and CASCADE",
			reservation.ContainmentRef, "rackRef")
	}

	want := []string{netboxv1alpha1.RackRef{}.TargetGVK().Kind}

	targets := reservation.ContainmentTargets()

	got := make([]string, 0, len(targets))
	for _, target := range targets {
		got = append(got, target.Kind)
	}

	if !reflect.DeepEqual(got, want) {
		t.Errorf("ContainmentTargets() = %v, want %v", got, want)
	}

	for _, kind := range []string{
		"NetBoxRackRole", "NetBoxRackType", "NetBoxRackGroup", "NetBoxRack",
	} {
		d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))
		if d.ContainmentRef != "" {
			t.Errorf("%s ContainmentRef = %q, want empty: every FK it holds is PROTECT or "+
				"SET_NULL, so nothing cascades", kind, d.ContainmentRef)
		}
	}
}

// TestRackCountersAndCachesAreReadOnly asserts the unwritable columns against the digest's own
// lists rather than against a count.
//
// NetBox ignores a write to any of them instead of refusing it, so the guard has to be here:
// with the column in ReadOnly, a field map that ever reaches for one fails Validate at boot
// (ErrFieldReadOnly) rather than PATCHing forever.
func TestRackCountersAndCachesAreReadOnly(t *testing.T) {
	for kind, columns := range map[string][]string{
		"NetBoxRackRole":  {"rack_count"},
		"NetBoxRackGroup": {"rack_count"},
		// RackBase's two weight caches, on both kinds that derive from it.
		"NetBoxRackType": {"rack_count", "_abs_weight", "_abs_max_weight"},
		"NetBoxRack": {
			"device_count", "powerfeed_count", "_abs_weight", "_abs_max_weight",
		},
		// Derived from `units` by NetBox, and refused on write.
		"NetBoxRackReservation": {"unit_count"},
	} {
		t.Run(kind, func(t *testing.T) {
			d, _ := Get(netboxv1alpha1.GroupVersion.WithKind(kind))

			for _, column := range columns {
				if !slices.Contains(d.ReadOnly, column) {
					t.Errorf("%q is not in ReadOnly; NetBox maintains it and drops a write to it",
						column)
				}
			}
		})
	}
}

// TestRackBaseFieldsAreIdenticalOnBothKinds is what makes v1alpha1.RackDimensions worth having.
//
// `dcim.RackType` and `dcim.Rack` both derive from `dcim.RackBase` (docs/netbox-schema.md,
// bases), so the twelve dimension columns are one fact. Sharing rackBaseFields() is how the
// registry says so, and this is the assertion that a future edit to one kind's table cannot
// quietly make them disagree -- which would mean the same YAML field wrote a different column
// depending on which kind carried it.
func TestRackBaseFieldsAreIdenticalOnBothKinds(t *testing.T) {
	rackType, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxRackType"))
	rack, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxRack"))

	for _, base := range rackBaseFields() {
		onType, okType := rackType.FieldFor(base.Spec)
		onRack, okRack := rack.FieldFor(base.Spec)

		if !okType || !okRack {
			t.Errorf("%q is on NetBoxRackType=%v NetBoxRack=%v; RackBase declares it for both",
				base.Spec, okType, okRack)

			continue
		}

		if !reflect.DeepEqual(onType, onRack) {
			t.Errorf("%q maps to %+v on NetBoxRackType and %+v on NetBoxRack",
				base.Spec, onType, onRack)
		}
	}
}

// TestReservationUnitsAreAnOrderedArray pins the comparison rule for `units`, which is the one
// place NBO-051's ticket and the engine disagree.
//
// The ticket asks for a set, so that reordering produces no PATCH. `units` is a Postgres
// ArrayField and NetBox returns it in stored order (docs/netbox-schema.md ->
// dcim.RackReservation, `units ArrayField REQ`), so ClassArray is what ships: comparing it
// order-independently would report two genuinely different server states as equal. It also
// cannot be ClassRefMany -- the elements are integers, not references to anything.
func TestReservationUnitsAreAnOrderedArray(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxRackReservation"))

	units, ok := d.FieldFor("units")
	if !ok {
		t.Fatal("no `units` entry in the field map")
	}

	if units.Class != ClassArray {
		t.Errorf("units Class = %q, want %q", units.Class, ClassArray)
	}

	if got := d.ArrayFields(); !slices.Contains(got, "units") {
		t.Errorf("ArrayFields() = %v, want it to contain `units`", got)
	}

	if got := d.M2MFields(); len(got) != 0 {
		t.Errorf("M2MFields() = %v, want none: `units` is an array, not a many-to-many", got)
	}
}

// TestReservationUserIsAValueNotAReference records the engine gap NBO-051 runs into, as an
// assertion rather than as a comment.
//
// `dcim.RackReservation.user` is `ForeignKey REQ -> settings.AUTH_USER_MODEL` and the `users`
// app is deferred whole, so there is no Kind to point at. internal/resolver dispatches every
// reference mode -- `name`, `slug`, `lookup` and `id` alike -- through
// `Descriptors.Get(Field.Target)` to learn which endpoint to query, so a reference whose target
// Kind has no Descriptor cannot resolve in any mode and would sit at
// `RefsResolved=False, Reason=RefKindUnavailable` forever. The column is therefore written from
// a literal id in a plain value field.
//
// If a Descriptor ever gains a way to name an endpoint for a Kind-less reference, this is the
// test that should change.
func TestReservationUserIsAValueNotAReference(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxRackReservation"))

	user, ok := d.FieldFor("userID")
	if !ok {
		t.Fatal("no `userID` entry in the field map")
	}

	if user.API != "user" {
		t.Errorf("userID API = %q, want `user`", user.API)
	}

	if user.Class != ClassValue {
		t.Errorf("userID Class = %q, want %q: there is no NetBoxUser Kind to resolve against",
			user.Class, ClassValue)
	}

	if !user.Target.Empty() {
		t.Errorf("userID Target = %s, want empty", user.Target)
	}
}

// TestRackWritesSiteAndNotAScopePair is the NetBox 4.2 trap, asserted in the negative.
//
// ipam.Prefix, ipam.VLANGroup and virtualization.Cluster moved to a `(scope_type, scope_id)`
// pair with cached `site`/`location` columns, and writing the cached `site` there returns 201
// and sets nothing (docs/concepts/generic-refs.md, docs/reference/netboxcluster.md).
// `dcim.Rack` was not part of that change: `site` and `location` are real writable foreign keys
// on it (docs/netbox-schema.md -> dcim.Rack). So this Kind declares no generic FK, and `site`
// is a column it really does write.
func TestRackWritesSiteAndNotAScopePair(t *testing.T) {
	d, _ := Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxRack"))

	if len(d.GenericFKs) != 0 {
		t.Errorf("GenericFKs = %+v, want none: dcim.Rack has no (scope_type, scope_id) pair",
			d.GenericFKs)
	}

	for spec, api := range map[string]string{"siteRef": "site", "locationRef": "location"} {
		field, ok := d.FieldFor(spec)
		if !ok {
			t.Errorf("no %q entry in the field map", spec)

			continue
		}

		if field.API != api || field.Class != ClassRefOne {
			t.Errorf("%s -> %q/%q, want %q/%q", spec, field.API, field.Class, api, ClassRefOne)
		}

		if slices.Contains(d.ReadOnly, api) {
			t.Errorf("%q is in ReadOnly; on dcim.Rack it is a writable foreign key, not a "+
				"CachedScopeMixin column", api)
		}
	}
}
