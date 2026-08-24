package controller

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestReportModeTargetDoesNotBlockItsReferrers is NBO-089, end to end.
//
// The shape is the one the ticket describes and the one an adoption actually has: a catalogue
// object at an endpoint whose `driftMode` is `Report`, and something in a team's own namespace
// pointing at it. Report detects drift, reports it and never corrects it, so the catalogue
// object is `Ready=False` as a *steady state* rather than a transient -- and under the old rule
// (a ref requires its target to be Ready) every object pointing at it blocked for as long as
// the adoption ran, which is the week Report exists for.
//
// The referrer now resolves on the target's **id** and reconciles, and says on its own
// `RefsResolved` that the object behind that id is unfinished.
func TestReportModeTargetDoesNotBlockItsReferrers(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, regionKind)

	// Two endpoints in one namespace, which is what makes this a two-line test rather than a
	// two-namespace one: `spec.endpointRef` is per object, so the catalogue region can be in
	// Report while the team's region is applied for real.
	readyEndpointNamed(t, ns, "catalogue", target, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.DriftMode = netboxv1alpha1.DriftReport
	})
	readyEndpointNamed(t, ns, "homelab", target, nil)

	// Seeded and adopted, because that is how a Report-mode object comes to hold an id at all:
	// a suppressed *create* leaves status.id at zero, and then the referrer is correctly not
	// ready. An adoption is a read, so it happens in Report like anywhere else.
	parentID := stub.seed(netbox.Object{"name": "EMEA", "slug": "emea", "description": "edited by hand"})

	makeRegion(t, ns, "emea", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.EndpointRef = "catalogue"
		r.Spec.Name = "EMEA"
		r.Spec.OnConflict = netboxv1alpha1.ConflictAdopt
		r.Spec.Description = "managed by the operator"

		// A Report endpoint hands the engine a client that cannot write at all, the
		// finalizer's DELETE included, so a Delete policy would leave this region terminating
		// forever. Retain drops the finalizer without asking NetBox, which is the policy that
		// exists for exactly this (docs/concepts/deletion.md).
		r.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain
	})

	// The precondition, asserted rather than assumed: an id, and Ready=False that will never
	// become True by itself.
	eventually(t, "the catalogue region reports rather than corrects", func() bool {
		region := fetchRegion(ns, "emea")

		return region != nil && region.Status.ID != 0 &&
			readyReason(region) == netboxv1alpha1.ReasonReportPending
	})

	makeRegion(t, ns, "ams", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.Name = "Amsterdam"
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "emea"}
	})

	eventually(t, "the referrer reaches Ready", func() bool { return regionIsReady(ns, "ams") })

	child := fetchRegion(ns, "ams")
	if child == nil {
		t.Fatal("the referrer disappeared")
	}

	if got := readyReason(child); got != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonSynced)
	}

	// The id reached NetBox. Without this the test would pass on a referrer that reached Ready
	// having quietly dropped the reference.
	if got := stub.get(child.Status.ID)["parent"]; got != float64(parentID) {
		t.Errorf("netbox parent = %v, want %d", got, parentID)
	}

	// Resolved, and *said so*. A referrer that is Ready over an id whose object is unfinished
	// is exactly what somebody debugging needs told, and this is the condition they read.
	resolved := conditionOfRegion(child, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionTrue || resolved.Reason != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("RefsResolved = %s/%s, want True/%s",
			resolved.Status, resolved.Reason, netboxv1alpha1.ReasonAllResolved)
	}

	if !containsAll(resolved.Message, "parentRef", "resolved, target not ready", "ReportPending") {
		t.Errorf("RefsResolved message = %q, want it to name the field and quote the target's state",
			resolved.Message)
	}
}

// TestConflictedTargetStillBlocksItsReferrers is the other half of the decision: proceeding on
// an id is not proceeding on anything.
//
// A target the engine refused to claim holds no id -- `onConflict: Refuse` means it never
// adopted the object it found -- so the referrer waits with `RefNotReady`, which is what
// `ErrRefNotReady` is now reserved for. Nothing is written for the reference, and nothing is
// written to NetBox for the referrer either, because `parentRef` is part of dcim.Region's
// identity: no candidate is applicable, so the engine cannot tell create from adopt.
//
// The variant where a Conflict *does* hold an id -- a CR that adopted an object and later
// described a different one -- is refused with `RefTargetFailed`, and is asserted in
// internal/resolver: it takes a target status no sequence of reconciles produces on demand.
func TestConflictedTargetStillBlocksItsReferrers(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, regionKind)
	readyEndpointNamed(t, ns, "homelab", target, nil)

	// Not created by the operator and not adoptable, which is the default: onConflict is
	// Refuse, so the engine finds the object, will not claim it, and records no id.
	stub.seed(netbox.Object{"name": "EMEA", "slug": "emea"})

	makeRegion(t, ns, "emea", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.Name = "EMEA"
	})

	eventually(t, "the target reports a conflict", func() bool {
		region := fetchRegion(ns, "emea")

		return region != nil && readyReason(region) == netboxv1alpha1.ReasonConflict
	})

	if got := fetchRegion(ns, "emea").Status.ID; got != 0 {
		t.Fatalf("target status.id = %d, want 0: a refused adoption claims nothing", got)
	}

	makeRegion(t, ns, "ams", func(r *netboxv1alpha1.NetBoxRegion) {
		r.Spec.Name = "Amsterdam"
		r.Spec.ParentRef = &netboxv1alpha1.RegionRef{Name: "emea"}
	})

	eventually(t, "the referrer reports the unresolved reference", func() bool {
		return refsReason(ns, "ams") != ""
	})

	child := fetchRegion(ns, "ams")
	if child == nil {
		t.Fatal("the referrer disappeared")
	}

	resolved := conditionOfRegion(child, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionFalse || resolved.Reason != netboxv1alpha1.ReasonRefNotReady {
		t.Errorf("RefsResolved = %s/%s, want False/%s -- a target with no id is a wait",
			resolved.Status, resolved.Reason, netboxv1alpha1.ReasonRefNotReady)
	}

	// Nothing created. `parentRef` is part of dcim.Region's identity, so with it unresolved no
	// natural-key candidate applies and the engine must not guess (docs/concepts/lookups.md).
	if got := stub.countByKey("Amsterdam"); got != 0 {
		t.Errorf("netbox holds %d Amsterdam regions, want none", got)
	}

	if got := readyReason(child); got != netboxv1alpha1.ReasonWaitingForKey {
		t.Errorf("Ready reason = %q, want %q", got, netboxv1alpha1.ReasonWaitingForKey)
	}
}

// conditionOfRegion returns one condition, or the zero value when it was never set.
func conditionOfRegion(region *netboxv1alpha1.NetBoxRegion, condType string) metav1.Condition {
	for _, condition := range region.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// readyReason is the reason on a region's Ready condition, and "" when it has none. It is the
// half of the condition these two tests turn on: whether the referrer proceeded is a question
// about the *target's* reason (resolver.targetFailures).
func readyReason(region *netboxv1alpha1.NetBoxRegion) string {
	return conditionOfRegion(region, netboxv1alpha1.ConditionReady).Reason
}
