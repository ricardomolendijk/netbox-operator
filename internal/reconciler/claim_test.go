package reconciler

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// The allocation engine's seams exist to be faked, and are only worth having if the real
// things satisfy them.
var (
	_ Allocator        = (*netbox.Client)(nil)
	_ Claim            = (*netboxv1alpha1.NetBoxIPAddressClaim)(nil)
	_ PoolResolver     = (*resolver.Resolver)(nil)
	_ ClaimDescriptors = claimLookup{}
)

const (
	poolEndpoint  = "ipam/prefixes"
	claimEndpoint = "ipam/ip-addresses"
	testURL       = "https://netbox.example"

	// testClusterID is the fixture endpoint's spec.managedBy.clusterID, and the cluster every
	// stamp in this file is written by.
	testClusterID = "prod-eu"
)

// TestAllocationIdentityIsPinned is the golden test the whole reclaim story rests on.
//
// The identity is a pure function of (endpoint URL, namespace, Kind, name), and changing the
// derivation silently re-rolls every address in every cluster that upgrades: every claim
// searches for an identity no object carries, finds nothing, and allocates a second address.
// Nothing else in the operator would notice. So the value is pinned here, and an accidental
// change breaks the build instead.
func TestAllocationIdentityIsPinned(t *testing.T) {
	// Independently derived, not copied from this implementation's output:
	//   python3 -c "import hashlib; print(hashlib.sha256(
	//     b'https://netbox.example\nhomelab\nNetBoxIPAddressClaim\ndns-eth0'
	//   ).hexdigest()[:16])"
	const want = "f3fb013aa1d2ffc2"

	got := AllocationIdentity("https://netbox.example", "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	if got != want {
		t.Errorf("AllocationIdentity() = %q, want the pinned %q."+
			" If this change is deliberate, understand first that it re-rolls every allocated"+
			" address in every cluster that upgrades: see docs/concepts/claims.md.", got, want)
	}

	if len(got) != identityLength {
		t.Errorf("identity is %d characters, want %d", len(got), identityLength)
	}
}

// TestAllocationIdentityDependsOnEveryComponent proves all four components are load-bearing,
// and that the separator keeps them apart.
//
// The last pair is the reason the components are joined on a newline: concatenated, the
// tuples ("a", "bc") and ("ab", "c") render to one string, so two different claims would
// share one identity -- and sharing an identity is the AllocationConflict state that can
// never be resolved automatically.
func TestAllocationIdentityDependsOnEveryComponent(t *testing.T) {
	base := AllocationIdentity("https://a", "ns", "Kind", "name")

	cases := map[string]string{
		"url":       AllocationIdentity("https://b", "ns", "Kind", "name"),
		"namespace": AllocationIdentity("https://a", "other", "Kind", "name"),
		"kind":      AllocationIdentity("https://a", "ns", "Other", "name"),
		"name":      AllocationIdentity("https://a", "ns", "Kind", "other"),
		"boundary":  AllocationIdentity("https://a", "ns", "Kindname", ""),
	}

	for changed, got := range cases {
		if got == base {
			t.Errorf("changing the %s did not change the identity: both are %q", changed, got)
		}
	}
}

// TestClaimAllocatesOnceAndOnlyOnce is the assertion this whole file exists for.
//
// Fifty passes over one claim, and exactly one POST to the allocation endpoint. Counted at
// the fake rather than inspected in the code, because the failure this prevents is silent:
// every extra POST is another address burned out of somebody's /24, and nothing reports it
// until the prefix is full.
func TestClaimAllocatesOnceAndOnlyOnce(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	for pass := range 50 {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	if nb.posts != 1 {
		t.Errorf("%d POSTs to the allocation endpoint, want exactly 1", nb.posts)
	}

	if claim.Status.Address != "10.0.20.37/24" {
		t.Errorf("status.address = %q, want 10.0.20.37/24", claim.Status.Address)
	}

	if claim.Status.AllocationIdentity == "" || claim.Status.ClaimUID != "uid-1" {
		t.Errorf("status did not record the identity and the uid: %+v", claim.Status)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionAllocated); got.Reason !=
		netboxv1alpha1.ReasonAddressAllocated || got.Status != metav1.ConditionTrue {
		t.Errorf("Allocated = %s/%s, want True/AddressAllocated", got.Status, got.Reason)
	}

	// The steady state makes no NetBox request at all: the guard on status.address returns
	// before the endpoint is even resolved. So the whole fifty passes cost exactly what the
	// first one did -- read the pool, search the identity, allocate, verify.
	if requests := len(nb.calls); requests != 4 {
		t.Errorf("%d netbox requests over 50 passes (%v), want the 4 of the first pass alone",
			requests, nb.methods())
	}
}

// TestClaimPayloadCarriesIdentityAndStamp checks the one body that matters.
//
// The identity has to ride along on the allocating POST itself. NetBox's available-ips view
// honours the full write serializer, so there is no window in which an allocated address
// exists without the value that says whose it is -- and a follow-up PATCH would open exactly
// that window.
func TestClaimPayloadCarriesIdentityAndStamp(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	payload := nb.allocatePayload

	if got := netbox.CustomFieldOf(payload, provenance.DefaultAllocationIdentityField); got !=
		claim.Status.AllocationIdentity {
		t.Errorf("payload carried %s=%q, want the claim's identity %q",
			provenance.DefaultAllocationIdentityField, got, claim.Status.AllocationIdentity)
	}

	if got := netbox.CustomFieldOf(payload, provenance.DefaultUIDField); got != "uid-1" {
		t.Errorf("payload carried %s=%q, want the claim's uid", provenance.DefaultUIDField, got)
	}

	if got := netbox.CustomFieldOf(payload, provenance.DefaultOwnerField); got !=
		"netboxipaddressclaim/homelab/dns-eth0" {
		t.Errorf("payload carried %s=%q, want the claim's ref", provenance.DefaultOwnerField, got)
	}

	if _, tagged := payload[provenance.TagsField]; !tagged {
		t.Error("payload carried no tags; the provenance tag rides on the same atomic call")
	}

	if claim.Status.Provenance == nil || claim.Status.Provenance.ClusterID != "prod-eu" {
		t.Errorf("status.provenance = %+v, want the stamp that was written", claim.Status.Provenance)
	}
}

// TestClaimReclaimsByIdentity is the recovery path, and there is only one.
//
// A lost HTTP response, a pod evicted between the POST and the status write, a
// controller-runtime retry and a cluster rebuilt from Git are the same thing from NetBox's
// side: the object exists and carries this claim's identity. So one search covers all four,
// and the assertion is zero POSTs.
func TestClaimReclaimsByIdentity(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	nb.list = []netbox.Object{allocatedObject(412, "10.0.20.99/24", identity, "uid-0")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.posts != 0 {
		t.Errorf("%d POSTs, want 0: the object carrying this identity must be reclaimed", nb.posts)
	}

	if claim.Status.Address != "10.0.20.99/24" || claim.Status.NetBoxID != 412 {
		t.Errorf("status = %q/%d, want the reclaimed 10.0.20.99/24 and 412",
			claim.Status.Address, claim.Status.NetBoxID)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionAllocated); got.Reason !=
		netboxv1alpha1.ReasonReclaimedByIdentity {
		t.Errorf("Allocated reason = %q, want ReclaimedByIdentity", got.Reason)
	}

	// The handover is reported and not judged: on a rebuilt cluster it is entirely
	// legitimate, and when two claims are given one name over time it is a mistake, and the
	// two are indistinguishable from inside the operator.
	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionAllocated); !strings.Contains(got.Message, "uid-0") ||
		!strings.Contains(got.Message, "uid-1") {
		t.Errorf("Allocated message %q names neither uid; the handover has to be visible", got.Message)
	}
}

// TestClaimRefusesWhenTwoObjectsShareOneIdentity is the state the operator must never try to
// fix on its own.
//
// Two objects carrying one identity means a previous over-allocation, and the operator cannot
// prove which is unused -- a NIC or a DNS record may point at either. So: no POST, no delete,
// and a message naming both.
func TestClaimRefusesWhenTwoObjectsShareOneIdentity(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	nb.list = []netbox.Object{
		allocatedObject(412, "10.0.20.99/24", identity, "uid-0"),
		allocatedObject(413, "10.0.20.98/24", identity, "uid-0"),
	}

	result, err := engine.Reconcile(context.Background(), claim)
	if err != nil {
		t.Fatalf("a refusal must not be returned as an error: %v", err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonAllocationConflict)

	if !strings.Contains(readyOfClaim(claim).Message, "412") ||
		!strings.Contains(readyOfClaim(claim).Message, "413") {
		t.Errorf("Ready message %q must name every matching id", readyOfClaim(claim).Message)
	}

	if result.RequeueAfter < refusedRetry/2 {
		t.Errorf("requeue is %s, want the no-spin tier of about %s", result.RequeueAfter, refusedRetry)
	}
}

// TestClaimRefusesAnIdentityOutsideItsPool is the price of a deterministic identity.
//
// Renaming a prefix, repointing a claim or reusing a claim name for another purpose all reach
// this state, where a UID-keyed identity never could. Both alternatives to refusing are
// worse: allocating a second address leaves two objects carrying one identity, and accepting
// the out-of-pool object makes prefixRef a lie.
func TestClaimRefusesAnIdentityOutsideItsPool(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	nb.list = []netbox.Object{allocatedObject(412, "10.9.9.9/24", identity, "uid-1")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonReclaimedOutsidePool)
}

// TestClaimRefusesAPoolItWillNotAllocateFrom covers both descriptor-declared refusals.
//
// `mark_utilized` only forces NetBox's utilisation gauge to 100% -- `available-ips` would
// still hand out an address -- and a container's free space is subdivided by child prefixes
// rather than populated by addresses. Both are the NetBox operator having said something the
// allocation endpoint does not honour on its own, so honouring it is this operator's job.
func TestClaimRefusesAPoolItWillNotAllocateFrom(t *testing.T) {
	cases := map[string]netbox.Object{
		"mark_utilized": {"prefix": "10.0.20.0/24", "mark_utilized": true},
		"container":     {"prefix": "10.0.20.0/24", "status": map[string]any{"value": "container"}},
		// A choice NetBox flattened to a bare string, which the brief serialisers do.
		"flat container": {"prefix": "10.0.20.0/24", "status": "container"},
	}

	for name, pool := range cases {
		t.Run(name, func(t *testing.T) {
			claim, engine, nb := newClaimFixture(t)
			nb.byEndpoint[poolEndpoint] = pool

			if _, err := engine.Reconcile(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			assertRefused(t, claim, nb, netboxv1alpha1.ReasonPoolNotAllocatable)

			// The identity search never even ran: an inadmissible pool is refused before
			// anything is looked up in it.
			if got := nb.methods(); len(got) != 1 {
				t.Errorf("requests = %v, want the pool read alone", got)
			}
		})
	}
}

// TestClaimWaitsOnAnExhaustedPool is decision #178 as a test.
//
// It waits rather than failing terminally, on a fixed ten-minute tier rather than the
// workqueue's millisecond backoff, and the condition names the pool and its utilisation --
// because a reader told only "exhausted" goes and looks the prefix up by hand.
func TestClaimWaitsOnAnExhaustedPool(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	nb.allocErr = &netbox.ExhaustedError{
		Endpoint: poolEndpoint + "/available-ips", ID: 11,
		Body: `{"detail": "Insufficient resources are available to satisfy the request"}`,
	}

	result, err := engine.Reconcile(context.Background(), claim)
	if err != nil {
		t.Fatalf("exhaustion must never be returned as an error, or the workqueue backs off in"+
			" milliseconds: %v", err)
	}

	if claim.Status.Address != "" {
		t.Errorf("status.address = %q, want empty: an exhausted claim holds no address it did not get",
			claim.Status.Address)
	}

	ready := readyOfClaim(claim)
	if ready.Reason != netboxv1alpha1.ReasonPoolExhausted {
		t.Fatalf("Ready reason = %q, want PoolExhausted", ready.Reason)
	}

	for _, want := range []string{"10.0.20.0/24", "ipam/prefixes/11", "utilised", "Insufficient resources"} {
		if !strings.Contains(ready.Message, want) {
			t.Errorf("Ready message %q does not mention %q; the condition has to name the pool"+
				" and its utilisation", ready.Message, want)
		}
	}

	if result.RequeueAfter < 5*time.Minute {
		t.Errorf("requeue is %s, want at least 5m: a fast retry on an exhausted pool is a spin",
			result.RequeueAfter)
	}
}

// TestClaimWithNowhereToStoreAnIdentityAllocatesNothing is the one refusal with no override.
//
// The provenance stamp is optional for an ordinary object. For a claim the identity store is
// what makes a lost response recoverable, and without it every retry of a POST that committed
// burns another address -- so the claim refuses before the first POST rather than after.
func TestClaimWithNowhereToStoreAnIdentityAllocatesNothing(t *testing.T) {
	cases := map[string]provenance.Stamp{
		"provenance off":    {},
		"field not created": {Config: identityConfig(), TagID: 7, Fields: []string{"k8s_uid"}},
		"identity field unset": {
			Config: provenance.Config{ClusterID: "prod-eu", UIDField: "k8s_uid"},
			TagID:  7, Fields: []string{"k8s_uid"},
		},
	}

	for name, stamp := range cases {
		t.Run(name, func(t *testing.T) {
			claim, engine, nb := newClaimFixture(t)
			endpoints, _ := engine.Endpoints.(fakeEndpoints)
			endpoints.endpoint.Provenance = stamp
			engine.Endpoints = endpoints

			if _, err := engine.Reconcile(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			assertRefused(t, claim, nb, netboxv1alpha1.ReasonIdempotencyKeyUnavailable)

			if len(nb.calls) != 0 {
				t.Errorf("requests = %v, want none at all", nb.methods())
			}
		})
	}
}

// TestClaimDoesNotAllocateWhenTheEndpointWritesNothing covers the two ways an endpoint can be
// told not to write.
//
// Neither leaves a status.address behind, because `kubectl wait` must not pass on an
// allocation that never happened -- and the two are reported separately, since they are set in
// different fields and switched off in different places.
func TestClaimDoesNotAllocateWhenTheEndpointWritesNothing(t *testing.T) {
	t.Run("dry run", func(t *testing.T) {
		claim, engine, nb := newClaimFixture(t)
		nb.dryRun = dryRunClient(t)

		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatal(err)
		}

		assertNotAllocated(t, claim, netboxv1alpha1.ReasonDryRunPending)
	})

	t.Run("drift mode report", func(t *testing.T) {
		claim, engine, nb := newClaimFixture(t)
		endpoints, _ := engine.Endpoints.(fakeEndpoints)
		endpoints.endpoint.DriftMode = netboxv1alpha1.DriftReport
		engine.Endpoints = endpoints

		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatal(err)
		}

		assertNotAllocated(t, claim, netboxv1alpha1.ReasonReportPending)

		if nb.posts != 0 {
			t.Errorf("%d POSTs under driftMode: Report, want 0", nb.posts)
		}
	})
}

// TestClaimRefusesToRecordAnUnverifiedAllocation is the read-after-write.
//
// The cost of trusting a POST response is an address recorded in status that NetBox does not
// hold, handed to a human who configures a NIC with it. So every disagreement between the
// answer and the read that follows it writes nothing at all, and the identity search on the
// next pass reconciles whatever actually landed.
func TestClaimRefusesToRecordAnUnverifiedAllocation(t *testing.T) {
	cases := map[string]netbox.Object{
		"a different address": allocatedObject(412, "10.0.20.38/24",
			AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0"), "uid-1"),
		"no identity": allocatedObject(412, "10.0.20.37/24", "", "uid-1"),
		"out of pool": allocatedObject(412, "10.9.9.9/24", "", "uid-1"),
		"not there":   nil,
	}

	for name, verified := range cases {
		t.Run(name, func(t *testing.T) {
			claim, engine, nb := newClaimFixture(t)
			nb.byEndpoint[claimEndpoint] = verified

			if _, err := engine.Reconcile(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			if claim.Status.Address != "" || claim.Status.NetBoxID != 0 {
				t.Errorf("status recorded %q/%d from an unverified allocation",
					claim.Status.Address, claim.Status.NetBoxID)
			}

			if got := readyOfClaim(claim).Reason; got != netboxv1alpha1.ReasonAllocationPending {
				t.Errorf("Ready reason = %q, want AllocationPending", got)
			}
		})
	}
}

// TestClaimWaitsForItsPoolReference asserts the ordering rule that keeps an allocation from
// happening against a pool nobody has resolved.
//
// It returns no error and no timer: the ref watch on the NetBoxPrefix is what re-enqueues the
// claim, and that same watch is what makes a widened prefix converge immediately.
func TestClaimWaitsForItsPoolReference(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	engine.Refs = &fakePool{err: &resolver.Error{
		Cause: resolver.ErrRefNotReady, Field: "prefixRef", Mode: resolver.ModeName,
		TargetGVK: netboxv1alpha1.PrefixRef{}.TargetGVK(),
	}}

	result, err := engine.Reconcile(context.Background(), claim)
	if err != nil {
		t.Fatalf("an unresolved reference is a state, not a failure: %v", err)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionRefsResolved); got.Status !=
		metav1.ConditionFalse || got.Reason != netboxv1alpha1.ReasonRefNotReady {
		t.Errorf("RefsResolved = %s/%s, want False/RefNotReady", got.Status, got.Reason)
	}

	if got := readyOfClaim(claim).Reason; got != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready reason = %q, want WaitingForRef", got)
	}

	if result.RequeueAfter != 0 {
		t.Errorf("requeue is %s, want none: the ref watch is what ends this wait",
			result.RequeueAfter)
	}

	if len(nb.calls) != 0 || claim.Status.Address != "" {
		t.Errorf("requests %v and address %q, want neither", nb.methods(), claim.Status.Address)
	}
}

// TestClaimWaitsForItsEndpoint is the guard before every other one that touches NetBox.
func TestClaimWaitsForItsEndpoint(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	engine.Endpoints = fakeEndpoints{}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := readyOfClaim(claim).Reason; got != netboxv1alpha1.ReasonWaitingForEndpoint {
		t.Errorf("Ready reason = %q, want WaitingForEndpoint", got)
	}

	if len(nb.calls) != 0 {
		t.Errorf("requests = %v, want none", nb.methods())
	}
}

// TestDeletingAClaimFreesItsAddress is the behaviour #225 reversed #182 to get.
//
// A claim's CR is the only record that its allocation exists, so a claim that goes away
// without freeing its address leaves litter nothing in the cluster can name. The DELETE has to
// be for the id in status and no other: an id is the only thing the operator can prove it
// allocated, and a natural-key or identity search at deletion time would be a DELETE aimed at
// whatever happened to match.
func TestDeletingAClaimFreesItsAddress(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	retained := watch(t, metrics.AllocationsRetained, []string{"NetBoxIPAddressClaim"})

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.deletes != 1 {
		t.Errorf("%d deletes reached netbox, want exactly 1 (%v)", nb.deletes, nb.methods())
	}

	deleted := lastCall(nb, "DELETE")
	if deleted.endpoint != claimEndpoint || deleted.id != 412 {
		t.Errorf("deleted %s/%d, want %s/412 -- the id recorded in status and no other",
			deleted.endpoint, deleted.id, claimEndpoint)
	}

	if len(claim.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none: the address is freed, so nothing is left to wait for",
			claim.Finalizers)
	}

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Normal/"+netboxv1alpha1.EventDeleted) {
		t.Errorf("events = %v, want a %s naming the address that was freed",
			events.events, netboxv1alpha1.EventDeleted)
	}

	if got := retained.delta("NetBoxIPAddressClaim"); got != 0 {
		t.Errorf("allocations_retained moved by %v, want 0: nothing was left behind", got)
	}
}

// TestDeletingARetainingClaimCallsNetBoxNotAtAll is #182's answer surviving as an opt-in.
//
// Retain is what a claim is set to when something outside Kubernetes depends on the address
// and cannot be told it has moved. Its whole contract is that the operator does not call
// NetBox and does say what it has stopped tracking -- this is the last moment it holds the
// identity, the id and the address together, and after the CR is gone there is no status left
// to read them from. The counter is here because the Event ages out of its namespace within
// the hour and "how many has this cluster left behind" has to stay answerable.
func TestDeletingARetainingClaimCallsNetBoxNotAtAll(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	retained := watch(t, metrics.AllocationsRetained, []string{"NetBoxIPAddressClaim"})

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	before := len(nb.calls)
	claim.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain
	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if len(nb.calls) != before {
		t.Errorf("deletion made netbox requests (%v); a retained allocation needs none",
			nb.methods())
	}

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Normal/"+netboxv1alpha1.EventAddressRetained) {
		t.Errorf("events = %v, want an %s naming what was left behind",
			events.events, netboxv1alpha1.EventAddressRetained)
	}

	if got := retained.delta("NetBoxIPAddressClaim"); got != 1 {
		t.Errorf("allocations_retained moved by %v, want 1", got)
	}

	if len(claim.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none: nothing is left to wait for", claim.Finalizers)
	}
}

// TestADeleteThatCannotSucceedStillReleasesTheClaim is the property #225 had to buy back.
//
// #213 shipped a deletion pass that made zero NetBox calls, and said why: "this finalizer
// cannot wedge a namespace". A Delete policy puts a DELETE on that path, so the guarantee is
// no longer free and has to be earned -- which is what claimDeleteAttempts does. Every case
// below is a delete that will never succeed however long it is retried, and in every one the
// CR ends up finalizable rather than stuck, with the leak reported through #182's Event and
// counter. That degradation is exactly the outcome #213 already shipped, which is why it is an
// acceptable floor; a namespace that will not delete would be a new failure mode.
func TestADeleteThatCannotSucceedStillReleasesTheClaim(t *testing.T) {
	cases := map[string]func(*claimClient, *ClaimEngine){
		// NetBox refusing the delete: a NAT relation or another PROTECT pointing at the
		// address. Retrying cannot clear it, because nothing else is going to be deleted.
		"refused": func(nb *claimClient, _ *ClaimEngine) {
			nb.deleteErr = &netbox.ProtectedError{Status: 409, Body: "protected foreign key"}
		},
		// NetBox unreachable, or answering 500. The claim cannot tell a five-minute restart
		// from a decommissioned server, so the bound is what distinguishes them.
		"unreachable": func(nb *claimClient, _ *ClaimEngine) {
			nb.deleteErr = errors.New("dial tcp: connection refused")
		},
		// The NetBoxEndpoint has stopped being Ready -- its token was rotated, its CR was
		// deleted -- so there is no client to delete through at all.
		"endpointNotReady": func(_ *claimClient, engine *ClaimEngine) {
			engine.Endpoints = fakeEndpoints{ready: false}
		},
	}

	for name, breakIt := range cases {
		t.Run(name, func(t *testing.T) {
			claim, engine, nb := newClaimFixture(t)
			retained := watch(t, metrics.AllocationsRetained, []string{"NetBoxIPAddressClaim"})

			if _, err := engine.Reconcile(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			breakIt(nb, engine)
			claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

			// One more pass than the bound, so a bound that is not enforced shows up as a
			// finalizer still present rather than as a hang.
			for pass := range claimDeleteAttempts + 1 {
				result, err := engine.Reconcile(context.Background(), claim)
				if err != nil {
					t.Fatalf("pass %d returned an error; a delete that cannot succeed is the"+
						" claim's state, not a controller failure: %v", pass, err)
				}

				if len(claim.Finalizers) == 0 {
					break
				}

				if result.RequeueAfter <= 0 {
					t.Fatalf("pass %d kept the finalizer and asked for no requeue, so nothing"+
						" will ever try again", pass)
				}

				// Each pass is meant to be the next attempt. Since #289 that takes the
				// backoff having run out -- which is the whole point of the bound being a
				// bound on *time*: without the rewind these twelve passes are twelve
				// wake-ups in one tick, which is precisely what used to spend the claim's
				// whole allowance before anything could unblock.
				claim.Status.LastDeletionAttempt = rewound(claim.Status.LastDeletionAttempt)
			}

			if len(claim.Finalizers) != 0 {
				t.Fatalf("finalizers = %v after %d passes: the claim is unfinalizable and its"+
					" namespace cannot be deleted", claim.Finalizers, claimDeleteAttempts+1)
			}

			events, _ := engine.Events.(*fakeRecorder)
			if !hasEvent(events, "Warning/"+netboxv1alpha1.EventAddressRetained) {
				t.Errorf("events = %v, want a Warning/%s: giving up leaves the address"+
					" allocated and that is not a routine outcome",
					events.events, netboxv1alpha1.EventAddressRetained)
			}

			if got := retained.delta("NetBoxIPAddressClaim"); got != 1 {
				t.Errorf("allocations_retained moved by %v, want 1: a leak nobody counted is"+
					" a leak nobody can find", got)
			}
		})
	}
}

// TestABlockedClaimDoesNotSpendItsAllowanceOnWakeUps is #289 on the allocation side, and the
// consequence there is worse than a slow teardown: the bound is what decides when the address
// is abandoned, so a count spent on wake-ups rather than on attempts abandons it immediately.
//
// The endpoint case is the one to picture. A NetBoxEndpoint is briefly not Ready -- its token
// is being rotated -- and a claim is deleted in that window. Every pass wrote a status, every
// status write woke the next pass, and the eleven attempts the claim is allowed were gone
// inside a millisecond: the finalizer came off and the address was reported retained, before
// the endpoint had any chance to come back.
func TestABlockedClaimDoesNotSpendItsAllowanceOnWakeUps(t *testing.T) {
	claim, engine, _ := newClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	engine.Endpoints = fakeEndpoints{ready: false}
	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	// Comfortably more passes than the bound allows attempts. Without the hold-off every one
	// of them counted, so this loop alone used to release the finalizer.
	for pass := range claimDeleteAttempts * 3 {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatalf("pass %d = %v", pass, err)
		}
	}

	if claim.Status.DeletionAttempts != 1 {
		t.Errorf("status.deletionAttempts = %d after %d passes, want 1: a wake-up is not an"+
			" attempt", claim.Status.DeletionAttempts, claimDeleteAttempts*3)
	}

	if len(claim.Finalizers) == 0 {
		t.Errorf("the finalizer came off and the address was abandoned inside one tick, which"+
			" is the %d-attempt bound being spent on wake-ups rather than on retries",
			claimDeleteAttempts)
	}
}

// TestARefusedDeleteIsReportedBeforeItIsGivenUpOn checks the middle of that sequence, not just
// its end. A block that is only visible once the operator has stopped trying is a block nobody
// could have intervened in.
func TestARefusedDeleteIsReportedBeforeItIsGivenUpOn(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	nb.deleteErr = &netbox.ProtectedError{Status: 409, Body: "protected foreign key"}
	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	for range protectedEventAfter {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatal(err)
		}

		// One attempt per pass, which takes the interval having run out (#289, deletionHold).
		claim.Status.LastDeletionAttempt = rewound(claim.Status.LastDeletionAttempt)
	}

	if len(claim.Finalizers) == 0 {
		t.Fatalf("the finalizer came off after %d refusals, well before the bound of %d",
			protectedEventAfter, claimDeleteAttempts)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionDeleting); got.Reason !=
		netboxv1alpha1.ReasonProtected || got.Status != metav1.ConditionFalse {
		t.Errorf("Deleting = %s/%s, want False/%s", got.Status, got.Reason,
			netboxv1alpha1.ReasonProtected)
	}

	if claim.Status.DeletionAttempts != protectedEventAfter {
		t.Errorf("status.deletionAttempts = %d, want %d: the backoff and the bound are both"+
			" computed from it, so a count that does not survive a requeue is neither",
			claim.Status.DeletionAttempts, protectedEventAfter)
	}

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Warning/"+netboxv1alpha1.EventDeleteBlocked) {
		t.Errorf("events = %v, want a Warning/%s carrying netbox's own reason",
			events.events, netboxv1alpha1.EventDeleteBlocked)
	}
}

// TestANonWritingEndpointFreesNothing is driftMode Report and mode DryRun on the deletion path.
//
// Both reach the engine as a client that physically cannot mutate NetBox, so what is asserted
// here is that the claim reads the suppressed answer rather than believing the call. A DryRun
// that reported "freed" would be worse than useless: it would say an address is available when
// it is not.
func TestANonWritingEndpointFreesNothing(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	retained := watch(t, metrics.AllocationsRetained, []string{"NetBoxIPAddressClaim"})

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	// The real client in DryRun, so suppression comes from the code that produces it.
	reporting, err := netbox.New(netbox.Config{URL: testURL, Token: "t", Mode: netbox.ModeDryRun})
	if err != nil {
		t.Fatalf("building a non-writing client: %v", err)
	}
	nb.dryRun = reporting

	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.deletes != 0 {
		t.Errorf("%d deletes were counted as sent; a non-writing endpoint sends none", nb.deletes)
	}

	events, _ := engine.Events.(*fakeRecorder)
	if hasEvent(events, "Normal/"+netboxv1alpha1.EventDeleted) {
		t.Errorf("events = %v, want no %s: nothing was deleted",
			events.events, netboxv1alpha1.EventDeleted)
	}

	if !hasEvent(events, "Warning/"+netboxv1alpha1.EventAddressRetained) {
		t.Errorf("events = %v, want a Warning/%s saying the address is still allocated",
			events.events, netboxv1alpha1.EventAddressRetained)
	}

	if got := retained.delta("NetBoxIPAddressClaim"); got != 1 {
		t.Errorf("allocations_retained moved by %v, want 1", got)
	}

	if len(claim.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none: a reporting endpoint must not wedge deletion",
			claim.Finalizers)
	}
}

// TestDeletingAClaimWhoseAddressIsAlreadyGoneReleases: already-freed is the end state the claim
// asked for, reached by somebody else. Treating a 404 as a failure would keep the finalizer on
// forever waiting for a delete that can never succeed, because there is nothing left to delete.
func TestDeletingAClaimWhoseAddressIsAlreadyGoneReleases(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	nb.deleteErr = &netbox.NotFoundError{Endpoint: claimEndpoint, ID: 412}
	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if len(claim.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none", claim.Finalizers)
	}

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Normal/"+netboxv1alpha1.EventDeleted) {
		t.Errorf("events = %v, want a Normal/%s", events.events, netboxv1alpha1.EventDeleted)
	}
}

// TestTheSkipFinalizerAnnotationWorksOnAClaimToo: the break-glass is the same annotation on
// both engines, because a human who has decided to accept an orphan should not have to know
// which engine owns the CR. It overrides the delete rather than being considered after it.
func TestTheSkipFinalizerAnnotationWorksOnAClaimToo(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	before := len(nb.calls)
	claim.Annotations = map[string]string{netboxv1alpha1.SkipFinalizerAnnotation: "true"}
	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if len(nb.calls) != before {
		t.Errorf("the annotation still let netbox be called (%v)", nb.methods())
	}

	events, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(events, "Warning/"+netboxv1alpha1.EventFinalizerSkipped) {
		t.Errorf("events = %v, want a Warning/%s",
			events.events, netboxv1alpha1.EventFinalizerSkipped)
	}

	if len(claim.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none", claim.Finalizers)
	}
}

// TestDeletingAClaimThatNeverAllocatedNeedsNothing keeps the reporting path from crying wolf,
// and keeps the commonest deletion of all -- a claim removed while its pool was still
// unresolved -- from needing an endpoint it never used.
func TestDeletingAClaimThatNeverAllocatedNeedsNothing(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	claim.Finalizers = []string{netboxv1alpha1.Finalizer}
	claim.DeletionTimestamp = &metav1.Time{Time: metav1.Now().Time}
	engine.Endpoints = fakeEndpoints{ready: false}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if len(nb.calls) != 0 {
		t.Errorf("requests = %v, want none", nb.methods())
	}

	events, _ := engine.Events.(*fakeRecorder)
	if hasEvent(events, "Normal/"+netboxv1alpha1.EventAddressRetained) ||
		hasEvent(events, "Warning/"+netboxv1alpha1.EventAddressRetained) {
		t.Errorf("events = %v, want no retention Event: nothing was ever allocated", events.events)
	}

	if len(claim.Finalizers) != 0 {
		t.Errorf("finalizers = %v, want none", claim.Finalizers)
	}
}

// lastCall returns the most recent call of one method, or the zero value.
func lastCall(nb *claimClient, method string) call {
	var out call
	for _, made := range nb.calls {
		if made.method == method {
			out = made
		}
	}

	return out
}

// TestClaimNeverPerformsADeclarativeWrite is a structural assertion rather than a behavioural
// one: the allocation path has no business creating, patching or deleting anything by name.
func TestClaimNeverPerformsADeclarativeWrite(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	for _, made := range nb.methods() {
		if made == "POST" || made == "PATCH" || made == "DELETE" {
			t.Errorf("the allocation path made a %s through the declarative client (%v)",
				made, nb.methods())
		}
	}
}

// --- fixture ---------------------------------------------------------------------------

// newClaimFixture is one claim, one engine and one NetBox, in the state where an allocation
// is about to succeed. Every test above breaks exactly one of those.
func newClaimFixture(t *testing.T) (*netboxv1alpha1.NetBoxIPAddressClaim, *ClaimEngine, *claimClient) {
	t.Helper()

	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	nb := &claimClient{
		url: testURL,
		byEndpoint: map[string]netbox.Object{
			poolEndpoint:  {"prefix": "10.0.20.0/24", "status": map[string]any{"value": "active"}},
			claimEndpoint: allocatedObject(412, "10.0.20.37/24", identity, "uid-1"),
		},
		allocated: allocatedObject(412, "10.0.20.37/24", identity, "uid-1"),
	}

	claim := &netboxv1alpha1.NetBoxIPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "homelab", Name: "dns-eth0", UID: "uid-1", Generation: 1,
		},
		Spec: netboxv1alpha1.NetBoxIPAddressClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
			PrefixRef:       netboxv1alpha1.PrefixRef{Name: "home-lan"},
		},
	}

	engine := &ClaimEngine{
		Claims: fakeClaims{descriptor: claimDescriptor(), registered: true},
		Pools:  fakeDescriptors{descriptor: poolDescriptor(), registered: true},
		Endpoints: fakeEndpoints{ready: true, endpoint: Endpoint{
			Client:     nb,
			Allocator:  nb,
			Provenance: provenance.Stamp{Config: identityConfig(), TagID: 7, Fields: identityFields()},
		}},
		Refs:       &fakePool{id: 11},
		Status:     &fakeStatus{},
		Finalizers: &fakeFinalizers{},
		Events:     &fakeRecorder{},
		Scheme:     claimScheme(t),
	}

	return claim, engine, nb
}

func claimScheme(t *testing.T) *runtime.Scheme {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := netboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("adding the api types to a scheme: %v", err)
	}

	return scheme
}

// claimDescriptor is the registered NetBoxIPAddressClaim descriptor, read from the registry
// rather than rebuilt: a fixture that drifts from the shipped data tests the fixture.
func claimDescriptor() registry.ClaimDescriptor {
	desc, _ := registry.Claim(netboxv1alpha1.GroupVersion.WithKind("NetBoxIPAddressClaim"))

	return desc
}

func poolDescriptor() registry.Descriptor {
	desc, _ := registry.Get(netboxv1alpha1.PrefixRef{}.TargetGVK())

	return desc
}

func identityConfig() provenance.Config {
	return provenance.FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})
}

func identityFields() []string {
	return identityConfig().CustomFieldNames()
}

// allocatedObject is what NetBox returns for an allocated ipam.IPAddress.
func allocatedObject(id int, address, identity, uid string) netbox.Object {
	obj := netbox.Object{
		"id":      float64(id),
		"address": address,
		"url":     fmt.Sprintf("%s/api/ipam/ip-addresses/%d/", testURL, id),
	}

	netbox.SetCustomField(obj, provenance.DefaultAllocationIdentityField, identity)
	netbox.SetCustomField(obj, provenance.DefaultUIDField, uid)

	return obj
}

// claimClient is a NetBox that answers per endpoint and counts allocations.
//
// Per endpoint rather than one canned object, because an allocating pass reads two different
// models -- the pool and the object it allocated -- and a fake that cannot tell them apart
// cannot express "the read-after-write disagreed".
type claimClient struct {
	calls      []call
	byEndpoint map[string]netbox.Object
	getErr     error

	list    []netbox.Object
	listErr error

	allocated       netbox.Object
	allocErr        error
	allocatePayload netbox.Object
	posts           int

	url    string
	dryRun *netbox.Client

	// deleteErr is what the DELETE that frees an allocation answers with. Nil is a clean
	// 204, which is what NetBox sends and why the answer is a nil Object.
	deleteErr error
	deletes   int
}

func (c *claimClient) URL() string { return c.url }

func (c *claimClient) GetByID(_ context.Context, endpoint string, id int) (netbox.Object, error) {
	c.calls = append(c.calls, call{method: "GET", endpoint: endpoint, id: id})

	return c.byEndpoint[endpoint], c.getErr
}

func (c *claimClient) GetOne(_ context.Context, endpoint string, params netbox.Params) (netbox.Object, error) {
	c.calls = append(c.calls, call{method: "GETONE", endpoint: endpoint, params: params})

	if c.listErr != nil {
		return nil, c.listErr
	}

	return netbox.One(endpoint, params, c.list)
}

func (c *claimClient) Allocate(
	ctx context.Context, endpoint string, id int, sub string, payload netbox.Object,
) (netbox.Object, error) {
	c.calls = append(c.calls, call{method: "ALLOCATE", endpoint: endpoint + "/" + sub, id: id, payload: payload})
	c.allocatePayload = payload

	if c.dryRun != nil {
		// Through the real client, so the suppressed shape the engine has to recognise comes
		// from the code that produces it rather than from a copy of its marker.
		return c.dryRun.Create(ctx, endpoint, payload)
	}

	if c.allocErr != nil {
		return nil, c.allocErr
	}

	c.posts++

	return c.allocated, nil
}

// The declarative half of NetBoxClient. Present because Endpoint.Client is the whole
// interface, and failing loudly because the allocation path has no business using it.
func (c *claimClient) Create(_ context.Context, endpoint string, payload netbox.Object) (netbox.Object, error) {
	c.calls = append(c.calls, call{method: "POST", endpoint: endpoint, payload: payload})

	return nil, errors.New("the allocation path must not create by name")
}

func (c *claimClient) Patch(_ context.Context, endpoint string, id int, payload netbox.Object) (netbox.Object, error) {
	c.calls = append(c.calls, call{method: "PATCH", endpoint: endpoint, id: id, payload: payload})

	return nil, errors.New("the allocation path must not patch")
}

// Delete is the one declarative method the allocation path is allowed to use, since #225 made
// a claim free its allocation. It goes through the real client when dryRun is set, so the
// suppressed shape the engine has to recognise comes from the code that produces it rather
// than from a copy of its marker.
func (c *claimClient) Delete(ctx context.Context, endpoint string, id int) (netbox.Object, error) {
	c.calls = append(c.calls, call{method: "DELETE", endpoint: endpoint, id: id})

	if c.dryRun != nil {
		return c.dryRun.Delete(ctx, endpoint, id)
	}

	if c.deleteErr != nil {
		return nil, c.deleteErr
	}

	c.deletes++

	return nil, nil
}

func (c *claimClient) methods() []string {
	out := make([]string, 0, len(c.calls))
	for _, made := range c.calls {
		out = append(out, made.method+" "+made.endpoint)
	}

	return out
}

// fakeClaims serves one claim descriptor.
type fakeClaims struct {
	descriptor registry.ClaimDescriptor
	registered bool
}

func (f fakeClaims) Claim(_ schema.GroupVersionKind) (registry.ClaimDescriptor, bool) {
	return f.descriptor, f.registered
}

// fakePool resolves the pool reference to one id, or refuses.
type fakePool struct {
	id  int64
	err error
}

func (f *fakePool) Resolve(_ context.Context, _ resolver.Request) (resolver.Result, error) {
	if f.err != nil {
		return resolver.Result{}, f.err
	}

	return resolver.Result{ID: f.id, Mode: resolver.ModeName}, nil
}

// --- assertions ------------------------------------------------------------------------

func conditionOfClaim(claim *netboxv1alpha1.NetBoxIPAddressClaim, condType string) metav1.Condition {
	for _, condition := range claim.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

func readyOfClaim(claim *netboxv1alpha1.NetBoxIPAddressClaim) metav1.Condition {
	return conditionOfClaim(claim, netboxv1alpha1.ConditionReady)
}

// assertRefused is the shape every refusal shares: nothing allocated, nothing POSTed, both
// conditions False with the same reason, and an Event.
func assertRefused(
	t *testing.T, claim *netboxv1alpha1.NetBoxIPAddressClaim, nb *claimClient, reason string,
) {
	t.Helper()

	if claim.Status.Address != "" {
		t.Errorf("status.address = %q, want empty", claim.Status.Address)
	}

	if nb.posts != 0 {
		t.Errorf("%d POSTs, want 0", nb.posts)
	}

	if got := readyOfClaim(claim); got.Reason != reason || got.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %s/%s, want False/%s", got.Status, got.Reason, reason)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionAllocated); got.Reason != reason {
		t.Errorf("Allocated reason = %q, want %s", got.Reason, reason)
	}
}

// assertNotAllocated is the shape a deliberate non-allocation shares: Ready=False with the
// reason naming which switch was set, and no address.
func assertNotAllocated(t *testing.T, claim *netboxv1alpha1.NetBoxIPAddressClaim, reason string) {
	t.Helper()

	if claim.Status.Address != "" || claim.Status.NetBoxID != 0 {
		t.Errorf("status recorded %q/%d from a write that was never sent",
			claim.Status.Address, claim.Status.NetBoxID)
	}

	if got := readyOfClaim(claim).Reason; got != reason {
		t.Errorf("Ready reason = %q, want %s", got, reason)
	}
}

func hasEvent(recorder *fakeRecorder, want string) bool {
	for _, got := range recorder.events {
		if got == want {
			return true
		}
	}

	return false
}

// stampedAs stamps a live object as belonging to the named CR in this fixture's cluster, the
// way an allocating POST from that CR would have.
//
// The cluster is the fixture's own rather than a parameter: every caller stamps
// testClusterID, because a *different* cluster is already the ForeignCluster arm that
// internal/provenance's table owns. What the claim engine has to be shown is the arm the
// stamp cannot settle on its own -- one cluster, two CRs.
func stampedAs(obj netbox.Object, owner string) netbox.Object {
	netbox.SetCustomField(obj, provenance.DefaultOwnerField, owner)
	netbox.SetCustomField(obj, provenance.DefaultClusterField, testClusterID)

	return obj
}

// TestClaimRefusesAGivenIdentityOwnedByAnotherCR is the cross-namespace takeover this guard
// exists for.
//
// The identity is the claim engine's entire ownership proof: one custom field is matched and
// the match is adopted. A derived identity contains the claim's own namespace, so nobody can
// compute another namespace's -- but spec.allocationIdentity is a free string on a CR any
// namespace may create, and the value it needs is printed in the victim's own
// status.allocationIdentity. Without this check, a claim in `other-team` naming `homelab`'s
// identity adopts its address, reports it as its own, and deletes the live NetBox object when
// it is deleted, because deletionPolicy defaults to Delete.
//
// Pointing at the same pool is what the attack does, so the ReclaimedOutsidePool guard cannot
// catch it. The assertion is therefore the strong one: no POST, no delete, no address.
func TestClaimRefusesAGivenIdentityOwnedByAnotherCR(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	// The identity `homelab/dns-eth0` derives, typed into a claim that is not it.
	victim := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")
	claim.Namespace, claim.Name, claim.UID = "other-team", "borrowed", "uid-9"
	claim.Spec.AllocationIdentity = victim

	nb.list = []netbox.Object{stampedAs(
		allocatedObject(412, "10.0.20.37/24", victim, "uid-1"),
		"netboxipaddressclaim/homelab/dns-eth0")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a refusal must not be returned as an error: %v", err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonForeignAllocation)

	if nb.deletes != 0 {
		t.Errorf("%d DELETEs, want 0: a refused claim must not touch the object it was refused", nb.deletes)
	}

	// The message has to name the other writer, or the next step -- unset the field, or go and
	// talk to whoever owns that object -- is not one a human can take from the condition.
	if got := readyOfClaim(claim).Message; !strings.Contains(got, "homelab/dns-eth0") {
		t.Errorf("Ready message %q does not name the owning cr", got)
	}
}

// TestClaimRefusesAGivenIdentityOnAnUnstampedObject pins the fail-closed half of the guard,
// and the behaviour change issue #299 charges for it.
//
// #271 let this through: an object with no readable stamp was unattributable rather than
// foreign, so a given identity could adopt a pre-existing NetBox object. That reading is only
// sound if "unstamped" is a fact about the object, and it is not -- it is a fact about the
// field names the *reading* endpoint chose, which for a claim in another namespace is that
// namespace's own configuration. So the same read now refuses.
//
// What it costs is exactly this test's old subject: a migration onto a NetBox object nobody
// stamped. It is refused rather than lost -- no POST, no PATCH, no DELETE -- and the message
// names the custom field to set to hand the object over, which is the same proof of ownership
// the guard would have wanted in the first place.
func TestClaimRefusesAGivenIdentityOnAnUnstampedObject(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	claim.Spec.AllocationIdentity = "carriedoveridentity"

	obj := netbox.Object{
		"id":      float64(412),
		"address": "10.0.20.99/24",
		"url":     fmt.Sprintf("%s/api/ipam/ip-addresses/412/", testURL),
	}
	netbox.SetCustomField(obj, provenance.DefaultAllocationIdentityField, "carriedoveridentity")
	nb.list = []netbox.Object{obj}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a refusal must not be returned as an error: %v", err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonForeignAllocation)

	if nb.deletes != 0 {
		t.Errorf("%d DELETEs, want 0: a refusal touches nothing", nb.deletes)
	}

	// The remedy has to be in the message, or a migration that this refusal blocks has nowhere
	// to go: the field to set, and the value that makes the object this claim's.
	got := readyOfClaim(claim).Message
	if !strings.Contains(got, provenance.DefaultOwnerField) ||
		!strings.Contains(got, "netboxipaddressclaim/homelab/dns-eth0") {
		t.Errorf("Ready message %q does not say which custom field to set to what", got)
	}
}

// TestClaimReclaimsAGivenIdentityOnAnObjectStampedAsItsOwn is the given identity's remaining
// happy path, and the one the refusal above hands people to.
//
// The identity is derived from the endpoint's URL among other things, so moving NetBox behind
// a new hostname re-rolls every claim's identity; spec.allocationIdentity carries the old value
// across, and the owner stamp on the object still names this very claim. Attributable, ours,
// reclaimed -- no POST.
func TestClaimReclaimsAGivenIdentityOnAnObjectStampedAsItsOwn(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	claim.Spec.AllocationIdentity = "carriedoveridentity"

	obj := allocatedObject(412, "10.0.20.99/24", "carriedoveridentity", "uid-1")
	nb.list = []netbox.Object{stampedAs(obj, "netboxipaddressclaim/homelab/dns-eth0")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.posts != 0 {
		t.Errorf("%d POSTs, want 0: an object stamped as this claim's own is reclaimed", nb.posts)
	}
	if claim.Status.Address != "10.0.20.99/24" {
		t.Errorf("status.address = %q, want the reclaimed 10.0.20.99/24", claim.Status.Address)
	}
}

// TestClaimReclaimsADerivedIdentityWhateverTheStampSays keeps the guard off the path it must
// never touch.
//
// A derived identity cannot be forged across namespaces, so a stamp that disagrees with it is
// not a takeover -- it is a renamed owner field, an endpoint whose managedBy changed, or the
// handover the reclaim path has always reported and never judged. Judging it here would break
// a cluster rebuilt from Git, which is the whole reason the identity is deterministic.
func TestClaimReclaimsADerivedIdentityWhateverTheStampSays(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)
	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	nb.list = []netbox.Object{stampedAs(
		allocatedObject(412, "10.0.20.99/24", identity, "uid-0"),
		"netboxipaddressclaim/homelab/renamed-away")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got := conditionOfClaim(claim, netboxv1alpha1.ConditionAllocated); got.Reason !=
		netboxv1alpha1.ReasonReclaimedByIdentity {
		t.Errorf("Allocated reason = %q, want ReclaimedByIdentity: a derived identity is not gated", got.Reason)
	}
	if claim.Status.Address != "10.0.20.99/24" {
		t.Errorf("status.address = %q, want the reclaimed 10.0.20.99/24", claim.Status.Address)
	}
}

// TestClaimRefusesAnAllocationItCannotReadTheStampOf is issue #299: the guard above, aimed
// around rather than through.
//
// refuseForeignReclaim asks provenance.Stamp.Conflict for the verdict, and Conflict reads the
// live object by the field names the *reading* endpoint is configured with. Those names come
// off spec.managedBy of the endpoint the claim refers to -- which, for a claim in another
// namespace, is an endpoint that namespace wrote. Renaming uidField, clusterField and
// ownerField therefore makes every object the endpoint next door stamped read back as
// unstamped, and an unstamped object used to be reclaimable: the guard was switched off by the
// party it guards against.
//
// allocationIdentityField is the one name that may not be renamed, because findByIdentity has
// to keep matching the victim's identity -- and it is the only one that has to agree.
//
// Two endpoints against one NetBox stub, differing in nothing else.
func TestClaimRefusesAnAllocationItCannotReadTheStampOf(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	// homelab's endpoint, on the defaults, and the address it allocated. Stamped by the
	// provenance code itself rather than by hand, so the fixture cannot drift from what this
	// operator actually writes.
	victim := provenance.FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: testClusterID})
	victimStamp := provenance.Stamp{Config: victim, TagID: 7, Fields: victim.CustomFieldNames()}
	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")

	live := netbox.Object{
		"id":      float64(412),
		"address": "10.0.20.37/24",
		"url":     fmt.Sprintf("%s/api/ipam/ip-addresses/412/", testURL),
	}
	netbox.SetCustomField(live, provenance.DefaultAllocationIdentityField, identity)
	victimStamp.Apply(live, nil, provenance.Owner{
		Kind: "NetBoxIPAddressClaim", Namespace: "homelab", Name: "dns-eth0", UID: "uid-1",
	}, provenance.Target{Taggable: true, CustomFields: true})
	nb.list = []netbox.Object{live}

	// other-team's endpoint against that same NetBox: the same allocationIdentityField, and
	// provenance field names of its own choosing.
	attacker := provenance.FromSpec(&netboxv1alpha1.ManagedBy{
		ClusterID:    "other-team",
		UIDField:     "ot_uid",
		ClusterField: "ot_cluster",
		OwnerField:   "ot_owner",
	})
	engine.Endpoints = fakeEndpoints{ready: true, endpoint: Endpoint{
		Client:     nb,
		Allocator:  nb,
		Provenance: provenance.Stamp{Config: attacker, TagID: 8, Fields: attacker.CustomFieldNames()},
	}}

	claim.Namespace, claim.Name, claim.UID = "other-team", "borrowed", "uid-9"
	claim.Spec.AllocationIdentity = identity

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a refusal must not be returned as an error: %v", err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonForeignAllocation)

	if nb.deletes != 0 {
		t.Errorf("%d DELETEs, want 0: a refused claim must not touch the object it was refused", nb.deletes)
	}
}

// TestClaimRefusalNamesTheSpecWhenTheEndpointAttributesNothing is the other end of the same
// change, and the second thing it costs.
//
// An endpoint may have all three stamp fields switched off and still allocate: the identity
// field is the only one a claim needs (docs/operations/provenance.md, "Stamp less"). Such an
// endpoint can attribute no object to any CR, so under the fail-closed guard no given identity
// reclaims anything through it -- and the refusal has to say that the fix is in spec.managedBy
// rather than send somebody looking for a custom field that does not exist.
func TestClaimRefusalNamesTheSpecWhenTheEndpointAttributesNothing(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	// Built as a Config rather than from a spec, because FromSpec defaults an empty name back
	// to the canonical one: this is the resolved state, not a manifest.
	stampless := provenance.Config{
		ClusterID:               testClusterID,
		Tag:                     provenance.DefaultTag,
		AllocationIdentityField: provenance.DefaultAllocationIdentityField,
		Bootstrap:               true,
	}
	engine.Endpoints = fakeEndpoints{ready: true, endpoint: Endpoint{
		Client:     nb,
		Allocator:  nb,
		Provenance: provenance.Stamp{Config: stampless, TagID: 7, Fields: stampless.CustomFieldNames()},
	}}

	claim.Spec.AllocationIdentity = "carriedoveridentity"
	nb.list = []netbox.Object{allocatedObject(412, "10.0.20.99/24", "carriedoveridentity", "uid-1")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a refusal must not be returned as an error: %v", err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonForeignAllocation)

	if got := readyOfClaim(claim).Message; !strings.Contains(got, "spec.managedBy") {
		t.Errorf("Ready message %q does not name spec.managedBy as the thing to fix", got)
	}
}

// TestClaimRenamedOntoItsOldIdentityIsToldHowToTakeIt pins the guard's answer to the one case
// spec.allocationIdentity was added for, and the remedy it now has to carry.
//
// A rename is a new CR: new name, new uid, and a derived identity that no longer matches the
// object. Setting the old identity finds it -- and finds it stamped with the *old* name, which
// #271 already classifies as a foreign owner and refuses. That is not this change (an
// unattributable object was the only kind a given identity could ever adopt, and it is what
// #299 closes), but it is the case a human is most likely to hit, so the refusal has to say
// what to do rather than only "unset the field", which here would allocate a second address.
func TestClaimRenamedOntoItsOldIdentityIsToldHowToTakeIt(t *testing.T) {
	claim, engine, nb := newClaimFixture(t)

	old := AllocationIdentity(testURL, "homelab", "NetBoxIPAddressClaim", "dns-eth0")
	claim.Name, claim.UID = "dns-eth1", "uid-2"
	claim.Spec.AllocationIdentity = old

	nb.list = []netbox.Object{stampedAs(
		allocatedObject(412, "10.0.20.37/24", old, "uid-1"),
		"netboxipaddressclaim/homelab/dns-eth0")}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatalf("a refusal must not be returned as an error: %v", err)
	}

	assertRefused(t, claim, nb, netboxv1alpha1.ReasonForeignAllocation)

	got := readyOfClaim(claim).Message
	if !strings.Contains(got, provenance.DefaultOwnerField) ||
		!strings.Contains(got, "netboxipaddressclaim/homelab/dns-eth1") {
		t.Errorf("Ready message %q does not say how to hand the object to the renamed claim", got)
	}
}
