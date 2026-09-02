package reconciler

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// protectedBody is what NetBox sends back when a foreign key declared PROTECT still points
// at the object. It names the blocker, and that is the part the condition has to carry:
// "cannot delete" without a reason is the worst possible operator experience.
const protectedBody = `{"detail":"Unable to delete object. ` +
	`Cannot delete some instances of model 'Prefix' because they are referenced through ` +
	`protected foreign keys: 'IPAddress.vrf'."}`

func TestEngineReconcileDeleting(t *testing.T) {
	tests := []struct {
		name     string
		object   func() *fakeKind
		client   func() *fakeClient
		notReady bool
		dryRun   bool

		wantMethods    []string
		wantFinalizers []string
		wantDeleting   string
		wantMessage    string
		wantEvents     []string
		wantRequeue    time.Duration
		wantAttempts   int32
		wantWrites     int
	}{
		{
			name:   "Delete removes the netbox object and then the finalizer",
			object: deletingObject,
			client: func() *fakeClient { return &fakeClient{} },
			// The order is the assertion: the DELETE is recorded before the finalizer
			// write, so the finalizer cannot come off ahead of the delete succeeding.
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{},
			wantEvents:     []string{"Normal/Deleted"},
			wantWrites:     0,
		},
		{
			name: "Retain drops the finalizer and writes nothing to netbox",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain

				return obj
			},
			client:         func() *fakeClient { return &fakeClient{} },
			wantMethods:    nil,
			wantFinalizers: []string{},
			wantEvents:     []string{"Normal/Retained"},
			wantWrites:     0,
		},
		{
			name:   "a protected delete is a condition naming the blocker, and the finalizer stays on",
			object: deletingObject,
			client: func() *fakeClient {
				return &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}}
			},
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{netboxv1alpha1.Finalizer},
			wantDeleting:   netboxv1alpha1.ReasonProtected,
			wantMessage:    "protected foreign keys: 'IPAddress.vrf'",
			wantRequeue:    protectedRetryBase,
			wantAttempts:   1,
			wantWrites:     1,
		},
		{
			// The pass that arrives between two attempts, which on a live cluster is most of
			// them: the refusal's own status write wakes the controller straight away (#289).
			name: "a pass inside the backoff window sends nothing and writes nothing",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Status.DeletionAttempts = 1
				now := metav1.Now()
				obj.Status.LastDeletionAttempt = &now
				// The pass that recorded the refusal stamped this too, and it is what makes
				// "writes nothing" observable: without it the write below would be the
				// generation being stamped for the first time rather than the deletion path.
				obj.Status.ObservedGeneration = obj.Generation

				return obj
			},
			client: func() *fakeClient {
				return &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}}
			},
			wantMethods:    nil,
			wantFinalizers: []string{netboxv1alpha1.Finalizer},
			wantRequeue:    protectedRetryBase,
			wantAttempts:   1,
			wantWrites:     0,
		},
		{
			name:   "a netbox object that is already gone completes the deletion",
			object: deletingObject,
			client: func() *fakeClient {
				return &fakeClient{deleteErr: &netbox.NotFoundError{Endpoint: "extras/tags", ID: 9}}
			},
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{},
			wantEvents:     []string{"Normal/Deleted"},
			wantWrites:     0,
		},
		{
			name: "an unset status.id deletes nothing, even when something matches the natural key",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Status.ID = 0

				return obj
			},
			// A live object the engine could have found by lookup. It must not go looking:
			// status.id is the claim, and without one this object is not ours to delete.
			client: func() *fakeClient {
				return &fakeClient{list: []netbox.Object{liveTag(9)}}
			},
			wantMethods:    nil,
			wantFinalizers: []string{},
			wantEvents:     []string{"Normal/NothingToDelete"},
			wantWrites:     0,
		},
		{
			name:           "an endpoint that is not ready blocks the deletion rather than orphaning the object",
			object:         deletingObject,
			client:         func() *fakeClient { return &fakeClient{} },
			notReady:       true,
			wantMethods:    nil,
			wantFinalizers: []string{netboxv1alpha1.Finalizer},
			wantDeleting:   netboxv1alpha1.ReasonWaitingForEndpoint,
			wantMessage:    netboxv1alpha1.SkipFinalizerAnnotation,
			wantRequeue:    endpointRetry,
			wantWrites:     1,
		},
		{
			name: "an object adopted under onConflict is owned, and is deleted",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
				obj.Status.Adopted = true

				return obj
			},
			client:         func() *fakeClient { return &fakeClient{} },
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{},
			wantEvents:     []string{"Normal/Deleted"},
			wantWrites:     0,
		},
		{
			name: "the break-glass annotation drops the finalizer without a netbox call",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Annotations = map[string]string{netboxv1alpha1.SkipFinalizerAnnotation: "true"}

				return obj
			},
			client:         func() *fakeClient { return &fakeClient{} },
			wantMethods:    nil,
			wantFinalizers: []string{},
			wantEvents:     []string{"Warning/FinalizerSkipped"},
			wantWrites:     0,
		},
		{
			name: "the break-glass annotation wins over a delete that would otherwise be attempted",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Annotations = map[string]string{netboxv1alpha1.SkipFinalizerAnnotation: "true"}

				return obj
			},
			client: func() *fakeClient {
				return &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}}
			},
			wantMethods:    nil,
			wantFinalizers: []string{},
			wantEvents:     []string{"Warning/FinalizerSkipped"},
			wantWrites:     0,
		},
		{
			name: "switching to Retain gets a protected object out of the blocked state",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain
				obj.Status.DeletionAttempts = 7

				return obj
			},
			client: func() *fakeClient {
				return &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}}
			},
			wantMethods:    nil,
			wantFinalizers: []string{},
			wantEvents:     []string{"Normal/Retained"},
			wantAttempts:   7,
			wantWrites:     0,
		},
		{
			name: "a CR with no finalizer of ours is left to kubernetes",
			object: func() *fakeKind {
				obj := deletingObject()
				obj.Finalizers = []string{"someone.else/finalizer"}

				return obj
			},
			client:         func() *fakeClient { return &fakeClient{} },
			wantMethods:    nil,
			wantFinalizers: []string{"someone.else/finalizer"},
			wantWrites:     0,
		},
		{
			name:   "a transient failure keeps the finalizer and comes back soon",
			object: deletingObject,
			client: func() *fakeClient {
				return &fakeClient{deleteErr: &netbox.TransientError{Status: 503}}
			},
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{netboxv1alpha1.Finalizer},
			wantDeleting:   netboxv1alpha1.ReasonAPIError,
			wantRequeue:    transientRetry,
			wantWrites:     1,
		},
		{
			name:           "a dry-run endpoint reports that the object was left in place",
			object:         deletingObject,
			client:         func() *fakeClient { return &fakeClient{} },
			dryRun:         true,
			wantMethods:    []string{"DELETE"},
			wantFinalizers: []string{},
			wantEvents:     []string{"Warning/Deleted"},
			wantWrites:     0,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			client := tc.client()
			if tc.dryRun {
				// A real DryRun client, so the suppressed answer the finalizer has to
				// recognise is produced by the code under test rather than described here.
				client.dryRun = dryRunClient(t)
			}

			status := &fakeStatus{}
			finalizers := &fakeFinalizers{}
			events := &fakeRecorder{}
			obj := tc.object()

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: client, Resync: testResync},
					ready:    !tc.notReady,
				},
				Status:     status,
				LiveStatus: &fakeLiveStatus{},
				Finalizers: finalizers,
				Events:     events,
				Scheme:     fakeScheme(t),
			}

			result, err := engine.Reconcile(context.Background(), obj)
			if err != nil {
				t.Fatalf("Reconcile() = %v, want no error: a blocked delete is a condition, not a failure", err)
			}

			if got := client.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Errorf("netbox calls = %v, want %v", got, tc.wantMethods)
			}

			if got := obj.GetFinalizers(); !slices.Equal(got, tc.wantFinalizers) {
				t.Errorf("finalizers = %v, want %v", got, tc.wantFinalizers)
			}

			deleting := conditionOf(obj, netboxv1alpha1.ConditionDeleting)
			if deleting.Reason != tc.wantDeleting {
				t.Errorf("Deleting reason = %q, want %q", deleting.Reason, tc.wantDeleting)
			}

			// Only ever False: the finalizer comes off the moment the NetBox side settles,
			// so a True would describe a CR that is already gone.
			if tc.wantDeleting != "" && deleting.Status != metav1.ConditionFalse {
				t.Errorf("Deleting = %s, want False", deleting.Status)
			}

			if tc.wantMessage != "" && !strings.Contains(deleting.Message, tc.wantMessage) {
				t.Errorf("Deleting message = %q, want it to contain %q", deleting.Message, tc.wantMessage)
			}

			if !slices.Equal(events.events, tc.wantEvents) {
				t.Errorf("events = %v, want %v", events.events, tc.wantEvents)
			}

			if obj.Status.DeletionAttempts != tc.wantAttempts {
				t.Errorf("status.deletionAttempts = %d, want %d",
					obj.Status.DeletionAttempts, tc.wantAttempts)
			}

			if status.writes != tc.wantWrites {
				t.Errorf("status writes = %d, want %d: a released finalizer takes the object with it, "+
					"so a status write would race the delete", status.writes, tc.wantWrites)
			}

			assertRequeue(t, result.RequeueAfter, tc.wantRequeue)
		})
	}
}

// TestEngineClaimsBeforeItWrites is the ordering that stops an orphan. The finalizer has to
// be persisted by a real API write *before* the POST goes out: a finalizer that exists only
// in memory while the create happens is the add-after-create window in disguise, and a
// process that dies in that window leaves a NetBox object nothing knows about.
func TestEngineClaimsBeforeItWrites(t *testing.T) {
	client := &fakeClient{created: liveTag(7)}
	finalizers := &fakeFinalizers{}
	obj := fakeObject()

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  finalizers,
		Scheme:      fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := [][]string{{netboxv1alpha1.Finalizer}}
	if !slices.EqualFunc(finalizers.writes, want, slices.Equal) {
		t.Fatalf("finalizer writes = %v, want %v", finalizers.writes, want)
	}

	// Persisted once and then left alone: re-adding it every pass would churn the
	// resourceVersion of every object in the cluster.
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() second pass = %v", err)
	}

	if len(finalizers.writes) != 1 {
		t.Errorf("finalizer writes over two reconciles = %d, want 1", len(finalizers.writes))
	}
}

// TestEngineClaimFailureWritesNothing is the other half of the ordering: if the finalizer
// cannot be persisted, the pass must stop before the create rather than carry on with a
// finalizer that exists only in this process.
func TestEngineClaimFailureWritesNothing(t *testing.T) {
	client := &fakeClient{created: liveTag(7)}
	obj := fakeObject()

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{err: errFinalizerWrite},
		Scheme:      fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err == nil {
		t.Fatal("Reconcile() = nil, want the finalizer write error")
	}

	if got := client.methods(); len(got) != 0 {
		t.Errorf("netbox calls = %v, want none: nothing may be written without a durable finalizer", got)
	}

	if got := obj.GetFinalizers(); len(got) != 0 {
		t.Errorf("finalizers = %v, want none: the in-memory object must match what the API server accepted", got)
	}
}

// TestEngineReleaseFailureKeepsTheFinalizer checks the reverse: a failed removal has to be
// retried, and until it succeeds the object still claims the NetBox object -- even though
// the NetBox object is already gone, because a second DELETE of a missing object completes
// the deletion anyway.
func TestEngineReleaseFailureKeepsTheFinalizer(t *testing.T) {
	obj := deletingObject()

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: &fakeClient{}, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{err: errFinalizerWrite},
		Scheme:      fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err == nil {
		t.Fatal("Reconcile() = nil, want the finalizer write error")
	}

	if got := obj.GetFinalizers(); !slices.Equal(got, []string{netboxv1alpha1.Finalizer}) {
		t.Errorf("finalizers = %v, want the finalizer still on", got)
	}
}

// TestProtectedBackoffIsCapped is the property the ticket asks for by name: a genuinely
// stuck delete must not spin, and must not back off past a horizon where nobody notices it
// recovering.
func TestProtectedBackoffIsCapped(t *testing.T) {
	tests := []struct {
		attempts int32
		want     time.Duration
	}{
		{attempts: 0, want: protectedRetryBase},
		{attempts: 1, want: protectedRetryBase},
		{attempts: 2, want: 4 * time.Second},
		{attempts: 3, want: 8 * time.Second},
		{attempts: 5, want: 32 * time.Second},
		{attempts: 8, want: 256 * time.Second},
		{attempts: 9, want: protectedRetryCap},
		{attempts: 64, want: protectedRetryCap},
		// int32 is what status carries, so the shift has to survive its whole range.
		{attempts: 1 << 30, want: protectedRetryCap},
	}

	for _, tc := range tests {
		if got := protectedBackoff(tc.attempts); got != tc.want {
			t.Errorf("protectedBackoff(%d) = %s, want %s", tc.attempts, got, tc.want)
		}
	}
}

// TestProtectedDeleteEventuallyWarns checks that a permanently blocked delete becomes
// visible: once, at the threshold, rather than on every attempt.
func TestProtectedDeleteEventuallyWarns(t *testing.T) {
	events := &fakeRecorder{}
	obj := deletingObject()

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{
				Client: &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}},
				Resync: testResync,
			},
			ready: true,
		},
		Status:     &fakeStatus{},
		LiveStatus: &fakeLiveStatus{},
		Finalizers: &fakeFinalizers{},
		Events:     events,
		Scheme:     fakeScheme(t),
	}

	const passes = 6

	for i := range passes {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}

		// Each pass here is meant to be the next *attempt*, and since #289 that takes the
		// backoff having run out -- a pass that arrives early sends nothing, which is the
		// whole fix. Rewinding is what a test does instead of sleeping for minutes.
		obj.Status.LastDeletionAttempt = rewound(obj.Status.LastDeletionAttempt)
	}

	if want := []string{"Warning/" + netboxv1alpha1.EventDeleteBlocked}; !slices.Equal(events.events, want) {
		t.Errorf("events over %d refusals = %v, want %v: once, not every attempt and not never",
			passes, events.events, want)
	}

	if obj.Status.DeletionAttempts != passes {
		t.Errorf("status.deletionAttempts = %d, want %d", obj.Status.DeletionAttempts, passes)
	}

	if got := obj.GetFinalizers(); !slices.Equal(got, []string{netboxv1alpha1.Finalizer}) {
		t.Errorf("finalizers = %v, want the finalizer still on: the netbox object is still there", got)
	}
}

// rewound moves a last-attempt timestamp far enough into the past that the backoff after it
// has certainly run out, so that the next Reconcile is the next attempt.
//
// A test helper rather than a clock seam on the Engine: what the engine has to get right is
// that it reads the *stored* timestamp, and a test that rewinds the stored value exercises
// exactly that. The alternative is sleeping for the interval, which is minutes.
func rewound(last *metav1.Time) *metav1.Time {
	if last == nil {
		return nil
	}

	out := metav1.NewTime(last.Add(-protectedRetryCap - time.Second))

	return &out
}

// TestARefusedDeleteIsNotRetriedUntilItsBackoffHasRun is the regression test for #289.
//
// The engine chose an interval and returned it in a ctrl.Result, which says when to come back
// *at the latest*. What it did not account for is that the status write recording the refusal
// is itself an event on the object, so the controller wakes immediately, and every wake-up
// sent another DELETE and wrote another status: measured in envtest at ~320 refused DELETEs a
// second against a single object, with the attempt count past six thousand inside twenty
// seconds. Five referenced CRs doing that at once is why a two-minute teardown ran out.
//
// So the assertion is not "it retries" but "it does not retry *yet*": a second pass a moment
// after the first must send nothing, write nothing, and come back with what is left of the
// interval.
func TestARefusedDeleteIsNotRetriedUntilItsBackoffHasRun(t *testing.T) {
	client := &fakeClient{deleteErr: &netbox.ProtectedError{Status: 409, Body: protectedBody}}
	status := &fakeStatus{}
	obj := deletingObject()

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: client, Resync: testResync},
			ready:    true,
		},
		Status:     status,
		LiveStatus: &fakeLiveStatus{},
		Finalizers: &fakeFinalizers{},
		Events:     &fakeRecorder{},
		Scheme:     fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("the first pass = %v", err)
	}

	if got := len(client.methods()); got != 1 {
		t.Fatalf("the first pass made %d netbox calls, want the one DELETE", got)
	}

	// Every wake-up a live cluster produces between two attempts: the object's own status
	// write, a resync, a reference target changing. None of them is new information about
	// whether NetBox will accept the delete this time.
	const wakeUps = 20

	for i := range wakeUps {
		result, err := engine.Reconcile(context.Background(), obj)
		if err != nil {
			t.Fatalf("wake-up %d = %v", i, err)
		}

		// The remainder of the interval, give or take Jitter's tenth either way.
		if ceiling := protectedBackoff(1) * 11 / 10; result.RequeueAfter <= 0 ||
			result.RequeueAfter > ceiling {
			t.Fatalf("wake-up %d asked to come back in %s, want what is left of %s",
				i, result.RequeueAfter, protectedBackoff(1))
		}
	}

	if got := len(client.methods()); got != 1 {
		t.Errorf("netbox saw %d calls over %d wake-ups, want the one DELETE: the interval the"+
			" engine chose has to hold whatever wakes it", got, wakeUps+1)
	}

	if obj.Status.DeletionAttempts != 1 {
		t.Errorf("status.deletionAttempts = %d, want 1: a wake-up is not an attempt, and a count"+
			" that says otherwise takes the backoff to its ceiling in milliseconds",
			obj.Status.DeletionAttempts)
	}

	if status.writes != 1 {
		t.Errorf("status writes = %d, want 1: a write per wake-up is what wakes the next one",
			status.writes)
	}

	if got := obj.GetFinalizers(); !slices.Equal(got, []string{netboxv1alpha1.Finalizer}) {
		t.Errorf("finalizers = %v, want the finalizer still on", got)
	}

	// And the other half: once the interval has run, the retry happens. A hold-off that never
	// releases is the stuck finalizer this fix exists to prevent, wearing a different hat.
	obj.Status.LastDeletionAttempt = rewound(obj.Status.LastDeletionAttempt)
	client.deleteErr = nil

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("the pass after the interval = %v", err)
	}

	if got := obj.GetFinalizers(); len(got) != 0 {
		t.Errorf("finalizers = %v after the delete succeeded, want none", got)
	}
}

// TestDeletionHold is the arithmetic on its own, including the two ways it must refuse to
// hold: no attempt has been made yet, and a clock that has moved backwards. Stranding an
// object on a wait that never expires would be the same bug in the other direction.
func TestDeletionHold(t *testing.T) {
	now := time.Now()
	at := func(d time.Duration) *metav1.Time {
		out := metav1.NewTime(now.Add(d))

		return &out
	}

	tests := []struct {
		name     string
		attempts int32
		last     *metav1.Time
		wantHold bool
		want     time.Duration
	}{
		{name: "no attempt yet", attempts: 0, last: nil},
		{name: "a count with no timestamp is not a schedule", attempts: 3, last: nil},
		{name: "a timestamp with no count is not one either", attempts: 0, last: at(0)},
		{name: "inside the first interval", attempts: 1, last: at(-time.Second),
			wantHold: true, want: protectedRetryBase - time.Second},
		{name: "exactly at the interval", attempts: 1, last: at(-protectedRetryBase)},
		{name: "past the interval", attempts: 1, last: at(-time.Hour)},
		{name: "inside a later interval", attempts: 4, last: at(-time.Second),
			wantHold: true, want: protectedBackoff(4) - time.Second},
		{name: "at the ceiling", attempts: 40, last: at(-time.Minute),
			wantHold: true, want: protectedRetryCap - time.Minute},
		// A last attempt in the future is a clock that jumped. Waiting for it would hold the
		// finalizer for however far the jump was.
		{name: "a last attempt in the future is due now", attempts: 4, last: at(time.Hour)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, holding := deletionHold(tc.attempts, tc.last, now)

			if holding != tc.wantHold {
				t.Fatalf("deletionHold(%d, %v) holding = %v, want %v",
					tc.attempts, tc.last, holding, tc.wantHold)
			}

			if got != tc.want {
				t.Errorf("deletionHold(%d, %v) = %s, want %s", tc.attempts, tc.last, got, tc.want)
			}
		})
	}
}

// TestDeletionPolicyNeverReachesNetBox is the guard on the envelope. deletionPolicy
// configures the operator; NetBox has no such column and would ignore the field silently
// rather than rejecting it, so a leak here would be invisible.
func TestDeletionPolicyNeverReachesNetBox(t *testing.T) {
	if !envelopeFields["deletionPolicy"] {
		t.Fatal("envelopeFields is missing deletionPolicy")
	}

	client := &fakeClient{created: liveTag(7)}
	obj := fakeObject()
	obj.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := client.lastPayload()
	if payload == nil {
		t.Fatal("no payload was sent, so the assertion proves nothing")
	}

	for _, field := range []string{"deletionPolicy", "deletion_policy"} {
		if _, leaked := payload[field]; leaked {
			t.Errorf("payload = %v, want no %s in it", payload, field)
		}
	}
}

// TestDeletionPolicyDefaultsToDelete guards the unset case, which is every object of every
// kind that does not state a policy: there is no CRD default to lean on -- the field is
// declared once on the shared envelope, so a marker there could only give ~120 kinds one
// answer -- and defaulting it the other way would leave objects behind.
//
// The per-kind exception is the second half: decision #176 makes IPAM retain, declared as
// registry.Descriptor.RetainOnDelete, because deleting an address frees it for reallocation.
//
// The last row is a claim, which reads its default off claimRetainsByDefault rather than off a
// Descriptor and reaches the same function to do it (#225). It is asserted here rather than in
// claim_test.go on purpose: the point of the row is that there is one rule, and a rule tested
// once per caller is two rules with a shared name.
func TestDeletionPolicyDefaultsToDelete(t *testing.T) {
	cases := []struct {
		name            string
		stated          netboxv1alpha1.DeletionPolicy
		retainByDefault bool
		want            netboxv1alpha1.DeletionPolicy
	}{
		{"unset on a deleting kind", "", false, netboxv1alpha1.DeletionDelete},
		{"unset on a retaining kind", "", registry.Descriptor{RetainOnDelete: true}.RetainOnDelete,
			netboxv1alpha1.DeletionRetain},
		{"stated beats the default", netboxv1alpha1.DeletionDelete, true, netboxv1alpha1.DeletionDelete},
		{"stated beats the default, the other way", netboxv1alpha1.DeletionRetain, false,
			netboxv1alpha1.DeletionRetain},
		{"unset on a claim", "", claimRetainsByDefault, netboxv1alpha1.DeletionDelete},
	}

	for _, tc := range cases {
		if got := deletionPolicyOf(tc.stated, tc.retainByDefault); got != tc.want {
			t.Errorf("%s: deletionPolicyOf(%q, %v) = %q, want %q",
				tc.name, tc.stated, tc.retainByDefault, got, tc.want)
		}
	}
}

// deletionDefaults is the effective spec.deletionPolicy of every registered object kind when
// the CR states none, and it is the whole of the answer: docs/concepts/deletion.md's table is
// prose about this map.
//
// It is a written-out list rather than a derived one on purpose. Deriving it from
// Descriptor.RetainOnDelete would make the test agree with whatever the registry says,
// including the wrong thing -- which is exactly how five kinds shipped as `Delete` while the
// docs promised `Retain` (#186) with `make test` green. Adding a kind therefore means adding a
// row here and deciding, in writing, whether deleting its NetBox object destroys state.
var deletionDefaults = map[string]netboxv1alpha1.DeletionPolicy{
	// Decision #176: an IPAM object holds state. Deleting one frees an allocation or
	// destroys the record of who a range belonged to, and neither is undone by a recreate.
	"NetBoxIPAddress": netboxv1alpha1.DeletionRetain,
	"NetBoxPrefix":    netboxv1alpha1.DeletionRetain,
	"NetBoxIPRange":   netboxv1alpha1.DeletionRetain,
	"NetBoxVLAN":      netboxv1alpha1.DeletionRetain,
	"NetBoxVRF":       netboxv1alpha1.DeletionRetain,

	// NBO-055: the same rule applied to the ipam remainder. Each holds an allocation or an
	// identity nothing else records -- see docs/concepts/deletion.md's table for the reason
	// per kind.
	"NetBoxRIR":                 netboxv1alpha1.DeletionRetain,
	"NetBoxAggregate":           netboxv1alpha1.DeletionRetain,
	"NetBoxASN":                 netboxv1alpha1.DeletionRetain,
	"NetBoxASNRange":            netboxv1alpha1.DeletionRetain,
	"NetBoxRole":                netboxv1alpha1.DeletionRetain,
	"NetBoxFHRPGroup":           netboxv1alpha1.DeletionRetain,
	"NetBoxFHRPGroupAssignment": netboxv1alpha1.DeletionRetain,
	"NetBoxService":             netboxv1alpha1.DeletionRetain,
	"NetBoxServiceTemplate":     netboxv1alpha1.DeletionRetain,

	// The carve-out inside ipam, and the row worth reading twice: a VLAN group is an
	// organisational container rather than an allocation, so deleting one frees nothing and
	// the rule the table turns on puts it with the configuration kinds.
	"NetBoxVLANGroup": netboxv1alpha1.DeletionDelete,

	// NBO-068: the same carve-out, twice more. A VLAN translation policy is a table of
	// rewrites and a rule is one row of it; neither hands anything out, so deleting one frees
	// no address, no VLAN ID and no range. The rule would be incoherent as `Retain` in any
	// case: NetBox cascades a policy's rules away with the policy, so retaining one leaves a
	// CR pointing at a row that no longer exists.
	"NetBoxVLANTranslationPolicy": netboxv1alpha1.DeletionDelete,
	"NetBoxVLANTranslationRule":   netboxv1alpha1.DeletionDelete,

	// NBO-051: racks are configuration a manifest recreates, not allocated state -- see each
	// kind's own RetainOnDelete comment in internal/registry/dcim_rack*.go.
	"NetBoxRack":            netboxv1alpha1.DeletionDelete,
	"NetBoxRackGroup":       netboxv1alpha1.DeletionDelete,
	"NetBoxRackReservation": netboxv1alpha1.DeletionDelete,
	"NetBoxRackRole":        netboxv1alpha1.DeletionDelete,
	"NetBoxRackType":        netboxv1alpha1.DeletionDelete,

	// Configuration: cheap to delete, cheap to recreate, and mostly PROTECT-refused while
	// anything still points at it.
	"NetBoxRouteTarget":       netboxv1alpha1.DeletionDelete,
	"NetBoxTag":               netboxv1alpha1.DeletionDelete,
	"NetBoxRegion":            netboxv1alpha1.DeletionDelete,
	"NetBoxSite":              netboxv1alpha1.DeletionDelete,
	"NetBoxSiteGroup":         netboxv1alpha1.DeletionDelete,
	"NetBoxLocation":          netboxv1alpha1.DeletionDelete,
	"NetBoxTenant":            netboxv1alpha1.DeletionDelete,
	"NetBoxTenantGroup":       netboxv1alpha1.DeletionDelete,
	"NetBoxContact":           netboxv1alpha1.DeletionDelete,
	"NetBoxContactGroup":      netboxv1alpha1.DeletionDelete,
	"NetBoxContactRole":       netboxv1alpha1.DeletionDelete,
	"NetBoxContactAssignment": netboxv1alpha1.DeletionDelete,
	"NetBoxManufacturer":      netboxv1alpha1.DeletionDelete,
	"NetBoxDeviceRole":        netboxv1alpha1.DeletionDelete,
	"NetBoxDeviceType":        netboxv1alpha1.DeletionDelete,
	"NetBoxPlatform":          netboxv1alpha1.DeletionDelete,
	"NetBoxDevice":            netboxv1alpha1.DeletionDelete,
	"NetBoxInterface":         netboxv1alpha1.DeletionDelete,

	// The physical plant. A cable is a statement about a connection that the manifest is the
	// record of, so re-creating one loses nothing that was not in Git -- and a bundle is a
	// label whose deletion clears `dcim.Cable.bundle` (SET_NULL) and destroys no cables at
	// all. Neither holds allocated state, so neither is a #176 carve-out.
	//
	// `Retain` is nonetheless load-bearing on NetBoxCable if a user sets it: the kind is
	// `UpdateStrategy: Recreate`, and a recreate destroys the object, so the engine refuses
	// the destructive write rather than violating the policy (docs/reference/netboxcable.md,
	// "deletionPolicy: Retain refuses a recreate").
	"NetBoxCable":          netboxv1alpha1.DeletionDelete,
	"NetBoxCableBundle":    netboxv1alpha1.DeletionDelete,
	"NetBoxClusterType":    netboxv1alpha1.DeletionDelete,
	"NetBoxClusterGroup":   netboxv1alpha1.DeletionDelete,
	"NetBoxCluster":        netboxv1alpha1.DeletionDelete,
	"NetBoxVirtualMachine": netboxv1alpha1.DeletionDelete,
	"NetBoxVMInterface":    netboxv1alpha1.DeletionDelete,
	"NetBoxVirtualDisk":    netboxv1alpha1.DeletionDelete,

	// `extras` -- NetBox's own configuration, and the app where "cheap to recreate" is most
	// literally true: a link, a filter or a template holds no state, and deleting one loses a
	// button rather than a record.
	//
	// NetBoxCustomField is `Delete` too, and that is not the loose end it looks like. Deleting
	// one *does* destroy data, on every object in NetBox that has the field -- which is why it
	// declares DataLossOnDelete and the finalizer refuses by default with
	// `Deleting=False, Reason=DataLossBlocked`. That guard is a separate axis from this one:
	// `Retain` would mean "leave the definition in NetBox and forget it", and defaulting to
	// that would leave a schema column nothing manages behind every deleted CR. Refusing until
	// a human says the loss is acceptable is the honest default, and it is reversible.
	"NetBoxCustomField":          netboxv1alpha1.DeletionDelete,
	"NetBoxCustomFieldChoiceSet": netboxv1alpha1.DeletionDelete,
	"NetBoxCustomLink":           netboxv1alpha1.DeletionDelete,
	"NetBoxSavedFilter":          netboxv1alpha1.DeletionDelete,
	"NetBoxExportTemplate":       netboxv1alpha1.DeletionDelete,
	"NetBoxConfigTemplate":       netboxv1alpha1.DeletionDelete,
	"NetBoxConfigContextProfile": netboxv1alpha1.DeletionDelete,
	"NetBoxConfigContext":        netboxv1alpha1.DeletionDelete,

	// A MAC address is a property of a component rather than an allocation: nothing hands it
	// out and nothing else has to be told it was freed, so deleting one destroys no state a
	// recreate does not restore. NetBox's own `GenericRelation` deletes it with its interface
	// in any case, which is the shape the containment owner reference mirrors.
	"NetBoxMACAddress": netboxv1alpha1.DeletionDelete, // Wireless. An SSID, its group and a radio link are all configuration: nothing is handed
	// out, so deleting one destroys no state a recreate does not restore. The group and the
	// SSID are `SET_NULL`/`PROTECT`-guarded on the way out anyway, and a link is pointed at by
	// nothing at all.
	"NetBoxWirelessLAN":      netboxv1alpha1.DeletionDelete,
	"NetBoxWirelessLANGroup": netboxv1alpha1.DeletionDelete,
	"NetBoxWirelessLink":     netboxv1alpha1.DeletionDelete}

// TestEveryKindsDeletionDefaultIsStated is criterion 2 of #186, and it is the test whose
// absence was the bug. `Descriptor.RetainOnDelete` and `deletionPolicyOf` both shipped, five
// of the six kinds docs/concepts/deletion.md documented as `Retain` did not set the flag, and
// nothing failed -- because no test read the default *per kind* at all.
//
// So it reads it the way finalizer.go does, through Descriptor.RetainOnDelete and
// deletionPolicyOf, rather than off the generated CRD. There is no `+kubebuilder:default` to
// read there and cannot be: spec.deletionPolicy is declared once on the shared envelope, so a
// marker would be one answer for every kind. A test against the schema would therefore pass
// while the engine deleted a production prefix.
//
// It is exhaustive in both directions. A registered kind with no row fails, so adding a kind
// forces somebody to state its default; a row naming a kind that is not registered fails too,
// so the table cannot rot into a list of kinds that used to exist.
//
// Claims are not in it. A claim's default is claimRetainsByDefault rather than a Descriptor,
// asserted in the last row of TestDeletionPolicyDefaultsToDelete above.
func TestEveryKindsDeletionDefaultIsStated(t *testing.T) {
	descriptors := registry.List()
	if len(descriptors) == 0 {
		t.Fatal("the registry is empty; this test would pass by describing nothing")
	}

	for _, descriptor := range descriptors {
		kind := descriptor.GVK.Kind

		want, stated := deletionDefaults[kind]
		if !stated {
			t.Errorf("%s is registered and states no deletion default; add it to "+
				"deletionDefaults and to the table in docs/concepts/deletion.md", kind)

			continue
		}

		if got := deletionPolicyOf("", descriptor.RetainOnDelete); got != want {
			t.Errorf("%s defaults to %q, want %q: RetainOnDelete = %v", kind, got, want,
				descriptor.RetainOnDelete)
		}
	}

	registered := make(map[string]bool, len(descriptors))
	for _, descriptor := range descriptors {
		registered[descriptor.GVK.Kind] = true
	}

	for kind := range deletionDefaults {
		if !registered[kind] {
			t.Errorf("deletionDefaults names %s, which is not registered", kind)
		}
	}
}

// TestClaimWithoutAFinalizerWriterFailsLoudly pins the guard added after a rebase found
// the nil path the hard way: claim ran before any NetBox call and segfaulted, which tells
// whoever is paged nothing. A wiring mistake must name what is missing, and failing here
// is also the safest place to fail -- nothing has been created that could leak.
func TestClaimWithoutAFinalizerWriterFailsLoudly(t *testing.T) {
	engine := &Engine{}
	obj := fakeObject()

	err := engine.claim(context.Background(), obj)
	if err == nil {
		t.Fatal("claim with no FinalizerWriter returned nil; it must report the wiring mistake")
	}
	if !errors.Is(err, errNotConfigured) {
		t.Errorf("err = %v, want errNotConfigured", err)
	}
	if slices.Contains(obj.GetFinalizers(), netboxv1alpha1.Finalizer) {
		t.Error("the finalizer was left on the object after a failed claim")
	}
}

// deletingStampedObject is stampedObject() as the API server hands it back after
// `kubectl delete`, with the status.id the lost write never recorded.
func deletingStampedObject() *fakeKind {
	obj := stampedObject()
	obj.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
	obj.Finalizers = []string{netboxv1alpha1.Finalizer}

	return obj
}

// TestDeleteFindsTheObjectALostStatusWriteLeftBehind is the orphan this operator could
// previously only report. status.id is 0 because the update that would have set it never
// landed, and the object in NetBox carries this CR's own uid stamp -- which is proof of
// authorship a natural key could never be, so the delete goes out.
func TestDeleteFindsTheObjectALostStatusWriteLeftBehind(t *testing.T) {
	client := &fakeClient{list: []netbox.Object{stampedLiveTag(9, "6f1a-uid")}}
	events := &fakeRecorder{}

	engine := stampedEngine(t, stampableDescriptor(), client)
	engine.Events = events

	obj := deletingStampedObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := client.methods(); !slices.Equal(got, []string{"GETONE", "DELETE"}) {
		t.Fatalf("netbox calls = %v, want the stamp search and then the DELETE", got)
	}

	// The id came from the search, and the DELETE has to have used it rather than the 0 in
	// status: an operator that deletes /0 has recovered nothing.
	if got := client.calls[1].id; got != 9 {
		t.Errorf("DELETE id = %d, want 9", got)
	}

	// The search is by the uid custom field. A natural-key filter here would be the lookup
	// this file refuses to make -- it would delete whatever happened to match.
	if got := client.calls[0].params["cf_k8s_uid"]; got != "6f1a-uid" {
		t.Errorf("search params = %v, want cf_k8s_uid=6f1a-uid", client.calls[0].params)
	}

	if got := obj.GetFinalizers(); len(got) != 0 {
		t.Errorf("finalizers = %v, want none", got)
	}

	if !slices.Equal(events.events, []string{"Normal/Deleted"}) {
		t.Errorf("events = %v, want Normal/Deleted", events.events)
	}
}

// TestDeleteReportsNothingToDeleteAfterSearching is the other answer the same search gives,
// and the reason it is worth making: an empty result proves the create never happened, where
// before the operator could only say it could not tell.
func TestDeleteReportsNothingToDeleteAfterSearching(t *testing.T) {
	client := &fakeClient{}
	events := &fakeRecorder{}

	engine := stampedEngine(t, stampableDescriptor(), client)
	engine.Events = events

	obj := deletingStampedObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := client.methods(); !slices.Equal(got, []string{"GETONE"}) {
		t.Fatalf("netbox calls = %v, want the search and nothing else", got)
	}

	if got := obj.GetFinalizers(); len(got) != 0 {
		t.Errorf("finalizers = %v, want none: nothing exists to hold the CR back", got)
	}

	if !slices.Equal(events.events, []string{"Normal/NothingToDelete"}) {
		t.Errorf("events = %v, want Normal/NothingToDelete", events.events)
	}
}

// TestDeleteWithoutAnEndpointStillCompletes keeps the property the search must not cost: a CR
// that never created anything deletes while NetBox is unreachable. An escape hatch that only
// works when it is not needed is not an escape hatch.
func TestDeleteWithoutAnEndpointStillCompletes(t *testing.T) {
	client := &fakeClient{}
	events := &fakeRecorder{}

	engine := stampedEngine(t, stampableDescriptor(), client)
	engine.Events = events
	engine.Endpoints = fakeEndpoints{endpoint: stampedEndpoint(client), ready: false}

	obj := deletingStampedObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := client.methods(); len(got) != 0 {
		t.Errorf("netbox calls = %v, want none: there is no client to search with", got)
	}

	if got := obj.GetFinalizers(); len(got) != 0 {
		t.Errorf("finalizers = %v, want none", got)
	}

	if !slices.Equal(events.events, []string{"Normal/NothingToDelete"}) {
		t.Errorf("events = %v, want Normal/NothingToDelete", events.events)
	}
}
