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
// answer -- and defaulting it the other way is what #304 was reported as.
//
// There is no per-kind exception any more. Decision #176 made the IPAM kinds retain and #304
// reversed it: a default of Retain deletes the CR, keeps the NetBox object, and the object it
// keeps is the one NetBox then cites with a PROTECT to refuse the delete of the site above it.
// A claim reaches the same function and gets the same answer, which is the point of there
// being one function (#225).
func TestDeletionPolicyDefaultsToDelete(t *testing.T) {
	cases := []struct {
		name   string
		stated netboxv1alpha1.DeletionPolicy
		want   netboxv1alpha1.DeletionPolicy
	}{
		{"unset", "", netboxv1alpha1.DeletionDelete},
		{"stated Delete", netboxv1alpha1.DeletionDelete, netboxv1alpha1.DeletionDelete},
		{"stated Retain beats the default", netboxv1alpha1.DeletionRetain, netboxv1alpha1.DeletionRetain},
	}

	for _, tc := range cases {
		if got := deletionPolicyOf(tc.stated); got != tc.want {
			t.Errorf("%s: deletionPolicyOf(%q) = %q, want %q", tc.name, tc.stated, got, tc.want)
		}
	}
}

// TestEveryKindDefaultsToDelete is what is left of criterion 2 of #186, and it is worth
// keeping in the weaker form.
//
// #186's bug was a per-kind default that the docs and the registry disagreed about, with
// `make test` green because nothing read the default per kind at all. The written-out table
// that fixed it is gone with the per-kind default it described (#304) -- a table of ~120 rows
// all saying Delete is a table nobody would keep correct -- but the *reading* stays: this
// walks the registry and asserts through the same function finalizer.go calls, so a kind that
// somehow reintroduces a Retain default fails here rather than in somebody's cluster.
func TestEveryKindDefaultsToDelete(t *testing.T) {
	descriptors := registry.List()
	if len(descriptors) == 0 {
		t.Fatal("the registry is empty; this test would pass by describing nothing")
	}

	for _, descriptor := range descriptors {
		if got := deletionPolicyOf(""); got != netboxv1alpha1.DeletionDelete {
			t.Errorf("%s defaults to %q, want %q", descriptor.GVK.Kind, got,
				netboxv1alpha1.DeletionDelete)
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
