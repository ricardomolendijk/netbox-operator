package reconciler

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// The real resolver is the second implementation of RefResolver, and the engine's own client
// is the second implementation of the resolver's NetBox half. Both asserted at compile time,
// because an interface with one implementation is a guess about a seam.
var (
	_ RefResolver           = (*resolver.Resolver)(nil)
	_ resolver.LookupClient = (NetBoxClient)(nil)
)

// fakeRefs is a resolver whose answer is canned, so a test states the resolution it is about
// rather than assembling a cluster to produce one.
type fakeRefs struct {
	resolution resolver.Resolution
	err        error
	calls      int
}

func (f *fakeRefs) ResolveAll(
	_ context.Context, _ resolver.LookupClient, _ client.Object, _ registry.Descriptor,
) (resolver.Resolution, error) {
	f.calls++

	return f.resolution, f.err
}

// blockedOnParent is a resolution that refused the one reference these tests declare, as the
// resolver would report it.
func blockedOnParent(cause error, requeue time.Duration) resolver.Resolution {
	err := refError("parentRef", cause)

	return resolver.Resolution{
		ByField: map[string]resolver.Result{},
		Blocked: []resolver.Blocker{{
			Field: "parentRef", Reason: resolver.Classify(err).Reason, Requeue: requeue, Err: err,
		}},
	}
}

// refError is a typed resolution failure of one cause, built the way the resolver builds one
// so that the engine's own classification is exercised rather than mimicked.
func refError(field string, cause error) error {
	return &resolver.Error{
		Cause: cause, Field: field, Mode: resolver.ModeName, TargetGVK: fakeGVK,
		Target: types.NamespacedName{Namespace: "team-a", Name: "europe"},
	}
}

// resolvedTo is a resolution that turned one reference into an id.
func resolvedTo(field string, id int64) resolver.Resolution {
	return resolver.Resolution{ByField: map[string]resolver.Result{
		field: {ID: id, ObjectType: "extras.tag", Mode: resolver.ModeName},
	}}
}

// TestUnresolvedRefKeepsTheObjectFromReadiness is issue #132, and the test is the deliverable.
//
// On a kind whose identity does not include the reference, the object *is* created -- spec
// omission means "do not manage" and a graph applied in any order has to make progress -- so
// the only thing standing between a dropped reference and a silent success is Ready. If this
// test ever passes with Ready=True, `kubectl apply` reports success, `kubectl wait
// --for=condition=Ready` passes, and NetBox never received the field.
func TestUnresolvedRefKeepsTheObjectFromReadiness(t *testing.T) {
	tests := []struct {
		name        string
		resolution  resolver.Resolution
		wantRefs    string
		wantRequeue time.Duration
	}{
		{
			// The first-apply case: the target CR exists and has not reconciled yet. No timer,
			// so the endpoint's resync is the retry until NBO-013's watch lands.
			name:        "a target that is not ready yet",
			resolution:  blockedOnParent(resolver.ErrRefNotReady, 0),
			wantRefs:    netboxv1alpha1.ReasonRefNotReady,
			wantRequeue: testResync,
		},
		{
			// A NetBox object that does not exist: nothing in Kubernetes will ever announce
			// it, so the resolver's own minute is the only thing that notices -- and it is
			// sooner than the resync, so it wins.
			name:        "a netbox object that does not exist",
			resolution:  blockedOnParent(resolver.ErrRefNotFound, time.Minute),
			wantRefs:    netboxv1alpha1.ReasonRefNotFound,
			wantRequeue: time.Minute,
		},
		{
			// Ten minutes is the resolver's answer for a state only a human clears, but the
			// endpoint resyncs in five and would have re-examined the object anyway.
			name:        "an ambiguous slug",
			resolution:  blockedOnParent(resolver.ErrRefAmbiguous, 10*time.Minute),
			wantRefs:    netboxv1alpha1.ReasonRefAmbiguous,
			wantRequeue: testResync,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := fakeObject()
			obj.Spec.ParentRef = &fakeRef{Name: "europe"}

			nb := &fakeClient{created: liveTag(7)}
			refs := &fakeRefs{resolution: tc.resolution}
			engine := engineWith(t, fakeDescriptor(), nb, refs)

			result, err := engine.Reconcile(context.Background(), obj)
			if err != nil {
				t.Fatalf("Reconcile() = %v, want no error: an unresolved reference is a state", err)
			}

			// Created, and without the reference: that is the recorded product decision.
			if got := nb.methods(); !slices.Equal(got, []string{"GETONE", "POST"}) {
				t.Errorf("netbox calls = %v, want the object to be created anyway", got)
			}

			if payload := nb.lastPayload(); payload["parent"] != nil {
				t.Errorf("payload = %v, want no parent: the reference did not resolve", payload)
			}

			ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
			if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForRef {
				t.Errorf("Ready = %s/%s, want False/%s: a dropped reference must not pass a readiness check",
					ready.Status, ready.Reason, netboxv1alpha1.ReasonWaitingForRef)
			}

			if !strings.Contains(ready.Message, "parentRef") {
				t.Errorf("Ready message = %q, want it to name the reference", ready.Message)
			}

			resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
			if resolved.Status != metav1.ConditionFalse || resolved.Reason != tc.wantRefs {
				t.Errorf("RefsResolved = %s/%s, want False/%s", resolved.Status, resolved.Reason, tc.wantRefs)
			}

			// The reference is why the object is not Ready; the write itself succeeded, and
			// saying otherwise would send the reader looking at NetBox.
			if got := conditionOf(obj, netboxv1alpha1.ConditionSynced).Reason; got != netboxv1alpha1.ReasonDriftCorrected {
				t.Errorf("Synced reason = %q, want %q", got, netboxv1alpha1.ReasonDriftCorrected)
			}

			assertRequeue(t, result.RequeueAfter, tc.wantRequeue)
		})
	}
}

// TestResolvedRefReachesTheObject is the other half: a reference that resolves is written, and
// the object is Ready. Without this the test above would pass on an engine that never resolves
// anything at all.
func TestResolvedRefReachesTheObject(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, fakeDescriptor(), nb, &fakeRefs{resolution: resolvedTo("parentRef", 42)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.lastPayload()["parent"]; got != int64(42) {
		t.Errorf("payload[parent] = %v, want 42: a resolved reference is written as its id", got)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready = %s/%s, want True/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonSynced)
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionTrue || resolved.Reason != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("RefsResolved = %s/%s, want True/%s",
			resolved.Status, resolved.Reason, netboxv1alpha1.ReasonAllResolved)
	}

	if !strings.Contains(resolved.Message, "parentRef") {
		t.Errorf("RefsResolved message = %q, want it to name what resolved", resolved.Message)
	}
}

// TestUnresolvedIdentityRefWritesNothing is the case #132 says was already correct, pinned so
// it stays that way: when the reference is part of the natural key, no candidate is applicable,
// so the engine cannot tell whether the object exists and must not write at all. Creating there
// would duplicate the object, and falling through to a candidate that omits the parent would
// adopt an unrelated one.
func TestUnresolvedIdentityRefWritesNothing(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	nb := &fakeClient{}
	engine := engineWith(t, parentedDescriptor(), nb,
		&fakeRefs{resolution: blockedOnParent(resolver.ErrRefNotReady, 0)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if len(nb.calls) != 0 {
		t.Errorf("netbox calls = %v, want none: identity cannot be established", nb.calls)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForKey {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonWaitingForKey)
	}

	// The reference is still reported, because "no usable key" is the symptom and the
	// unresolved parent is the cause.
	if got := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved).Reason; got != netboxv1alpha1.ReasonRefNotReady {
		t.Errorf("RefsResolved reason = %q, want %q", got, netboxv1alpha1.ReasonRefNotReady)
	}
}

// TestResolverFailureIsAFailure separates the two error shapes. A blocked reference is a state
// the engine reports; a resolver that could not decide -- the API server refusing, a spec that
// will not decode -- is a failure, and must not be reported as a reference the user should look
// at.
func TestResolverFailureIsAFailure(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	nb := &fakeClient{}
	engine := engineWith(t, fakeDescriptor(), nb, &fakeRefs{err: errors.New("the api server said no")})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v, want the failure reported on the object rather than returned", err)
	}

	if len(nb.calls) != 0 {
		t.Errorf("netbox calls = %v, want none: nothing was decided", nb.calls)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady).Reason; got != netboxv1alpha1.ReasonAPIError {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonAPIError)
	}
}

// TestResolvedRefBecomesTheNaturalKey is the end-to-end seam, with the real resolver over a
// fake cluster: dcim.Region is unique on `(parent, name)` and filters on `parent_id`, so the
// id a reference resolved to has to reach the lookup that decides between creating and
// adopting -- not only the payload.
//
// This is the case that makes NBO-011's NetBoxRegion work at all, and the one a resolver that
// only fed the payload would pass every other test without.
func TestResolvedRefBecomesTheNaturalKey(t *testing.T) {
	descriptor := parentedDescriptor()
	descriptor.Fields = withTarget(descriptor.Fields, "parentRef", fakeGVK)

	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	nb := &fakeClient{created: liveTag(7)}
	refs := &resolver.Resolver{
		Objects: fakeCluster{objects: []*unstructured.Unstructured{
			readyTarget(fakeGVK, "team-a", "europe", 42),
		}},
		Kinds: fakeDescriptors{descriptor: descriptor, registered: true},
	}

	engine := engineWith(t, descriptor, nb, refs)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	wantParams := netbox.Params{"parent_id": "42", "name": "Managed"}
	if got := nb.calls[0].params; !slices.Equal(sortedPairs(got), sortedPairs(wantParams)) {
		t.Errorf("lookup params = %v, want %v: the resolved id has to reach the natural key", got, wantParams)
	}

	if got := nb.lastPayload()["parent"]; got != int64(42) {
		t.Errorf("payload[parent] = %v, want 42", got)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady).Reason; got != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonSynced)
	}
}

// engineWith assembles an engine around one descriptor, one NetBox and one resolver.
func engineWith(t *testing.T, descriptor registry.Descriptor, nb NetBoxClient, refs RefResolver) *Engine {
	t.Helper()

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: descriptor, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        refs,
		Status:      &fakeStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}
}

// withTarget fills in the target Kind of one reference. The fake descriptors in this package
// predate typed aliases, and the resolver dispatches on the target rather than on the field
// name, so it needs one.
func withTarget(fields []registry.Field, spec string, gvk schema.GroupVersionKind) []registry.Field {
	out := slices.Clone(fields)
	for i := range out {
		if out[i].Spec == spec {
			out[i].Target = gvk
		}
	}

	return out
}

// fakeCluster is the cluster as the resolver reads it: unstructured objects, because the
// resolver has no per-kind types.
type fakeCluster struct {
	objects []*unstructured.Unstructured
}

func (f fakeCluster) Get(_ context.Context, key client.ObjectKey, obj client.Object, _ ...client.GetOption) error {
	live, ok := obj.(*unstructured.Unstructured)
	if !ok {
		return fmt.Errorf("fakeCluster was handed a %T", obj)
	}

	for _, candidate := range f.objects {
		if candidate.GroupVersionKind() != live.GroupVersionKind() ||
			candidate.GetNamespace() != key.Namespace || candidate.GetName() != key.Name {
			continue
		}

		live.Object = candidate.DeepCopy().Object

		return nil
	}

	return apierrors.NewNotFound(schema.GroupResource{Resource: live.GetKind()}, key.Name)
}

// readyTarget is a reference target the engine has already written to NetBox.
func readyTarget(gvk schema.GroupVersionKind, namespace, name string, id int64) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": namespace, "name": name},
		"status": map[string]any{
			"id": id,
			"conditions": []any{map[string]any{
				"type": netboxv1alpha1.ConditionReady, "status": string(metav1.ConditionTrue),
				"reason": netboxv1alpha1.ReasonSynced, "message": "netbox extras/tags/42 matches the spec",
			}},
		},
	}}
	obj.SetGroupVersionKind(gvk)

	return obj
}

// sortedPairs renders a param map as sorted `key=value` strings, so two lookups are comparable
// without depending on map order.
func sortedPairs(params netbox.Params) []string {
	pairs := make([]string, 0, len(params))
	for key, value := range params {
		pairs = append(pairs, key+"="+value)
	}

	slices.Sort(pairs)

	return pairs
}

// TestRefWait covers the requeue arithmetic on its own: the resolver's interval and the
// endpoint's resync are both real answers, and the sooner one wins.
func TestRefWait(t *testing.T) {
	tests := []struct {
		name    string
		wait    refWait
		resync  time.Duration
		wantOut time.Duration
	}{
		{
			name:    "no resolver interval falls back to the resync",
			wait:    refWait{message: "waiting"},
			resync:  testResync,
			wantOut: testResync,
		},
		{
			name:    "a sooner resolver interval wins",
			wait:    refWait{message: "waiting", requeue: time.Minute},
			resync:  testResync,
			wantOut: time.Minute,
		},
		{
			name:    "a later resolver interval loses to the resync",
			wait:    refWait{message: "waiting", requeue: 10 * time.Minute},
			resync:  testResync,
			wantOut: testResync,
		},
		{
			// driftMode: Off. Turning periodic drift checks off must not also turn off the one
			// retry that will ever resolve a NetBox-side reference.
			name:    "no resync leaves the resolver's interval",
			wait:    refWait{message: "waiting", requeue: time.Minute},
			wantOut: time.Minute,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.wait.wait(tc.resync); got != tc.wantOut {
				t.Errorf("wait(%s) = %s, want %s", tc.resync, got, tc.wantOut)
			}
		})
	}
}

// TestReferenceIsResolvedOncePerPass guards the cost: the resolver is asked once, not once per
// step that happens to need an id.
func TestReferenceIsResolvedOncePerPass(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	refs := &fakeRefs{resolution: resolvedTo("parentRef", 42)}
	engine := engineWith(t, fakeDescriptor(), &fakeClient{created: liveTag(7)}, refs)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if refs.calls != 1 {
		t.Errorf("ResolveAll called %d times, want once per pass", refs.calls)
	}
}

// TestNoResolverReportsRatherThanDrops is the wiring guard. An engine assembled without a
// resolver -- a test about something else, or a caller that forgot -- must not silently drop
// the reference: it reports every declared one as unresolved, which Ready then refuses to
// pass over.
func TestNoResolverReportsRatherThanDrops(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	engine := engineWith(t, fakeDescriptor(), &fakeClient{created: liveTag(7)}, nil)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved).Reason; got != netboxv1alpha1.ReasonNotImplemented {
		t.Errorf("RefsResolved reason = %q, want %q", got, netboxv1alpha1.ReasonNotImplemented)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady).Reason; got != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonWaitingForRef)
	}
}
