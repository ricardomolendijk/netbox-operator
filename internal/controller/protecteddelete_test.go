package controller

import (
	"context"
	"testing"
	"time"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// A refused delete against a real API server, which is the only place the defect in #289
// exists: the engine's own unit tests call Reconcile once and see the interval it asked for,
// and it is the *controller* that then wakes it again before that interval is anywhere near
// up. What wakes it is the pass's own status write -- the object watch does not care which
// client made the change -- so a blocked deletion that records anything new on every pass
// re-triggers itself for as long as it stays blocked.
//
// Measured here on the shipped code: 6,217 refused DELETEs and as many status writes in
// twenty seconds against one object, with status.deletionAttempts past six thousand, so the
// backoff was at its five-minute ceiling within the first second of a teardown. Five CRs that
// other fixtures reference doing that at once against one NetBox and one API server is why
// the e2e teardown timed out with those five still holding their finalizers, and why nothing
// in the surviving log looked like an orderly retry.

// protectedDeleteWindow is how long the test watches a blocked deletion. Long enough for the
// first few tiers of the backoff -- 2 s, 4 s, 8 s -- to be distinguishable from a loop, and
// short enough to belong in a package that runs on every PR.
const protectedDeleteWindow = 10 * time.Second

// TestARefusedDeleteBacksOffInsteadOfStorming is the regression test for #289.
//
// The assertion is a *rate*, which is unusual here and is the point: the deletion did not fail
// and no state was wrong -- every individual pass did exactly what the design says, and the
// defect was only visible in how often it did it.
func TestARefusedDeleteBacksOffInsteadOfStorming(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, tagKind)
	readyEndpoint(t, ns, target)
	tag := makeTag(t, ns, "blocked", nil)

	eventually(t, "Ready=True", func() bool { return tagIsReady(ns, "blocked") })
	id := mustFetchTag(t, ns, "blocked").Status.ID

	// NetBox refuses the delete, as it does for every object something still points at.
	stub.protect(id)

	if err := k8sClient.Delete(context.Background(), tag); err != nil {
		t.Fatalf("deleting the tag: %v", err)
	}

	eventually(t, "the first refused DELETE", func() bool { return stub.deletes(id) > 0 })

	time.Sleep(protectedDeleteWindow)

	// The schedule over ten seconds is one attempt at 0 s, 2 s, 6 s and 14 s, so four is the
	// generous ceiling: it allows the whole of the window to be off by a tier and still fails
	// by three orders of magnitude on the storm.
	const budget = 4

	if got := stub.deletes(id); got > budget {
		t.Errorf("netbox saw %d DELETEs for object %d in %s, want at most %d: a refused delete"+
			" that retries on every wake-up is a hot loop against netbox and the API server"+
			" at once, and it is what starves the deletes that would unblock it",
			got, id, protectedDeleteWindow, budget)
	}

	blocked := mustFetchTag(t, ns, "blocked")

	if got := blocked.Status.DeletionAttempts; got > budget {
		t.Errorf("status.deletionAttempts = %d, want at most %d: the backoff is computed from"+
			" the count, so a count that moves on every wake-up is at its ceiling immediately",
			got, budget)
	}

	if blocked.Status.LastDeletionAttempt == nil {
		t.Error("status.lastDeletionAttempt is unset after a refused delete, so nothing can" +
			" tell an expired backoff from an early wake-up")
	}

	if got := tagCondition(blocked, netboxv1alpha1.ConditionDeleting); got.Reason !=
		netboxv1alpha1.ReasonProtected {
		t.Errorf("Deleting reason = %q, want %q", got.Reason, netboxv1alpha1.ReasonProtected)
	}

	// The other half, and the one that matters more: a hold-off that never releases is the
	// stuck finalizer in a different disguise. Once the dependent is gone -- which is what
	// releasing the protection stands for -- the object has to finish deleting itself with no
	// help from anybody.
	stub.release(id)

	eventually(t, "the CR to release its finalizer once netbox accepts the delete", func() bool {
		return fetchTag(ns, "blocked") == nil
	})

	if stub.get(id) != nil {
		t.Errorf("netbox object %d is still there after the CR was finalized", id)
	}
}
