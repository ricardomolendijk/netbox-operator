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
		ByField: map[string]resolver.FieldRefs{},
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
	return resolver.Resolution{ByField: map[string]resolver.FieldRefs{
		field: {{ID: id, ObjectType: "extras.tag", Mode: resolver.ModeName}},
	}}
}

// clearedDescriptor declares its reference on a nullable column, the way a descriptor does
// for a spec field typed v1alpha1.OptionalRef.
func clearedDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	for i := range d.Fields {
		if d.Fields[i].Spec == "parentRef" {
			d.Fields[i].EmptyIsNull = true
		}
	}

	return d
}

// TestEmptyRefIsWrittenAsNull is the write half of #185.
//
// `parentRef: {}` on a nullable column means "explicitly no parent", and the difference
// between that and an omitted `parentRef` is the entire ticket: an omission means "do not
// manage the column" and leaves NetBox's value alone, while an empty reference is an
// instruction and has to arrive as null. A payload that merely *lacks* `parent` would read as
// success and leave the old parent in place -- the same silent no-op #170 fixed for a nullable
// scalar and #121 for a claimed field.
//
// The zero Result is what the resolver answers with for one (resolver.Resolve, and the case
// in resolver_test.go that pins it), so that is what this test hands the engine.
func TestEmptyRefIsWrittenAsNull(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{}

	nb := &fakeClient{created: liveTag(7)}
	resolution := resolver.Resolution{ByField: map[string]resolver.FieldRefs{"parentRef": {{}}}}
	engine := engineWith(t, clearedDescriptor(), nb, &fakeRefs{resolution: resolution})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	got, sent := payload["parent"]
	if !sent {
		t.Fatalf("payload = %v, want a `parent` key: an empty reference clears the column, and "+
			"leaving the key out means do not manage it", payload)
	}

	if got != nil {
		t.Errorf("payload[parent] = %v, want null", got)
	}

	// True, not False: the reference resolved. It resolved to no object, which is an answer
	// rather than a wait, and reporting it as unresolved would keep the object out of Ready
	// forever over a value that is already correct.
	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionTrue || resolved.Reason != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("RefsResolved = %s/%s, want True/%s",
			resolved.Status, resolved.Reason, netboxv1alpha1.ReasonAllResolved)
	}

	if ready := conditionOf(obj, netboxv1alpha1.ConditionReady); ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", ready.Status, ready.Reason)
	}
}

// TestUnresolvedRefKeepsTheObjectFromReadiness is issue #132, and the test is the deliverable.
// TestUnresolvedDeclaredRefWithholdsTheWrite is issue #195, and the test is the deliverable.
//
// The descriptor here keys on `slug` alone, so `parentRef` is *outside* the natural key and
// the engine could perfectly well create the object without it -- which is exactly what it
// used to do (this test asserted `[GETONE POST]` and a payload with no `parent`). That was
// never designed: on parentedDescriptor(), where the same reference happens to be part of the
// key, the identical failure wrote nothing. #195 answered it one way for both: a reference the
// spec declares is a precondition for the write.
//
// Issue #132's guarantee is still asserted below and is now free: an object nothing was
// written for cannot reach Ready either. Whichever way the write goes, `kubectl wait
// --for=condition=Ready` must not pass over a field NetBox never received.
func TestUnresolvedDeclaredRefWithholdsTheWrite(t *testing.T) {
	tests := []struct {
		name        string
		resolution  resolver.Resolution
		wantRefs    string
		wantRequeue time.Duration
	}{
		{
			// The first-apply case: the target CR exists and has not reconciled yet. No timer,
			// so the ref watch is what re-enqueues it, with the endpoint's resync as the backstop.
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

			// Not even a lookup: the decision is made before locate(), because the rule is
			// about the update as much as the create. An unscoped row in NetBox for the
			// length of an unimplemented Kind is the outcome #195 refused.
			if len(nb.calls) != 0 {
				t.Errorf("netbox calls = %v, want none: a declared reference did not resolve", nb.calls)
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

			// No Synced condition at all: nothing was compared and nothing was sent, so
			// claiming either state would be a report of a write that did not happen.
			if got := conditionOf(obj, netboxv1alpha1.ConditionSynced).Reason; got != "" {
				t.Errorf("Synced reason = %q, want none: nothing was written", got)
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

// TestUnresolvedIdentityRefWritesNothing is the same failure on a kind that keys on the
// reference, and it is here to pin the *uniformity* #195 asked for: the assertions below are
// now identical to the ones above, where they used to differ in both the calls made and the
// Ready reason.
//
// The reason moved with the rule -- WaitingForKey before #195, WaitingForRef now -- and that
// is the point rather than a side effect. "No usable natural key" was the symptom of an
// unresolved parent; the cause is the parent, and locate()'s errNoCandidate is no longer
// reached because the write is refused before the lookup.
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
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonWaitingForRef)
	}

	// RefsResolved carries which of the eight causes it was, and Ready carries the one
	// question a `kubectl wait` asks. Conflating them -- one reason for "the Kind has no
	// descriptor", "the target is not ready" and "there is nothing to point at" -- is what
	// #195 explicitly refused.
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
		LiveStatus:  &fakeLiveStatus{},
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

// TestResolvedGenericFKWritesBothColumns is the atomicity contract, asserted where it
// matters: on the payload NetBox receives.
//
// The type half and the id half are one reference. An id written against a stale type is not
// a partial update -- it points the object at a row of a different model that happens to
// share a primary key, which NetBox accepts without complaint. So the assertion is on both
// keys of the payload and never on the condition alone.
func TestResolvedGenericFKWritesBothColumns(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "ams"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{
			"scope": {{ID: 31, ObjectType: "dcim.site", Mode: resolver.ModeName}},
		},
	}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()
	if payload["scope_type"] != "dcim.site" || payload["scope_id"] != int64(31) {
		t.Errorf("payload (scope_type, scope_id) = (%v, %v), want (dcim.site, 31)",
			payload["scope_type"], payload["scope_id"])
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionTrue || !strings.Contains(resolved.Message, "scope") {
		t.Errorf("RefsResolved = %s/%q, want True naming scope", resolved.Status, resolved.Message)
	}
}

// TestEmptyGenericFKClearsBothColumns covers the union written and left empty, which is an
// instruction rather than an omission: clear the reference.
//
// Both columns are nulled, not one. NetBox validates the pair together, so a `scope_id` of
// null against a `scope_type` that still names a model is a rejected payload at best.
func TestEmptyGenericFKClearsBothColumns(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{"scope": {{}}},
	}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	for _, column := range []string{"scope_type", "scope_id"} {
		value, written := payload[column]
		if !written || value != nil {
			t.Errorf("payload[%s] = %v (written: %v), want an explicit null", column, value, written)
		}
	}
}

// TestClaimedButAbsentGenericFKWritesNeitherColumn is the guard against NBO-079 (#169)
// turning a union nobody wrote into a union written empty.
//
// #169 gives optional fields three states by reading `metadata.managedFields`: a spec field
// another manager has claimed but the spec no longer carries is restored to its Go **empty
// value** before the payload is built, so `description: ""` can clear a NetBox description.
// For a union the Go empty value is `{}` -- and an empty union is this file's instruction to
// *clear both columns*. Composed naively the two rules read "somebody else once mentioned
// scope, so detach the object", which nobody asked for and which no manifest states.
//
// It does not fire, and the reason is structural rather than lucky: #169 derives the empty
// form by reflection over the spec struct and produces one only for a slice, a map or a
// scalar. A struct and a pointer to one are deliberately excluded -- a nil pointer already
// marshals to `null`, which is a state of its own -- and a union member field is exactly a
// `*struct`. So there is no empty form to restore and the field stays absent.
//
// This test is what keeps that true, and it asserts both halves of "absent", because a
// regression could land as either one. Neither column is written, and RefsResolved is
// AllResolved: a union restored to `{}` would show up first as a *declared* reference this
// pass has to account for, and only then as two nulls on the wire.
func TestClaimedButAbsentGenericFKWritesNeitherColumn(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = nil

	// A Flux/SSA-shaped claim on the union field, which is what #169 reads.
	obj.ManagedFields = []metav1.ManagedFieldsEntry{{
		Manager:    "kustomize-controller",
		Operation:  metav1.ManagedFieldsOperationApply,
		APIVersion: fakeGVK.GroupVersion().String(),
		FieldsType: "FieldsV1",
		FieldsV1:   &metav1.FieldsV1{Raw: []byte(`{"f:spec":{"f:scope":{}}}`)},
	}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{},
	}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()
	for _, column := range []string{"scope_type", "scope_id"} {
		if value, written := payload[column]; written {
			t.Errorf("payload[%s] = %v, want the column absent: an unwritten union is not an empty one",
				column, value)
		}
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved).Reason; got != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("RefsResolved reason = %q, want %q: the spec declares no union to resolve",
			got, netboxv1alpha1.ReasonAllResolved)
	}
}

// TestRefusedGenericFKIsTerminalAndWritesNothingToTheColumns pins the refusal path: an illegal
// target is reported, both columns are left alone, and nothing comes back on a timer.
func TestRefusedGenericFKIsTerminalAndWritesNothingToTheColumns(t *testing.T) {
	obj := fakeObject()
	obj.Spec.Scope = &netboxv1alpha1.ScopeRef{SiteRef: &netboxv1alpha1.SiteRef{Name: "ams"}}

	err := &resolver.Error{
		Cause: resolver.ErrRefTypeNotAllowed, Field: "scope",
		Detail: `siteRef resolves to object type "dcim.site", and scope_type accepts only [dcim.region]`,
	}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, scopedDescriptor(), nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{},
		Blocked: []resolver.Blocker{{
			Field: "scope", Reason: resolver.Classify(err).Reason, Err: err,
		}},
	}})

	result, reconcileErr := engine.Reconcile(context.Background(), obj)
	if reconcileErr != nil {
		t.Fatalf("Reconcile() = %v", reconcileErr)
	}

	payload := nb.lastPayload()
	for _, column := range []string{"scope_type", "scope_id"} {
		if value, written := payload[column]; written {
			t.Errorf("payload[%s] = %v, want the column left alone", column, value)
		}
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Reason != netboxv1alpha1.ReasonRefTypeNotAllowed {
		t.Errorf("RefsResolved reason = %q, want %q", resolved.Reason, netboxv1alpha1.ReasonRefTypeNotAllowed)
	}

	// The endpoint's own resync, and nothing sooner: no NetBox object appearing and no CR
	// being created makes an illegal target legal.
	assertRequeue(t, result.RequeueAfter, testResync)
}
