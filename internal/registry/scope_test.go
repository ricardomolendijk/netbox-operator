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

	if got := ScopeFK("scope").MemberSpecs(); !slices.Equal(got, want) {
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

	if got := ScopeFK("scope").AllowedTypes; !slices.Equal(got, want) {
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
