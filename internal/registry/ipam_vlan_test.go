package registry

import (
	"reflect"
	"slices"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func descriptorFor(t *testing.T, kind string) Descriptor {
	t.Helper()

	gvk := netboxv1alpha1.GroupVersion.WithKind(kind)

	d, ok := Get(gvk)
	if !ok {
		t.Fatalf("Get(%s) found no descriptor; the init() for %s did not run", gvk, kind)
	}

	return d
}

// TestVLANDescriptorIsRegisteredAndValid is the boot check.
func TestVLANDescriptorIsRegisteredAndValid(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLAN")

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "ipam/vlans" {
		t.Errorf("Endpoint = %q, want ipam/vlans (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "ipam.vlan" {
		t.Errorf("ObjectType = %q, want ipam.vlan", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.UpdateStrategy != UpdatePatch {
		t.Errorf("UpdateStrategy = %q, want Patch", d.UpdateStrategy)
	}

	// No containment parent, and this is the interesting part of the descriptor.
	// ADR-0003 rule 4 as amended by NBO-193: the containment parent is whichever FK the
	// server cascades. Both of this kind's candidates -- `site` and `group` -- are
	// `on_delete=PROTECT` (docs/netbox-schema.md -> ipam.VLAN), so neither does. An owner
	// reference on a PROTECT-ed FK promises a cascade the server refuses, leaving the row
	// alive after garbage collection has removed the CR.
	//
	// This test asserted `siteRef` when it was written, and Validate() refused the descriptor
	// at boot once ErrContainmentNotCascade landed. That is the check working.
	if d.ContainmentRef != "" {
		t.Errorf("ContainmentRef = %q, want empty: neither site nor group cascades", d.ContainmentRef)
	}
}

// TestVLANWritesSiteAsARealForeignKeyAndHasNoScope is the whole point of shipping this kind
// beside NetBoxPrefix, and it is asserted in both directions because both failures are silent.
//
// `ipam.VLAN.site` is a genuine ForeignKey to dcim.Site (docs/netbox-schema.md -> ipam.VLAN),
// so `site` must be in the field map. `ipam.Prefix` has no such column since NetBox 4.2, so
// `NetBoxPrefix` must have a scope pair and no `site` -- and NetBox drops a field it does not
// know rather than rejecting it, so getting either one backwards returns 201 and writes
// nothing.
func TestVLANWritesSiteAsARealForeignKeyAndHasNoScope(t *testing.T) {
	vlan := descriptorFor(t, "NetBoxVLAN")

	field, ok := vlan.FieldFor("siteRef")
	if !ok {
		t.Fatal("no siteRef in the field map; ipam.VLAN.site is a real ForeignKey to dcim.Site")
	}

	if field.API != "site" {
		t.Errorf("siteRef writes %q, want site", field.API)
	}

	if field.Class != ClassRefOne {
		t.Errorf("siteRef class = %q, want %q", field.Class, ClassRefOne)
	}

	if len(vlan.GenericFKs) != 0 {
		t.Errorf("GenericFKs = %+v, want none: ipam.VLAN has no scope pair", vlan.GenericFKs)
	}

	// The other half of the contrast, so a change to either kind fails here.
	prefix := descriptorFor(t, "NetBoxPrefix")
	if _, ok := prefix.FieldFor("siteRef"); ok {
		t.Error("NetBoxPrefix declares siteRef; since NetBox 4.2 ipam.Prefix has no site column " +
			"and writing it returns 201 and sets nothing")
	}
}

// TestVLANNaturalKeysMatchTheConstraintsThatExist pins the three candidates and, more
// importantly, which of them a database constraint backs.
//
// `meta.constraints` on ipam.VLAN is `(group, vid)`, `(group, name)`, `(qinq_svlan, vid)` and
// `(qinq_svlan, name)` (docs/netbox-schema.md -> ipam.VLAN). There is no `(site, vid)`
// constraint, so candidates 2 and 3 are conventions -- which is exactly why the `group_id` pin
// on both is not optional: without it a VLAN whose group has not been created yet would adopt
// an ungrouped VLAN by site and vid.
func TestVLANNaturalKeysMatchTheConstraintsThatExist(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLAN")

	want := []NaturalKey{
		{Fields: []KeyField{{Filter: "group_id", Spec: "groupRef"}, {Filter: "vid", Spec: "vid"}}},
		{
			Fields:     []KeyField{{Filter: "site_id", Spec: "siteRef"}, {Filter: "vid", Spec: "vid"}},
			NullFields: []NullField{{Filter: "group_id", Spec: "groupRef"}},
		},
		{
			Fields: []KeyField{{Filter: "vid", Spec: "vid"}},
			NullFields: []NullField{
				{Filter: "group_id", Spec: "groupRef"},
				{Filter: "site_id", Spec: "siteRef"},
			},
		},
	}

	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}
}

// TestVLANWithADeclaredGroupNeverFallsThroughToTheSiteCandidate is the NBO-015 rule read off
// the candidates, and it is the one that costs data when it is wrong.
//
// A VLAN whose `groupRef` names a group that does not exist yet must match no candidate at
// all, so the engine waits. Falling through to `{site_id, vid}` would adopt an ungrouped VLAN
// with the same vid on the same site and the follow-up PATCH would move somebody else's VLAN
// into this group.
func TestVLANWithADeclaredGroupNeverFallsThroughToTheSiteCandidate(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLAN")

	pending := SpecState{
		Declared: []string{"vid", "name", "siteRef", "groupRef"},
		Resolved: []string{"vid", "name", "siteRef"},
	}

	for i, key := range d.NaturalKeys {
		if key.Applicable(pending) {
			t.Errorf("natural key %d is applicable while groupRef is declared but unresolved: %+v", i, key)
		}
	}

	// And the ordinary site-and-no-group case, which is every VLAN in ../inventory.yaml, does
	// reach exactly one candidate.
	sited := SpecState{
		Declared: []string{"vid", "name", "siteRef"},
		Resolved: []string{"vid", "name", "siteRef"},
	}

	applicable := 0

	for _, key := range d.NaturalKeys {
		if key.Applicable(sited) {
			applicable++
		}
	}

	if applicable != 1 {
		t.Errorf("%d candidates applicable for a sited, ungrouped VLAN, want exactly 1", applicable)
	}
}

// TestVLANDefersItsSelfReference covers the one field that cannot be written at create time.
func TestVLANDefersItsSelfReference(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLAN")

	want := []DeferredField{{APIField: "qinq_svlan", Mode: DeferAlways}}
	if !reflect.DeepEqual(d.Deferred, want) {
		t.Errorf("Deferred = %+v, want %+v", d.Deferred, want)
	}
}

// TestVLANHasNoCachedColumns records the difference between the two scoped-adjacent kinds in
// this milestone. ipam.VLAN holds a real `site` foreign key rather than a scope pair, so there
// is no `_site` cache to exclude and no hierarchy counters either.
func TestVLANHasNoCachedColumns(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLAN")

	want := []string{"created", "last_updated", "url", "display"}
	if !reflect.DeepEqual(d.ReadOnly, want) {
		t.Errorf("ReadOnly = %v, want %v", d.ReadOnly, want)
	}
}

// TestVLANGroupDescriptorIsRegisteredAndValid is the boot check for the second kind. Validate
// is what enforces the rule this one is most able to get wrong -- every Cached column also in
// ReadOnly -- and clearing Cached is the whole of what makes it pass.
func TestVLANGroupDescriptorIsRegisteredAndValid(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANGroup")

	if err := d.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}

	if d.Endpoint != "ipam/vlan-groups" {
		t.Errorf("Endpoint = %q, want ipam/vlan-groups (docs/netbox-schema.md, endpoint map)", d.Endpoint)
	}

	if d.ObjectType != "ipam.vlangroup" {
		t.Errorf("ObjectType = %q, want ipam.vlangroup", d.ObjectType)
	}

	if d.Scope != apiextensionsv1.NamespaceScoped {
		t.Errorf("Scope = %q, want Namespaced (docs/decisions/0002-crd-scoping.md)", d.Scope)
	}

	if d.ContainmentRef != "scope" {
		t.Errorf("ContainmentRef = %q, want scope", d.ContainmentRef)
	}
}

// TestVLANGroupCarriesTheScopePairWithoutTheCaches is the one line that differs from every
// other scoped kind.
//
// ipam.Prefix inherits the pair from dcim.CachedScopeMixin, which brings `_region`,
// `_site_group`, `_site` and `_location`; ipam.VLANGroup declares `scope_type` / `scope_id` on
// the model itself and has none of the four (docs/netbox-schema.md -> ipam.VLANGroup). Leaving
// the cache list in place would put four columns this table does not have into ReadOnly, which
// Validate would accept and which would be a lie.
func TestVLANGroupCarriesTheScopePairWithoutTheCaches(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANGroup")

	if len(d.GenericFKs) != 1 {
		t.Fatalf("GenericFKs = %+v, want exactly the scope pair", d.GenericFKs)
	}

	pair := d.GenericFKs[0]

	// The union itself is not restated: the members and the four permitted object types still
	// come from ScopeFK, so this asserts sameness rather than re-listing them.
	// Stated for the same reason the descriptor states it, and per member for the same reason
	// (#214): all four scope targets declare a `vlan_groups` GenericRelation, so the scope
	// genuinely cascades from every one of them -- and here the GenericRelation is the whole
	// of the cascade, since this model carries no cached scope columns. ScopeFK cannot default
	// the table, because a union's cascade is a fact about the referring model.
	shared := ScopeFK("scope", ScopeCascadesFromEvery())
	shared.Cached = nil

	if !reflect.DeepEqual(pair, shared) {
		t.Errorf("scope pair = %+v, want registry.ScopeFK(\"scope\") with Cached cleared: %+v", pair, shared)
	}

	for _, column := range ScopeCacheColumns() {
		if slices.Contains(d.ReadOnly, column) {
			t.Errorf("ReadOnly names %s; ipam.VLANGroup has no cached scope columns", column)
		}
	}

	// The counter NetBox maintains from vid_ranges. Writing it silently no-ops, so it has to be
	// excluded or every reconcile PATCHes it again.
	if !slices.Contains(d.ReadOnly, "total_vlan_ids") {
		t.Errorf("ReadOnly = %v, want total_vlan_ids: NetBox maintains it from vid_ranges", d.ReadOnly)
	}
}

// TestVLANGroupIsKeyedOnTheScopePairAndSlug is the candidate #180 exists for: a natural key
// whose filters are the two *columns* of a polymorphic pair.
//
// `slug` alone is deliberately not a candidate. ipam.VLANGroup carries no UNIQUE on the column
// and is unique only on `(scope_type, scope_id, slug)` and `(scope_type, scope_id, name)`
// (docs/netbox-schema.md -> ipam.VLANGroup, meta.constraints), so a slug-only key would make
// every scoped group adopt an unrelated group of the same slug in a different scope.
func TestVLANGroupIsKeyedOnTheScopePairAndSlug(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANGroup")

	want := []NaturalKey{
		{Fields: []KeyField{
			{Filter: ScopeTypeField, Spec: ScopeTypeField},
			{Filter: ScopeIDField, Spec: ScopeIDField},
			{Filter: "slug", Spec: "slug"},
		}},
		{
			Fields: []KeyField{{Filter: "slug", Spec: "slug"}},
			NullFields: []NullField{
				{Filter: ScopeTypeField, Spec: "scope"},
				{Filter: ScopeIDField, Spec: "scope"},
			},
		},
	}

	if !reflect.DeepEqual(d.NaturalKeys, want) {
		t.Errorf("NaturalKeys = %+v, want %+v", d.NaturalKeys, want)
	}
}

// TestVLANGroupScopeCandidatesAreMutuallyExclusive is the half that keeps the two candidates
// from being a fallback chain, in all three states a scope can be in.
func TestVLANGroupScopeCandidatesAreMutuallyExclusive(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANGroup")

	cases := []struct {
		name  string
		state SpecState
		want  int
	}{
		{
			name: "scope resolved",
			state: SpecState{
				Declared: []string{"name", "slug", "scope"},
				Resolved: []string{"name", "slug", "scope", ScopeTypeField, ScopeIDField},
			},
			want: 1,
		},
		{
			name:  "no scope at all",
			state: SpecState{Declared: []string{"name", "slug"}, Resolved: []string{"name", "slug"}},
			want:  1,
		},
		{
			// The one that must find nothing: falling through would adopt the globally-scoped
			// group of the same slug and then PATCH a scope onto it.
			name: "scope declared, not resolved",
			state: SpecState{
				Declared: []string{"name", "slug", "scope"},
				Resolved: []string{"name", "slug"},
			},
			want: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			applicable := 0

			for _, key := range d.NaturalKeys {
				if key.Applicable(tc.state) {
					applicable++
				}
			}

			if applicable != tc.want {
				t.Errorf("%d applicable candidates, want %d", applicable, tc.want)
			}
		})
	}
}

// TestVLANGroupComparesVIDRangesAsAnOrderedArray is NBO-003 item 8 on the one field that needs
// it. NetBox stores vid_ranges as a Postgres array and returns it in stored order, so an
// order-independent compare would treat two different NetBox values as equal.
func TestVLANGroupComparesVIDRangesAsAnOrderedArray(t *testing.T) {
	d := descriptorFor(t, "NetBoxVLANGroup")

	field, ok := d.FieldFor("vidRanges")
	if !ok {
		t.Fatal("no vidRanges in the field map")
	}

	if field.API != "vid_ranges" {
		t.Errorf("vidRanges writes %q, want vid_ranges", field.API)
	}

	if field.Class != ClassArray {
		t.Errorf("vidRanges class = %q, want %q: the order is data, not incidental",
			field.Class, ClassArray)
	}

	if slices.Contains(d.M2MFields(), "vid_ranges") {
		t.Error("vid_ranges is compared as an M2M id set; NetBox returns it order-sensitively")
	}
}
