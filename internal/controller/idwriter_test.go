package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestIDWriterKeepsAnIDAStatusUpdateWouldHaveLost is the wiring half of issues #289 and #291.
//
// The engine keeps the id of an object it has just created through a writer that must be able
// to succeed where the ordinary status update is refused, and the difference is invisible from
// the engine's own tests: both are interfaces there, and a fake satisfies either without ever
// meeting an API server's optimistic-concurrency check.
//
// So both are exercised here against a real one, from the same stale copy: the update loses,
// the id write does not, and it leaves everything else alone.
func TestIDWriterKeepsAnIDAStatusUpdateWouldHaveLost(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)

	// No NetBoxEndpoint in this namespace, for the reason TestLiveStatusAnswersFromTheAPIServer
	// gives: the object never reaches locate(), so nothing here goes looking for a NetBox.
	tag := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "kept-id"},
		Spec: netboxv1alpha1.NetBoxTagSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Kept Id",
			Slug:             "kept-id",
		},
	}
	if err := apiClient.Create(ctx, tag); err != nil {
		t.Fatalf("creating the tag: %v", err)
	}

	t.Cleanup(func() {
		setTagID(t, tag, 0)
		removeObject(t, &netboxv1alpha1.NetBoxTag{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "kept-id"},
		})
	})

	// The copy a creating pass is holding: read before another pass of the same key wrote, and
	// therefore carrying a resourceVersion the API server has moved past by the time the write
	// at the end of the pass goes out.
	stale := tag.DeepCopy()

	writeTagStatus(t, tag, func(status *netboxv1alpha1.NetBoxObjectStatus) {
		status.Conditions = []metav1.Condition{{
			Type: netboxv1alpha1.ConditionReady, Status: metav1.ConditionFalse,
			Reason: netboxv1alpha1.ReasonWaitingForEndpoint, LastTransitionTime: metav1.Now(),
		}}
	})

	// The id a creating pass has just proved server-side, and the write that used to lose it.
	const created = 4242

	stale.Status.ID = created

	err := statusWriter{apiClient}.UpdateStatus(ctx, stale.DeepCopy())
	if !apierrors.IsConflict(err) {
		t.Fatalf("the status update = %v, want a conflict: without one there is no race for the"+
			" id write to survive and this test proves nothing", err)
	}

	if err := (idWriter{apiClient}).RecordID(ctx, stale, created); err != nil {
		t.Fatalf("RecordID() = %v, want the id kept: it carries no resourceVersion precisely so"+
			" that the write above losing cannot lose it too", err)
	}

	kept := &netboxv1alpha1.NetBoxTag{}
	if err := apiClient.Get(ctx, client.ObjectKeyFromObject(tag), kept); err != nil {
		t.Fatalf("re-reading the tag: %v", err)
	}

	if kept.Status.ID != created {
		t.Errorf("status.id = %d, want %d", kept.Status.ID, created)
	}

	// The other pass's work is still there. An id write that replaced the status would be the
	// stale-conclusion problem of #252 wearing a different hat.
	if len(kept.Status.Conditions) != 1 {
		t.Errorf("conditions = %v, want the one the winning write left: RecordID may write"+
			" nothing but the id", kept.Status.Conditions)
	}
}

// TestIDWriterLeavesTheCallersStatusAlone is the other half of the adapter's contract, and the
// same one liveStatus has: a PATCH is answered with the whole object, and controller-runtime
// decodes that answer into whatever object it was handed. Writing it back over the caller's
// would replace the status the pass is still holding with the one the API server had before the
// patch -- the shape of issue #243.
func TestIDWriterLeavesTheCallersStatusAlone(t *testing.T) {
	ctx := context.Background()
	ns := newNamespace(t)

	tag := &netboxv1alpha1.NetBoxTag{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "untouched"},
		Spec: netboxv1alpha1.NetBoxTagSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Untouched",
			Slug:             "untouched",
		},
	}
	if err := apiClient.Create(ctx, tag); err != nil {
		t.Fatalf("creating the tag: %v", err)
	}

	t.Cleanup(func() {
		setTagID(t, tag, 0)
		removeObject(t, &netboxv1alpha1.NetBoxTag{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "untouched"},
		})
	})

	holding := tag.DeepCopy()
	holding.Status.ID = 77
	holding.Status.Conditions = []metav1.Condition{{
		Type: netboxv1alpha1.ConditionSynced, Status: metav1.ConditionTrue,
		Reason: netboxv1alpha1.ReasonDriftCorrected, LastTransitionTime: metav1.Now(),
	}}

	if err := (idWriter{apiClient}).RecordID(ctx, holding, 77); err != nil {
		t.Fatalf("RecordID() = %v", err)
	}

	if holding.Status.ID != 77 || len(holding.Status.Conditions) != 1 {
		t.Errorf("the id write read %+v back onto the caller's object, which is the pass's own"+
			" work in progress", holding.Status)
	}
}

// writeTagStatus applies mutate to one tag's status, retrying the conflict its own controller
// can cause. setTagID is the same shape narrowed to the id; this is the general one, because
// what makes the copy above stale has to be a field the id write does not touch.
func writeTagStatus(t *testing.T, tag *netboxv1alpha1.NetBoxTag,
	mutate func(*netboxv1alpha1.NetBoxObjectStatus),
) {
	t.Helper()

	ctx := context.Background()
	key := client.ObjectKeyFromObject(tag)

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		fresh := &netboxv1alpha1.NetBoxTag{}
		if err := apiClient.Get(ctx, key, fresh); err != nil {
			return err
		}

		mutate(&fresh.Status)

		return apiClient.Status().Update(ctx, fresh)
	})
	if err != nil {
		t.Fatalf("writing the status of %s: %v", key, err)
	}
}
