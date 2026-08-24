package reconciler

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// testResync is deliberately not the default, so a case that expects the endpoint's own
// interval cannot pass by accident.
const testResync = 5 * time.Minute

// liveTag is the NetBox object that matches fakeObject() exactly, in the shape NetBox
// returns it: a nested object-type list, a url the engine records, and the `display` every
// NetBox serializer sends -- which is what a Conflict names alongside each ambiguous id.
func liveTag(id int) netbox.Object {
	return netbox.Object{
		"id":           float64(id),
		"url":          "https://netbox.invalid/api/extras/tags/1/",
		"display":      "Managed",
		"name":         "Managed",
		"slug":         "managed",
		"color":        "9e9e9e",
		"object_types": []any{},
		"created":      "2026-08-01T00:00:00Z",
	}
}

func TestEngineReconcile(t *testing.T) {
	tests := []struct {
		name       string
		descriptor registry.Descriptor
		object     func() *fakeKind
		client     func(t *testing.T) *fakeClient
		notReady   bool

		wantMethods []string
		wantPayload netbox.Object
		wantID      int64
		wantAdopted bool
		wantReady   metav1.ConditionStatus
		wantReason  string
		wantSynced  string
		wantRefs    string
		wantEvents  []string
		wantMessage string
		wantRequeue time.Duration
		wantWrites  int

		// noRefsCondition marks a case that stops before it reads the spec, so there is no
		// reference report to make.
		noRefsCondition bool
	}{
		{
			name:   "create when nothing matches",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{created: liveTag(7)}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantPayload: netbox.Object{"name": "Managed", "slug": "managed", "color": "9e9e9e"},
			wantID:      7,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantSynced:  netboxv1alpha1.ReasonDriftCorrected,
			wantRefs:    netboxv1alpha1.ReasonAllResolved,
			wantEvents:  []string{"Normal/Created"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name: "adopt an existing object when the policy allows it",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.OnConflict = netboxv1alpha1.ConflictAdopt

				return obj
			},
			client: func(*testing.T) *fakeClient {
				return &fakeClient{list: []netbox.Object{liveTag(9)}}
			},
			wantMethods: []string{"GETONE"},
			wantID:      9,
			wantAdopted: true,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantSynced:  netboxv1alpha1.ReasonNoDrift,
			wantEvents:  []string{"Normal/Adopted"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:   "refuse to adopt by default, naming the object it will not touch",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{list: []netbox.Object{liveTag(9)}}
			},
			wantMethods: []string{"GETONE"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantMessage: "netbox object 9 already matches",
			wantEvents:  []string{"Warning/Conflict"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name: "adopt-only never creates",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.OnConflict = netboxv1alpha1.ConflictAdoptOnly

				return obj
			},
			client:      func(*testing.T) *fakeClient { return &fakeClient{} },
			wantMethods: []string{"GETONE"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonAdoptOnly,
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name: "patch exactly the field that drifted",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Status.ID = 7

				return obj
			},
			client: func(*testing.T) *fakeClient {
				live := liveTag(7)
				live["color"] = "ff0000"

				return &fakeClient{get: live, patched: liveTag(7)}
			},
			wantMethods: []string{"GET", "PATCH"},
			wantPayload: netbox.Object{"color": "9e9e9e"},
			wantID:      7,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantSynced:  netboxv1alpha1.ReasonDriftCorrected,
			wantEvents:  []string{"Normal/Updated"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name: "no drift writes nothing to netbox",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Status.ID = 7

				return obj
			},
			client:      func(*testing.T) *fakeClient { return &fakeClient{get: liveTag(7)} },
			wantMethods: []string{"GET"},
			wantID:      7,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantSynced:  netboxv1alpha1.ReasonNoDrift,
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			// The user's next question is always "which two?", so the ids -- and the display
			// each one carries, which is what a human recognises -- have to be in the
			// message. A count would leave them to reproduce the query by hand (NBO-074).
			name:   "more than one match is a conflict naming every id",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{list: []netbox.Object{liveTag(4), liveTag(9)}}
			},
			wantMethods: []string{"GETONE"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantMessage: "matched 2 netbox objects, id 4 (Managed), id 9 (Managed)",
			wantEvents:  []string{"Warning/Conflict"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:       "an unresolvable parent leaves no candidate, and nothing is written",
			descriptor: parentedDescriptor(),
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.ParentRef = &fakeRef{Name: "europe"}

				return obj
			},
			client:      func(*testing.T) *fakeClient { return &fakeClient{} },
			wantMethods: nil,
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonWaitingForKey,
			wantRefs:    netboxv1alpha1.ReasonNotImplemented,
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:   "a 400 reports netbox's field errors and stops",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{createErr: &netbox.ValidationError{
					Status: 400,
					Fields: map[string][]string{"slug": {"This field must be unique."}},
				}}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonInvalid,
			wantMessage: "slug: This field must be unique.",
			wantEvents:  []string{"Warning/Invalid"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:   "a 409 is a conflict, not a retry",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{createErr: &netbox.ProtectedError{Status: 409, Body: "already exists"}}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantEvents:  []string{"Warning/Conflict"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:   "a transient failure comes back soon and says nothing about the spec",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{createErr: &netbox.TransientError{Status: 503}}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: transientRetry,
			wantWrites:  1,
		},
		{
			name:   "rate limiting waits as long as the server asked",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{createErr: &netbox.RateLimitError{RetryAfter: 90 * time.Second}}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: 90 * time.Second,
			wantWrites:  1,
		},
		{
			name:   "a dry-run create invents no id",
			object: fakeObject,
			client: func(t *testing.T) *fakeClient {
				return &fakeClient{dryRun: dryRunClient(t)}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantID:      0,
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonDryRunPending,
			wantSynced:  netboxv1alpha1.ReasonDriftDetectedDryRun,
			wantEvents:  []string{"Normal/Created"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:            "an endpoint that is not ready is a wait, not a failure",
			object:          fakeObject,
			client:          func(*testing.T) *fakeClient { return &fakeClient{} },
			notReady:        true,
			wantMethods:     nil,
			wantReady:       metav1.ConditionFalse,
			wantReason:      netboxv1alpha1.ReasonWaitingForEndpoint,
			wantRequeue:     endpointRetry,
			wantWrites:      1,
			noRefsCondition: true,
		},
		{
			name: "an id pointing at a deleted object recovers by creating a new one",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Status.ID = 7

				return obj
			},
			client: func(*testing.T) *fakeClient {
				return &fakeClient{
					getErr:  &netbox.NotFoundError{Endpoint: "extras/tags", ID: 7},
					created: liveTag(11),
				}
			},
			wantMethods: []string{"GET", "GETONE", "POST"},
			wantID:      11,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantSynced:  netboxv1alpha1.ReasonDriftCorrected,
			wantEvents:  []string{"Normal/Created"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name:       "a change to an identity-bearing field is a delete and a create",
			descriptor: recreateDescriptor(),
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Status.ID = 7

				return obj
			},
			client: func(*testing.T) *fakeClient {
				live := liveTag(7)
				live["slug"] = "was-managed"

				return &fakeClient{get: live, created: liveTag(12)}
			},
			wantMethods: []string{"GET", "DELETE", "POST"},
			wantID:      12,
			wantReady:   metav1.ConditionTrue,
			wantReason:  netboxv1alpha1.ReasonSynced,
			wantSynced:  netboxv1alpha1.ReasonDriftCorrected,
			wantEvents:  []string{"Normal/Recreated"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
		{
			name: "a spec field with no mapping is refused rather than dropped",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.Unmapped = "surprise"

				return obj
			},
			client:          func(*testing.T) *fakeClient { return &fakeClient{} },
			wantMethods:     nil,
			wantReady:       metav1.ConditionFalse,
			wantReason:      netboxv1alpha1.ReasonInvalid,
			wantMessage:     "unmapped",
			wantEvents:      []string{"Warning/Invalid"},
			wantRequeue:     testResync,
			wantWrites:      1,
			noRefsCondition: true,
		},
		{
			// The object is created without the reference and reports Ready=False, which is
			// issue #132: on a kind whose identity does not include the reference, reporting
			// Ready would make `kubectl wait --for=condition=Ready` pass over a field NetBox
			// never received. TestUnresolvedRefKeepsTheObjectFromReadiness is the dedicated
			// case; this one holds the line for the whole outcome table.
			name: "a declared reference that does not resolve is created without it and is not Ready",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.ParentRef = &fakeRef{Name: "europe"}

				return obj
			},
			client: func(*testing.T) *fakeClient {
				return &fakeClient{created: liveTag(7)}
			},
			wantMethods: []string{"GETONE", "POST"},
			wantPayload: netbox.Object{"name": "Managed", "slug": "managed", "color": "9e9e9e"},
			wantID:      7,
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonWaitingForRef,
			wantMessage: "parentRef",
			wantSynced:  netboxv1alpha1.ReasonDriftCorrected,
			wantRefs:    netboxv1alpha1.ReasonNotImplemented,
			wantEvents:  []string{"Normal/Created"},
			wantRequeue: testResync,
			wantWrites:  1,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := tc.descriptor
			if descriptor.GVK.Empty() {
				descriptor = fakeDescriptor()
			}

			client := tc.client(t)
			status := &fakeStatus{}
			events := &fakeRecorder{}
			obj := tc.object()

			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: descriptor, registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: client, Resync: testResync},
					ready:    !tc.notReady,
				},
				Status:     status,
				Finalizers: &fakeFinalizers{},
				Events:     events,
				Scheme:     fakeScheme(t),
			}

			result, err := engine.Reconcile(context.Background(), obj)
			if err != nil {
				t.Fatalf("Reconcile() = %v, want no error: netbox availability is a condition, not a failure", err)
			}

			if got := client.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Errorf("netbox calls = %v, want %v", got, tc.wantMethods)
			}

			if tc.wantPayload != nil {
				if got := client.lastPayload(); !reflect.DeepEqual(got, tc.wantPayload) {
					t.Errorf("payload = %v, want %v", got, tc.wantPayload)
				}
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

			if tc.wantMessage != "" && !strings.Contains(ready.Message, tc.wantMessage) {
				t.Errorf("Ready message = %q, want it to contain %q", ready.Message, tc.wantMessage)
			}

			if got := conditionOf(obj, netboxv1alpha1.ConditionSynced).Reason; got != tc.wantSynced {
				t.Errorf("Synced reason = %q, want %q", got, tc.wantSynced)
			}

			// Unless a case says otherwise, the spec declares no references and the
			// condition says so rather than staying unset.
			wantRefs := tc.wantRefs
			if wantRefs == "" && !tc.noRefsCondition {
				wantRefs = netboxv1alpha1.ReasonAllResolved
			}

			if got := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved).Reason; got != wantRefs {
				t.Errorf("RefsResolved reason = %q, want %q", got, wantRefs)
			}

			if !slices.Equal(events.events, tc.wantEvents) {
				t.Errorf("events = %v, want %v", events.events, tc.wantEvents)
			}

			if status.writes != tc.wantWrites {
				t.Errorf("status writes = %d, want %d", status.writes, tc.wantWrites)
			}

			assertRequeue(t, result.RequeueAfter, tc.wantRequeue)

			if obj.Status.ObservedGeneration != obj.Generation {
				t.Errorf("status.observedGeneration = %d, want %d: kubectl wait lies without it",
					obj.Status.ObservedGeneration, obj.Generation)
			}
		})
	}
}

// TestEngineReconcileIsIdempotent is the assertion the whole design exists to support: an
// object that is already correct costs one read, no write to NetBox, and no write to the
// cluster either -- a status update every resync would churn the resourceVersion of every
// object in the cluster for no new information.
func TestEngineReconcileIsIdempotent(t *testing.T) {
	client := &fakeClient{created: liveTag(7)}
	status := &fakeStatus{}
	obj := fakeObject()

	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: client, Resync: testResync}, ready: true},
		Status:      status,
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}

	// The first pass creates; every pass after it finds the object by id and matches.
	client.get = liveTag(7)

	const passes = 50

	settled := 0
	for i := range passes {
		if _, err := engine.Reconcile(context.Background(), obj); err != nil {
			t.Fatalf("Reconcile() pass %d = %v", i, err)
		}

		// Two writes are expected and then no more: the create, and the pass after it,
		// where Synced settles from DriftCorrected to NoDrift.
		if i == 1 {
			settled = status.writes
		}
	}

	posts := 0
	for _, method := range client.methods() {
		if method == "POST" {
			posts++
		}
	}

	if posts != 1 {
		t.Errorf("POSTs over %d reconciles = %d, want exactly 1", passes, posts)
	}

	if settled != 2 {
		t.Errorf("status writes over the first two reconciles = %d, want 2", settled)
	}

	if status.writes != settled {
		t.Errorf("status writes over %d reconciles = %d, want no more than the %d it took to settle",
			passes, status.writes, settled)
	}
}

// TestEngineReconcileUnregisteredKind is the one failure the engine returns as an error: a
// controller wired for a kind with no descriptor cannot be fixed by requeueing.
func TestEngineReconcileUnregisteredKind(t *testing.T) {
	engine := &Engine{
		Descriptors: fakeDescriptors{},
		Endpoints:   fakeEndpoints{ready: false},
		Status:      &fakeStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), fakeObject()); err == nil {
		t.Fatal("Reconcile() = nil, want an error for an unregistered kind")
	}
}

// TestEngineReconcileStatusWriteFails checks the one error worth returning from the write
// path: a failed status update has to be retried, or the object's status silently lags its
// NetBox object forever.
func TestEngineReconcileStatusWriteFails(t *testing.T) {
	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: &fakeClient{created: liveTag(7)}, Resync: testResync},
			ready:    true,
		},
		Status:     &fakeStatus{err: errStatusWrite},
		Finalizers: &fakeFinalizers{},
		Scheme:     fakeScheme(t),
	}

	if _, err := engine.Reconcile(context.Background(), fakeObject()); err == nil {
		t.Fatal("Reconcile() = nil, want the status write error")
	}
}

// TestFakeDescriptorIsValid keeps the fixtures honest: a test that proves the engine
// against a descriptor the registry would reject proves nothing.
func TestFakeDescriptorIsValid(t *testing.T) {
	for _, d := range []registry.Descriptor{fakeDescriptor(), parentedDescriptor(), recreateDescriptor()} {
		if err := d.Validate(); err != nil {
			t.Errorf("descriptor %s: %v", d.GVK.Kind, err)
		}
	}
}

// assertRequeue allows for the jitter every requeue carries.
func assertRequeue(t *testing.T, got, want time.Duration) {
	t.Helper()

	if want == 0 {
		if got != 0 {
			t.Errorf("requeueAfter = %s, want none", got)
		}

		return
	}

	low, high := want-want/10, want+want/10
	if got < low || got > high {
		t.Errorf("requeueAfter = %s, want %s ± 10%%", got, want)
	}
}

// TestTruncatedLookupCreatesNothing is the regression test for the worst defect found in
// this codebase so far. Client.List used to return partial results when it hit its page
// cap, the lookup below found no match in the pages it received, and the engine took the
// create path -- so a NetBox object that already existed was created a second time, caused
// by a safety limit.
//
// The engine's part of the fix is that it must treat the error as an error. Asserting it
// here rather than only in the client keeps the two halves honest: the client could regress
// to returning partial data and this test would still fail.
func TestTruncatedLookupCreatesNothing(t *testing.T) {
	descriptor := fakeDescriptor()
	client := &fakeClient{
		listErr: &netbox.TruncatedError{Endpoint: descriptor.Endpoint, MaxPages: 3, Collected: 750},
	}
	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: descriptor, registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: client, Resync: testResync},
			ready:    true,
		},
		Status:     &fakeStatus{},
		Finalizers: &fakeFinalizers{},
		Scheme:     fakeScheme(t),
	}

	obj := fakeObject()
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v; a NetBox failure is a condition, not a returned error", err)
	}

	// The truncation must reach the object, or a user has no way to learn why nothing
	// happened.
	if !mentions(obj, "truncated") {
		t.Errorf("no condition mentions the truncation; conditions = %v", obj.NetBoxStatus().Conditions)
	}
	// The assertion that matters: no write of any kind.
	for _, method := range client.methods() {
		if method == "POST" || method == "PATCH" || method == "DELETE" {
			t.Errorf("engine issued %s on a truncated lookup; this is the duplicate-object bug", method)
		}
	}
}

// mentions reports whether any condition message on obj contains want. Conditions are the
// only channel a user has for a failure the engine deliberately does not return.
func mentions(obj Object, want string) bool {
	for _, condition := range obj.NetBoxStatus().Conditions {
		if strings.Contains(condition.Message, want) {
			return true
		}
	}

	return false
}
