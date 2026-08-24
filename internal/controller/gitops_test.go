package controller

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestStatusOnlyReconcileNeverBumpsGeneration is the never-write-spec invariant against a
// real API server, which is the only place it can be proved: the API server is what
// increments metadata.generation, and it does so for a change outside metadata and status
// and for nothing else. So a generation that has not moved across a reconcile that wrote
// status is a spec the operator did not touch (docs/decisions/0005-gitops-coexistence.md §1).
//
// This is the assertion an Argo CD Application depends on. Argo compares the live object
// with the manifest and reports OutOfSync on a spec that has diverged; a generation bump is
// how it finds out.
func TestStatusOnlyReconcileNeverBumpsGeneration(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)
	makeTag(t, ns, "stable", func(tag *netboxv1alpha1.NetBoxTag) { tag.Spec.Color = "2196f3" })

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "stable") })

	settled := mustFetchTag(t, ns, "stable")
	generation, version := settled.Generation, settled.ResourceVersion
	id := int(settled.Status.ID)

	// A NetBox-side edit, corrected: the busiest status-writing path there is, since it
	// writes lastSyncTime, lastAppliedHash and two conditions.
	stub.setField(t, id, "color", "ff0000")
	eventually(t, "the colour corrected", func() bool { return stub.tag(id)["color"] == "2196f3" })

	// Several resyncs on top, because the interesting failure is a slow leak: one path in
	// ten that writes the whole object rather than the status.
	time.Sleep(3 * time.Second)

	after := mustFetchTag(t, ns, "stable")

	if after.Generation != generation {
		t.Errorf("metadata.generation = %d, want the unchanged %d; a bump is the signature of a spec write",
			after.Generation, generation)
	}

	// The other half of the assertion: the object was genuinely written, so the
	// generation holding still is a statement about what was written rather than about
	// nothing having happened.
	if after.ResourceVersion == version {
		t.Errorf("resourceVersion is unchanged at %s; this test proved nothing", version)
	}

	if after.Status.ObservedGeneration != generation {
		t.Errorf("observedGeneration = %d, want %d", after.Status.ObservedGeneration, generation)
	}
}

// TestDriftModeReportLeavesNetBoxUntouched is the acceptance criterion for `Report`, and the
// reason it is asserted here rather than in the engine's own tests: the stub records
// requests that actually arrived, so "zero mutating requests" is observable rather than
// inferred.
//
// A half-mutating dry run is worse than none, because it teaches people to distrust the
// mode -- which is why Report is implemented by handing the engine a client that cannot
// write at all, and why this test asserts on the wire and not on a condition.
func TestDriftModeReportLeavesNetBoxUntouched(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpointWith(t, ns, stub.URL, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.DriftMode = netboxv1alpha1.DriftReport
	})

	// Seeded rather than created by the operator, so the drift is a field on an object that
	// already exists -- the case Report is turned on for: a team still editing NetBox by
	// hand, and a first week where you want to see what the operator would change.
	id := stub.seed(netbox.Object{"name": "Managed", "slug": "watched", "color": "ff0000"})

	makeTag(t, ns, "watched", func(tag *netboxv1alpha1.NetBoxTag) {
		tag.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
		tag.Spec.Color = "2196f3"
	})

	eventually(t, "DriftDetected=True", func() bool {
		tag := fetchTag(ns, "watched")

		return tag != nil &&
			tagCondition(tag, netboxv1alpha1.ConditionDriftDetected).Status == metav1.ConditionTrue
	})

	// Several resyncs, so the assertion is that Report never writes rather than that it had
	// not written yet.
	time.Sleep(3 * time.Second)

	if writes := stub.recorded(); len(writes) != 0 {
		t.Errorf("netbox saw %+v; Report must send nothing at all", writes)
	}

	if got := stub.tag(id)["color"]; got != "ff0000" {
		t.Errorf("colour = %v, want the human's ff0000 left alone", got)
	}

	tag := mustFetchTag(t, ns, "watched")

	// The field list is the whole product of the mode: "there is drift" is not something
	// anyone can act on.
	drift := tagCondition(tag, netboxv1alpha1.ConditionDriftDetected)
	if drift.Reason != netboxv1alpha1.ReasonDriftDetected {
		t.Errorf("DriftDetected reason = %q, want %q", drift.Reason, netboxv1alpha1.ReasonDriftDetected)
	}

	if !containsAll(drift.Message, "color", "ff0000", "2196f3") {
		t.Errorf("DriftDetected message = %q, want it to name the field and both values", drift.Message)
	}

	if synced := tagCondition(tag, netboxv1alpha1.ConditionSynced); synced.Status != metav1.ConditionFalse ||
		synced.Reason != netboxv1alpha1.ReasonDriftReported {
		t.Errorf("Synced = %s/%s, want False/%s",
			synced.Status, synced.Reason, netboxv1alpha1.ReasonDriftReported)
	}

	// Not Ready, deliberately: NetBox does not match the spec, and `kubectl wait` must not
	// be told otherwise about a write that never happened. The reason names driftMode
	// rather than DryRun, because that is the field to change.
	if ready := tagCondition(tag, netboxv1alpha1.ConditionReady); ready.Status != metav1.ConditionFalse ||
		ready.Reason != netboxv1alpha1.ReasonReportPending {
		t.Errorf("Ready = %s/%s, want False/%s",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonReportPending)
	}
}

// TestReportModeOverridesApplyAtTheClient is the structural half of Report: the endpoint
// says Apply and the client it produces still cannot write.
//
// That is the whole of the enforcement. A flag the engine consults before each mutation
// would hold until the first write path somebody forgets to guard -- the finalizer's delete
// is already a second one -- and "genuinely non-mutating" is not a property that survives
// being everybody's responsibility.
func TestReportModeOverridesApplyAtTheClient(t *testing.T) {
	ns := newNamespace(t)
	srv := netboxStub(t, "4.6.8", http.StatusOK)
	makeSecret(t, k8sClient, ns, "nb-token", "valid-token")

	endpoint := &netboxv1alpha1.NetBoxEndpoint{
		ObjectMeta: metav1.ObjectMeta{Name: "reporting", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxEndpointSpec{
			URL:            srv.URL,
			TokenSecretRef: netboxv1alpha1.SecretKeyRef{Name: "nb-token", Key: "token"},
			Mode:           netboxv1alpha1.EndpointModeApply,
			DriftMode:      netboxv1alpha1.DriftReport,
		},
	}
	if err := k8sClient.Create(context.Background(), endpoint); err != nil {
		t.Fatalf("creating endpoint: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), endpoint) })

	eventually(t, "a client for the reporting endpoint", func() bool {
		_, ok := clients.Lookup(ns, "reporting")

		return ok
	})

	nbClient, _ := clients.Lookup(ns, "reporting")
	if !nbClient.DryRun() {
		t.Error("driftMode Report produced a client that can write to netbox")
	}
}

// TestDriftModeOffDoesNotResync is the other end of the range: `Off` is for a NetBox large
// enough that the resync cost is real, and it buys that by letting a UI edit stand until
// something touches the object again.
func TestDriftModeOffDoesNotResync(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpointWith(t, ns, stub.URL, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.DriftMode = netboxv1alpha1.DriftOff
	})
	makeTag(t, ns, "unwatched", func(tag *netboxv1alpha1.NetBoxTag) { tag.Spec.Color = "2196f3" })

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "unwatched") })
	id := int(mustFetchTag(t, ns, "unwatched").Status.ID)

	stub.setField(t, id, "color", "ff0000")

	// The endpoint's resyncPeriod is one second, so this is several periods of the
	// requeue that Off is meant to have suppressed.
	time.Sleep(3 * time.Second)

	if got := stub.tag(id)["color"]; got != "ff0000" {
		t.Errorf("colour = %v; Off must not re-check netbox on a timer", got)
	}

	if writes := stub.recorded(); len(writes) != 1 || writes[0].method != http.MethodPost {
		t.Errorf("netbox saw %+v, want only the original POST", writes)
	}

	// A CR change is a watch event rather than a requeue, so it still reconciles -- and it
	// corrects the whole object, the human's edit included. That is the trade Off makes,
	// and it is the half of it people are surprised by.
	tag := mustFetchTag(t, ns, "unwatched")
	tag.Spec.Color = "4caf50"

	if err := k8sClient.Update(context.Background(), tag); err != nil {
		t.Fatalf("editing spec.color: %v", err)
	}

	eventually(t, "the spec change reaching netbox", func() bool { return stub.tag(id)["color"] == "4caf50" })
}

// TestSpecEditsSurviveTheGuard is the regression test for the guard itself: it sits in front
// of every object controller's client, so a rule that was slightly too broad would stop the
// operator taking a finalizer or writing a status, and the symptom would be an orphaned
// NetBox object rather than an error anybody reads.
func TestSpecEditsSurviveTheGuard(t *testing.T) {
	ns, stub := newNamespace(t), newTagStub(t)
	readyEndpoint(t, ns, stub.URL)
	makeTag(t, ns, "guarded-write", nil)

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "guarded-write") })

	tag := mustFetchTag(t, ns, "guarded-write")

	// The finalizer went on through the guard, which is the one metadata write the engine
	// makes.
	if !hasFinalizer(tag) {
		t.Error("no finalizer; the guard refused the one metadata patch the engine makes")
	}

	// And the status landed, which is every other write it makes.
	if tag.Status.ID == 0 {
		t.Error("status.id is unset; the guard refused the status write")
	}
}

func hasFinalizer(tag *netboxv1alpha1.NetBoxTag) bool {
	for _, finalizer := range tag.Finalizers {
		if finalizer == netboxv1alpha1.Finalizer {
			return true
		}
	}

	return false
}

// containsAll reports whether text mentions every one of wants, which is how a condition
// message is asserted without pinning its exact wording.
func containsAll(text string, wants ...string) bool {
	for _, want := range wants {
		if !strings.Contains(text, want) {
			return false
		}
	}

	return true
}
