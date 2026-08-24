package registry

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestScopeFKMembersMatchTheAPIType is the join the whole mechanism rests on, for the one
// union a shipped Kind actually carries.
//
// The API server validates the member the CRD schema declares; the resolver dispatches on
// the member the descriptor declares. A member present in one and absent from the other is
// accepted by `kubectl apply` and never written to NetBox -- which is the same silent no-op
// as writing `site`, arrived at from the other end. Reflected over ScopeRef's JSON tags
// rather than listed, so a rename fails here.
//
// The generic form of this check is TestIPAssignmentMembersMatchTheDescriptor; both exist
// because there is one of them per union, not one per mechanism.
func TestScopeFKMembersMatchTheAPIType(t *testing.T) {
	want := jsonFieldNames(reflect.TypeFor[netboxv1alpha1.ScopeRef]())

	if got := ScopeFK("scope", ScopeCascadesFromEvery()).MemberSpecs(); !slices.Equal(got, want) {
		t.Errorf("ScopeFK members = %v, ScopeRef members = %v", got, want)
	}
}

// TestScopeFKObjectTypesAreDjangoSpellings pins the four spellings `scope_type` takes.
//
// The spelling is the one thing about a generic FK that cannot be inferred from anything
// else: it is Django's `model` attribute, lowercased and unpunctuated. `dcim.SiteGroup` is a
// 400 and `dcim.site_group` is a 400, and neither is distinguishable from the other by
// reading the model definition. TestObjectTypeSpelling asserts Validate enforces the shape;
// this asserts these four are the values.
func TestScopeFKObjectTypesAreDjangoSpellings(t *testing.T) {
	want := []string{"dcim.region", "dcim.sitegroup", "dcim.site", "dcim.location"}

	if got := ScopeFK("scope", ScopeCascadesFromEvery()).AllowedTypes; !slices.Equal(got, want) {
		t.Errorf("AllowedTypes = %v, want %v", got, want)
	}
}

// TestScopeFKCachesAreReadOnly is the populator's bug as a boot check, and the one thing
// GenericFKSpec.Cached exists for.
//
// `_site` is maintained by NetBox from `(scope_type, scope_id)` and ignored on write, so a
// scoped kind that treats it as writable does not fail -- it PATCHes the same column on every
// resync forever. Declaring the caches on the pair is what lets the registry insist they are
// read-only, and this asserts it insists.
func TestScopeFKCachesAreReadOnly(t *testing.T) {
	if err := scopedTestDescriptor().Validate(); err != nil {
		t.Fatalf("validating a correctly-declared scoped kind: %v", err)
	}

	d := scopedTestDescriptor()
	d.ReadOnly = slices.DeleteFunc(d.ReadOnly, func(column string) bool { return column == "_site" })

	err := d.Validate()
	if !errors.Is(err, ErrCachedNotReadOnly) {
		t.Fatalf("Validate() = %v, want %v", err, ErrCachedNotReadOnly)
	}

	if !strings.Contains(err.Error(), "_site") {
		t.Errorf("Validate() = %q, want it to name the column", err)
	}
}

// scopedTestDescriptor is ipam.Prefix's shape as far as the scope is concerned: the pair, its
// four caches in ReadOnly, and no `site` field anywhere.
func scopedTestDescriptor() Descriptor {
	return Descriptor{
		GVK:        testGVK("NetBoxPrefix"),
		Endpoint:   "ipam/prefixes",
		ObjectType: "ipam.prefix",
		Scope:      apiextensionsv1.NamespaceScoped,
		Fields: []Field{
			{Spec: "prefix", API: "prefix"},
			{Spec: "status", API: "status"},
		},
		NaturalKeys:    []NaturalKey{{Fields: []KeyField{{Filter: "prefix", Spec: "prefix"}}}},
		UpdateStrategy: UpdatePatch,
		ReadOnly: append(ScopeCacheColumns(),
			"created", "last_updated", "url", "display", "depth", "children"),
		GenericFKs:     []GenericFKSpec{prefixScopeFK()},
		ContainmentRef: "scope",
	}
}

// TestScopeCascadesFromEveryCoversEveryMember keeps the shared table and the shared union in
// step.
//
// ScopeCascadesFromEvery is keyed on member spec names, so adding a fifth scope target to
// ScopeFK without adding it here would leave that member unstated -- which every scoped kind
// would then fail Validate on (ErrMemberCascadePartial). That is the correct failure, and this
// test is where it is cheap to read: the message names the table rather than four descriptors.
func TestScopeCascadesFromEveryCoversEveryMember(t *testing.T) {
	cascades := ScopeCascadesFromEvery()

	for _, member := range ScopeFK("scope", cascades).Members {
		if member.CascadeOnDelete == nil {
			t.Errorf("ScopeCascadesFromEvery does not state %s, so every scoped kind that "+
				"reaches for it fails Validate", member.Spec)

			continue
		}

		if !*member.CascadeOnDelete {
			t.Errorf("ScopeCascadesFromEvery states %s = false; it is the table for a kind that "+
				"cascades from *every* member", member.Spec)
		}
	}

	if len(cascades) != len(ScopeFK("scope", nil).Members) {
		t.Errorf("ScopeCascadesFromEvery has %d entries and the union has %d members: an entry "+
			"naming no member is a typo that leaves a real member unstated",
			len(cascades), len(ScopeFK("scope", nil).Members))
	}
}

// TestScopeFKCascadeIsSuppliedNotDefaulted is the #202 decision as a test, and #214's
// correction of its shape: the flags are per member, and ScopeFK states only what the caller
// says.
//
// A union's cascade is a fact about the *referring* model, and the two mechanisms behind it
// live in different places -- a GenericRelation on the target, or a CASCADE cached column on
// the referrer (docs/netbox-schema.md, dcim.CachedScopeMixin). So ScopeFK has nothing to
// default from, and a caller that says nothing gets a union that cascades from nothing rather
// than one that quietly cascades from everything.
func TestScopeFKCascadeIsSuppliedNotDefaulted(t *testing.T) {
	for _, member := range ScopeFK("scope", nil).Members {
		if member.CascadeOnDelete != nil {
			t.Errorf("ScopeFK defaulted the cascade of %s to %v; it is the referring kind's "+
				"statement to make", member.Spec, *member.CascadeOnDelete)
		}
	}

	if ScopeFK("scope", nil).anyCascades() {
		t.Error("an unstated scope union cascades; a containment ref on it would promise a " +
			"cascade nothing in netbox performs")
	}
}

// TestScopeFKPartialCascadeIsABootFailure is the check that makes a forgotten member loud.
//
// The flags are hand-supplied per referring kind, so the realistic mistake is a table covering
// three of the four members -- and the silent reading of that is the dangerous one: the fourth
// member reads as "does not cascade", an object scoped through it gets no owner reference, its
// CR outlives the row NetBox deleted, and the engine's create-if-absent step recreates it.
func TestScopeFKPartialCascadeIsABootFailure(t *testing.T) {
	partial := ScopeCascadesFromEvery()
	delete(partial, "locationRef")

	d := scopedTestDescriptor()
	d.GenericFKs = []GenericFKSpec{ScopeFK("scope", partial)}

	err := d.Validate()
	if !errors.Is(err, ErrMemberCascadePartial) {
		t.Fatalf("Validate() = %v, want %v", err, ErrMemberCascadePartial)
	}

	if !strings.Contains(err.Error(), "locationRef") {
		t.Errorf("Validate() = %q, want it to name the member left out", err)
	}
}

// TestScopeUnionThatDisagreesKeepsItsContainmentParent is the whole point of #214, at the
// boundary between the two checks.
//
// A union whose members disagree is legal: the objects using a cascading member get their
// owner reference, and the ones using a member that does not cascade are refused per object by
// reconciler/owners.go. Before this, one flag per pair forced the choice between a cascade
// that was wrong for half the scopes and no containment parent at all -- and
// virtualization.Cluster shipped with the second (#210).
func TestScopeUnionThatDisagreesKeepsItsContainmentParent(t *testing.T) {
	d := scopedTestDescriptor()
	d.GenericFKs = []GenericFKSpec{ScopeFK("scope", map[string]bool{
		"regionRef": true, "siteGroupRef": true, "siteRef": false, "locationRef": false,
	})}

	if err := d.Validate(); err != nil {
		t.Fatalf("validating a union whose members disagree: %v", err)
	}

	pair := d.GenericFKs[0]
	region := netboxv1alpha1.RegionRef{}.TargetGVK()
	site := netboxv1alpha1.SiteRef{}.TargetGVK()

	if !pair.Cascades(region) {
		t.Error("the region member does not cascade, so a region-scoped object gets no owner " +
			"reference and outlives the row netbox deleted with the region")
	}

	if pair.Cascades(site) {
		t.Error("the site member cascades, so a site-scoped object would carry an owner " +
			"reference promising a deletion netbox does not perform")
	}

	// A Kind this union does not accept at all. False rather than "the pair cascades", because
	// the question only has an answer per member.
	if pair.Cascades(testGVK("NetBoxTenant")) {
		t.Error("a Kind that is not a member of the union cascades")
	}
}

// TestScopeUnionThatCascadesFromNothingIsRefused is the other side of the boot check: "any
// member" is the weakest thing it can ask, and it still has to refuse the union that cannot
// ever produce an owner reference.
func TestScopeUnionThatCascadesFromNothingIsRefused(t *testing.T) {
	d := scopedTestDescriptor()
	d.GenericFKs = []GenericFKSpec{ScopeFK("scope", map[string]bool{
		"regionRef": false, "siteGroupRef": false, "siteRef": false, "locationRef": false,
	})}

	err := d.Validate()
	if !errors.Is(err, ErrContainmentNotCascade) {
		t.Fatalf("Validate() = %v, want %v", err, ErrContainmentNotCascade)
	}
}
