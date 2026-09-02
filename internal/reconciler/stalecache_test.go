package reconciler

import (
	"context"
	"slices"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Issue #252: the engine reconciles the copy of a CR an informer cache handed it, and a second
// reconcile of one key can begin before that cache has caught up with the status write the
// first one made. The second pass then reads `status.id == 0`, falls through to the natural
// key, and finds the object the operator itself created milliseconds earlier.
//
// Two things came of that, and both are asserted here: the pass reported `Conflict` on an
// object nothing else had ever touched -- advising `spec.onConflict: Adopt`, which on a shared
// catalogue kind is the state docs/reference/netboxtenantgroup.md warns against -- and its
// status write was then refused as stale and counted as a failed reconcile. A 17-object graph
// that converged correctly produced 18 error reconciles and 27 error lines.
//
// The cases that must *not* change are here too, because the fix is one live read away from
// masking the refusal it is narrowing: a natural key that matched somebody else's object is
// still a Conflict, and adopting one is still an adoption.

// staleStatus is what the API server holds for an object whose status write has landed while
// the cache the pass read from has not caught up.
func staleStatus(id int64) *netboxv1alpha1.NetBoxObjectStatus {
	return &netboxv1alpha1.NetBoxObjectStatus{ID: id}
}

func TestStaleCacheIsNotAForeignNetBoxObject(t *testing.T) {
	tests := []struct {
		name   string
		object func() *fakeKind

		// live is the status the API server holds. Nil means it agrees with the copy the pass
		// was handed, which is every case that is not about a stale read.
		live    *netboxv1alpha1.NetBoxObjectStatus
		liveErr error

		client *fakeClient

		wantErr     bool
		wantMethods []string
		wantID      int64
		wantAdopted bool
		wantReady   metav1.ConditionStatus
		wantReason  string
		wantEvents  []string
		wantResult  string
	}{
		{
			// The bug. `status.id` is written by this operator for this CR and by nothing
			// else, so an id the API server records there and the natural key has just matched
			// is one object seen twice.
			name:        "an object the natural key matched under this object's own id is its own",
			object:      fakeObject,
			live:        staleStatus(1),
			client:      &fakeClient{list: []netbox.Object{liveTag(1)}},
			wantMethods: []string{"GETONE"},
			wantID:      1,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantResult:  metrics.ResultUnchanged,
		},
		{
			// Unchanged, and the reason the check compares ids rather than merely noticing
			// that the cached copy was behind: this object's own id is 9 and the key matched
			// 1, so the match is somebody else's whatever the cache knew.
			name:        "a foreign object matching the natural key is still refused",
			object:      fakeObject,
			live:        staleStatus(0),
			client:      &fakeClient{list: []netbox.Object{liveTag(9)}},
			wantMethods: []string{"GETONE"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantEvents:  []string{"Warning/Conflict"},
			wantResult:  metrics.ResultError,
		},
		{
			// The path that would break if staleness were inferred from "the API server holds
			// an id and this pass did not". NetBox lost id 9, locate() cleared it, and the key
			// then matched an unrelated object -- so the API server's own id is the id this
			// pass read, and the refusal is real.
			name: "an object netbox lost, whose key now matches another, is still refused",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Status.ID = 9

				return obj
			},
			live:        staleStatus(9),
			client:      &fakeClient{getErr: &netbox.NotFoundError{Endpoint: "extras/tags", ID: 9}, list: []netbox.Object{liveTag(1)}},
			wantMethods: []string{"GET", "GETONE"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantEvents:  []string{"Warning/Conflict"},
			wantResult:  metrics.ResultError,
		},
		{
			name: "adopting somebody else's object is still an adoption",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.OnConflict = netboxv1alpha1.ConflictAdopt

				return obj
			},
			live:        staleStatus(0),
			client:      &fakeClient{list: []netbox.Object{liveTag(9)}},
			wantMethods: []string{"GETONE"},
			wantID:      9,
			wantAdopted: true,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantEvents:  []string{"Normal/Adopted"},
			wantResult:  metrics.ResultUnchanged,
		},
		{
			// The same object, adopted by an earlier pass whose write this one has not seen
			// yet. Nothing is adopted twice: no second Event, and status.adopted comes from
			// the pass that established the id rather than from this one.
			name: "an object an earlier pass already adopted is not adopted again",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.OnConflict = netboxv1alpha1.ConflictAdopt

				return obj
			},
			live:        &netboxv1alpha1.NetBoxObjectStatus{ID: 9, Adopted: true},
			client:      &fakeClient{list: []netbox.Object{liveTag(9)}},
			wantMethods: []string{"GETONE"},
			wantID:      9,
			wantAdopted: true,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantResult:  metrics.ResultUnchanged,
		},
		{
			// A live read that did not answer leaves the engine unable to tell the two apart,
			// and the one thing it must not do is guess. Returned as an error so
			// controller-runtime retries it, exactly as it does a failed status write.
			name:        "a live read that fails decides nothing",
			object:      fakeObject,
			liveErr:     errLiveStatusRead,
			client:      &fakeClient{list: []netbox.Object{liveTag(9)}},
			wantErr:     true,
			wantMethods: []string{"GETONE"},
			wantReady:   "",
			wantResult:  metrics.ResultError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := tc.object()
			status, events := &fakeStatus{}, &fakeRecorder{}
			live := &fakeLiveStatus{status: tc.live, err: tc.liveErr}
			reconciles := watch(t, metrics.ReconcileTotal, labelSets(fakeGVK.Kind, results)...)

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: tc.client, Resync: testResync},
					ready:    true,
				},
				Status:     status,
				LiveStatus: live,
				Finalizers: &fakeFinalizers{},
				Events:     events,
				Scheme:     fakeScheme(t),
			}

			_, err := engine.Reconcile(context.Background(), obj)
			if (err != nil) != tc.wantErr {
				t.Fatalf("Reconcile() = %v, want an error: %v", err, tc.wantErr)
			}

			if live.reads != 1 {
				t.Errorf("live status reads = %d, want exactly 1: the adoption question is"+
					" the one decision that does not trust the cache", live.reads)
			}

			if got := tc.client.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Errorf("netbox calls = %v, want %v", got, tc.wantMethods)
			}

			if obj.Status.ID != tc.wantID {
				t.Errorf("status.id = %d, want %d", obj.Status.ID, tc.wantID)
			}

			if obj.Status.Adopted != tc.wantAdopted {
				t.Errorf("status.adopted = %v, want %v", obj.Status.Adopted, tc.wantAdopted)
			}

			ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
			if ready.Status != tc.wantReady || ready.Reason != tc.wantReason {
				t.Errorf("Ready = %s/%s, want %s/%s", ready.Status, ready.Reason, tc.wantReady, tc.wantReason)
			}

			if !slices.Equal(events.events, tc.wantEvents) {
				t.Errorf("events = %v, want %v", events.events, tc.wantEvents)
			}

			assertOneResult(t, reconciles, fakeGVK.Kind, tc.wantResult)
		})
	}
}

// TestStatusWriteThatLostARaceIsNotAnError is issue #252's second half, reproduced with the
// first: a pass whose cached copy was behind is a pass whose resourceVersion is behind too, so
// its status write is refused by the API server.
//
// That is the ordinary outcome of reading from a cache and the retry is a requeue away, so it
// must not be an `error` line, a failed reconcile in the metric, or an error returned into
// controller-runtime's backoff. A write refused for any other reason still is all three --
// TestEngineReconcileStatusWriteFails is the other side of this.
func TestStatusWriteThatLostARaceIsNotAnError(t *testing.T) {
	obj := fakeObject()
	client := &fakeClient{list: []netbox.Object{liveTag(1)}}
	events := &fakeRecorder{}
	reconciles := watch(t, metrics.ReconcileTotal, labelSets(fakeGVK.Kind, results)...)

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: client, Resync: testResync},
			ready:    true,
		},
		Status:     &fakeStatus{err: lostStatusRace(obj)},
		LiveStatus: &fakeLiveStatus{status: staleStatus(1)},
		Finalizers: &fakeFinalizers{},
		Events:     events,
		Scheme:     fakeScheme(t),
	}

	result, err := engine.Reconcile(context.Background(), obj)
	if err != nil {
		t.Fatalf("Reconcile() = %v, want no error: a lost optimistic-concurrency write is"+
			" the ordinary outcome of a cached read", err)
	}

	// Soon, and on the engine's own timer: the copy that won the race is already on its way
	// into the cache the next pass reads.
	assertRequeue(t, result.RequeueAfter, staleRetry)

	if len(events.events) > 0 {
		t.Errorf("events = %v, want none: nothing here is for a human", events.events)
	}

	assertOneResult(t, reconciles, fakeGVK.Kind, metrics.ResultWaiting)
}

// lostStatusRace is what the API server answers a status write carrying a resourceVersion it
// has already moved past. Built by apierrors so that the engine's classification is exercised
// against the real Status body rather than a copy of its shape.
func lostStatusRace(obj *fakeKind) error {
	return lostStatusRaceOn(obj.Name)
}

// lostStatusRaceOn is the same answer, for a caller that has the object's name and not the
// object -- a StatusWriter is handed a client.Object.
func lostStatusRaceOn(name string) error {
	return apierrors.NewConflict(
		schema.GroupResource{Group: fakeGVK.Group, Resource: "netboxfakes"}, name,
		apierrors.NewBadRequest("the object has been modified; please apply your changes to the latest version"))
}

// assertOneResult checks that exactly one result bucket moved, which is what keeps
// sum(reconcile_total) a count of reconciles.
func assertOneResult(t *testing.T, reconciles *counters, kind, want string) {
	t.Helper()

	for _, result := range results {
		expect := 0.0
		if result == want {
			expect = 1.0
		}

		if got := reconciles.delta(kind, result); got != expect {
			t.Errorf("reconcile_total{kind=%q,result=%q} moved by %v, want %v",
				kind, result, got, expect)
		}
	}
}
