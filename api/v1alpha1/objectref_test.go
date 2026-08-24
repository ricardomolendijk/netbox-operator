package v1alpha1

import (
	"testing"
)

// TestRefAliasTargets pins every typed alias to the Kind it resolves against.
//
// The alias is the only place that answer is written down, and the resolver dispatches on
// it, so an alias pointing at the wrong Kind would silently resolve a reference against
// the wrong CRs rather than fail. The table is duplicated in
// docs/concepts/references.md; if one moves, this test is what catches it.
func TestRefAliasTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  RefTarget
		kind string
	}{
		{"tag", TagRef{}, "NetBoxTag"},
		{"region", RegionRef{}, "NetBoxRegion"},
		{"site", SiteRef{}, "NetBoxSite"},
		{"siteGroup", SiteGroupRef{}, "NetBoxSiteGroup"},
		{"location", LocationRef{}, "NetBoxLocation"},
		{"tenant", TenantRef{}, "NetBoxTenant"},
		{"tenantGroup", TenantGroupRef{}, "NetBoxTenantGroup"},
		{"interface", InterfaceRef{}, "NetBoxInterface"},
		{"vmInterface", VMInterfaceRef{}, "NetBoxVMInterface"},
		{"fhrpGroup", FHRPGroupRef{}, "NetBoxFHRPGroup"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.ref.TargetGVK()

			if got.Kind != tc.kind {
				t.Errorf("TargetGVK().Kind = %q, want %q", got.Kind, tc.kind)
			}

			if got.Group != GroupVersion.Group || got.Version != GroupVersion.Version {
				t.Errorf("TargetGVK() = %s, want group/version %s", got, GroupVersion)
			}
		})
	}
}

// TestAsObjectRefRoundTrips checks the alias carries its payload through unchanged.
//
// The aliases are defined types over ObjectRef rather than wrappers, so this is really a
// guard against someone converting through a narrower struct and dropping a mode: a lost
// Lookup or ID would turn a precise reference into an unresolvable one.
func TestAsObjectRefRoundTrips(t *testing.T) {
	id := int64(12)
	want := ObjectRef{
		Name:      "eu-west",
		Namespace: "catalogue",
		Slug:      "eu-west",
		Lookup:    map[string]string{"vid": "20"},
		ID:        &id,
	}

	got := RegionRef(want).AsObjectRef()

	if got.Name != want.Name || got.Namespace != want.Namespace || got.Slug != want.Slug {
		t.Errorf("AsObjectRef() dropped a string field: %+v", got)
	}

	if got.ID == nil || *got.ID != *want.ID {
		t.Errorf("AsObjectRef() ID = %v, want %d", got.ID, *want.ID)
	}

	if got.Lookup["vid"] != "20" {
		t.Errorf("AsObjectRef() Lookup = %v, want vid=20", got.Lookup)
	}
}
