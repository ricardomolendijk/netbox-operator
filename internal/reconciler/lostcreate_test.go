package reconciler

import (
	"context"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Issue #289 root cause B, and issue #291: a pass that creates a NetBox object learns its id
// and persists it only in the status write at the end of that pass. When that write loses the
// #252 race the id used to be dropped with the rest of the pass's conclusions -- and unlike
// every other conclusion, *I created NetBox object 10* cannot be reached again. The next pass
// reads status.id == 0 from the API server, falls through to the natural key, finds the object
// the operator itself made, has no evidence that it is its own, and refuses to adopt on a
// guess. The CR is Ready=False/Conflict from then on, telling the user to set
// spec.onConflict: Adopt on an object nothing else has ever touched.
//
// Two passes over one shared API-server copy of the status, because that is where the defect
// lives: neither pass is wrong on its own, and the first is only recoverable by the second if
// what it wrote survived.

// apiServer is the API server's copy of one CR's status, and the three collaborators that read
// or write it.
//
// One fake rather than three, because the bug is entirely about whether what one pass wrote is
// there for the next one to read: a fakeStatus that counts writes and a fakeLiveStatus that
// answers from a canned value cannot disagree, and this has to be able to.
type apiServer struct {
	status netboxv1alpha1.NetBoxObjectStatus

	// refuse is how many further status updates are answered with the 409 a pass gets when
	// another pass of the same key has already moved the object on.
	refuse int

	// forget drops the id write instead of applying it, which is what the engine did before
	// this fix: it models the pre-#289 behaviour so the two halves can be asserted side by
	// side rather than described in a comment.
	forget bool

	updates int
	records int
}

func (a *apiServer) UpdateStatus(_ context.Context, obj client.Object) error {
	a.updates++

	if a.refuse > 0 {
		a.refuse--

		return lostStatusRaceOn(obj.GetName())
	}

	written, ok := obj.(Object)
	if !ok {
		return errStatusWrite
	}

	a.status = *written.NetBoxStatus().DeepCopy()

	return nil
}

func (a *apiServer) RecordID(_ context.Context, _ client.Object, id int64) error {
	a.records++

	if a.forget {
		return nil
	}

	a.status.ID = id

	return nil
}

func (a *apiServer) LiveStatus(_ context.Context, _ Object) (netboxv1alpha1.NetBoxObjectStatus, error) {
	return *a.status.DeepCopy(), nil
}

var (
	_ StatusWriter = (*apiServer)(nil)
	_ StatusReader = (*apiServer)(nil)
	_ IDWriter     = (*apiServer)(nil)
)

// TestACreateIsNotForgottenWhenItsStatusWriteLoses is the reproduction.
//
// Pass one creates the object and its status write is refused. Pass two starts from what the
// API server actually holds -- which is the whole question -- and either recognises the object
// as its own or accuses itself of it.
func TestACreateIsNotForgottenWhenItsStatusWriteLoses(t *testing.T) {
	tests := []struct {
		name string

		// forget drops the id the first pass tried to keep, which is the engine as it was.
		forget bool

		wantRecords int
		wantMethods []string
		wantID      int64
		wantReady   metav1.ConditionStatus
		wantReason  string
		wantEvents  []string
		wantResult  string
	}{
		{
			name: "the second pass locates the object the first one created, by its id",
			// GET by the recorded id and nothing else: the natural key is never reached, so
			// the adoption question is never asked.
			wantRecords: 1,
			wantMethods: []string{"GET"},
			wantID:      10,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantResult:  metrics.ResultUnchanged,
		},
		{
			name:   "the bug: an id that did not survive leaves the object conflicting with itself",
			forget: true,

			wantRecords: 1,
			wantMethods: []string{"GETONE"},
			wantID:      0,
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantEvents:  []string{"Warning/Conflict"},
			wantResult:  metrics.ResultError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			api := &apiServer{refuse: 1, forget: tc.forget}
			nb := &fakeClient{created: liveTag(10)}
			events := &fakeRecorder{}
			engine := lostCreateEngine(t, api, nb, events)

			// Pass one: nothing matches the natural key, so the object is created. Its status
			// write is the one the API server refuses.
			first, err := engine.Reconcile(context.Background(), fakeObject())
			if err != nil {
				t.Fatalf("the creating pass = %v, want no error: a refused status write is the"+
					" ordinary outcome of a cached read", err)
			}

			assertRequeue(t, first.RequeueAfter, staleRetry)

			if api.records != tc.wantRecords {
				t.Errorf("id writes = %d, want %d", api.records, tc.wantRecords)
			}

			if !slices.Equal(nb.methods(), []string{"GETONE", "POST"}) {
				t.Fatalf("the creating pass made %v, want [GETONE POST]", nb.methods())
			}

			// NetBox now holds the object, and answers for it under both routes.
			nb.calls, nb.get, nb.list = nil, liveTag(10), []netbox.Object{liveTag(10)}

			// Pass two starts from the copy the API server holds, which is the point: the
			// engine reconciles what it is handed, and what it is handed is whatever the first
			// pass managed to persist.
			obj := fakeObject()
			obj.Status = *api.status.DeepCopy()

			reconciles := watch(t, metrics.ReconcileTotal, labelSets(fakeGVK.Kind, results)...)
			events.events = nil

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("the second pass = %v, want no error", err)
			}

			if got := nb.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Errorf("the second pass made %v, want %v", got, tc.wantMethods)
			}

			if obj.Status.ID != tc.wantID {
				t.Errorf("status.id = %d, want %d", obj.Status.ID, tc.wantID)
			}

			ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
			if ready.Status != tc.wantReady || ready.Reason != tc.wantReason {
				t.Errorf("Ready = %s/%s, want %s/%s",
					ready.Status, ready.Reason, tc.wantReady, tc.wantReason)
			}

			if !slices.Equal(events.events, tc.wantEvents) {
				t.Errorf("events = %v, want %v", events.events, tc.wantEvents)
			}

			assertOneResult(t, reconciles, fakeGVK.Kind, tc.wantResult)
		})
	}
}

// TestAnIdThatCannotBeKeptIsReportedRatherThanDropped states what happens when the write of
// last resort fails too.
//
// The refused status update on its own is a requeue and nothing more (#252). Failing to keep
// the id of an object NetBox already holds is not: it is the one state the engine cannot
// reconcile its way out of, so it is returned into controller-runtime's backoff and counted as
// a failed reconcile rather than reported as an ordinary lost race.
func TestAnIdThatCannotBeKeptIsReportedRatherThanDropped(t *testing.T) {
	obj := fakeObject()
	nb := &fakeClient{created: liveTag(10)}
	reconciles := watch(t, metrics.ReconcileTotal, labelSets(fakeGVK.Kind, results)...)

	engine := lostCreateEngine(t, &apiServer{refuse: 1}, nb, &fakeRecorder{})

	// Unwired, which is the failure this covers as well as a wiring mistake: without an
	// IDWriter there is no way to keep the id at all, and the engine has to say so rather than
	// carry on as if the create had been recorded.
	engine.IDs = nil

	if _, err := engine.Reconcile(context.Background(), obj); err == nil {
		t.Fatal("Reconcile() = nil, want the error for a created object whose id could not be kept")
	}

	assertOneResult(t, reconciles, fakeGVK.Kind, metrics.ResultError)
}

// TestARefusedStatusWriteThatCreatedNothingKeepsNoID is the guard on the other side: the second
// reconcile of a settled object also loses its status write, routinely, and it must not turn
// that into an extra API call. Only a pass that proved a new id server-side has anything to
// keep.
func TestARefusedStatusWriteThatCreatedNothingKeepsNoID(t *testing.T) {
	obj := fakeObject()
	obj.Status.ID = 10

	api := &apiServer{status: netboxv1alpha1.NetBoxObjectStatus{ID: 10}, refuse: 1}
	nb := &fakeClient{get: liveTag(10)}

	// A spec change NetBox has to be told about, so the pass has a status to write at all.
	obj.Spec.Description = "moved"
	nb.patched = liveTag(10)

	if _, err := lostCreateEngine(t, api, nb, &fakeRecorder{}).
		Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v, want no error", err)
	}

	if api.records != 0 {
		t.Errorf("id writes = %d, want 0: the id was already the API server's and a PATCH"+
			" proves nothing new about it", api.records)
	}
}

// lostCreateEngine is the engine these cases share, wired against one API-server copy.
func lostCreateEngine(t *testing.T, api *apiServer, nb *fakeClient, events *fakeRecorder) *Engine {
	t.Helper()

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: nb, Resync: testResync},
			ready:    true,
		},
		Status:     api,
		IDs:        api,
		LiveStatus: api,
		Finalizers: &fakeFinalizers{},
		Events:     events,
		Scheme:     fakeScheme(t),
	}
}
