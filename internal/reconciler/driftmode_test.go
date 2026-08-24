package reconciler

import (
	"context"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestDriftModes covers what each of Correct, Report and Off makes the engine say and when
// it comes back (NBO-065, docs/decisions/0005-gitops-coexistence.md §5).
//
// What it deliberately does not assert is that Report sends no HTTP request, because the
// engine is the wrong place to observe that: Report is enforced by handing the engine a
// client that suppresses mutations, so the engine still calls Patch and the suppression
// happens below it. The fake records the call it made either way. That NetBox receives
// nothing is asserted where it is observable -- against a real HTTP stub in
// internal/controller/gitops_test.go, and against the client itself in
// internal/netbox/client_test.go.
func TestDriftModes(t *testing.T) {
	tests := []struct {
		name      string
		driftMode netboxv1alpha1.DriftMode
		object    func() *fakeKind
		client    func(t *testing.T) *fakeClient

		wantMethods   []string
		wantSynced    string
		wantReady     metav1.ConditionStatus
		wantReason    string
		wantDrift     metav1.ConditionStatus
		wantDriftWhy  string
		wantEvents    []string
		wantResult    string
		wantRequeue   time.Duration
		wantCorrected bool
	}{
		{
			name:      "Correct fixes a netbox-side edit",
			driftMode: netboxv1alpha1.DriftCorrect,
			object:    adoptedObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(), patched: liveTag(adoptedID)}
			},
			wantMethods:   []string{"GET", "PATCH"},
			wantSynced:    netboxv1alpha1.ReasonDriftCorrected,
			wantReady:     metav1.ConditionTrue,
			wantReason:    netboxv1alpha1.ReasonSynced,
			wantDrift:     metav1.ConditionFalse,
			wantDriftWhy:  netboxv1alpha1.ReasonDriftCorrected,
			wantEvents:    []string{"Normal/Updated"},
			wantResult:    metrics.ResultUpdated,
			wantRequeue:   testResync,
			wantCorrected: true,
		},
		{
			// An endpoint stored before spec.driftMode existed carries the empty value, and
			// the CRD default cannot retrofit itself onto it. It has to behave as Correct or
			// an upgrade would silently stop correcting drift.
			name:      "an unset mode is Correct",
			driftMode: "",
			object:    adoptedObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(), patched: liveTag(adoptedID)}
			},
			wantMethods:   []string{"GET", "PATCH"},
			wantSynced:    netboxv1alpha1.ReasonDriftCorrected,
			wantReady:     metav1.ConditionTrue,
			wantReason:    netboxv1alpha1.ReasonSynced,
			wantDrift:     metav1.ConditionFalse,
			wantDriftWhy:  netboxv1alpha1.ReasonDriftCorrected,
			wantEvents:    []string{"Normal/Updated"},
			wantResult:    metrics.ResultUpdated,
			wantRequeue:   testResync,
			wantCorrected: true,
		},
		{
			name:      "Report names the fields it is not correcting",
			driftMode: netboxv1alpha1.DriftReport,
			object:    adoptedObject,
			client: func(t *testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(), dryRun: dryRunClient(t)}
			},
			wantMethods: []string{"GET", "PATCH"},
			wantSynced:  netboxv1alpha1.ReasonDriftReported,
			// Not Ready, and deliberately so: NetBox does not match the spec and saying it
			// does would make `kubectl wait` lie about a write that never happened.
			wantReady:    metav1.ConditionFalse,
			wantReason:   netboxv1alpha1.ReasonReportPending,
			wantDrift:    metav1.ConditionTrue,
			wantDriftWhy: netboxv1alpha1.ReasonDriftDetected,
			wantEvents:   []string{"Normal/DriftDetected"},
			wantResult:   metrics.ResultReported,
			wantRequeue:  testResync,
		},
		{
			// The reasons have to distinguish the two, because they are set in different
			// fields and fixed in different ways.
			name:      "a DryRun endpoint still reports DryRun",
			driftMode: netboxv1alpha1.DriftCorrect,
			object:    adoptedObject,
			client: func(t *testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(), dryRun: dryRunClient(t)}
			},
			wantMethods:  []string{"GET", "PATCH"},
			wantSynced:   netboxv1alpha1.ReasonDriftDetectedDryRun,
			wantReady:    metav1.ConditionFalse,
			wantReason:   netboxv1alpha1.ReasonDryRunPending,
			wantDrift:    metav1.ConditionTrue,
			wantDriftWhy: netboxv1alpha1.ReasonDriftDetected,
			wantEvents:   []string{"Normal/Updated"},
			wantResult:   metrics.ResultDryRun,
			wantRequeue:  testResync,
		},
		{
			// A missing object is drift too, and it is the one case where the drift is not a
			// field list. Reporting "created" for something that was not created is the kind
			// of message that makes people stop believing the mode.
			name:      "Report creates nothing and says the object is absent",
			driftMode: netboxv1alpha1.DriftReport,
			object:    fakeObject,
			client: func(t *testing.T) *fakeClient {
				return &fakeClient{dryRun: dryRunClient(t)}
			},
			wantMethods:  []string{"GETONE", "POST"},
			wantSynced:   netboxv1alpha1.ReasonDriftReported,
			wantReady:    metav1.ConditionFalse,
			wantReason:   netboxv1alpha1.ReasonReportPending,
			wantDrift:    metav1.ConditionTrue,
			wantDriftWhy: netboxv1alpha1.ReasonDriftDetected,
			wantEvents:   []string{"Normal/DriftDetected"},
			wantResult:   metrics.ResultReported,
			wantRequeue:  testResync,
		},
		{
			name:      "Off does not come back on its own",
			driftMode: netboxv1alpha1.DriftOff,
			object:    adoptedObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: liveTag(adoptedID)}
			},
			wantMethods:  []string{"GET"},
			wantSynced:   netboxv1alpha1.ReasonNoDrift,
			wantReady:    metav1.ConditionTrue,
			wantReason:   netboxv1alpha1.ReasonSynced,
			wantDrift:    metav1.ConditionFalse,
			wantDriftWhy: netboxv1alpha1.ReasonNoDrift,
			wantResult:   metrics.ResultUnchanged,
			wantRequeue:  0,
		},
		{
			// Off suppresses the drift re-check, not the retry that gets a blocked object
			// unstuck. A conflict that never comes back is an object stuck forever on a
			// state somebody else's next apply would have resolved.
			name:      "Off still retries an object it could not claim",
			driftMode: netboxv1alpha1.DriftOff,
			object:    fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{list: []netbox.Object{liveTag(9)}}
			},
			wantMethods: []string{"GETONE"},
			wantReady:   metav1.ConditionFalse,
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantEvents:  []string{"Warning/Conflict"},
			wantResult:  metrics.ResultError,
			wantRequeue: testResync,
		},
		{
			// The point of Off: a spec change is a CR event, and a CR event reconciles
			// whatever the mode.
			name:      "Off still writes when the CR changed",
			driftMode: netboxv1alpha1.DriftOff,
			object:    adoptedObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(), patched: liveTag(adoptedID)}
			},
			wantMethods:   []string{"GET", "PATCH"},
			wantSynced:    netboxv1alpha1.ReasonDriftCorrected,
			wantReady:     metav1.ConditionTrue,
			wantReason:    netboxv1alpha1.ReasonSynced,
			wantDrift:     metav1.ConditionFalse,
			wantDriftWhy:  netboxv1alpha1.ReasonDriftCorrected,
			wantEvents:    []string{"Normal/Updated"},
			wantResult:    metrics.ResultUpdated,
			wantRequeue:   0,
			wantCorrected: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := test.client(t)
			obj := test.object()
			events := &fakeRecorder{}
			engine := &Engine{
				Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
				Endpoints: fakeEndpoints{ready: true, endpoint: Endpoint{
					Client:    client,
					Resync:    testResync,
					DriftMode: test.driftMode,
				}},
				Status:     &fakeStatus{},
				Finalizers: &fakeFinalizers{},
				Events:     events,
				Scheme:     fakeScheme(t),
			}

			corrected := watch(t, metrics.DriftCorrected, []string{fakeGVK.Kind, "color"})
			reconciles := watch(t, metrics.ReconcileTotal, []string{fakeGVK.Kind, test.wantResult})

			result, err := engine.Reconcile(context.Background(), obj)
			if err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			if got := client.methods(); !slices.Equal(got, test.wantMethods) {
				t.Errorf("netbox requests = %v, want %v", got, test.wantMethods)
			}

			if ready := conditionOf(obj, netboxv1alpha1.ConditionReady); ready.Status != test.wantReady ||
				ready.Reason != test.wantReason {
				t.Errorf("Ready = %s/%s, want %s/%s",
					ready.Status, ready.Reason, test.wantReady, test.wantReason)
			}

			if synced := conditionOf(obj, netboxv1alpha1.ConditionSynced); test.wantSynced != "" &&
				synced.Reason != test.wantSynced {
				t.Errorf("Synced reason = %q, want %q", synced.Reason, test.wantSynced)
			}

			drift := conditionOf(obj, netboxv1alpha1.ConditionDriftDetected)
			if test.wantDriftWhy != "" && (drift.Status != test.wantDrift || drift.Reason != test.wantDriftWhy) {
				t.Errorf("DriftDetected = %s/%s, want %s/%s",
					drift.Status, drift.Reason, test.wantDrift, test.wantDriftWhy)
			}

			if test.wantEvents != nil && !slices.Equal(events.events, test.wantEvents) {
				t.Errorf("events = %v, want %v", events.events, test.wantEvents)
			}

			assertRequeue(t, result.RequeueAfter, test.wantRequeue)

			// drift_detected_total moving while drift_corrected_total does not is the whole
			// signal Report mode produces on a dashboard: without the gap, "reporting as
			// configured" and "healthy" are the same shape.
			if got := reconciles.delta(fakeGVK.Kind, test.wantResult); got != 1 {
				t.Errorf("reconcile_total{result=%q} moved by %v, want 1", test.wantResult, got)
			}

			if moved := corrected.delta(fakeGVK.Kind, "color") > 0; moved != test.wantCorrected {
				t.Errorf("drift_corrected_total{field=color} moved = %v, want %v",
					moved, test.wantCorrected)
			}
		})
	}
}

// TestReportModeCountsDetectedButNotCorrected states the metric pair on its own, because it
// is the acceptance criterion a dashboard is built against.
func TestReportModeCountsDetectedButNotCorrected(t *testing.T) {
	client := &fakeClient{get: driftedTag(), dryRun: dryRunClient(t)}
	engine := &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{ready: true, endpoint: Endpoint{
			Client:    client,
			Resync:    testResync,
			DriftMode: netboxv1alpha1.DriftReport,
		}},
		Status:     &fakeStatus{},
		Finalizers: &fakeFinalizers{},
		Scheme:     fakeScheme(t),
	}

	detected := watch(t, metrics.DriftDetected, []string{fakeGVK.Kind, "color"})
	corrected := watch(t, metrics.DriftCorrected, []string{fakeGVK.Kind, "color"})

	if _, err := engine.Reconcile(context.Background(), adoptedObject()); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := detected.delta(fakeGVK.Kind, "color"); got != 1 {
		t.Errorf("drift_detected_total{field=color} moved by %v, want 1", got)
	}

	if got := corrected.delta(fakeGVK.Kind, "color"); got != 0 {
		t.Errorf("drift_corrected_total{field=color} moved by %v, want 0: Report corrects nothing", got)
	}
}
