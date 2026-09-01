package v1alpha1

import (
	"os"
	"regexp"
	"slices"
	"strings"
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
		{"contact", ContactRef{}, "NetBoxContact"},
		{"contactRole", ContactRoleRef{}, "NetBoxContactRole"},
		{"contactGroup", ContactGroupRef{}, "NetBoxContactGroup"},
		{"interface", InterfaceRef{}, "NetBoxInterface"},
		{"vmInterface", VMInterfaceRef{}, "NetBoxVMInterface"},
		{"fhrpGroup", FHRPGroupRef{}, "NetBoxFHRPGroup"},
		{"device", DeviceRef{}, "NetBoxDevice"},
		{"deviceType", DeviceTypeRef{}, "NetBoxDeviceType"},
		{"deviceRole", DeviceRoleRef{}, "NetBoxDeviceRole"},
		{"platform", PlatformRef{}, "NetBoxPlatform"},
		{"cluster", ClusterRef{}, "NetBoxCluster"},
		{"ipAddress", IPAddressRef{}, "NetBoxIPAddress"},
		{"role", RoleRef{}, "NetBoxRole"},
		{"rir", RIRRef{}, "NetBoxRIR"},
		// The one target with no slug column: ipam.ASN is unique on `asn` alone
		// (docs/netbox-schema.md), so a slug-mode ref matches nothing there.
		{"asn", ASNRef{}, "NetBoxASN"}, {"rackRole", RackRoleRef{}, "NetBoxRackRole"},
		{"rackType", RackTypeRef{}, "NetBoxRackType"},
		{"rackGroup", RackGroupRef{}, "NetBoxRackGroup"},
		{"rack", RackRef{}, "NetBoxRack"}} {
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

// TestOptionalRefMirrorsObjectRef keeps the copy honest.
//
// OptionalRef has to be a copy of ObjectRef rather than a defined type over it, because
// controller-gen merges the underlying type's XValidation markers into the derived schema and
// the strict `== 1` rule would survive (see the type's comment). A copy that drifted would be
// the worst of both: a reference the API server validates differently from every other one,
// with nothing saying so.
//
// AsObjectRef already fails the *build* if the field sets diverge -- a Go struct conversion
// requires identical fields and types. What a conversion ignores is exactly what this test
// reads: the struct tags, so a JSON name cannot drift, and the field-level markers, so a
// bound cannot. The type-level rules are compared too, with the one deliberate difference
// spelled out: the arity rule, `== 1` against `<= 1`, which is the whole of the type.
func TestOptionalRefMirrorsObjectRef(t *testing.T) {
	body, err := os.ReadFile("objectref.go")
	if err != nil {
		t.Fatalf("reading the reference's Go source: %v", err)
	}

	strict, optional := declarationOf(t, string(body), "ObjectRef"), declarationOf(t, string(body), "OptionalRef")

	if len(strict.rules) != len(optional.rules) {
		t.Fatalf("ObjectRef carries %d CEL rules and OptionalRef %d", len(strict.rules), len(optional.rules))
	}

	// The arity rule is the first on both, and relaxing it is the only licensed difference:
	// every other rule is about what a *set* mode may contain, which an empty reference does
	// not change.
	relaxed := strings.Replace(strict.rules[0], ".size() == 1", ".size() <= 1", 1)
	if relaxed == strict.rules[0] {
		t.Fatalf("ObjectRef's first CEL rule is no longer the `== 1` arity rule:\n  %s", strict.rules[0])
	}

	if optional.rules[0] != relaxed {
		t.Errorf("OptionalRef's arity rule is\n  %s\nand ObjectRef's relaxed is\n  %s",
			optional.rules[0], relaxed)
	}

	if !slices.Equal(strict.rules[1:], optional.rules[1:]) {
		t.Errorf("the rules after the arity rule differ:\n  %v\n  %v", strict.rules[1:], optional.rules[1:])
	}

	if !slices.Equal(strict.body, optional.body) {
		t.Errorf("the fields and their markers differ:\n  %v\n  %v", strict.body, optional.body)
	}
}

// refDeclaration is one reference struct as its source says it: the CEL rules above it, and
// the field declarations and field-level markers inside it. Everything else in the block is
// prose, which is free to differ and does.
type refDeclaration struct {
	rules []string
	body  []string
}

var (
	celRule   = regexp.MustCompile(`XValidation:rule="([^"]*)"`)
	fieldLine = regexp.MustCompile("(?m)^\t(?:// \\+kubebuilder:[^\n]*|[A-Z][A-Za-z0-9]* [^\n]*`[^\n]*)$")
)

// declarationOf pulls one struct's rules and fields out of the source file.
//
// Read from the source rather than through reflect because half of what has to match is not
// in the type system at all: a `+kubebuilder` marker is a comment, and controller-gen is the
// only thing that reads it.
func declarationOf(t *testing.T, source, name string) refDeclaration {
	t.Helper()

	block := regexp.MustCompile(`(?s)\n((?:// \+kubebuilder:[^\n]*\n)+)type ` + name + ` struct \{\n(.*?)\n\}\n`)

	found := block.FindStringSubmatch(source)
	if found == nil {
		t.Fatalf("no marked `type %s struct` in objectref.go", name)
	}

	rules := make([]string, 0, 5)
	for _, rule := range celRule.FindAllStringSubmatch(found[1], -1) {
		rules = append(rules, rule[1])
	}

	if len(rules) == 0 {
		t.Fatalf("type %s carries no CEL rules", name)
	}

	return refDeclaration{rules: rules, body: fieldLine.FindAllString(found[2], -1)}
}
