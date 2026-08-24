package controller

import (
	"context"
	"net/http"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// siteKind points the shared stub at dcim.Site.
var siteKind = stubKind{endpoint: "dcim/sites", key: "slug"}

// makeSite applies a NetBoxSite whose slug is its name, and removes it afterwards so the
// finalizer does not outlive the stub it needs in order to come off.
func makeSite(t *testing.T, ns, slug string, mutate func(*netboxv1alpha1.NetBoxSite)) *netboxv1alpha1.NetBoxSite {
	t.Helper()

	site := &netboxv1alpha1.NetBoxSite{
		ObjectMeta: metav1.ObjectMeta{Name: slug, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxSiteSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Home",
			Slug:             slug,
			Status:           netboxv1alpha1.SiteStatusActive,
		},
	}
	if mutate != nil {
		mutate(site)
	}
	if err := k8sClient.Create(context.Background(), site); err != nil {
		t.Fatalf("creating site %s/%s: %v", ns, slug, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), site) })

	return site
}

func fetchSite(ns, slug string) *netboxv1alpha1.NetBoxSite {
	site := &netboxv1alpha1.NetBoxSite{}
	key := client.ObjectKey{Namespace: ns, Name: slug}
	if err := k8sClient.Get(context.Background(), key, site); err != nil {
		return nil
	}

	return site
}

func siteIsReady(ns, slug string) bool {
	site := fetchSite(ns, slug)
	if site == nil {
		return false
	}
	for _, c := range site.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestSiteIsCreatedInNetBoxAndReachesReady is the second kind's end-to-end proof. The first
// kind proved the engine works; this one proves it works for a kind it was not written
// against.
func TestSiteIsCreatedInNetBoxAndReachesReady(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	makeSite(t, ns, "home", func(s *netboxv1alpha1.NetBoxSite) {
		s.Spec.Latitude = "51.9244"
		s.Spec.Longitude = "4.4777"
		s.Spec.Description = "Home lab"
	})

	eventually(t, "the site to be Ready", func() bool { return siteIsReady(ns, "home") })

	site := fetchSite(ns, "home")
	if site.Status.ID == 0 {
		t.Error("status.id is unset on a Ready site; it is only set once the object provably exists")
	}
	if got := stub.countByKey("home"); got != 1 {
		t.Errorf("netbox holds %d sites with slug home, want exactly 1", got)
	}

	live := stub.get(site.Status.ID)
	if live["name"] != "Home" {
		t.Errorf("netbox name = %v, want Home", live["name"])
	}
}

// TestSiteChoiceAndDecimalDoNotHotLoop is the reason this kind is worth having as the second
// one. NetBox returns `status` as {"value","label"} and the decimals as padded strings, so a
// comparison that got either wrong would find a difference on every pass and PATCH forever.
//
// Asserted by letting several resyncs elapse and counting the writes, which is the only way
// to observe a hot loop -- a single reconcile cannot show one.
func TestSiteChoiceAndDecimalDoNotHotLoop(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	makeSite(t, ns, "steady", func(s *netboxv1alpha1.NetBoxSite) {
		s.Spec.Latitude = "51.9244"
		s.Spec.Longitude = "4.4777"
	})
	eventually(t, "the site to be Ready", func() bool { return siteIsReady(ns, "steady") })

	// The endpoint's resyncPeriod is one second in this suite, so this covers several
	// reconciles that should each find nothing to do.
	writesAfterCreate := len(stub.recorded())

	// Wait out several resync intervals. There is no way to observe a hot loop other than
	// letting time pass: a single reconcile finding a spurious difference looks identical
	// to one finding a real one.
	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: a choice or a decimal is comparing unequal to itself",
			got, writesAfterCreate)
	}
}

// TestSiteDriftIsCorrected edits NetBox behind the operator's back, the way a human in the
// UI would, and asserts the operator puts it back.
func TestSiteDriftIsCorrected(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	makeSite(t, ns, "drifty", nil)
	eventually(t, "the site to be Ready", func() bool { return siteIsReady(ns, "drifty") })

	id := fetchSite(ns, "drifty").Status.ID
	stub.setField(id, "status", "planned")

	eventually(t, "the status to be corrected", func() bool {
		live := stub.get(id)
		status, ok := live["status"].(map[string]any)

		return ok && status["value"] == "active"
	})
}

// TestSiteAdoptsAPreExistingNetBoxSite covers the opt-in adoption path, and its inverse in
// the test below. Adoption has to be explicit: silently taking over an object somebody else
// created is not a default worth having.
func TestSiteAdoptsAPreExistingNetBoxSite(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	existing := stub.seed(netbox.Object{
		"name": "Home", "slug": "adoptme", "status": "active", "description": "made by hand",
	})

	makeSite(t, ns, "adoptme", func(s *netboxv1alpha1.NetBoxSite) {
		s.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
	})

	eventually(t, "the site to be Ready", func() bool { return siteIsReady(ns, "adoptme") })

	site := fetchSite(ns, "adoptme")
	if site.Status.ID != existing {
		t.Errorf("status.id = %d, want %d: the pre-existing object should have been adopted, not duplicated",
			site.Status.ID, existing)
	}
	if got := stub.countByKey("adoptme"); got != 1 {
		t.Errorf("netbox holds %d sites with slug adoptme, want 1", got)
	}
}

func TestSiteRefusesToAdoptByDefault(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	stub.seed(netbox.Object{"name": "Home", "slug": "handsoff", "status": "active"})
	makeSite(t, ns, "handsoff", nil)

	eventually(t, "a Conflict", func() bool {
		site := fetchSite(ns, "handsoff")
		if site == nil {
			return false
		}
		for _, c := range site.Status.Conditions {
			if c.Reason == netboxv1alpha1.ReasonConflict {
				return true
			}
		}

		return false
	})

	if got := stub.countByKey("handsoff"); got != 1 {
		t.Errorf("netbox holds %d sites with slug handsoff, want 1: refusing to adopt must not create a duplicate", got)
	}
	if fetchSite(ns, "handsoff").Status.ID != 0 {
		t.Error("status.id is set on a Conflict; the operator has claimed an object it refused to adopt")
	}
}

// TestSameSiteSlugInTwoNamespaces is the accepted footgun of namespaced scoping, made a
// tested behaviour rather than a surprise. NetBox enforces slug uniqueness globally, so the
// first namespace to claim a slug gets it.
func TestSameSiteSlugInTwoNamespaces(t *testing.T) {
	first := newNamespaceSuffixed(t, "-a")
	second := newNamespaceSuffixed(t, "-b")
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, first, target)
	readyEndpoint(t, second, target)

	makeSite(t, first, "shared", nil)
	eventually(t, "the first site to be Ready", func() bool { return siteIsReady(first, "shared") })

	makeSite(t, second, "shared", nil)
	eventually(t, "the second site to report Conflict", func() bool {
		site := fetchSite(second, "shared")
		if site == nil {
			return false
		}
		for _, c := range site.Status.Conditions {
			if c.Reason == netboxv1alpha1.ReasonConflict {
				return true
			}
		}

		return false
	})

	// Neither corrupts the other: one NetBox object, owned by the first claimant.
	if got := stub.countByKey("shared"); got != 1 {
		t.Errorf("netbox holds %d sites with slug shared, want 1", got)
	}
	if !siteIsReady(first, "shared") {
		t.Error("the first site stopped being Ready when the second one lost")
	}
}

func TestDeletingASiteRemovesItFromNetBox(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	readyEndpoint(t, ns, target)

	site := makeSite(t, ns, "transient", nil)
	eventually(t, "the site to be Ready", func() bool { return siteIsReady(ns, "transient") })

	if err := k8sClient.Delete(context.Background(), site); err != nil {
		t.Fatalf("deleting site: %v", err)
	}

	eventually(t, "netbox to lose the site", func() bool { return stub.countByKey("transient") == 0 })
	eventually(t, "the CR to go away once its finalizer is released", func() bool {
		return fetchSite(ns, "transient") == nil
	})
}

// TestSiteRejectedByNetBoxReportsAndStops covers a 400: the payload is wrong, retrying it
// unchanged cannot help, and the operator must say so rather than spin.
func TestSiteRejectedByNetBoxReportsAndStops(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, siteKind)
	stub.createStatus = http.StatusBadRequest
	readyEndpoint(t, ns, target)

	makeSite(t, ns, "rejected", nil)

	eventually(t, "the site to report not Ready", func() bool {
		site := fetchSite(ns, "rejected")
		if site == nil {
			return false
		}
		for _, c := range site.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionReady && c.Status == metav1.ConditionFalse {
				return true
			}
		}

		return false
	})

	if fetchSite(ns, "rejected").Status.ID != 0 {
		t.Error("status.id is set after a rejected create; nothing exists server-side to have an id")
	}
}

// waitResyncs blocks for roughly n endpoint resync intervals. The suite sets
// resyncPeriod to one second, so this is deliberately short and deliberately real time:
// there is no hook that says "the engine has reconciled and found nothing".
func waitResyncs(t *testing.T, n int) {
	t.Helper()
	time.Sleep(time.Duration(n) * time.Second)
}
