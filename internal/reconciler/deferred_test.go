package reconciler

import (
	"context"
	"maps"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// deferringDescriptor is dcim.Device's shape, reduced to the part NBO-015 is about:
// `primary_ip4` is a reference that cannot exist at create time by construction, so it is
// DeferAlways.
//
// It is not a natural-key field here, and cannot be: registry.ErrDeferredNaturalKey rejects
// that combination at boot, which is asserted against a real descriptor in
// internal/registry/deferred_test.go.
func deferringDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.Fields = append(d.Fields, registry.Field{Spec: "primaryIP4Ref", API: "primary_ip4", Ref: true})
	d.Deferred = []registry.DeferredField{{APIField: "primary_ip4", Mode: registry.DeferAlways}}

	return d
}

// conditionalDescriptor defers `parent` only when it does not resolve, which is the mode an
// MPTT kind has to use: stripping a resolved parent from the create would change the
// object's natural key from `(parent, name)` to `(name)`.
func conditionalDescriptor() registry.Descriptor {
	d := fakeDescriptor()
	d.Deferred = []registry.DeferredField{{APIField: "parent", Mode: registry.DeferIfUnresolved}}

	return d
}

// TestDeferredDescriptorsAreValid keeps the fixtures honest: proving the engine against a
// descriptor the registry would refuse to boot with proves nothing.
func TestDeferredDescriptorsAreValid(t *testing.T) {
	for _, d := range []registry.Descriptor{deferringDescriptor(), conditionalDescriptor()} {
		if err := d.Validate(); err != nil {
			t.Errorf("descriptor %+v: %v", d.Deferred, err)
		}
	}
}

// deferringObject is an object whose deferred reference is set.
func deferringObject() *fakeKind {
	obj := fakeObject()
	obj.Spec.PrimaryIP4Ref = &fakeRef{Name: "loopback0"}

	return obj
}

// resolvedRefs is a resolution that turned several references into ids.
func resolvedRefs(ids map[string]int64) resolver.Resolution {
	byField := make(map[string]resolver.Result, len(ids))
	for field, id := range ids {
		byField[field] = resolver.Result{ID: id, ObjectType: "ipam.ipaddress", Mode: resolver.ModeName}
	}

	return resolver.Resolution{ByField: byField}
}

// blockedOn is a resolution that refused one named reference.
func blockedOn(field string, cause error, requeue time.Duration) resolver.Resolution {
	err := refError(field, cause)

	return resolver.Resolution{
		ByField: map[string]resolver.Result{},
		Blocked: []resolver.Blocker{{
			Field: field, Reason: resolver.Classify(err).Reason, Requeue: requeue, Err: err,
		}},
	}
}

// netboxState is a NetBox that remembers what was written to it.
//
// A canned-response client cannot tell "the PATCH landed" from "the PATCH is being re-sent
// forever", and telling those apart is the whole of what this ticket has to prove. So this
// fake applies writes to its own state and serves reads from it, which is the only shape in
// which "reconcile ten more times and assert no further writes" means anything.
type netboxState struct {
	calls  []call
	object netbox.Object
	nextID int

	// nested are the columns read back as a nested object rather than as the id that was
	// written, which is how NetBox returns every foreign key. Declared per test because the
	// fake has no field map; it is what makes the no-further-writes assertion exercise
	// Drift's unwrapping rather than an accidental identity.
	nested map[string]bool

	// ignore are the columns a write silently drops, which is what NetBox does with a
	// read-only column a descriptor failed to declare (docs/netbox-schema.md, preamble).
	// The operator cannot tell such a write from a successful one, so the only defence is
	// what it does next.
	ignore map[string]bool

	patchErr  error
	patchFail int
}

func (n *netboxState) GetByID(_ context.Context, endpoint string, id int) (netbox.Object, error) {
	n.calls = append(n.calls, call{method: "GET", endpoint: endpoint, id: id})

	if n.object == nil {
		return nil, &netbox.NotFoundError{Endpoint: endpoint, ID: id}
	}

	return maps.Clone(n.object), nil
}

func (n *netboxState) GetOne(_ context.Context, endpoint string, params netbox.Params) (netbox.Object, error) {
	n.calls = append(n.calls, call{method: "GETONE", endpoint: endpoint, params: params})

	if n.object == nil {
		return nil, nil
	}

	// Through netbox.One rather than by returning the object directly, so the fake cannot
	// disagree with the client about what "exactly one match" means (NBO-074).
	return netbox.One(endpoint, params, []netbox.Object{maps.Clone(n.object)})
}

func (n *netboxState) Create(_ context.Context, endpoint string, payload netbox.Object) (netbox.Object, error) {
	n.calls = append(n.calls, call{method: "POST", endpoint: endpoint, payload: maps.Clone(payload)})

	n.nextID++
	n.object = netbox.Object{}
	n.apply(payload)
	n.object["id"] = float64(n.nextID)
	n.object["url"] = "https://netbox.invalid/api/" + endpoint + "/1/"

	return maps.Clone(n.object), nil
}

func (n *netboxState) Patch(_ context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error) {
	n.calls = append(n.calls, call{method: "PATCH", endpoint: endpoint, id: id, payload: maps.Clone(payload)})

	if n.patchFail > 0 {
		n.patchFail--

		return nil, n.patchErr
	}

	n.apply(payload)

	return maps.Clone(n.object), nil
}

func (n *netboxState) Delete(_ context.Context, endpoint string, id int) (netbox.Object, error) {
	n.calls = append(n.calls, call{method: "DELETE", endpoint: endpoint, id: id})
	n.object = nil

	return nil, nil
}

// apply merges a written payload into the stored object, in NetBox's read representation.
func (n *netboxState) apply(payload netbox.Object) {
	for field, value := range payload {
		if n.ignore[field] {
			continue
		}

		if n.nested[field] {
			n.object[field] = map[string]any{"id": float64(value.(int64))} //nolint:forcetypeassert // a resolved reference is always an int64 id

			continue
		}

		n.object[field] = value
	}
}

func (n *netboxState) methods() []string {
	out := make([]string, 0, len(n.calls))
	for _, c := range n.calls {
		out = append(out, c.method)
	}

	return out
}

// writes are the calls that changed NetBox, which is what the anti-hot-loop assertions count.
func (n *netboxState) writes() []call {
	out := make([]call, 0, len(n.calls))

	for _, c := range n.calls {
		if c.method == "POST" || c.method == "PATCH" || c.method == "DELETE" {
			out = append(out, c)
		}
	}

	return out
}

// deferringEngine wires an engine around a stateful NetBox and a canned resolution.
func deferringEngine(
	t *testing.T, d registry.Descriptor, nb NetBoxClient, refs RefResolver, status StatusWriter,
) *Engine {
	t.Helper()

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: d, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        refs,
		Status:      status,
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}
}

// TestDeferAlwaysIsStrippedThenPatched is the two passes, end to end, and the write count is
// the assertion.
//
// One POST without the field, one PATCH carrying only it, and then nothing at all however
// many times the object is reconciled. A third write would mean either the differ is
// comparing a field the create never sent -- a PATCH that can never satisfy its own diff --
// or the second pass re-sending a value NetBox already holds. Both are the hot loop
// docs/concepts/drift.md opens by warning about, and both would show up here as an extra
// call.
func TestDeferAlwaysIsStrippedThenPatched(t *testing.T) {
	obj := deferringObject()
	nb := &netboxState{nested: map[string]bool{"primary_ip4": true}}
	status := &fakeStatus{}
	engine := deferringEngine(t, deferringDescriptor(), nb,
		&fakeRefs{resolution: resolvedRefs(map[string]int64{"primaryIP4Ref": 42})}, status)

	// Pass one: the create, with the deferred column stripped.
	result, err := engine.Reconcile(context.Background(), obj)
	if err != nil {
		t.Fatalf("Reconcile() pass 1 = %v", err)
	}

	wantCreate := netbox.Object{"name": "Managed", "slug": "managed", "color": "9e9e9e"}
	if got := nb.writes()[0].payload; !reflect.DeepEqual(got, wantCreate) {
		t.Errorf("create payload = %v, want %v: primary_ip4 cannot be set at create time", got, wantCreate)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonDeferredFieldPending {
		t.Errorf("Ready after the create = %s/%s, want False/%s: the field is not in netbox yet",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonDeferredFieldPending)
	}

	if !strings.Contains(ready.Message, "primaryIP4Ref") {
		t.Errorf("Ready message = %q, want it to name the pending field", ready.Message)
	}

	if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"primaryIP4Ref"}) {
		t.Errorf("status.deferredPending = %v, want [primaryIP4Ref]", got)
	}

	// Soon, not at the resync: the value is in hand and only the object it belongs to was
	// missing, so making it wait ten minutes would land primary_ip4 ten minutes late.
	assertRequeue(t, result.RequeueAfter, deferredRetry)

	// Pass two: the deferred PATCH, carrying nothing else.
	result, err = engine.Reconcile(context.Background(), obj)
	if err != nil {
		t.Fatalf("Reconcile() pass 2 = %v", err)
	}

	writes := nb.writes()
	if len(writes) != 2 || writes[1].method != "PATCH" {
		t.Fatalf("calls = %v, want the create followed by one patch", nb.methods())
	}

	wantPatch := netbox.Object{"primary_ip4": int64(42)}
	if got := writes[1].payload; !reflect.DeepEqual(got, wantPatch) {
		t.Errorf("patch payload = %v, want %v: only the deferred column", got, wantPatch)
	}

	ready = conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready after the patch = %s/%s, want True/%s", ready.Status, ready.Reason,
			netboxv1alpha1.ReasonSynced)
	}

	if got := obj.Status.DeferredPending; got != nil {
		t.Errorf("status.deferredPending = %v, want none once the field is applied", got)
	}

	assertRequeue(t, result.RequeueAfter, testResync)

	// One more pass lets Synced settle from DriftCorrected to NoDrift, which is the last
	// status write this object will ever take -- the same settling
	// TestEngineReconcileIsIdempotent accounts for, one pass later here because there were
	// two writes rather than one.
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() settling pass = %v", err)
	}

	// And then nothing, forever. This is the assertion the ticket most needs.
	settled := status.writes

	for i := range 10 {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() settled pass %d = %v", i, err)
		}
	}

	if got := nb.writes(); len(got) != 2 {
		t.Errorf("writes after settling = %v, want exactly the create and the deferred patch",
			nb.methods())
	}

	if status.writes != settled {
		t.Errorf("status writes after settling = %d, want no more than the %d it took to settle",
			status.writes, settled)
	}
}

// TestDeferIfUnresolvedRidesTheCreate is the other mode, and the reason the two are not
// cosmetic.
//
// A resolved `parent` goes in the create. Stripping it would create the object as top-level,
// where the natural key the engine looked it up by -- `(parent, name)` -- no longer describes
// what was written, and the follow-up PATCH would reparent whatever the create adopted.
func TestDeferIfUnresolvedRidesTheCreate(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	nb := &netboxState{}
	engine := deferringEngine(t, conditionalDescriptor(), nb,
		&fakeRefs{resolution: resolvedRefs(map[string]int64{"parentRef": 7})}, &fakeStatus{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.writes()[0].payload["parent"]; got != int64(7) {
		t.Errorf("create payload[parent] = %v, want 7: a resolved conditional deferral is not deferred", got)
	}

	if got := obj.Status.DeferredPending; got != nil {
		t.Errorf("status.deferredPending = %v, want none: the field was written by the create", got)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", ready.Status, ready.Reason)
	}
}

// TestDeferIfUnresolvedWaitsWhenItDoesNotResolve is the same mode's other half: the object is
// still created, and the field is reported pending rather than dropped.
func TestDeferIfUnresolvedWaitsWhenItDoesNotResolve(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "europe"}

	nb := &netboxState{}
	engine := deferringEngine(t, conditionalDescriptor(), nb,
		&fakeRefs{resolution: blockedOn("parentRef", resolver.ErrRefNotReady, 0)}, &fakeStatus{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if _, sent := nb.writes()[0].payload["parent"]; sent {
		t.Errorf("create payload = %v, want no parent: the reference did not resolve", nb.writes()[0].payload)
	}

	if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"parentRef"}) {
		t.Errorf("status.deferredPending = %v, want [parentRef]", got)
	}

	// WaitingForRef rather than DeferredFieldPending: the engine has nothing to write, which
	// is a different problem from having it and not having sent it, and is fixed elsewhere.
	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason,
			netboxv1alpha1.ReasonWaitingForRef)
	}
}

// TestDeferredFieldThatNeverResolvesDoesNotSpin is the failure mode a deferred field makes
// possible: a `primary_ip4` whose address is never created.
//
// The object stays Ready=False forever, on purpose -- `kubectl wait --for=condition=Ready`
// failing is the correct outcome for an object missing a field the user asked for. What must
// not happen is a retry storm or a write per pass, and the object has to keep saying what it
// is waiting for rather than merely that it is not ready.
func TestDeferredFieldThatNeverResolvesDoesNotSpin(t *testing.T) {
	obj := deferringObject()
	nb := &netboxState{}
	status := &fakeStatus{}
	engine := deferringEngine(t, deferringDescriptor(), nb,
		// A NetBox address that does not exist: nothing in Kubernetes will ever announce it,
		// so the resolver's own minute is the only thing that comes back for it.
		&fakeRefs{resolution: blockedOn("primaryIP4Ref", resolver.ErrRefNotFound, time.Minute)}, status)

	var settled int

	for i := range 12 {
		result, err := engine.Reconcile(context.Background(), obj)
		if err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}

		assertRequeue(t, result.RequeueAfter, time.Minute)

		if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"primaryIP4Ref"}) {
			t.Fatalf("status.deferredPending on pass %d = %v, want [primaryIP4Ref] on every pass", i, got)
		}

		ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
		if ready.Status != metav1.ConditionFalse {
			t.Fatalf("Ready on pass %d = %s/%s, want False forever", i, ready.Status, ready.Reason)
		}

		if !strings.Contains(ready.Message, "primaryIP4Ref") {
			t.Fatalf("Ready message on pass %d = %q, want it to name what is missing", i, ready.Message)
		}

		if i == 1 {
			settled = status.writes
		}
	}

	if got := nb.writes(); len(got) != 1 || got[0].method != "POST" {
		t.Errorf("writes = %v, want only the create: there is nothing to patch", nb.methods())
	}

	if status.writes != settled {
		t.Errorf("status writes = %d, want no more than the %d it took to settle: a permanent wait must not churn",
			status.writes, settled)
	}
}

// TestDeferredPatchFailureIsRetried is the case where the target resolves and NetBox refuses
// the write. The object must stay pending and the next pass must try again -- a deferred field
// that is dropped after one failed PATCH is the silent omission with extra steps.
func TestDeferredPatchFailureIsRetried(t *testing.T) {
	obj := deferringObject()
	nb := &netboxState{
		nested:    map[string]bool{"primary_ip4": true},
		patchErr:  &netbox.TransientError{Status: 503},
		patchFail: 1,
	}
	engine := deferringEngine(t, deferringDescriptor(), nb,
		&fakeRefs{resolution: resolvedRefs(map[string]int64{"primaryIP4Ref": 42})}, &fakeStatus{})

	// The create, then a PATCH NetBox refuses.
	for i := range 2 {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonAPIError {
		t.Errorf("Ready after the refused patch = %s/%s, want False/%s",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonAPIError)
	}

	// The pending list survives the failure: it is what the next pass is for, and clearing it
	// would report an object that is missing the field as done with it.
	if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"primaryIP4Ref"}) {
		t.Errorf("status.deferredPending after the refused patch = %v, want [primaryIP4Ref]", got)
	}

	// The retry.
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() retry = %v", err)
	}

	writes := nb.writes()
	if len(writes) != 3 || writes[2].method != "PATCH" {
		t.Fatalf("calls = %v, want the create, the refused patch and the retry", nb.methods())
	}

	if got := writes[2].payload; !reflect.DeepEqual(got, netbox.Object{"primary_ip4": int64(42)}) {
		t.Errorf("retried patch payload = %v, want only the deferred column", got)
	}

	ready = conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready after the retry = %s/%s, want True/%s",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonSynced)
	}

	if got := obj.Status.DeferredPending; got != nil {
		t.Errorf("status.deferredPending = %v, want none", got)
	}
}

// TestDeferredFieldRemovedWhilePendingIsNoLongerPending is the user changing their mind. A
// deferral for a field the spec no longer declares is not pending and must not hold the object
// out of readiness -- spec omission means "do not manage", and NetBox never received the value
// so there is nothing to undo either.
func TestDeferredFieldRemovedWhilePendingIsNoLongerPending(t *testing.T) {
	obj := deferringObject()
	nb := &netboxState{nested: map[string]bool{"primary_ip4": true}}
	engine := deferringEngine(t, deferringDescriptor(), nb,
		&fakeRefs{resolution: blockedOn("primaryIP4Ref", resolver.ErrRefNotFound, time.Minute)}, &fakeStatus{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() pass 1 = %v", err)
	}

	if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"primaryIP4Ref"}) {
		t.Fatalf("status.deferredPending = %v, want [primaryIP4Ref] before the edit", got)
	}

	obj.Spec.PrimaryIP4Ref = nil
	obj.Generation++

	result, err := engine.Reconcile(context.Background(), obj)
	if err != nil {
		t.Fatalf("Reconcile() pass 2 = %v", err)
	}

	if got := obj.Status.DeferredPending; got != nil {
		t.Errorf("status.deferredPending = %v, want none once the field is gone from the spec", got)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready = %s/%s, want True/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonSynced)
	}

	if got := nb.writes(); len(got) != 1 {
		t.Errorf("writes = %v, want only the create: removing a field that was never written writes nothing",
			nb.methods())
	}

	assertRequeue(t, result.RequeueAfter, testResync)
}

// TestDryRunReportsWhatIsDeferred is docs/concepts/object-lifecycle.md's commitment that the
// pending list is computed from references rather than from writes: a rehearsal endpoint has to
// answer "what would this object still be waiting for" as well as a live one.
func TestDryRunReportsWhatIsDeferred(t *testing.T) {
	obj := deferringObject()
	nb := &fakeClient{dryRun: dryRunClient(t)}
	engine := deferringEngine(t, deferringDescriptor(), nb,
		&fakeRefs{resolution: resolvedRefs(map[string]int64{"primaryIP4Ref": 42})}, &fakeStatus{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"primaryIP4Ref"}) {
		t.Errorf("status.deferredPending = %v, want [primaryIP4Ref]", got)
	}

	// DryRunPending rather than DeferredFieldPending: nothing was written at all, and a reason
	// naming the deferral would send the reader looking at the reference instead of at
	// `mode: DryRun`.
	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonDryRunPending {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason,
			netboxv1alpha1.ReasonDryRunPending)
	}
}

// TestDeferralCreatePayloadLeavesDesiredIntact pins the asymmetry the whole design rests on:
// the strip is a copy, and the desired payload every later pass diffs against still holds the
// field. Strip both and the field is never written at all; strip only the request while diffing
// with it present and the diff can never be satisfied.
func TestDeferralCreatePayloadLeavesDesiredIntact(t *testing.T) {
	d := deferringDescriptor()
	desired := netbox.Object{"name": "Managed", "primary_ip4": int64(42)}
	state := registry.SpecState{Declared: []string{"name", "primaryIP4Ref"}}

	payload, stripped := newDeferral(d, state, desired).createPayload(desired)

	if !slices.Equal(stripped, []string{"primary_ip4"}) {
		t.Errorf("stripped = %v, want [primary_ip4]", stripped)
	}

	if _, sent := payload["primary_ip4"]; sent {
		t.Errorf("create payload = %v, want the deferred column removed", payload)
	}

	if got := desired["primary_ip4"]; got != int64(42) {
		t.Errorf("desired[primary_ip4] = %v, want 42 still: the comparison basis must keep the field", got)
	}
}

// TestDeferralPending is the pendingness rule on its own, including the shape that makes it
// non-trivial: NetBox returns a foreign key as a nested object and takes it as an id, so an
// applied field only looks applied through the differ.
func TestDeferralPending(t *testing.T) {
	tests := []struct {
		name     string
		declared []string
		desired  netbox.Object
		live     netbox.Object
		want     []string
	}{
		{
			name:     "not declared is not pending",
			declared: []string{"name"},
			desired:  netbox.Object{"name": "Managed"},
			live:     netbox.Object{"name": "Managed"},
		},
		{
			name:     "declared and unresolved is pending",
			declared: []string{"primaryIP4Ref"},
			desired:  netbox.Object{},
			live:     netbox.Object{"id": float64(7)},
			want:     []string{"primaryIP4Ref"},
		},
		{
			name:     "resolved with nothing in netbox to hold it is pending",
			declared: []string{"primaryIP4Ref"},
			desired:  netbox.Object{"primary_ip4": int64(42)},
			live:     nil,
			want:     []string{"primaryIP4Ref"},
		},
		{
			name:     "resolved and absent from the live object is pending",
			declared: []string{"primaryIP4Ref"},
			desired:  netbox.Object{"primary_ip4": int64(42)},
			live:     netbox.Object{"id": float64(7)},
			want:     []string{"primaryIP4Ref"},
		},
		{
			// The one that would loop if it were compared by equality: NetBox read this back
			// as a nested object, and it is applied.
			name:     "read back as a nested object is applied",
			declared: []string{"primaryIP4Ref"},
			desired:  netbox.Object{"primary_ip4": int64(42)},
			live:     netbox.Object{"primary_ip4": map[string]any{"id": float64(42), "display": "10.0.0.1/32"}},
		},
		{
			name:     "pointing somewhere else is pending",
			declared: []string{"primaryIP4Ref"},
			desired:  netbox.Object{"primary_ip4": int64(42)},
			live:     netbox.Object{"primary_ip4": map[string]any{"id": float64(9)}},
			want:     []string{"primaryIP4Ref"},
		},
	}

	d := deferringDescriptor()

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			deferral := newDeferral(d, registry.SpecState{Declared: tc.declared}, tc.desired)

			got := deferral.pending(tc.live, tc.desired, fieldRules(d))
			if !slices.Equal(got, tc.want) {
				t.Errorf("pending() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestSilentlyIgnoredDeferredPatchLoopsAtTheResync is the safety property behind the short
// requeue between the two passes.
//
// Five seconds is right exactly once -- after the create, where the value is in hand and only
// the object was missing. It must not become the interval of a standing failure: a PATCH NetBox
// accepts and silently ignores leaves the field pending forever, and at five seconds that is a
// hot loop against the API rather than the once-per-resync one an undeclared read-only column
// already costs.
func TestSilentlyIgnoredDeferredPatchLoopsAtTheResync(t *testing.T) {
	obj := deferringObject()
	nb := &netboxState{ignore: map[string]bool{"primary_ip4": true}}
	engine := deferringEngine(t, deferringDescriptor(), nb,
		&fakeRefs{resolution: resolvedRefs(map[string]int64{"primaryIP4Ref": 42})}, &fakeStatus{})

	result, err := engine.Reconcile(context.Background(), obj)
	if err != nil {
		t.Fatalf("Reconcile() pass 1 = %v", err)
	}

	assertRequeue(t, result.RequeueAfter, deferredRetry)

	// Every pass after the create: the PATCH goes out, NetBox keeps not holding the value, and
	// the object comes back on the endpoint's own interval rather than in five seconds.
	for i := range 3 {
		result, err = engine.Reconcile(context.Background(), obj)
		if err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i+2, err)
		}

		assertRequeue(t, result.RequeueAfter, testResync)

		ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
		if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonDeferredFieldPending {
			t.Fatalf("Ready on pass %d = %s/%s, want False/%s", i+2, ready.Status, ready.Reason,
				netboxv1alpha1.ReasonDeferredFieldPending)
		}

		if got := obj.Status.DeferredPending; !slices.Equal(got, []string{"primaryIP4Ref"}) {
			t.Fatalf("status.deferredPending on pass %d = %v, want [primaryIP4Ref]", i+2, got)
		}
	}
}
