package resolver

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestResolveScopeUnion is the mapping the whole ticket turns on: which member is set
// decides the `app_label.model` string written to `scope_type`, and the id written to
// `scope_id` is that target's.
//
// The type string is asserted rather than the member name, because a member resolving to
// the wrong type is the failure NetBox cannot catch for us: `scope_type: dcim.region` with a
// Site's id is a valid pair pointing at whatever Region holds that primary key.
func TestResolveScopeUnion(t *testing.T) {
	tests := []struct {
		name  string
		scope map[string]any
		want  Result
	}{
		{
			name:  "a site",
			scope: map[string]any{"siteRef": map[string]any{"name": "hq"}},
			want: Result{
				ID: 5, ObjectType: "dcim.site", Mode: ModeName,
				Target: types.NamespacedName{Namespace: "team-a", Name: "hq"},
			},
		},
		{
			name:  "a region",
			scope: map[string]any{"regionRef": map[string]any{"name": "emea"}},
			want: Result{
				ID: 12, ObjectType: "dcim.region", Mode: ModeName,
				Target: types.NamespacedName{Namespace: "team-a", Name: "emea"},
			},
		},
		{
			// The three NetBox-side modes carry the type just as `name` does: it comes off
			// the target Kind's Descriptor either way, never off the reference.
			name:  "a site by slug",
			scope: map[string]any{"siteRef": map[string]any{"slug": "hq"}},
			want:  Result{ID: 41, ObjectType: "dcim.site", Mode: ModeSlug},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resolver := &Resolver{
				Objects: &fakeReader{objects: []target{readyTarget(), readySite()}},
				Kinds:   kinds(),
			}
			nb := &fakeNetBox{list: []netbox.Object{{"id": float64(41), "slug": "hq"}}}

			resolution, err := resolver.ResolveAll(context.Background(), nb,
				referrer("prefix", map[string]any{"scope": tc.scope}), prefixDescriptor())
			if err != nil {
				t.Fatalf("ResolveAll() = %v", err)
			}

			if len(resolution.Blocked) != 0 {
				t.Fatalf("Blocked = %+v, want nothing blocked", resolution.Blocked)
			}

			// Keyed by the union's own spec field and not by the member: one spec field
			// writes both columns, so that is the name the payload builder looks under.
			if got := resolution.ByField["scope"]; !reflect.DeepEqual(got, tc.want) {
				t.Errorf("ByField[scope] = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestResolveScopeMemberWithNoKind is the state two of the four members are in until
// NBO-066 and NBO-048 build them: no Descriptor, so no endpoint to query and no CRD to read.
//
// RefKindUnavailable and not RefNotFound, because the manifest is correct and the fix is an
// operator upgrade. Nothing is written and nothing is retried sooner than the ten minutes a
// human-cleared state gets.
func TestResolveScopeMemberWithNoKind(t *testing.T) {
	for _, member := range []string{"siteGroupRef", "locationRef"} {
		t.Run(member, func(t *testing.T) {
			resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds()}

			resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{},
				referrer("prefix", map[string]any{
					"scope": map[string]any{member: map[string]any{"name": "west"}},
				}), prefixDescriptor())
			if err != nil {
				t.Fatalf("ResolveAll() = %v", err)
			}

			if len(resolution.Blocked) != 1 || resolution.Blocked[0].Field != "scope" {
				t.Fatalf("Blocked = %+v, want one blocker naming scope", resolution.Blocked)
			}

			if got := resolution.Blocked[0].Reason; got != netboxv1alpha1.ReasonRefKindUnavailable {
				t.Errorf("Reason = %q, want %q", got, netboxv1alpha1.ReasonRefKindUnavailable)
			}

			if len(resolution.ByField) != 0 {
				t.Errorf("ByField = %+v, want nothing resolved", resolution.ByField)
			}
		})
	}
}

// TestResolveScopeNotReady is the first-apply case: the NetBoxSite exists and has not been
// written to NetBox yet. A state rather than a failure -- no error is returned, so nothing
// backs off, and the ref watch on the target is what re-enqueues this object.
func TestResolveScopeNotReady(t *testing.T) {
	young := readySite()
	young.id = 0

	resolver := &Resolver{Objects: &fakeReader{objects: []target{young}}, Kinds: kinds()}

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{},
		referrer("prefix", map[string]any{
			"scope": map[string]any{"siteRef": map[string]any{"name": "hq"}},
		}), prefixDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v, want no error: not-ready is a state", err)
	}

	if len(resolution.Blocked) != 1 {
		t.Fatalf("Blocked = %+v, want one blocker", resolution.Blocked)
	}

	blocker := resolution.Blocked[0]
	if blocker.Reason != netboxv1alpha1.ReasonRefNotReady || blocker.Requeue != 0 {
		t.Errorf("blocker = %+v, want RefNotReady with no timer", blocker)
	}
}

// TestResolveScopeRefusesTwoMembers is defence in depth behind ScopeRef's CEL rule.
//
// The API server rejects two members, so this is only reachable by an object stored before
// the rule or a spec written past the schema. It matters anyway: the alternative to refusing
// is picking one of the two scopes the user asked for, silently, and a scope written to the
// wrong parent is exactly what this type exists to prevent.
func TestResolveScopeRefusesTwoMembers(t *testing.T) {
	resolver := &Resolver{
		Objects: &fakeReader{objects: []target{readyTarget(), readySite()}},
		Kinds:   kinds(),
	}
	nb := &fakeNetBox{}

	resolution, err := resolver.ResolveAll(context.Background(), nb,
		referrer("prefix", map[string]any{"scope": map[string]any{
			"regionRef": map[string]any{"name": "emea"},
			"siteRef":   map[string]any{"name": "hq"},
		}}), prefixDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	if len(resolution.ByField) != 0 {
		t.Fatalf("ByField = %+v, want nothing resolved", resolution.ByField)
	}

	if len(resolution.Blocked) != 1 || resolution.Blocked[0].Field != "scope" {
		t.Fatalf("Blocked = %+v, want one blocker naming scope", resolution.Blocked)
	}

	message := resolution.Message()
	if !strings.Contains(message, "scope.regionRef") || !strings.Contains(message, "scope.siteRef") {
		t.Errorf("Message() = %q, want it to name both members", message)
	}

	// Zero NetBox requests: there is nothing a read could settle, and a refused reference
	// must not cost a request per pass.
	if len(nb.calls) != 0 {
		t.Errorf("netbox calls = %+v, want none", nb.calls)
	}
}

// TestResolveScopeEmptyAndAbsent are the two shapes that are not references.
//
// Neither is resolved and neither is blocked. What separates them is what the payload
// builder does with them -- an empty union writes both columns as null, an absent one writes
// neither -- and that is asserted in internal/reconciler, where the payload is.
func TestResolveScopeEmptyAndAbsent(t *testing.T) {
	tests := map[string]map[string]any{
		"an empty union":          {"scope": map[string]any{}},
		"a union set to null":     {"scope": nil},
		"no scope field at all":   {"name": "192.0.2.0/24"},
		"a member explicitly nil": {"scope": map[string]any{"siteRef": nil}},
	}

	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			reader := &fakeReader{}
			resolver := &Resolver{Objects: reader, Kinds: kinds()}

			resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{},
				referrer("prefix", spec), prefixDescriptor())
			if err != nil {
				t.Fatalf("ResolveAll() = %v", err)
			}

			if len(resolution.ByField) != 0 || len(resolution.Blocked) != 0 {
				t.Errorf("resolution = %+v, want nothing resolved and nothing blocked", resolution)
			}

			if reader.reads != 0 {
				t.Errorf("cluster reads = %d, want 0", reader.reads)
			}
		})
	}
}

// TestScopeUnionIsWatched is what makes a scoped object converge on an event rather than on
// its resync: every member Kind has to be watched, so a NetBoxSite gaining an id re-enqueues
// the prefixes scoped to it.
//
// Derived from the descriptor's union members, so a new member is a data change and a new
// watch at once. Without it a prefix waiting on its site waits a full resync interval.
func TestScopeUnionIsWatched(t *testing.T) {
	targets := RefTargets(prefixDescriptor())

	for _, want := range []string{"NetBoxRegion", "NetBoxSiteGroup", "NetBoxSite", "NetBoxLocation"} {
		found := false

		for _, gvk := range targets {
			found = found || gvk.Kind == want
		}

		if !found {
			t.Errorf("RefTargets() = %v, want it to include %s", targets, want)
		}
	}
}

// TestScopeUnionIsIndexed is the other half of the same watch: the reverse edge has to be
// queryable, or the map function finds no referrers and the event changes nothing.
func TestScopeUnionIsIndexed(t *testing.T) {
	obj := referrer("prefix", map[string]any{
		"scope": map[string]any{"siteRef": map[string]any{"name": "hq", "namespace": "catalogue"}},
	})

	keys := refIndexer(prefixDescriptor())(obj)

	want := IndexValue(netboxv1alpha1.SiteRef{}.TargetGVK(), "catalogue", "hq")
	if len(keys) != 1 || keys[0] != want {
		t.Fatalf("index keys = %v, want [%s]", keys, want)
	}

	// A slug terminates in NetBox, where no Kubernetes event can arrive, so indexing one
	// would create a key nothing ever queries.
	slugged := referrer("prefix", map[string]any{
		"scope": map[string]any{"siteRef": map[string]any{"slug": "hq"}},
	})

	if keys := refIndexer(prefixDescriptor())(slugged); len(keys) != 0 {
		t.Errorf("index keys for a slug = %v, want none", keys)
	}
}

// readySite is a NetBoxSite the engine has already written to NetBox, for the union member
// whose Kind exists.
func readySite() target {
	return target{
		gvk: siteGVK, namespace: "team-a", name: "hq", id: 5,
		ready: metav1.ConditionTrue, reason: netboxv1alpha1.ReasonSynced,
	}
}

// prefixDescriptor is ipam.Prefix as far as the scope is concerned: one scalar and the
// polymorphic pair, and deliberately no `site` field of any kind.
func prefixDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxPrefix"),
		Endpoint:   "ipam/prefixes",
		ObjectType: "ipam.prefix",
		Fields:     []registry.Field{{Spec: "name", API: "name"}},
		GenericFKs: []registry.GenericFKSpec{registry.ScopeFK("scope")},
	}
}
