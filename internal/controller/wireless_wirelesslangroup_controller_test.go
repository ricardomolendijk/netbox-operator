package controller

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// wirelessLANGroupKind points the shared stub at wireless.WirelessLANGroup. Keyed by `slug`,
// which is this kind's whole natural key -- `parent` is deliberately not in it, and that is
// what makes the parent deferrable.
var wirelessLANGroupKind = stubKind{endpoint: "wireless/wireless-lan-groups", key: "slug"}

func makeWirelessLANGroup(t *testing.T, ns, name, slug string,
	mutate func(*netboxv1alpha1.NetBoxWirelessLANGroup),
) {
	t.Helper()

	group := &netboxv1alpha1.NetBoxWirelessLANGroup{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxWirelessLANGroupSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             slug,
			Slug:             slug,
		},
	}
	if mutate != nil {
		mutate(group)
	}
	if err := k8sClient.Create(context.Background(), group); err != nil {
		t.Fatalf("creating wireless LAN group %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), group) })
}

func fetchWirelessLANGroup(ns, name string) *netboxv1alpha1.NetBoxWirelessLANGroup {
	group := &netboxv1alpha1.NetBoxWirelessLANGroup{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, group); err != nil {
		return nil
	}

	return group
}

func wirelessLANGroupIsReady(ns, name string) bool {
	group := fetchWirelessLANGroup(ns, name)
	if group == nil {
		return false
	}
	for _, c := range group.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestWirelessLANGroupDefersItsParentAndNeverPinsItNull is the pair of properties that follow
// from this kind's constraint lines, asserted against what the operator actually sent.
//
// `name` and `slug` are column-level `UNIQUE` (netbox/wireless/models.py:53-63) and the one
// table constraint is `unique(parent, name)` with **no** `condition=` clause (:70-75). So,
// unlike every dcim nested group, there is no `parent IS NULL` variant: the lookup is `?slug=`
// and nothing else. Adding the `?parent_id__empty=true` pin plan.md 8.1 asserts every MPTT kind
// needs would make a *nested* group's slug unfindable -- the request would match nothing and
// the engine would create a second row.
//
// And because `parent` is outside the identity, it is deferrable: a child applied before its
// parent is created top-level and PATCHed once the reference resolves, which is what makes a
// parent and a child applied in one batch converge without waiting out a resync.
func TestWirelessLANGroupDefersItsParentAndNeverPinsItNull(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, wirelessLANGroupKind)
	readyEndpoint(t, ns, target)

	// The child first, naming a parent that does not exist yet.
	makeWirelessLANGroup(t, ns, "child", "guest", func(g *netboxv1alpha1.NetBoxWirelessLANGroup) {
		g.Spec.ParentRef = &netboxv1alpha1.WirelessLANGroupRef{Name: "parent"}
	})

	eventually(t, "the child to be created top-level with its parent deferred", func() bool {
		group := fetchWirelessLANGroup(ns, "child")

		return group != nil && group.Status.ID != 0
	})

	child := fetchWirelessLANGroup(ns, "child")
	if _, present := stub.get(child.Status.ID)["parent"]; present {
		t.Error("the create carried `parent`, which could not have resolved yet")
	}
	if len(child.Status.DeferredPending) == 0 {
		t.Error("status.deferredPending is empty, so nothing records that the parent is owed")
	}

	// The lookup that located it, filter by filter. `slug` alone: no `parent_id` term and no
	// null pin, because this kind's one table constraint carries no `condition=` clause.
	if got := child.Status.NaturalKey; len(got) != 1 || got["slug"] != "guest" {
		t.Errorf("status.naturalKey = %v, want exactly {slug: guest} -- a parent term or a "+
			"parent_id null pin would make a nested group's slug unfindable", got)
	}

	// Now the parent. The child re-enqueues on the ref watch and PATCHes `parent` on.
	makeWirelessLANGroup(t, ns, "parent", "houses", nil)
	eventually(t, "the parent to be Ready", func() bool { return wirelessLANGroupIsReady(ns, "parent") })

	eventually(t, "the deferred parent to reach NetBox", func() bool {
		_, present := stub.get(child.Status.ID)["parent"]

		return present
	})

	if group := fetchWirelessLANGroup(ns, "child"); len(group.Status.DeferredPending) != 0 {
		t.Errorf("status.deferredPending = %v after the parent resolved, want empty",
			group.Status.DeferredPending)
	}
}
