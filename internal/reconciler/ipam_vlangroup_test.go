package reconciler

import (
	"context"
	"reflect"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// pairKeyedDescriptor is the shape ipam.VLANGroup's Descriptor has: a scope union, and a
// natural key whose first candidate matches on the pair's two *columns* while the second pins
// both to null.
//
// Built on the fake kind rather than on the real NetBoxVLANGroup, because what these tests are
// about is the mechanism #180 added -- a resolved union reaching a query parameter -- and the
// fake kind is the one whose spec the fakes can drive. The real descriptor's own key is
// asserted as data in internal/registry/ipam_vlan_test.go.
func pairKeyedDescriptor() registry.Descriptor {
	d := scopedDescriptor()
	d.NaturalKeys = []registry.NaturalKey{
		{
			Fields: []registry.KeyField{
				{Filter: registry.ScopeTypeField, Spec: registry.ScopeTypeField},
				{Filter: registry.ScopeIDField, Spec: registry.ScopeIDField},
				{Filter: "slug", Spec: "slug"},
			},
		},
		{
			Fields: []registry.KeyField{{Filter: "slug", Spec: "slug"}},
			NullFields: []registry.NullField{
				{Filter: registry.ScopeIDField, Spec: "scope", Column: registry.NullColumnNumeric},
			},
		},
	}

	return d
}

// TestPairKeyedLookupCarriesBothScopeColumns is the whole of #180 in one assertion: a natural
// key over a polymorphic pair produces a lookup with both halves of the pair on it.
//
// Before this, `applyGenericFK` recorded the union as resolved without writing a value
// anywhere the key could read, so such a candidate became *applicable* and then `params()`
// failed with errUnfilterable -- a descriptor that was accepted at boot and broke at the first
// lookup. The two column names are what the candidate matches on, and the values are the
// resolved type string and the resolved id.
//
// Both filters and not one. `?scope_id=31&slug=mgmt` would match a group scoped to the *site*
// with id 31 and a group scoped to the *region* with id 31 alike, because a generic FK's id is
// only unique within its type -- which is the same reason the pair is written atomically
// (docs/concepts/generic-refs.md).
//
// `scope_type` is `dcim.site` rather than a numeric ContentType id because that is the
// spelling VLANGroupFilterSet takes: `scope_type = MultiValueContentTypeFilter()` splits the
// value on `.` and resolves it through `ContentType.objects.get_by_natural_key`
// (NetBox 4.6.8, netbox/ipam/filtersets.py:948, netbox/utilities/filters.py:186-207).
func TestPairKeyedLookupCarriesBothScopeColumns(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "ams"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, pairKeyedDescriptor(), nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{
			"scope": {{ID: 31, ObjectType: "dcim.site", Mode: resolver.ModeName}},
		},
	}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Params{"scope_type": "dcim.site", "scope_id": "31", "slug": "managed"}
	if got := nb.calls[0].params; !reflect.DeepEqual(got, want) {
		t.Errorf("lookup params = %v, want %v", got, want)
	}
}

// TestUnscopedPairKeyedLookupPinsTheScopeIDToNull is the other candidate, and the pin is why
// it exists rather than being a slug-only lookup.
//
// With both scope columns null Postgres treats the NULLs as distinct, so `unique_scope_slug`
// does not fire and two globally-scoped VLAN groups may legitimately share a slug -- the
// lookup cannot be made unique and does not pretend to be. What the pin buys is the *other*
// direction: `?slug=managed` alone matches every scoped group with that slug too, so a global
// group would adopt a site's group and the follow-up PATCH would strip that group's scope.
//
// One pin for a two-column pair, and `scope_id__empty=true` rather than a sentinel, because
// that is the only thing NetBox will answer. `scope_id` is numeric and takes the `__empty`
// suffix; `scope_type` is a ContentType foreign key for which NetBox registers no null filter
// at all, and pinning it anyway makes the query match *nothing*. Pinning the id half alone
// loses nothing, since NetBox refuses one half of the pair without the other
// (docs/concepts/lookups.md#how-a-null-pin-is-spelled-and-why-it-depends-on-the-column).
func TestUnscopedPairKeyedLookupPinsTheScopeIDToNull(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = nil

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, pairKeyedDescriptor(), nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Params{"slug": "managed", "scope_id__empty": "true"}
	if got := nb.calls[0].params; !reflect.DeepEqual(got, want) {
		t.Errorf("lookup params = %v, want %v", got, want)
	}
}

// TestDeclaredButUnresolvedScopeMatchesNeitherPairCandidate is the NBO-015 half, and it is the
// reason the two candidates are not a fallback chain.
//
// A group whose scope names a CR that does not exist yet must match *nothing*: candidate 1
// needs the two columns resolved, candidate 2 needs `scope` never declared. Falling through to
// candidate 2 would find the globally-scoped group of the same slug, adopt it, and the
// follow-up PATCH would move somebody else's global group into this scope.
//
// Asserted as "no lookup happened at all", because that is the observable form of the engine
// waiting: with no applicable candidate there is nothing to ask NetBox.
func TestDeclaredButUnresolvedScopeMatchesNeitherPairCandidate(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "not-created-yet"}}

	nb := &fakeClient{created: liveTag(7)}

	// The resolver files nothing for `scope`, which is what an unresolved reference looks
	// like to the pass.
	engine := engineWith(t, pairKeyedDescriptor(), nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	for _, c := range nb.calls {
		if c.method == "GETONE" {
			t.Errorf("lookup happened with params %v; a declared-but-unresolved scope must "+
				"match no candidate so the engine waits", c.params)
		}
	}
}
