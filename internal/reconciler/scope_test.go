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
// The real resolver, not a canned resolution, so the `app_label.model` string in the body is
// the one the target kind's own Descriptor carries.
func TestScopeReachesThePayloadAsAPair(t *testing.T) {
	siteGVK := netboxv1alpha1.SiteRef{}.TargetGVK()

	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopeUnionDescriptor(), nb, &resolver.Resolver{
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

// TestEmptyScopeIsWrittenAsNull is the other half of the pair's semantics.
//
// An omitted pair means "leave whatever NetBox holds", which is right for every other field
// and would make a scope impossible to *clear* through this API: set it once and it stays
// forever. So an empty union is an instruction rather than an omission, and both columns go
// out as null.
func TestEmptyScopeIsWrittenAsNull(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopeUnionDescriptor(), nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	for _, column := range []string{registry.ScopeTypeField, registry.ScopeIDField} {
		value, present := payload[column]
		if !present || value != nil {
			t.Errorf("payload[%s] = %v (present %v), want an explicit null", column, value, present)
		}
	}

	assertNoForbiddenKeys(t, payload)
}

// TestAbsentScopeWritesNeitherColumn keeps the clear from happening by accident.
//
// A spec that never mentions the scope is not asking for one to be removed -- spec omission
// means "do not manage this field", which is what lets the operator co-exist with a human
// editing the same object in the NetBox UI (docs/decisions/0005-gitops-coexistence.md).
func TestAbsentScopeWritesNeitherColumn(t *testing.T) {
	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopeUnionDescriptor(), nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), fakeObject()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	for _, column := range []string{registry.ScopeTypeField, registry.ScopeIDField} {
		if _, present := payload[column]; present {
			t.Errorf("payload holds %s, want an unmentioned scope to be left to netbox", column)
		}
	}
}

// TestScopeMoveIsOnePatch covers moving a prefix from a Region to a Site.
//
// Both columns go out even though the user changed one field, because NetBox validates the
// pair together: a `scope_id` sent without its `scope_type` is either rejected or, worse,
// interpreted against the type NetBox still holds -- which points the object at whatever
// Region happens to own that primary key.
func TestScopeMoveIsOnePatch(t *testing.T) {
	siteGVK := netboxv1alpha1.SiteRef{}.TargetGVK()

	obj := fakeObject()
	obj.Status.ID = 7
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}

	live := liveTag(7)
	live[registry.ScopeTypeField] = "dcim.region"
	live[registry.ScopeIDField] = float64(3)

	// The caches NetBox maintains come back on every read of a scoped object. They must be
	// read and never echoed: a diff that includes one is a PATCH that can never satisfy
	// itself.
	for _, column := range registry.ScopeCacheColumns() {
		live[column] = map[string]any{"id": float64(3), "name": "emea"}
	}

	nb := &fakeClient{get: live, patched: live}
	engine := engineWith(t, scopeUnionDescriptor(), nb, &resolver.Resolver{
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

// TestUnresolvedScopeWritesNoScopeAtAll is the not-ready state: the NetBoxSite exists and
// has no NetBox id yet.
//
// Neither column is written -- half a reference is worse than none -- and the object is kept
// off Ready so that a dropped scope cannot pass `kubectl wait --for=condition=Ready`.
func TestUnresolvedScopeWritesNoScopeAtAll(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "hq"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopeUnionDescriptor(), nb,
		&fakeRefs{resolution: blockedOnScope(resolver.ErrRefNotReady)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	for _, column := range []string{registry.ScopeTypeField, registry.ScopeIDField} {
		if _, present := payload[column]; present {
			t.Errorf("payload holds %s, want no half-written reference", column)
		}
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

// blockedOnScope is the resolution a scope waiting for its target produces.
func blockedOnScope(cause error) resolver.Resolution {
	err := refError("scope", cause)

	return resolver.Resolution{
		ByField: map[string]resolver.Result{},
		Blocked: []resolver.Blocker{{Field: "scope", Reason: resolver.Classify(err).Reason, Err: err}},
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

// scopeUnionDescriptor is the fake kind with the real scope union: the pair, its four legal
// targets, and its four caches in ReadOnly.
func scopeUnionDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.ReadOnly = append(slices.Clone(d.ReadOnly), registry.ScopeCacheColumns()...)
	d.GenericFKs = []registry.GenericFKSpec{registry.ScopeFK("scope")}
	d.ContainmentRef = "scope"

	return d
}

// scopeKinds is a real registry holding the scoped kind and the one union target whose CRD
// this build carries, so the object type written to `scope_type` is read off that target's
// own Descriptor rather than supplied by the test.
func scopeKinds(t *testing.T) *registry.Registry {
	t.Helper()

	reg := registry.New()

	for _, d := range []registry.Descriptor{scopeUnionDescriptor(), siteTargetDescriptor()} {
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
