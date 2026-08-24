package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// childFixtureGVK is the test-only Kind nothing reconciles, so that a resourceVersion that
// did not move is a statement about the apply rather than about a controller not having
// caught up yet.
var childFixtureGVK = schema.GroupVersionKind{
	Group: "test.netbox.kubeforge.org", Version: "v1alpha1", Kind: "ChildFixture",
}

// materialisedChild is a child as internal/reconciler/children.go decorates one: the two
// markers, the managed-by label and a controller owner reference. Built as unstructured
// because the fixture has no Go type, which is also how the pruner reads a candidate.
func materialisedChild(ns string, owner *unstructured.Unstructured) *unstructured.Unstructured {
	path := []netboxv1alpha1.ChildSegment{{Field: "interfaces", Key: "eth0"}}

	child := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"managed": "declared"},
	}}
	child.SetGroupVersionKind(childFixtureGVK)
	child.SetNamespace(ns)
	child.SetName(netboxv1alpha1.ChildName(owner.GetName(), path))
	child.SetLabels(map[string]string{
		netboxv1alpha1.ManagedByLabel: netboxv1alpha1.ManagedByValue,
		netboxv1alpha1.OwnerUIDLabel:  string(owner.GetUID()),
	})
	child.SetAnnotations(map[string]string{
		netboxv1alpha1.OwnedByPathAnnotation: netboxv1alpha1.ChildPath(path),
		netboxv1alpha1.GeneratedByAnnotation: "netboxsite/" + ns + "/" + owner.GetName(),
	})

	yes := true
	child.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(),
		Name: owner.GetName(), UID: owner.GetUID(),
		Controller: &yes, BlockOwnerDeletion: &yes,
	}})

	return child
}

// childOwner is a real object with a real uid for the owner reference to point at.
//
// A ChildFixture rather than one of the shipped Kinds, so no controller in this suite
// reconciles it: the assertion below is about what the *apply* did, and a parent whose own
// controller was writing status would be a second writer in the middle of it.
func childOwner(t *testing.T, ns string) *unstructured.Unstructured {
	t.Helper()

	owner := &unstructured.Unstructured{}
	owner.SetGroupVersionKind(childFixtureGVK)
	owner.SetNamespace(ns)
	owner.SetName("parent")

	if err := apiClient.Create(context.Background(), owner); err != nil {
		t.Fatalf("creating the owner: %v", err)
	}

	return owner
}

// childApplier is the shipped wiring: specGuard, the operator's field manager, and the
// childWriter on top. Assembled exactly as newObjectController assembles it, so the test
// exercises the real route to the API server rather than a client wired up for the test.
func childApplier() reconciler.ChildWriter {
	return childWriter{specGuard{client.WithFieldOwner(apiClient, reconciler.FieldManager)}}
}

// TestChildApplyIsIdempotent is the measurable form of "reordering the inline list churns
// nothing". Server-side apply of identical content does not bump metadata.resourceVersion, so
// the claim is an assertion rather than a judgement -- and it is what makes the whole
// materialiser safe to run on every reconcile of every parent.
func TestChildApplyIsIdempotent(t *testing.T) {
	ns := newNamespace(t)
	owner := childOwner(t, ns)
	writer := childApplier()

	child := materialisedChild(ns, owner)
	if err := writer.Apply(context.Background(), child); err != nil {
		t.Fatalf("the first apply: %v", err)
	}

	first := child.GetResourceVersion()
	if first == "" {
		t.Fatal("the apply did not write the response back into the object, so the " +
			"materialiser could not read the child's readiness from it")
	}

	// A fresh object, exactly as InlineChildren() would build it on the next reconcile: the
	// point is that identical *content* is inert, not that the same pointer is.
	again := materialisedChild(ns, owner)
	if err := writer.Apply(context.Background(), again); err != nil {
		t.Fatalf("the second apply: %v", err)
	}

	if again.GetResourceVersion() != first {
		t.Errorf("an identical apply bumped resourceVersion from %s to %s; every reconcile of "+
			"every parent would then write every child", first, again.GetResourceVersion())
	}
}

// TestChildApplyOwnsOnlyWhatItSets is the two halves of "a child edited by hand", and they are
// deliberately different: a field the materialiser sets is taken back, because the parent's
// inline entry is the declared source of truth for it; a field the materialiser never sets is
// kept, because the operator does not manage fields it was not told about.
//
// The unforced apply in the middle is not a detail. A forced apply would take the field back
// silently; the refusal is what carries the field names into the ChildFieldReverted Event.
func TestChildApplyOwnsOnlyWhatItSets(t *testing.T) {
	ns := newNamespace(t)
	owner := childOwner(t, ns)
	writer := childApplier()

	child := materialisedChild(ns, owner)
	if err := writer.Apply(context.Background(), child); err != nil {
		t.Fatalf("the first apply: %v", err)
	}

	// A human edits both fields, taking ownership of each.
	edited := child.DeepCopy()
	spec, _ := edited.Object["spec"].(map[string]any)
	spec["managed"] = "hand edited"
	spec["unmanaged"] = "mine"

	if err := apiClient.Update(context.Background(), edited, client.FieldOwner("kubectl-edit")); err != nil {
		t.Fatalf("the hand edit: %v", err)
	}

	// The unforced apply the materialiser makes first.
	again := materialisedChild(ns, owner)

	err := writer.Apply(context.Background(), again)
	if !apierrors.IsConflict(err) {
		t.Fatalf("the unforced apply = %v, want a 409 naming the field it was refused over", err)
	}

	if !strings.Contains(err.Error(), "managed") {
		t.Errorf("the conflict does not name the field, so the Event could not either: %v", err)
	}

	// The forced retry.
	forced := materialisedChild(ns, owner)
	if err := writer.Apply(context.Background(), forced, client.ForceOwnership); err != nil {
		t.Fatalf("the forced apply: %v", err)
	}

	live, _ := forced.Object["spec"].(map[string]any)

	if live["managed"] != "declared" {
		t.Errorf("spec.managed = %v, want the declared value back", live["managed"])
	}

	if live["unmanaged"] != "mine" {
		t.Errorf("spec.unmanaged = %v, want the hand-set value kept: the materialiser never "+
			"sets this field, so it does not manage it", live["unmanaged"])
	}
}

// TestChildApplyIsAttributedToItsOwnManager is ADR-0005 §1 made checkable with
// `kubectl get -o yaml` rather than only by reading the code: `f:spec` under
// netbox-operator/children is the materialiser's own output, and `f:spec` under
// netbox-operator would be the invariant having been broken.
func TestChildApplyIsAttributedToItsOwnManager(t *testing.T) {
	ns := newNamespace(t)
	owner := childOwner(t, ns)

	child := materialisedChild(ns, owner)
	if err := childApplier().Apply(context.Background(), child); err != nil {
		t.Fatalf("apply: %v", err)
	}

	live := &unstructured.Unstructured{}
	live.SetGroupVersionKind(childFixtureGVK)

	if err := apiClient.Get(context.Background(), client.ObjectKeyFromObject(child), live); err != nil {
		t.Fatalf("get: %v", err)
	}

	managers := make([]string, 0, len(live.GetManagedFields()))

	for _, entry := range live.GetManagedFields() {
		managers = append(managers, entry.Manager)

		if entry.Manager == reconciler.FieldManager {
			t.Errorf("the plain %q manager owns fields on a materialised child: %s",
				reconciler.FieldManager, entry.FieldsV1)
		}
	}

	if !strings.Contains(strings.Join(managers, ","), reconciler.ChildFieldManager) {
		t.Errorf("managedFields names %v, want an entry for %q",
			managers, reconciler.ChildFieldManager)
	}
}
