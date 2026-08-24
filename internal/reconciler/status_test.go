package reconciler

import (
	"context"
	"slices"
	"testing"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// loggedLine is one line the engine emitted, with the level it was emitted at. The level
// is what these tests are about -- "one error line, and the repeat at debug" -- so it is
// recorded rather than reconstructed from formatted output, where an `"err"=` key and a
// real error are indistinguishable.
type loggedLine struct {
	// severe marks a line logged through Error rather than Info.
	severe bool

	// verbosity is the V() level of an Info line, and zero for an Error one.
	verbosity int

	msg string
}

// recordingSink is a logr.LogSink that keeps every line. Enabled at every level, so a
// test asserting that a repeat was demoted to debug can see that it was logged at all --
// a sink that dropped debug lines would make "demoted" and "deleted" look the same.
type recordingSink struct {
	lines *[]loggedLine
}

func (s recordingSink) Init(logr.RuntimeInfo) {}
func (s recordingSink) Enabled(int) bool      { return true }

func (s recordingSink) Info(level int, msg string, _ ...any) {
	*s.lines = append(*s.lines, loggedLine{verbosity: level, msg: msg})
}

func (s recordingSink) Error(_ error, msg string, _ ...any) {
	*s.lines = append(*s.lines, loggedLine{severe: true, msg: msg})
}

func (s recordingSink) WithValues(...any) logr.LogSink { return s }
func (s recordingSink) WithName(string) logr.LogSink   { return s }

// stopHarness is an engine wired to count everything a failing pass emits: the Events, the
// status writes, and the log lines with their levels.
type stopHarness struct {
	client *fakeClient
	events *fakeRecorder
	status *fakeStatus
	lines  *[]loggedLine
	engine *Engine
}

func newStopHarness(t *testing.T, client *fakeClient) *stopHarness {
	t.Helper()

	h := &stopHarness{
		client: client,
		events: &fakeRecorder{},
		status: &fakeStatus{},
		lines:  &[]loggedLine{},
	}

	h.engine = &Engine{
		Descriptors: fakeDescriptors{descriptor: fakeDescriptor(), registered: true},
		Endpoints: fakeEndpoints{
			endpoint: Endpoint{Client: client, Resync: testResync},
			ready:    true,
		},
		Status:     h.status,
		Finalizers: &fakeFinalizers{},
		Events:     h.events,
		Scheme:     fakeScheme(t),
	}

	return h
}

// reconcile runs one pass with the recording logger on the context, which is where the
// engine takes its logger from (CONTRIBUTING.md, "Logging").
func (h *stopHarness) reconcile(t *testing.T, obj *fakeKind) {
	t.Helper()

	ctx := logf.IntoContext(context.Background(), logr.New(recordingSink{lines: h.lines}))
	if _, err := h.engine.Reconcile(ctx, obj); err != nil {
		t.Fatalf("Reconcile() = %v, want no error: a spec netbox refuses is a condition, not a failure", err)
	}
}

// count returns how many recorded lines match.
func (h *stopHarness) count(match func(loggedLine) bool) int {
	total := 0

	for _, line := range *h.lines {
		if match(line) {
			total++
		}
	}

	return total
}

func (h *stopHarness) errorLines() int {
	return h.count(func(l loggedLine) bool { return l.severe })
}

// stillStopped counts the debug line a repeated severe failure is demoted to.
func (h *stopHarness) stillStopped() int {
	return h.count(func(l loggedLine) bool {
		return !l.severe && l.verbosity > 0 && l.msg == "reconcile is still stopped"
	})
}

// TestStandingFailureIsReportedOnce is the assertion whose absence is the whole reason
// NBO-081 existed: every failure case in generic_test.go reconciles exactly once, so a
// second identical pass was never looked at.
//
// A Conflict or an Invalid requeues at the endpoint's resync -- ten minutes by default --
// so an object whose spec NetBox keeps rejecting used to produce one Event and one
// error-level line every ten minutes, forever, on the path every object of every kind
// takes.
func TestStandingFailureIsReportedOnce(t *testing.T) {
	tests := []struct {
		name   string
		object func() *fakeKind
		client func() *fakeClient

		wantEvents []string
		wantReason string

		// wantErrorLines is one for a state that needs a human and zero for one that does
		// not; either way it is per standing failure and not per pass.
		wantErrorLines int

		// wantStillStopped is the debug line the suppressed error is demoted to, so the
		// information is still available to anyone who turns the verbosity up.
		wantStillStopped int

		wantResult string
	}{
		{
			// The permanent case: nothing about this object will change until a human
			// edits the spec or NetBox, and the requeue is the resync.
			name:   "a netbox object this CR may not take over",
			object: fakeObject,
			client: func() *fakeClient {
				return &fakeClient{list: []netbox.Object{liveTag(9)}}
			},
			wantEvents:       []string{"Warning/Conflict"},
			wantReason:       netboxv1alpha1.ReasonConflict,
			wantErrorLines:   1,
			wantStillStopped: 1,
			wantResult:       metrics.ResultError,
		},
		{
			name:   "a payload netbox rejects",
			object: fakeObject,
			client: func() *fakeClient {
				return &fakeClient{createErr: &netbox.ValidationError{
					Status: 400,
					Fields: map[string][]string{"slug": {"must be unique"}},
				}}
			},
			wantEvents:       []string{"Warning/Invalid"},
			wantReason:       netboxv1alpha1.ReasonInvalid,
			wantErrorLines:   1,
			wantStillStopped: 1,
			wantResult:       metrics.ResultError,
		},
		{
			// The transient case, where the volume is worst: a 5xx requeues every thirty
			// seconds, so twenty times as often as a Conflict. classify already keeps this
			// one off the Event stream and out of the error log by category -- the
			// assertion is that a repeat does not sneak either of them back in.
			name:   "netbox is unavailable",
			object: fakeObject,
			client: func() *fakeClient {
				return &fakeClient{listErr: &netbox.TransientError{Status: 503}}
			},
			wantEvents:     nil,
			wantReason:     netboxv1alpha1.ReasonAPIError,
			wantErrorLines: 0,
			wantResult:     metrics.ResultError,
		},
		{
			// A wait rather than a failure, and the reason classify's by-category
			// suppression cannot be replaced by the transition guard: every object in the
			// cluster passes through this state on its first pass.
			name: "still waiting for a natural key",
			object: func() *fakeKind {
				obj := fakeObject()
				obj.Spec.Slug = ""

				return obj
			},
			client:         func() *fakeClient { return &fakeClient{} },
			wantEvents:     nil,
			wantReason:     netboxv1alpha1.ReasonWaitingForKey,
			wantErrorLines: 0,
			wantResult:     metrics.ResultWaiting,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newStopHarness(t, tc.client())
			obj := tc.object()

			reconciles := watch(t, metrics.ReconcileTotal, []string{fakeGVK.Kind, tc.wantResult})

			// Twice, identically: the second pass is the resync a permanently failing
			// object spends the rest of its life in.
			h.reconcile(t, obj)
			h.reconcile(t, obj)

			if !slices.Equal(h.events.events, tc.wantEvents) {
				t.Errorf("events over two identical passes = %v, want %v: an Event is an API "+
					"object, and a duplicate every resync evicts the ones somebody needed",
					h.events.events, tc.wantEvents)
			}

			if got := h.errorLines(); got != tc.wantErrorLines {
				t.Errorf("error lines over two identical passes = %d, want %d: %v",
					got, tc.wantErrorLines, *h.lines)
			}

			if got := h.stillStopped(); got != tc.wantStillStopped {
				t.Errorf("`reconcile is still stopped` debug lines = %d, want %d: a suppressed "+
					"error line has to still be there for anyone who turns the verbosity up",
					got, tc.wantStillStopped)
			}

			// The condition is the standing state, which is the entire reason the Event
			// does not repeat -- so it has to be there after the pass that stayed quiet.
			ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
			if ready.Status != metav1.ConditionFalse || ready.Reason != tc.wantReason {
				t.Errorf("Ready after the second pass = %s/%s, want False/%s",
					ready.Status, ready.Reason, tc.wantReason)
			}

			if ready.Message == "" {
				t.Error("Ready message is empty after the second pass; the condition carries " +
					"what the suppressed Event would have said")
			}

			// The second pass had nothing new to say, so it must not churn the object's
			// resourceVersion either (NBO-078).
			if h.status.writes != 1 {
				t.Errorf("status writes over two identical passes = %d, want the one that "+
					"recorded the failure", h.status.writes)
			}

			// Metrics count reconciles, not changes: both passes, deliberately.
			if got := reconciles.delta(fakeGVK.Kind, tc.wantResult); got != 2 {
				t.Errorf("reconcile_total{result=%q} moved by %v over two passes, want 2: the "+
					"counter is of reconciles, and suppressing the repeat would hide the retry rate",
					tc.wantResult, got)
			}
		})
	}
}

// TestStandingFailureRestampsTheCondition is the other half of "the condition is the
// standing state": a spec edit that leaves the object just as invalid still has to move
// observedGeneration, or `kubectl wait` reports on a generation nobody looked at. The
// Event must not repeat, because the reason has not changed.
func TestStandingFailureRestampsTheCondition(t *testing.T) {
	h := newStopHarness(t, &fakeClient{createErr: &netbox.ValidationError{Status: 400, Body: "no"}})
	obj := fakeObject()

	h.reconcile(t, obj)

	// A spec edit: still refused, and still refused for the same reason.
	obj.Generation++
	obj.Spec.Color = "ff0000"
	h.reconcile(t, obj)

	if want := []string{"Warning/Invalid"}; !slices.Equal(h.events.events, want) {
		t.Errorf("events = %v, want %v: the reason did not change, so neither did the state",
			h.events.events, want)
	}

	if got := h.errorLines(); got != 1 {
		t.Errorf("error lines = %d, want 1: %v", got, *h.lines)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.ObservedGeneration != obj.Generation {
		t.Errorf("Ready observedGeneration = %d, want %d: the condition is written on every "+
			"pass, guard or no guard", ready.ObservedGeneration, obj.Generation)
	}

	if h.status.writes != 2 {
		t.Errorf("status writes = %d, want 2: the generation moved, so the status did", h.status.writes)
	}
}

// TestFailureReasonChangeIsReportedAgain is the message-insensitivity argument from the
// other side. The guard keys on status and reason, so a genuinely different failure is
// reported again -- and a message that differs by a word is not.
func TestFailureReasonChangeIsReportedAgain(t *testing.T) {
	client := &fakeClient{list: []netbox.Object{liveTag(9)}}
	h := newStopHarness(t, client)
	obj := fakeObject()

	// Conflict: a live object this CR is not allowed to adopt.
	h.reconcile(t, obj)

	// Same reason, different wording -- the id NetBox names has changed. Not a state
	// change, so nothing is reported again.
	client.list = []netbox.Object{liveTag(11)}
	h.reconcile(t, obj)

	if want := []string{"Warning/Conflict"}; !slices.Equal(h.events.events, want) {
		t.Fatalf("events after a message-only change = %v, want %v: keying on the message "+
			"would re-fire on every retry", h.events.events, want)
	}

	if got := h.errorLines(); got != 1 {
		t.Fatalf("error lines after a message-only change = %d, want 1: %v", got, *h.lines)
	}

	// Now the reason itself changes: nothing matches any more, and the create is refused.
	client.list = nil
	client.createErr = &netbox.ValidationError{Status: 400, Body: "slug already taken"}
	h.reconcile(t, obj)

	want := []string{"Warning/Conflict", "Warning/Invalid"}
	if !slices.Equal(h.events.events, want) {
		t.Errorf("events = %v, want %v: a different failure is a different state", h.events.events, want)
	}

	if got := h.errorLines(); got != 2 {
		t.Errorf("error lines = %d, want 2: %v", got, *h.lines)
	}

	if got := conditionOf(obj, netboxv1alpha1.ConditionReady).Reason; got != netboxv1alpha1.ReasonInvalid {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonInvalid)
	}
}

// TestRecoveryFromAStandingFailureIsReported closes the loop: suppressing the repeat must
// not suppress the object getting better, or the Event stream ends on the failure and
// `kubectl describe` says the object is still broken.
func TestRecoveryFromAStandingFailureIsReported(t *testing.T) {
	client := &fakeClient{createErr: &netbox.ValidationError{Status: 400, Body: "slug already taken"}}
	h := newStopHarness(t, client)
	obj := fakeObject()

	h.reconcile(t, obj)
	h.reconcile(t, obj)

	client.createErr = nil
	client.created = liveTag(7)
	h.reconcile(t, obj)

	want := []string{"Warning/Invalid", "Normal/Created"}
	if !slices.Equal(h.events.events, want) {
		t.Errorf("events = %v, want %v", h.events.events, want)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready after recovery = %s/%s, want True/%s",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonSynced)
	}
}
