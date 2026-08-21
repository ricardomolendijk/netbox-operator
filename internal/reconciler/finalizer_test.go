package reconciler

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
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
			status := &fakeStatus{}
			finalizers := &fakeFinalizers{}
			events := &fakeRecorder{}
			obj := tc.object()

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: client, Resync: testResync, DryRun: tc.dryRun},
					ready:    !tc.notReady,
				},
				Status:     status,
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
		{attempts: 2, want: 20 * time.Second},
		{attempts: 3, want: 40 * time.Second},
		{attempts: 5, want: 160 * time.Second},
		{attempts: 6, want: protectedRetryCap},
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
		Finalizers: &fakeFinalizers{},
		Events:     events,
		Scheme:     fakeScheme(t),
	}

	const passes = 6

	for i := range passes {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}
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

// TestDeletionPolicyDefaultsToDelete guards the stored-object case. The CRD default only
// applies to objects written after the marker existed, so the engine defaults it too --
// and defaulting it the other way would leave objects behind.
func TestDeletionPolicyDefaultsToDelete(t *testing.T) {
	if got := deletionPolicyOf(fakeObject()); got != netboxv1alpha1.DeletionDelete {
		t.Errorf("deletionPolicyOf(unset) = %q, want %q", got, netboxv1alpha1.DeletionDelete)
	}
}
