package reconciler

import (
	"context"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// The pin is `status.address`, and this file is about the one question ADR-0004 leaves open:
// what happens when NetBox stops agreeing with it.
//
// A settled claim short-circuits the *allocation* before any HTTP call, which is what makes
// "reconcile fifty times, POST once" structural. Until #167 it short-circuited the whole pass,
// so a claim went on reporting Ready=True for an address it no longer held, indefinitely, and
// nothing ever looked. The tests here are the two halves of fixing that without spending the
// property it was bought with: the pin is re-read, and the quiet path stays quiet.

// pinAddress is what every claim in this file holds, and what the second allocation takes.
const pinAddress = "10.0.20.37/24"

// TestASettledClaimReportsAConflictWhenSomethingElseHoldsItsAddress is issue #167, as the
// sequence actually runs against this engine.
//
//  1. Claim A allocates 10.0.20.37/24 and pins it.
//  2. NetBox is restored from a snapshot predating that allocation, so the object simply
//     vanishes: nothing carries A's allocation identity any more.
//  3. A second claim asks for an address. The restored NetBox has never heard of
//     10.0.20.37/24, so it offers it, and the second claim allocates and stamps it with an
//     identity of its own.
//  4. Claim A reconciles.
//
// Before the guard, step 4 returned at the first line of the pass and reported Ready=True for
// an address another claim was holding. Both claims Ready, one address, and neither of them
// did anything wrong -- which is exactly why this had to become a code guard rather than a
// documented hazard: nobody can detect it by looking at either CR.
func TestASettledClaimReportsAConflictWhenSomethingElseHoldsItsAddress(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	restoredWithout(nb)
	nb.holders = []netbox.Object{allocatedObject(57, pinAddress, "beefbeefbeefbeef", "uid-2")}
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a contested pin is the claim's state, not a controller failure: %v", err)
	}

	ready := readyOfClaim(claim)
	if ready.Status != metav1.ConditionFalse ||
		ready.Reason != netboxv1alpha1.ReasonAllocationConflict {
		t.Errorf("Ready = %s/%s, want False/%s: another allocation identity holds %s and this"+
			" claim is still reporting it as its own",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonAllocationConflict, pinAddress)
	}

	// The message is the whole remedy here, because the operator cannot act without knowing
	// which object to look at: the id that holds the address now, and the identity it carries.
	for _, want := range []string{"57", "beefbeefbeefbeef", pinAddress} {
		if !strings.Contains(ready.Message, want) {
			t.Errorf("Ready message %q does not name %q", ready.Message, want)
		}
	}

	assertPinReportedNotRewritten(t, claim, nb)

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Warning/"+netboxv1alpha1.EventAllocationConflict) {
		t.Errorf("events = %v, want a Warning/%s", events.events,
			netboxv1alpha1.EventAllocationConflict)
	}
}

// TestASettledClaimReportsItsAllocationLostWhenNothingHoldsItsAddress is the same restore
// with nobody else in it yet: the row is gone and no second claim has asked.
//
// A separate reason from the conflict above rather than one "unverified" state, because the
// safe remedies are not the same. Nothing holds the value, so nothing is in service at it and
// re-applying the claim under a new name is safe; when something *does* hold it, that same
// move can take an address a NIC is already configured with.
func TestASettledClaimReportsItsAllocationLostWhenNothingHoldsItsAddress(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	restoredWithout(nb)
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a pin nothing backs is the claim's state, not a controller failure: %v", err)
	}

	ready := readyOfClaim(claim)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonAllocationLost {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason,
			netboxv1alpha1.ReasonAllocationLost)
	}

	assertPinReportedNotRewritten(t, claim, nb)

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Warning/"+netboxv1alpha1.EventAllocationLost) {
		t.Errorf("events = %v, want a Warning/%s", events.events, netboxv1alpha1.EventAllocationLost)
	}
}

// TestASettledClaimStaysQuiescent is the property the guard is not allowed to buy its
// detection with.
//
// The e2e suite asserts zero *mutating* NetBox requests across two resync periods, and it
// asserts it at system level precisely because a check that converges by writing passes every
// other assertion in the suite. Verifying a pin is a GET and can never be anything else --
// there is no branch in the settled path that reaches a POST, a PATCH or a DELETE -- and the
// read itself is on the clock rather than on the wake-up, so fifty passes inside one interval
// cost one request between them and not fifty.
func TestASettledClaimStaysQuiescent(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	allocating := len(nb.calls)

	for pass := range 50 {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	if got := len(nb.calls) - allocating; got != 0 {
		t.Errorf("%d requests over 50 settled passes (%v), want none: the pass that allocated"+
			" already verified this object, so the interval has not run out",
			got, nb.methods())
	}

	// One interval later, exactly one read -- and the pass after it is quiet again, which is
	// what stops the status write this one makes from becoming its own next trigger.
	verificationDue(claim)

	for pass := range 5 {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatalf("pass %d after the interval: %v", pass, err)
		}
	}

	reads := len(nb.calls) - allocating
	if reads != 1 {
		t.Errorf("%d requests for one due verification (%v), want exactly 1",
			reads, nb.methods())
	}

	if nb.posts != 1 || nb.deletes != 0 {
		t.Errorf("%d allocating POSTs and %d DELETEs, want 1 and 0: verifying a pin must not"+
			" write to NetBox in any state", nb.posts, nb.deletes)
	}

	if readyOfClaim(claim).Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s, want True: NetBox still holds this claim's address",
			readyOfClaim(claim).Status)
	}
}

// TestASettledClaimKeepsItsAddressWhenNetBoxCannotAnswer protects the older promise the
// settled path carries: an already-allocated claim settles while NetBox is unreachable.
//
// A verification that could not run has found nothing, and "found nothing" is not "the
// address is gone". Flipping every claim in a cluster to Ready=False because NetBox is
// restarting would make the guard the outage.
func TestASettledClaimKeepsItsAddressWhenNetBoxCannotAnswer(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	nb.listErr = &netbox.TransientError{Status: 503}
	verificationDue(claim)

	result, err := engine.Reconcile(context.Background(), claim)
	if err != nil {
		t.Fatalf("an unreachable netbox is not a claim failure: %v", err)
	}

	if got := readyOfClaim(claim); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True: the pin is unverified, not lost", got.Status, got.Reason)
	}

	if claim.Status.LastVerifiedAt != nil && !claim.Status.LastVerifiedAt.IsZero() &&
		time.Since(claim.Status.LastVerifiedAt.Time) < time.Minute {
		t.Error("status.lastVerifiedAt was moved forward by a verification that never got an" +
			" answer, so the next one would be skipped")
	}

	if result.RequeueAfter <= 0 {
		t.Error("no requeue after a verification that could not run, so nothing will try again")
	}
}

// TestASettledClaimKeepsItsAddressWhileItsEndpointIsNotReady is the same promise one step
// earlier: no endpoint, no client, no verification, and no change to what the claim reports.
func TestASettledClaimKeepsItsAddressWhileItsEndpointIsNotReady(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	before := len(nb.calls)
	engine.Endpoints = fakeEndpoints{}
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("an endpoint that is not ready is not a claim failure: %v", err)
	}

	if got := readyOfClaim(claim); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", got.Status, got.Reason)
	}

	if got := len(nb.calls) - before; got != 0 {
		t.Errorf("%d requests with no ready endpoint, want none", got)
	}
}

// TestAPinVerificationReadsTheEndpointsOwnIdentityField is the configurability the guard has
// to honour.
//
// `k8s_allocation_identity` is a default, not a constant: spec.managedBy.allocationIdentityField
// renames it, and a guard that searched the default name on an endpoint that renamed it would
// find nothing and report every settled claim in that namespace as lost.
func TestAPinVerificationReadsTheEndpointsOwnIdentityField(t *testing.T) {
	const renamed = "ipam_reservation_key"

	claim, engine, nb := settledClaimOn(t, provenance.FromSpec(&netboxv1alpha1.ManagedBy{
		ClusterID: testClusterID, AllocationIdentityField: renamed,
	}))

	// Still held, under the renamed field: the identity search has to be the one that finds it.
	held := netbox.Object{"id": float64(412), "address": pinAddress}
	netbox.SetCustomField(held, renamed, claim.Status.AllocationIdentity)
	nb.list = []netbox.Object{held}

	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := readyOfClaim(claim); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True: the object is still there under %s",
			got.Status, got.Reason, renamed)
	}

	last := nb.calls[len(nb.calls)-1]
	if _, asked := last.params[customFieldFilter+renamed]; !asked {
		t.Errorf("the verification asked %v, want a filter on %s%s",
			last.params, customFieldFilter, renamed)
	}
}

// TestAnUnverifiablePinIsLeftAlone is the other half of that: an endpoint with nowhere to
// store an identity has nothing to verify against.
//
// Such an endpoint never allocated the claim in the first place -- IdempotencyKeyUnavailable
// refuses before the POST -- so this is the shape of an endpoint reconfigured after the fact.
// The answer is silence rather than a conflict: an object this endpoint cannot search for is
// not an object that is gone, and reporting it as gone would be the guard inventing the
// hazard it exists to find.
func TestAnUnverifiablePinIsLeftAlone(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	stamp := provenance.Stamp{Config: provenance.FromSpec(&netboxv1alpha1.ManagedBy{
		ClusterID: testClusterID,
	}), TagID: 7}
	stamp.AllocationIdentityField = ""

	engine.Endpoints = fakeEndpoints{ready: true, endpoint: Endpoint{
		Client: nb, Allocator: nb, Provenance: stamp,
	}}

	before := len(nb.calls)
	restoredWithout(nb)
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := readyOfClaim(claim); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True: there is no identity field to search on, so nothing"+
			" was established either way", got.Status, got.Reason)
	}

	if got := len(nb.calls) - before; got != 0 {
		t.Errorf("%d requests with no identity field configured, want none", got)
	}
}

// TestAContestedPinRecoversWhenItsAllocationComesBack asserts the report is a report and not
// a latch.
//
// The states this engine refuses out of are all recoverable without a human touching the CR
// -- an exhausted pool converges when the prefix is widened -- and this one is no different:
// restore the row, or hand the object back, and the next verification clears the condition.
// A conflict that could only be cleared by deleting the claim would push operators towards
// exactly the `kubectl delete` the runbook warns can delete the surviving allocation.
func TestAContestedPinRecoversWhenItsAllocationComesBack(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	restoredWithout(nb)
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := readyOfClaim(claim).Reason; got != netboxv1alpha1.ReasonAllocationLost {
		t.Fatalf("Ready reason = %q, want %s before the recovery",
			got, netboxv1alpha1.ReasonAllocationLost)
	}

	nb.list = []netbox.Object{
		allocatedObject(412, pinAddress, claim.Status.AllocationIdentity, "uid-1"),
	}
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := readyOfClaim(claim); got.Status != metav1.ConditionTrue ||
		got.Reason != netboxv1alpha1.ReasonAddressAllocated {
		t.Errorf("Ready = %s/%s, want True/%s once the object is back",
			got.Status, got.Reason, netboxv1alpha1.ReasonAddressAllocated)
	}
}

// TestAPinCarriedByTwoObjectsIsAConflict reuses the verdict the allocating path already has
// for the same finding.
//
// Two objects carrying one identity is an over-allocation the operator cannot resolve: it
// cannot prove which of them is unused. findByIdentity already refuses exactly this, and the
// verification runs the same query, so it inherits the same answer rather than inventing a
// second one that could disagree with it.
func TestAPinCarriedByTwoObjectsIsAConflict(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	nb.list = []netbox.Object{
		allocatedObject(412, pinAddress, claim.Status.AllocationIdentity, "uid-1"),
		allocatedObject(413, "10.0.20.38/24", claim.Status.AllocationIdentity, "uid-1"),
	}
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("two objects under one identity is a state, not a controller failure: %v", err)
	}

	if got := readyOfClaim(claim); got.Status != metav1.ConditionFalse ||
		got.Reason != netboxv1alpha1.ReasonAllocationConflict {
		t.Errorf("Ready = %s/%s, want False/%s", got.Status, got.Reason,
			netboxv1alpha1.ReasonAllocationConflict)
	}
}

// TestAnAddressHeldBySeveralObjectsIsAConflict is the other ambiguity, one query later.
//
// Two objects carrying one *identity* is an over-allocation; two objects holding one *value* is
// what a duplicate address in another VRF looks like from here, and after a restore it is also
// what "the row came back and somebody allocated it again" looks like. The claim cannot prove
// which of them is its own -- nothing carries its identity at all -- so it names them and
// refuses to guess, exactly as the lookup that found them does.
func TestAnAddressHeldBySeveralObjectsIsAConflict(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	restoredWithout(nb)
	nb.holders = []netbox.Object{
		allocatedObject(57, pinAddress, "beefbeefbeefbeef", "uid-2"),
		allocatedObject(58, pinAddress, "", ""),
	}
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("an ambiguous holder is a state, not a controller failure: %v", err)
	}

	ready := readyOfClaim(claim)
	if ready.Status != metav1.ConditionFalse ||
		ready.Reason != netboxv1alpha1.ReasonAllocationConflict {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason,
			netboxv1alpha1.ReasonAllocationConflict)
	}

	for _, want := range []string{"57", "58"} {
		if !strings.Contains(ready.Message, want) {
			t.Errorf("Ready message %q does not name every object holding the address (%s)",
				ready.Message, want)
		}
	}

	assertPinReportedNotRewritten(t, claim, nb)
}

// TestAPinIsVerifiedEvenWithDriftOff says which of the two switches on an endpoint this
// guard answers to, and it is neither.
//
// driftMode: Off turns off the periodic *drift* re-check -- the read whose only purpose is to
// find a difference this operator would then write away. This read never writes anything in
// any mode, and the state it finds is one no other mechanism reports at all, so switching off
// drift correction must not also switch off the one thing standing between two claims and one
// address.
func TestAPinIsVerifiedEvenWithDriftOff(t *testing.T) {
	claim, engine, nb := settledClaim(t)

	engine.Endpoints = fakeEndpoints{ready: true, endpoint: Endpoint{
		Client:     nb,
		Allocator:  nb,
		DriftMode:  netboxv1alpha1.DriftOff,
		Provenance: provenance.Stamp{Config: identityConfig(), TagID: 7, Fields: identityFields()},
	}}

	restoredWithout(nb)
	verificationDue(claim)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := readyOfClaim(claim).Reason; got != netboxv1alpha1.ReasonAllocationLost {
		t.Errorf("Ready reason = %q, want %s: driftMode Off is not a switch on this guard",
			got, netboxv1alpha1.ReasonAllocationLost)
	}
}

// --- fixture ---------------------------------------------------------------------------

// settledClaim is a claim in the state every pass after its first one sees: allocated,
// verified, pinned, with nothing left to do.
func settledClaim(t *testing.T) (*netboxv1alpha1.NetBoxIPAddressClaim, *ClaimEngine, *claimClient) {
	t.Helper()

	return settledClaimOn(t, identityConfig())
}

// settledClaimOn is the same, on an endpoint whose provenance is the caller's -- which is how
// the renamed-identity-field case gets a claim that was allocated under that name.
func settledClaimOn(
	t *testing.T, config provenance.Config,
) (*netboxv1alpha1.NetBoxIPAddressClaim, *ClaimEngine, *claimClient) {
	t.Helper()

	claim, engine, nb := newClaimFixture(t)
	engine.Endpoints = fakeEndpoints{ready: true, endpoint: Endpoint{
		Client:     nb,
		Allocator:  nb,
		Provenance: provenance.Stamp{Config: config, TagID: 7, Fields: config.CustomFieldNames()},
	}}

	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")
	allocated := netbox.Object{
		"id": float64(412), "address": pinAddress,
	}
	netbox.SetCustomField(allocated, config.AllocationIdentityField, identity)
	netbox.SetCustomField(allocated, config.UIDField, "uid-1")

	nb.allocated = allocated
	nb.byEndpoint[claimEndpoint] = allocated

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("allocating the fixture's address: %v", err)
	}

	if claim.Status.Address != pinAddress || claim.Status.AllocationIdentity == "" {
		t.Fatalf("the fixture did not settle: address %q, identity %q",
			claim.Status.Address, claim.Status.AllocationIdentity)
	}

	// The object carrying this claim's identity, for every test that does not take it away.
	nb.list = []netbox.Object{allocated}

	return claim, engine, nb
}

// restoredWithout is the restore: NetBox no longer has the row this claim allocated, so
// nothing carries its identity and nothing holds its address.
func restoredWithout(nb *claimClient) {
	nb.list, nb.holders = nil, nil
	delete(nb.byEndpoint, claimEndpoint)
}

// verificationDue rewinds the last verification past the endpoint's interval, which is what
// the passage of time looks like from inside a unit test.
func verificationDue(claim *netboxv1alpha1.NetBoxIPAddressClaim) {
	rewound := metav1.NewTime(time.Now().Add(-2 * DefaultResync))
	claim.Status.LastVerifiedAt = &rewound
}

// assertPinReportedNotRewritten is the shape both verdicts share: the finding is reported and
// nothing else moves.
//
// status.address in particular. It is the one field that must never be lost -- while it holds
// a value the first guard clause of every pass returns before anything can allocate again, so
// clearing it here would answer a double allocation by allocating a third address. Allocated
// stays True for the same reason it is documented as a historical fact: the allocation did
// happen, and ADR-0004 has no event that un-allocates it.
func assertPinReportedNotRewritten(
	t *testing.T, claim *netboxv1alpha1.NetBoxIPAddressClaim, nb *claimClient,
) {
	t.Helper()

	if claim.Status.Address != pinAddress {
		t.Errorf("status.address = %q, want it left at %q", claim.Status.Address, pinAddress)
	}

	if claim.Status.NetBoxID != 412 {
		t.Errorf("status.netboxID = %d, want it left at 412", claim.Status.NetBoxID)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionAllocated); got.Status !=
		metav1.ConditionTrue {
		t.Errorf("Allocated = %s/%s, want True: it is a historical fact, and this claim did"+
			" allocate", got.Status, got.Reason)
	}

	if nb.posts != 1 || nb.deletes != 0 {
		t.Errorf("%d allocating POSTs and %d DELETEs, want the 1 and 0 of the allocating pass:"+
			" the operator cannot prove which allocation is unused, so it changes neither",
			nb.posts, nb.deletes)
	}
}
