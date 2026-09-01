package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestLiveStatusAnswersFromTheAPIServer is the wiring half of issue #252.
//
// The engine refuses to conclude "this NetBox object is somebody else's" from a status.id an
// informer cache may be behind on, and asks this adapter instead. Two properties make that
// worth anything, and neither is visible from the engine's own tests:
//
//   - the answer is the API server's, not the caller's. The object handed in is the stale copy
//     a reconcile was fed, so an implementation that read its status -- or read *into* it --
//     would confidently return the very value that caused the bug.
//   - the caller's object comes back untouched. A pass is mid-reconcile and has already written
//     to the status it is holding; a read over it would silently drop that work.
//
// Against a real API server and a real generated kind, because the deep-copy-and-type-assert
// step is exactly what a hand-built fake gets right by construction.
func TestLiveStatusAnswersFromTheAPIServer(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)

	// No NetBoxEndpoint in this namespace, deliberately: the object then never reaches
	// locate(), so nothing but this test writes its status.id and there is no race to lose.
	tag := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "live-status"},
		Spec: netboxv1alpha1.NetBoxTagSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Live Status",
			Slug:             "live-status",
		},
	}
	if err := apiClient.Create(ctx, tag); err != nil {
		t.Fatalf("creating the tag: %v", err)
	}

	t.Cleanup(func() {
		// Registered before the id below is written and run after it is cleared, so the
		// deletion does not go looking for a NetBox this namespace has no endpoint for
		// (removeObject explains what a blocked release costs the rest of the suite).
		removeObject(t, &netboxv1alpha1.NetBoxTag{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "live-status"},
		})
	})

	// The write a second reconcile can be behind on.
	const written = 4242

	setTagID(t, tag, written)
	t.Cleanup(func() { setTagID(t, tag, 0) })

	// The copy such a reconcile is holding: this object, with the status it had before that
	// write, and one condition of its own already computed.
	stale := tag.DeepCopy()
	stale.Status = netboxv1alpha1.NetBoxObjectStatus{
		Conditions: []metav1.Condition{{
			Type: netboxv1alpha1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: netboxv1alpha1.ReasonWaitingForEndpoint, LastTransitionTime: metav1.Now(),
		}},
	}

	live, err := liveStatus{apiClient}.LiveStatus(ctx, stale)
	if err != nil {
		t.Fatalf("LiveStatus() = %v", err)
	}

	if live.ID != written {
		t.Errorf("live status.id = %d, want %d: the answer has to be the API server's, not the"+
			" caller's", live.ID, written)
	}

	if stale.Status.ID != 0 || len(stale.Status.Conditions) != 1 {
		t.Errorf("the read wrote status %+v back onto the caller's object, which is the pass's"+
			" own work in progress", stale.Status)
	}
}

// TestLiveStatusReportsAnObjectThatIsGone states the other half of the adapter's contract: the
// engine only ever asks about an object it is in the middle of reconciling, so "not there" is
// news and not an answer. Returning a zero status instead would read as "this CR has no NetBox
// object of its own" -- the one conclusion the live read exists to stop being guessed at.
func TestLiveStatusReportsAnObjectThatIsGone(t *testing.T) {
	ns := newNamespace(t)

	gone := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "never-created"},
	}

	reader := liveStatus{apiClient}
	if _, err := reader.LiveStatus(context.Background(), gone); err == nil {
		t.Error("LiveStatus() = nil, want an error for an object the API server does not hold")
	}
}

// setTagID writes one tag's status.id, retrying the conflict its own controller can cause.
func setTagID(t *testing.T, tag *netboxv1alpha1.NetBoxTag, id int64) {
	t.Helper()

	ctx := context.Background()
	key := client.ObjectKeyFromObject(tag)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &netboxv1alpha1.NetBoxTag{}
		if err := apiClient.Get(ctx, key, fresh); err != nil {
			return err
		}

		fresh.Status.ID = id

		return apiClient.Status().Update(ctx, fresh)
	})
	if err != nil {
		t.Fatalf("writing status.id = %d on %s: %v", id, key, err)
	}
}
