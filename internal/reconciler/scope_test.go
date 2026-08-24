package reconciler

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// NetBox's scope pair as the engine sees it. The mechanism itself -- both columns or
// neither, empty-clears, absent-does-not, a refused target -- is covered once, over the same
// descriptor, in refs_test.go. What is left here is what is true of *scope* and of no other
// union: the columns that must never appear in a request body, and the round trip through the
// real resolver that proves `scope_type` is spelled by the target's own Descriptor.

// forbiddenScopeKeys are the columns that must never appear in a request body for a scoped
// kind, and the reason this file exists.
//
// `site` is the populator's bug: NetBox 4.2 moved the foreign key to `(scope_type,
// scope_id)`, and a `site` key is not rejected -- it is *ignored*, so the object reports
// itself synced while carrying no scope. The four underscore-prefixed columns are the
// denormalised caches NetBox maintains from the pair; writing one is a PATCH NetBox drops on
// every resync, forever (docs/netbox-schema.md -> dcim.CachedScopeMixin).
var forbiddenScopeKeys = append([]string{"site", "site_id"}, registry.ScopeCacheColumns()...)

// TestScopeReachesThePayloadAsAPair is the acceptance criterion of NBO-018: a scope that
// resolves has to arrive in the *request body* as both columns.
//
// Asserted on the body and not on a condition, deliberately. A resolved scope that never
// reaches the payload is the exact failure this ticket exists to prevent, and it is
// invisible from RefsResolved=True: the operator would report every reference resolved and
// send NetBox an unscoped prefix.
//
// The real resolver, not a canned resolution, which is what this adds over
// TestResolvedGenericFKWritesBothColumns: the `app_label.model` string in the body has to be
// the one the *target* kind's own Descriptor carries, and a canned Result asserts only that
// the engine copies whatever it was handed.
func TestScopeReachesThePayloadAsAPair(t *testing.T) {
	siteGVK := netboxv1alpha1.SiteRef{}.TargetGVK()

	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &resolver.Resolver{
		Objects: fakeCluster{objects: []*unstructured.Unstructured{
			readyTarget(siteGVK, "team-a", "hq", 5),
		}},
		Kinds: scopeKinds(t),
	})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	if got := payload[registry.ScopeTypeField]; got != "dcim.site" {
		t.Errorf("payload[scope_type] = %v, want dcim.site", got)
	}

	if got := payload[registry.ScopeIDField]; got != int64(5) {
		t.Errorf("payload[scope_id] = %v, want 5", got)
	}

	assertNoForbiddenKeys(t, payload)

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", got.Status, got.Reason)
	}
}

// TestScopeMoveIsOnePatch covers moving a prefix from a Region to a Site, which is the only
// path on which NetBox hands the caches *back*.
//
// Both columns go out even though the user changed one field, because NetBox validates the
// pair together: a `scope_id` sent without its `scope_type` is either rejected or, worse,
// interpreted against the type NetBox still holds -- which points the object at whatever
// Region happens to own that primary key. And the four caches that come back on the read must
// not be echoed: a diff that includes one is a PATCH that can never satisfy itself.
func TestScopeMoveIsOnePatch(t *testing.T) {
	siteGVK := netboxv1alpha1.SiteRef{}.TargetGVK()

	obj := fakeObject()
	obj.Status.ID = 7
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}

	live := liveTag(7)
	live[registry.ScopeTypeField] = "dcim.region"
	live[registry.ScopeIDField] = float64(3)

	for _, column := range registry.ScopeCacheColumns() {
		live[column] = map[string]any{"id": float64(3), "name": "emea"}
	}

	nb := &fakeClient{get: live, patched: live}
	engine := engineWith(t, scopedDescriptor(), nb, &resolver.Resolver{
		Objects: fakeCluster{objects: []*unstructured.Unstructured{
			readyTarget(siteGVK, "team-a", "hq", 5),
		}},
		Kinds: scopeKinds(t),
	})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	if got := payload[registry.ScopeTypeField]; got != "dcim.site" {
		t.Errorf("patch[scope_type] = %v, want dcim.site", got)
	}

	if got := payload[registry.ScopeIDField]; got != int64(5) {
		t.Errorf("patch[scope_id] = %v, want 5", got)
	}

	// Exactly the pair: a scope move is one change, so nothing else may ride along.
	if len(payload) != 2 {
		t.Errorf("patch = %v, want only the pair", payload)
	}

	assertNoForbiddenKeys(t, payload)
}

// TestUnresolvedDeclaredScopeWritesNothingAtAll is issue #195 on the shape that made it a
// question: a kind whose identity is one scalar, so it is perfectly creatable without the
// scope -- ipam.Prefix, unique on `prefix`.
//
// It used to be creatable *and created*, with the pair omitted and RefsResolved=False naming
// the field, which left an unscoped prefix in NetBox for as long as the target was missing.
// #195 answered it: a reference the spec declares is a precondition for the write, so nothing
// is sent at all.
//
// Asserted on the recorded traffic and not on the conditions. "Neither column is in the
// payload" was the old assertion and it now passes vacuously against a nil payload, which
// would let a regression that creates the object again slip straight through.
//
// Distinct from TestRefusedGenericFKIsTerminalAndWritesNothingToTheColumns, which is the
// target that will never become legal; this one clears itself on a watch event.
func TestUnresolvedDeclaredScopeWritesNothingAtAll(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}

	err := refError("scope", resolver.ErrRefNotReady)
	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{},
		Blocked: []resolver.Blocker{{
			Field: "scope", Reason: resolver.Classify(err).Reason, Err: err,
		}},
	}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(nb.calls) != 0 {
		t.Errorf("netbox calls = %v, want none: the spec declares a scope that did not resolve", nb.calls)
	}

	if obj.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: nothing was created", obj.Status.ID)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved); got.Status != metav1.ConditionFalse ||
		got.Reason != netboxv1alpha1.ReasonRefNotReady {
		t.Errorf("RefsResolved = %s/%s, want False/%s", got.Status, got.Reason, netboxv1alpha1.ReasonRefNotReady)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady); got.Status != metav1.ConditionFalse ||
		got.Reason != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready = %s/%s, want False/%s", got.Status, got.Reason, netboxv1alpha1.ReasonWaitingForRef)
	}
}

// TestUndeclaredScopeIsCreatedImmediately is the other half of #195, and the half that keeps
// the rule from being "wait for every reference": *declared* is the precondition, not
// *possible*.
//
// A prefix with no `scope` key at all is created on the first pass, with neither column in the
// body and with no reference to wait for. Without this, option C would be indistinguishable
// from option B -- one unimplemented Kind blocking every object that could have referenced it
// -- and an optional field would have become a required one.
func TestUndeclaredScopeIsCreatedImmediately(t *testing.T) {
	obj := fakeObject()

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); !slices.Equal(got, []string{"GETONE", "POST"}) {
		t.Errorf("netbox calls = %v, want [GETONE POST]: an absent reference is not a precondition", got)
	}

	payload := nb.lastPayload()

	// Absent means "do not manage this reference", so neither column is written -- as
	// opposed to a scope written empty, which clears both (docs/reference/genericref.md).
	for _, column := range []string{registry.ScopeTypeField, registry.ScopeIDField} {
		if _, present := payload[column]; present {
			t.Errorf("payload holds %s, want an absent scope left alone", column)
		}
	}

	assertNoForbiddenKeys(t, payload)

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", got.Status, got.Reason)
	}
}

// assertNoForbiddenKeys is the regression guard the acceptance criteria ask for by name.
func assertNoForbiddenKeys(t *testing.T, payload netbox.Object) {
	t.Helper()

	for _, key := range forbiddenScopeKeys {
		if _, present := payload[key]; present {
			t.Errorf("payload holds %q: a scoped kind writes (scope_type, scope_id) and nothing else", key)
		}
	}
}

// scopeKinds is a real registry holding the scoped kind and the one union target whose CRD
// this build carries, so the object type written to `scope_type` is read off that target's own
// Descriptor rather than supplied by the test.
func scopeKinds(t *testing.T) *registry.Registry {
	t.Helper()

	reg := registry.New()

	for _, d := range []registry.Descriptor{scopedDescriptor(), siteTargetDescriptor()} {
		if err := reg.Add(d); err != nil {
			t.Fatalf("registering %s: %v", d.GVK, err)
		}
	}

	return reg
}

// siteTargetDescriptor carries the one fact the resolver reads off a union target: the
// `app_label.model` spelling to write into the pair's type column.
func siteTargetDescriptor() registry.Descriptor {
	return registry.Descriptor{
		GVK:        netboxv1alpha1.SiteRef{}.TargetGVK(),
		Endpoint:   "dcim/sites",
		ObjectType: "dcim.site",
	}
}
