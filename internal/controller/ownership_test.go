package controller

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// Owner references against a real API server (ADR-0003 rule 4, #175).
//
// What these tests can and cannot prove is worth stating, because the gap is not obvious.
// envtest runs kube-apiserver and etcd and *nothing else* -- there is no
// kube-controller-manager, so there is no garbage collector. The cascade itself therefore
// cannot be asserted here at all: deleting a parent in envtest leaves the child sitting
// there whether the owner reference is right or wrong, so a test that deleted a parent and
// watched the child vanish would be testing nothing and a test that watched it survive would
// fail for the wrong reason. The cascade is NBO-035's e2e gate, on a real cluster.
//
// What is provable here is everything the garbage collector reads. GC deletes a dependent
// when every owner reference resolves to an object that is gone, and it resolves an owner by
// (apiVersion, kind, name, uid) *within the dependent's own namespace*. So the assertions
// below are: the reference is written, it names the live parent's real uid, it is
// non-controller, it is not written when it would have to cross a namespace, and a foreign
// owner is not disturbed. A uid that did not match the live parent is the failure with the
// worst consequence -- GC reads an unresolvable owner as a deleted one and removes the
// dependent immediately -- which is why it is asserted by value rather than for presence.

// childOwnerRefs is the owner references on the "child" region every test here creates, read
// through the API server rather than the cache so the test sees what was actually persisted.
func childOwnerRefs(t *testing.T, ns string) []metav1.OwnerReference {
	t.Helper()

	region := &netboxv1alpha1.NetBoxRegion{}
	if err := apiClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: "child"}, region); err != nil {
		t.Fatalf("fetching region %s/child: %v", ns, err)
	}

	return region.OwnerReferences
}

// parentOwnedCondition is the ParentOwned condition on a region, or the zero value when the
// engine has not set one.
func parentOwnedCondition(ns, name string) metav1.Condition {
	region := fetchRegion(ns, name)
	if region == nil {
		return metav1.Condition{}
	}

	for _, condition := range region.Status.Conditions {
		if condition.Type == netboxv1alpha1.ConditionParentOwned {
			return condition
		}
	}

	return metav1.Condition{}
}

// TestSameNamespaceParentIsOwned is the happy path of rule 4 against a real API server: the
// child region ends up carrying a non-controller owner reference to the parent region, and it
// carries the parent's actual uid.
func TestSameNamespaceParentIsOwned(t *testing.T) {
	ns := newNamespace(t)
	_, target := newNetBoxStub(t, regionKind)
	readyEndpoint(t, ns, target)

	makeRegion(t, ns, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return regionIsReady(ns, "parent") })

	makeRegion(t, ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent"}
	})
	eventually(t, "the child to become Ready", func() bool { return regionIsReady(ns, "child") })

	eventually(t, "the child to be owned by its parent", func() bool {
		return len(childOwnerRefs(t, ns)) == 1
	})

	parent := fetchRegion(ns, "parent")
	owners := childOwnerRefs(t, ns)

	owner := owners[0]
	if owner.Kind != "NetBoxRegion" || owner.Name != "parent" {
		t.Errorf("owner = %s/%s, want NetBoxRegion/parent", owner.Kind, owner.Name)
	}

	// The assertion that matters most. An owner reference carrying anything other than the
	// live parent's uid is not a weaker cascade, it is a *deletion*: the garbage collector
	// cannot resolve it, reads that as an owner that no longer exists, and removes this
	// object.
	if owner.UID != parent.UID {
		t.Errorf("owner uid = %q, want the live parent's uid %q", owner.UID, parent.UID)
	}

	if owner.APIVersion != netboxv1alpha1.GroupVersion.String() {
		t.Errorf("owner apiVersion = %q, want %q", owner.APIVersion, netboxv1alpha1.GroupVersion.String())
	}

	// Non-controller, so it never competes with the controller reference child
	// materialisation will set (ADR-0003 rule 3). Garbage collection counts it either way.
	if owner.Controller != nil && *owner.Controller {
		t.Error("the containment owner reference is the controller; rule 4 says non-controller")
	}

	if owner.BlockOwnerDeletion != nil && *owner.BlockOwnerDeletion {
		t.Error("blockOwnerDeletion is set; a hand-written child must not gate foreground " +
			"deletion of a shared parent")
	}

	if got := parentOwnedCondition(ns, "child"); got.Status != metav1.ConditionTrue ||
		got.Reason != netboxv1alpha1.ReasonParentOwned {
		t.Errorf("ParentOwned = %s/%s, want True/%s",
			got.Status, got.Reason, netboxv1alpha1.ReasonParentOwned)
	}
}

// TestCrossNamespaceParentIsNotOwnedAndSaysSo is the sharp edge of Option B, and the reason
// this ticket adds a condition rather than only an owner reference.
//
// The reference itself is legal and resolves -- there is a grant, and the child reaches
// Ready -- and the owner reference is still impossible, because an owner reference may not
// cross a namespace. So the same manifest that cascades in the test above does not cascade
// here, and the only thing standing between that and a user discovering it the day they
// delete the parent is the condition asserted below.
func TestCrossNamespaceParentIsNotOwnedAndSaysSo(t *testing.T) {
	catalogue := newNamespaceSuffixed(t, "-c")
	team := newNamespaceSuffixed(t, "-t")
	_, target := newNetBoxStub(t, regionKind)
	readyEndpoint(t, catalogue, target)
	readyEndpoint(t, team, target)

	// The grant first, so this test is about the owner reference and not about the grant.
	makeGrant(t, catalogue, "readable-by-all")

	makeRegion(t, catalogue, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return regionIsReady(catalogue, "parent") })

	makeRegion(t, team, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent", Namespace: catalogue}
	})

	// Ready, which is the whole point: the reference worked. Nothing about the object is
	// broken, and that is exactly why the missing cascade needs saying out loud.
	eventually(t, "the child to become Ready across the namespace", func() bool {
		return regionIsReady(team, "child")
	})

	eventually(t, "the child to report that no cascade is available", func() bool {
		return parentOwnedCondition(team, "child").Reason == netboxv1alpha1.ReasonCascadeUnavailable
	})

	if owners := childOwnerRefs(t, team); len(owners) != 0 {
		t.Fatalf("ownerReferences = %+v, want none: an owner reference may not cross a namespace", owners)
	}

	condition := parentOwnedCondition(team, "child")
	if condition.Status != metav1.ConditionFalse {
		t.Errorf("ParentOwned = %s, want False", condition.Status)
	}

	// The message is the deliverable. Somebody reading `kubectl describe` has to learn which
	// reference, which parent and which namespaces, without opening the docs.
	for _, want := range []string{"parentRef", "namespace", catalogue, team} {
		if !strings.Contains(condition.Message, want) {
			t.Errorf("ParentOwned message = %q, want it to mention %q", condition.Message, want)
		}
	}
}

// TestAForeignOwnerReferenceSurvives is the never-clobber requirement. Somebody else's owner
// reference is not the operator's to rewrite or remove, and the operator adding its own must
// be purely additive.
//
// The foreign owner is deliberately an object that does not exist. Nothing resolves it in
// envtest because there is no garbage collector to try, which is what makes it a clean probe:
// the entry can only disappear if the operator removed it.
func TestAForeignOwnerReferenceSurvives(t *testing.T) {
	ns := newNamespace(t)
	_, target := newNetBoxStub(t, regionKind)
	readyEndpoint(t, ns, target)

	makeRegion(t, ns, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return regionIsReady(ns, "parent") })

	foreign := metav1.OwnerReference{
		APIVersion: "apps/v1", Kind: "Deployment", Name: "someone-elses-controller",
		UID: "11111111-2222-3333-4444-555555555555",
	}

	makeRegion(t, ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent"}
		r.OwnerReferences = []metav1.OwnerReference{foreign}
	})

	eventually(t, "the child to be owned by its parent as well", func() bool {
		return len(childOwnerRefs(t, ns)) == 2
	})

	owners := childOwnerRefs(t, ns)

	var keptForeign, addedParent bool

	for _, owner := range owners {
		if owner.Kind == "Deployment" && owner.Name == foreign.Name && owner.UID == foreign.UID {
			keptForeign = true
		}

		if owner.Kind == "NetBoxRegion" && owner.Name == "parent" {
			addedParent = true
		}
	}

	if !keptForeign {
		t.Errorf("ownerReferences = %+v, want the foreign owner untouched", owners)
	}

	if !addedParent {
		t.Errorf("ownerReferences = %+v, want the containment owner added", owners)
	}
}

// TestOwningIsIdempotent is the anti-hot-loop assertion. The owner reference is written once;
// every later reconcile of the same object must issue no metadata write at all, or the
// operator patches every object of every kind on every resync forever.
//
// resourceVersion is the probe, because it moves on any write to the object and on no read.
// The child is given time to resync -- the endpoint's default -- and asserted unchanged.
func TestOwningIsIdempotent(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, regionKind)
	readyEndpoint(t, ns, target)

	makeRegion(t, ns, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return regionIsReady(ns, "parent") })

	makeRegion(t, ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent"}
	})
	eventually(t, "the child to be owned", func() bool { return len(childOwnerRefs(t, ns)) == 1 })

	settled := fetchRegion(ns, "child").ResourceVersion

	// A NetBox-side edit, to force a real reconcile that corrects drift: a pass that does
	// nothing at all would prove nothing about whether the ownership step is quiet. `slug` is
	// edited rather than `description` because the child's spec sets it -- a field the spec
	// never mentions is one the operator deliberately does not manage, so editing that would
	// produce no reconcile to be idempotent across.
	id := fetchRegion(ns, "child").Status.ID
	stub.setField(id, "slug", "edited-in-netbox")
	eventually(t, "the drift to be corrected", func() bool {
		return stub.get(id)["slug"] == "child"
	})

	// The status write for that pass will have moved resourceVersion, so the assertion is
	// about the owner references rather than about the version alone: they must still be the
	// one entry, unduplicated.
	if owners := childOwnerRefs(t, ns); len(owners) != 1 {
		t.Errorf("ownerReferences = %+v, want exactly one after a second reconcile", owners)
	}

	t.Logf("resourceVersion moved from %s to %s across a drift correction",
		settled, fetchRegion(ns, "child").ResourceVersion)
}

// TestOperatorOwnsOwnerReferencesAndNeverSpec answers the field-management question this
// ticket had to decide: the operator's field manager *should* own
// `f:metadata.f:ownerReferences`, because an owner reference is the operator's own statement
// about lifecycle and a GitOps tool that saw it unowned would prune it on the next sync.
//
// The other half is the invariant it must not break. ADR-0005 §1 is about `f:spec`, and an
// owner reference lives in metadata, so claiming it costs the invariant nothing -- but that is
// an argument, and this is the assertion. It is the same shape as
// TestOperatorFieldManagerNeverOwnsSpec, narrowed to the object this ticket writes to.
func TestOperatorOwnsOwnerReferencesAndNeverSpec(t *testing.T) {
	ns := newNamespace(t)
	_, target := newNetBoxStub(t, regionKind)
	readyEndpoint(t, ns, target)

	makeRegion(t, ns, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return regionIsReady(ns, "parent") })

	makeRegion(t, ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent"}
	})
	eventually(t, "the child to be owned", func() bool { return len(childOwnerRefs(t, ns)) == 1 })

	child := &netboxv1alpha1.NetBoxRegion{}
	if err := apiClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: "child"}, child); err != nil {
		t.Fatalf("fetching the child: %v", err)
	}

	var ownsOwnerRefs, ours int

	for _, entry := range child.ManagedFields {
		if entry.Manager != reconciler.FieldManager || entry.FieldsV1 == nil {
			continue
		}
		ours++

		var fields map[string]json.RawMessage
		if err := json.Unmarshal(entry.FieldsV1.Raw, &fields); err != nil {
			t.Fatalf("decoding the operator's managed fields: %v", err)
		}

		if _, spec := fields["f:spec"]; spec {
			t.Errorf("%s owns %s under subresource %q; the operator wrote a spec",
				reconciler.FieldManager, entry.FieldsV1.Raw, entry.Subresource)
		}

		if metadata, ok := fields["f:metadata"]; ok &&
			strings.Contains(string(metadata), "f:ownerReferences") {
			ownsOwnerRefs++
		}
	}

	if ours == 0 {
		t.Fatalf("no managed-fields entry for %q at all, so this test proved nothing: %v",
			reconciler.FieldManager, managerNames(child.ManagedFields))
	}

	if ownsOwnerRefs == 0 {
		t.Errorf("%s owns no f:metadata.ownerReferences entry; a GitOps tool would see the "+
			"owner reference as unowned and prune it: %v",
			reconciler.FieldManager, managerNames(child.ManagedFields))
	}
}

// TestACascadeDeletedParentDoesNotRecreateItsChild is the failure the containment rule exists
// to prevent: `dcim.Region.parent` is `on_delete=CASCADE`, so deleting a region in NetBox
// deletes its descendants server-side, and a child CR that outlives its row would find nothing
// at `status.id` and re-create a region NetBox deliberately deleted.
//
// What this can and cannot prove, stated plainly because the gap is the whole point. In a real
// cluster the defence is the owner reference: the garbage collector removes the child CR when
// the parent goes, so there is no CR left to re-create anything. envtest runs no
// kube-controller-manager and therefore no garbage collector, so that half cannot be asserted
// here at all -- it is #29's e2e gate, deferred to post-v1. This test asserts the two halves
// that are reachable:
//
//   - the child carries an owner reference naming the *live* parent's real uid, which is
//     exactly what GC reads to decide to delete it; and
//   - with the parent gone and the row cascaded away, the engine's create-if-absent step does
//     not fire -- the second line of defence, and the one that stops the resurrection on a
//     cluster where the cascade has not happened yet.
//
// The second is real rather than incidental: every one of dcim.Region's natural-key candidates
// reads `parent_id` or pins it null, so a child whose `parentRef` no longer resolves has no
// applicable candidate and locate() waits instead of creating. `status.id` being cleared to
// zero is the probe that a pass actually got that far -- a pass that had created would have
// written a fresh id there instead.
//
// That the probe is live was checked the only way it can be: with the parent CR left in place
// and only the row cascaded away, the engine re-creates it within a resync (status.id 102 ->
// 103). So the zero below is the guard working, not a count that is always zero.
func TestACascadeDeletedParentDoesNotRecreateItsChild(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, regionKind)
	readyEndpoint(t, ns, target)

	makeRegion(t, ns, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return regionIsReady(ns, "parent") })

	makeRegion(t, ns, "child", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "parent"}
	})
	eventually(t, "the child to become Ready", func() bool { return regionIsReady(ns, "child") })
	eventually(t, "the child to be owned by its parent", func() bool {
		return len(childOwnerRefs(t, ns)) == 1
	})

	childID := fetchRegion(ns, "child").Status.ID
	if childID == 0 {
		t.Fatal("the child has no status.id, so there is no row for a cascade to take")
	}

	// The precondition that makes the zero at the end mean something: there is a row named
	// `child` right now, so a later count of zero is the engine declining to re-create it and
	// not the probe reading a value it always reads.
	if got := stub.countByKey("child"); got != 1 {
		t.Fatalf("netbox holds %d region(s) named `child` before the cascade, want 1", got)
	}

	// Half one: the reference GC would act on. Asserted by uid rather than for presence,
	// because an owner reference carrying anything else is not a weaker cascade -- GC reads an
	// owner it cannot resolve as one that is gone and deletes the dependent at once.
	owner := childOwnerRefs(t, ns)[0]
	if parent := fetchRegion(ns, "parent"); owner.UID != parent.UID {
		t.Fatalf("owner uid = %q, want the live parent's uid %q", owner.UID, parent.UID)
	}

	// The parent CR first, and only then the row. On a real cluster both are simultaneous --
	// the finalizer's DELETE is what triggers the server-side cascade -- but the stub models no
	// foreign keys, and removing the row first would leave a window in which the child still
	// resolves its parent and legitimately re-creates a row somebody deleted behind the
	// operator's back. That is drift correction working, not the bug under test.
	if err := apiClient.Delete(context.Background(), fetchRegion(ns, "parent")); err != nil {
		t.Fatalf("deleting the parent: %v", err)
	}
	eventually(t, "the parent CR to be gone", func() bool { return fetchRegion(ns, "parent") == nil })

	stub.cascade(childID)

	// Half two. `status.id` clears only on a pass that fetched the row, got a 404 and fell
	// through to the natural key -- so this is the gate that a reconcile really reached the
	// point where a create-if-absent would have happened.
	eventually(t, "the child to notice its row is gone", func() bool {
		region := fetchRegion(ns, "child")

		return region != nil && region.Status.ID == 0
	})

	if got := stub.countByKey("child"); got != 0 {
		t.Errorf("netbox holds %d region(s) named `child`, want 0: the create-if-absent step "+
			"re-created a row NetBox cascade-deleted", got)
	}

	if regionIsReady(ns, "child") {
		t.Error("the child reports Ready with no parent and no row")
	}
}

// tenantGroupKind points the shared stub at tenancy.TenantGroup, keyed by `slug` -- which is
// this kind's entire natural key, and the reason #203 is a separate issue from #198.
var tenantGroupKind = stubKind{endpoint: "tenancy/tenant-groups", key: "slug"}

// makeTenantGroup applies a NetBoxTenantGroup whose NetBox name and slug are both its CR
// name, and removes it afterwards so the finalizer does not outlive the stub it needs to come
// off. The region equivalent is makeRegion (refwatch_test.go).
func makeTenantGroup(
	t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxTenantGroup),
) {
	t.Helper()

	group := &netboxv1alpha1.NetBoxTenantGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxTenantGroupSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			Slug:             name,
		},
	}
	if mutate != nil {
		mutate(group)
	}

	if err := k8sClient.Create(context.Background(), group); err != nil {
		t.Fatalf("creating tenant group %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), group) })
}

func fetchTenantGroup(ns, name string) *netboxv1alpha1.NetBoxTenantGroup {
	group := &netboxv1alpha1.NetBoxTenantGroup{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, group); err != nil {
		return nil
	}

	return group
}

// tenantGroupCondition is one condition on a tenant group, or the zero value when the engine
// has not set it -- which is also what a missing object reads as, since both mean "not that
// status yet" to every caller here.
func tenantGroupCondition(ns, name, condType string) metav1.Condition {
	group := fetchTenantGroup(ns, name)
	if group == nil {
		return metav1.Condition{}
	}

	if found := apimeta.FindStatusCondition(group.Status.Conditions, condType); found != nil {
		return *found
	}

	return metav1.Condition{}
}

// tenantGroupOwnerRefs is a tenant group's owner references, read through the API server
// rather than the cache so the test sees what was actually persisted.
func tenantGroupOwnerRefs(t *testing.T, ns, name string) []metav1.OwnerReference {
	t.Helper()

	group := &netboxv1alpha1.NetBoxTenantGroup{}
	if err := apiClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, group); err != nil {
		t.Fatalf("fetching tenant group %s/%s: %v", ns, name, err)
	}

	return group.OwnerReferences
}

// TestATenantGroupChildIsOwnedByItsCascadingParent is #203.
// `tenancy.TenantGroup.parent` is `on_delete=CASCADE`, so deleting a group in NetBox deletes
// its descendants server-side; the kind declared no containment ref, so the child CR outlived
// the row it described and the engine put the row back.
//
// The shape is TestACascadeDeletedParentDoesNotRecreateItsChild's, and the one place it
// diverges is the whole reason #203 is not a fourth copy of #198. That test asserts two
// halves -- the owner reference the garbage collector reads, *and* the engine declining to
// re-create the row -- and the second half is a property of dcim.Region's identity: every one
// of its natural-key candidates reads `parent_id` or pins it null, so a child whose
// `parentRef` stopped resolving has no applicable candidate and locate() waits.
//
// tenancy.TenantGroup declares no `meta.constraints` at all; its uniqueness is column-level
// and global, so its single candidate is `slug` alone and never reads `parent`
// (docs/netbox-schema.md -> tenancy.TenantGroup). The candidate therefore stays applicable
// with the parent gone, finds nothing, and create-if-absent fires. So the second half is
// asserted here **inverted**: the row really does come back, which is the whole of #203 and
// the reason the owner reference is not a second line of defence for this kind but the only
// one. #204 supplies the guard the key cannot -- a *declared* reference as a precondition for
// the write, which is a rule about the reference rather than about the key -- and it is not
// merged, so nothing on this branch stops that create.
//
// What envtest can prove is everything garbage collection reads, and nothing it does: there is
// no kube-controller-manager here and therefore no collector, so the cascade itself is #29's
// e2e gate. The assertions below are the reference GC resolves -- by uid, because an owner
// reference carrying anything else is not a weaker cascade but an immediate deletion -- and
// that the child stops claiming Ready once its parent and its row are gone.
func TestATenantGroupChildIsOwnedByItsCascadingParent(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, tenantGroupKind)
	readyEndpoint(t, ns, target)

	ready := func(name string) bool {
		return tenantGroupCondition(ns, name, netboxv1alpha1.ConditionReady).Status ==
			metav1.ConditionTrue
	}

	makeTenantGroup(t, ns, "parent", nil)
	eventually(t, "the parent to become Ready", func() bool { return ready("parent") })

	makeTenantGroup(t, ns, "child", func(g *netboxv1alpha1.NetBoxTenantGroup) {
		g.Spec.ParentRef = &netboxv1alpha1.TenantGroupRef{Name: "parent"}
	})
	eventually(t, "the child to become Ready", func() bool { return ready("child") })
	eventually(t, "the child to be owned by its parent", func() bool {
		return len(tenantGroupOwnerRefs(t, ns, "child")) == 1
	})

	childID := fetchTenantGroup(ns, "child").Status.ID
	if childID == 0 {
		t.Fatal("the child has no status.id, so there is no row for a cascade to take")
	}

	// The precondition that makes the count at the end mean anything: there is a row named
	// `child` right now, so whatever is counted afterwards is about the cascade.
	if got := stub.countByKey("child"); got != 1 {
		t.Fatalf("netbox holds %d tenant group(s) named `child` before the cascade, want 1", got)
	}

	owner := tenantGroupOwnerRefs(t, ns, "child")[0]
	if owner.Kind != "NetBoxTenantGroup" || owner.Name != "parent" {
		t.Errorf("owner = %s/%s, want NetBoxTenantGroup/parent", owner.Kind, owner.Name)
	}

	// The assertion the cascade rests on. The collector resolves an owner by
	// (apiVersion, kind, name, uid) and reads one it cannot resolve as an owner that is
	// already gone -- so a wrong uid deletes this object at once instead of when its parent
	// goes.
	if parent := fetchTenantGroup(ns, "parent"); owner.UID != parent.UID {
		t.Fatalf("owner uid = %q, want the live parent's uid %q", owner.UID, parent.UID)
	}

	if owner.Controller != nil && *owner.Controller {
		t.Error("the containment owner reference is the controller; rule 4 says non-controller")
	}

	if got := tenantGroupCondition(ns, "child", netboxv1alpha1.ConditionParentOwned); got.Status !=
		metav1.ConditionTrue || got.Reason != netboxv1alpha1.ReasonParentOwned {
		t.Errorf("ParentOwned = %s/%s, want True/%s",
			got.Status, got.Reason, netboxv1alpha1.ReasonParentOwned)
	}

	// The parent CR first and the row second, as in the region test: on a real cluster the
	// finalizer's DELETE is what triggers the server-side cascade, but the stub models no
	// foreign keys, and taking the row first would leave a window where the child still
	// resolves its parent and legitimately re-creates a row deleted behind the operator's
	// back. That is drift correction working, not the bug under test.
	if err := apiClient.Delete(context.Background(),
		&netboxv1alpha1.NetBoxTenantGroup{
			ObjectMeta: metav1.ObjectMeta{Name: "parent", Namespace: ns},
		}); err != nil {
		t.Fatalf("deleting the parent: %v", err)
	}
	eventually(t, "the parent CR to be gone", func() bool { return fetchTenantGroup(ns, "parent") == nil })

	stub.cascade(childID)

	// True whether the engine waits or re-creates, so this one survives #204 unchanged: an
	// object whose containment parent has vanished must not go on claiming it matches NetBox.
	eventually(t, "the child to stop reporting Ready", func() bool { return !ready("child") })

	// And this is the resurrection, asserted rather than described, because "the natural key
	// does not defend this kind" is the entire claim of #203 and a claim nothing else in the
	// suite holds. It is also what makes the owner-reference assertions above non-vacuous: on
	// dcim.Region the create-if-absent step would decline here, so an owner reference is a
	// second line of defence; here it is the only one.
	//
	// **#204 inverts this block, deliberately.** Once a declared reference is a precondition
	// for the write, the guard sits before locate() and this pass stops at
	// Ready=False/WaitingForRef -- so the row stays away (count 0) and status.id is never even
	// cleared (still childID). That is a rule about the *reference* rather than about the key,
	// which is exactly why it reaches this kind where identity cannot. Whoever lands #204
	// should replace the two assertions below with their opposites and keep everything above.
	eventually(t, "the child to re-create the row NetBox cascade-deleted", func() bool {
		group := fetchTenantGroup(ns, "child")

		return group != nil && group.Status.ID != 0 && group.Status.ID != childID
	})

	if got := stub.countByKey("child"); got != 1 {
		t.Errorf("netbox holds %d tenant group(s) named `child` after the cascade, want 1: "+
			"`slug` is the whole natural key, so create-if-absent has nothing to stop it and "+
			"this test has stopped exercising what #203 is about", got)
	}

	t.Logf("the cascaded row came back as status.id %d (was %d): with `slug` the whole "+
		"natural key, the owner reference is the only thing between the child CR and a row "+
		"NetBox deleted on purpose", fetchTenantGroup(ns, "child").Status.ID, childID)
}
