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

// TestScopeFKMembersMatchTheAPIType is the join this whole mechanism rests on.
//
// The API server validates a member the CRD schema declares; the resolver reads the member
// the descriptor declares. A member present in one and absent from the other is accepted by
// `kubectl apply` and never written to NetBox -- which is the same silent no-op as writing
// `site`, arrived at from the other end. So the two lists are compared here rather than
// trusted to review.
func TestScopeFKMembersMatchTheAPIType(t *testing.T) {
	generic := ScopeFK("scope")

	declared := make([]string, 0, len(generic.Members))
	for _, member := range generic.Members {
		declared = append(declared, member.Field)
	}

	if !slices.Equal(declared, netboxv1alpha1.ScopeMemberFields) {
		t.Fatalf("ScopeFK members = %v, want %v", declared, netboxv1alpha1.ScopeMemberFields)
	}

	for _, name := range jsonFieldNames(reflect.TypeFor[netboxv1alpha1.ScopeRef]()) {
		if !slices.Contains(declared, name) {
			t.Errorf("ScopeRef declares %q, which no ScopeFK member resolves", name)
		}
	}

	if len(jsonFieldNames(reflect.TypeFor[netboxv1alpha1.ScopeRef]())) != len(declared) {
		t.Errorf("ScopeRef has %d fields and ScopeFK has %d members",
			len(jsonFieldNames(reflect.TypeFor[netboxv1alpha1.ScopeRef]())), len(declared))
	}
}

// TestScopeFKObjectTypesAreDjangoSpellings pins the spelling the REST API takes.
//
// `scope_type` is a ForeignKey to contenttypes.ContentType on the model and an
// `app_label.model` string over the wire, where `model` is the Django attribute:
// lowercased and unpunctuated. `dcim.SiteGroup` is a 400 and `dcim.site_group` is a 400,
// and neither is distinguishable from the other by reading the model definition.
func TestScopeFKObjectTypesAreDjangoSpellings(t *testing.T) {
	want := []string{"dcim.region", "dcim.sitegroup", "dcim.site", "dcim.location"}

	if got := ScopeFK("scope").AllowedTypes; !slices.Equal(got, want) {
		t.Fatalf("AllowedTypes = %v, want %v", got, want)
	}

	for _, objectType := range ScopeFK("scope").AllowedTypes {
		if objectType != strings.ToLower(objectType) || strings.Contains(objectType, "_") {
			t.Errorf("object type %q is not the lowercased unpunctuated Django spelling", objectType)
		}
	}
}

// TestScopeFKCachesAreReadOnly is the populator's bug as a boot check.
//
// `_site` is maintained by NetBox from `(scope_type, scope_id)` and ignored on write, so a
// scoped kind that treats it as writable does not fail -- it PATCHes the same column on
// every resync forever. Declaring the caches on the pair is what lets the registry insist
// they are read-only, and this asserts it insists.
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

// TestScopeFKMembersNeedATargetKind covers the member the resolver cannot dispatch on. A
// member with no Kind behind it would be indexed under `//ns/name` and resolved against
// nothing.
func TestScopeFKMembersNeedATargetKind(t *testing.T) {
	d := scopedTestDescriptor()
	d.GenericFKs[0].Members[2].Target = netboxv1alpha1.GroupVersion.WithKind("")

	if err := d.Validate(); !errors.Is(err, ErrInvalidGenericFK) {
		t.Fatalf("Validate() = %v, want %v", err, ErrInvalidGenericFK)
	}

	d = scopedTestDescriptor()
	d.GenericFKs[0].Members = append(d.GenericFKs[0].Members, d.GenericFKs[0].Members[0])

	if err := d.Validate(); !errors.Is(err, ErrInvalidGenericFK) {
		t.Fatalf("Validate() with a duplicate member = %v, want %v", err, ErrInvalidGenericFK)
	}
}

// TestRegistryChecksUnionMemberObjectTypes is the cross-descriptor half: AllowedTypes is
// the referring kind's statement of what NetBox accepts in its `scope_type`, and the value
// actually written comes off the target kind's own Descriptor. A disagreement between the
// two is a 400 at best, and at worst a type NetBox accepts for a different model.
func TestRegistryChecksUnionMemberObjectTypes(t *testing.T) {
	r := New()

	scoped := scopedTestDescriptor()
	scoped.GenericFKs[0].AllowedTypes = []string{"dcim.region", "dcim.sitegroup", "dcim.location"}

	if err := r.Add(scoped); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	// The site descriptor has to be registered for the mismatch to be knowable at all,
	// which is also what makes the check safe: an unbuilt Kind is skipped rather than
	// rejected.
	if err := r.Add(siteTestDescriptor()); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	err := r.Validate()
	if !errors.Is(err, ErrGenericFKTypeMismatch) {
		t.Fatalf("Validate() = %v, want %v", err, ErrGenericFKTypeMismatch)
	}

	if !strings.Contains(err.Error(), "dcim.site") {
		t.Errorf("Validate() = %q, want it to name the object type the target reports", err)
	}
}

// TestRegistrySkipsUnbuiltUnionMembers is the state every scoped kind is in until NBO-066
// and NBO-048 land: two of the four members point at Kinds this build does not carry.
func TestRegistrySkipsUnbuiltUnionMembers(t *testing.T) {
	r := New()

	if err := r.Add(scopedTestDescriptor()); err != nil {
		t.Fatalf("Add() = %v", err)
	}

	if err := r.Validate(); err != nil {
		t.Fatalf("Validate() = %v, want a descriptor whose union members have no CRD yet to pass", err)
	}
}

// jsonFieldNames are the JSON names of a struct's fields, which is the vocabulary the
// engine, the resolver and a user's YAML all share.
func jsonFieldNames(t reflect.Type) []string {
	names := make([]string, 0, t.NumField())

	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			names = append(names, tag)
		}
	}

	return names
}

// scopedTestDescriptor is ipam.Prefix's shape as far as the scope is concerned: the pair,
// its four caches in ReadOnly, and no `site` field anywhere.
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
		GenericFKs:     []GenericFKSpec{ScopeFK("scope")},
		ContainmentRef: "scope",
	}
}

// siteTestDescriptor is the target half of a union member, with the one field the
// cross-descriptor check reads.
func siteTestDescriptor() Descriptor {
	return Descriptor{
		GVK:            netboxv1alpha1.SiteRef{}.TargetGVK(),
		Endpoint:       "dcim/sites",
		ObjectType:     "dcim.site",
		Scope:          apiextensionsv1.NamespaceScoped,
		Fields:         []Field{{Spec: "slug", API: "slug"}},
		NaturalKeys:    []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},
		UpdateStrategy: UpdatePatch,
	}
}
