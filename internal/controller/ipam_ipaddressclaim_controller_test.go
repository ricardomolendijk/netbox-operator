package controller

import (
	"context"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// These are the claim facts only a real API server can prove. Everything about *how* an
// allocation happens is unit-tested against a fake NetBox in internal/reconciler, where the
// POST count is an assertion; what is left is the half of the contract the CRD carries, and
// the CRD is only enforced by the thing that stores the object.

// TestClaimPoolSelectionIsImmutable is the immutability of ADR-0004 as a contract rather than
// as controller logic.
//
// A CEL rule on the field is a better contract than a controller comparing spec against
// status after the fact: the API server rejects the edit, so there is no window in which a
// claim's spec and its allocated address disagree. It matters because "point this claim at a
// different prefix" reads like a small edit and would mean handing out an address from
// somewhere else while the old one stays allocated in NetBox forever.
func TestClaimPoolSelectionIsImmutable(t *testing.T) {
	ns := newNamespace(t)
	makeClaim(t, ns, "dns-eth0")

	err := updateClaim(t, ns, "dns-eth0", func(claim *netboxv1alpha1.NetBoxIPAddressClaim) {
		claim.Spec.PrefixRef = netboxv1alpha1.PrefixRef{Name: "other-lan"}
	})
	if err == nil {
		t.Fatal("the api server accepted a repointed prefixRef; a claim allocates once, so this" +
			" has to be rejected at admission")
	}

	// Invalid rather than any error at all: a Conflict from the controller writing status
	// underneath the test would otherwise pass this assertion while proving nothing.
	if !apierrors.IsInvalid(err) {
		t.Fatalf("rejection is %T (%v), want an admission rejection", err, err)
	}

	if !strings.Contains(err.Error(), "immutable") {
		t.Errorf("rejection %q does not say the field is immutable", err)
	}
}

// TestClaimAllocationIdentityIsAddableAndNotChangeable is the exact shape the escape hatch
// needs, and it is not "immutable" flatly.
//
// Adding it has to work: it is how an address is carried across a rename, which the derived
// identity cannot survive by construction. Changing it must not: an identity that moves is a
// claim pointed at somebody else's address.
func TestClaimAllocationIdentityIsAddableAndNotChangeable(t *testing.T) {
	ns := newNamespace(t)
	makeClaim(t, ns, "dns-eth0")

	if err := updateClaim(t, ns, "dns-eth0", func(claim *netboxv1alpha1.NetBoxIPAddressClaim) {
		claim.Spec.AllocationIdentity = "9f2c41b7ae05d813"
	}); err != nil {
		t.Fatalf("setting allocationIdentity on a claim that had none: %v", err)
	}

	err := updateClaim(t, ns, "dns-eth0", func(claim *netboxv1alpha1.NetBoxIPAddressClaim) {
		claim.Spec.AllocationIdentity = "0000000000000000"
	})
	if !apierrors.IsInvalid(err) {
		t.Fatalf("changing allocationIdentity gave %v, want an admission rejection", err)
	}
}

// TestClaimRejectsAMalformedAllocationIdentity keeps a value that could never match anything
// in NetBox out of the API.
//
// The identity is lowercase hex, and it is compared for equality against a custom field. An
// uppercase or punctuated value is not a typo the operator can report usefully at runtime: it
// simply matches nothing, allocates a fresh address, and looks like it worked.
func TestClaimRejectsAMalformedAllocationIdentity(t *testing.T) {
	ns := newNamespace(t)

	for _, identity := range []string{"9F2C41B7AE05D813", "9f2c-41b7", "not hex!"} {
		claim := &netboxv1alpha1.NetBoxIPAddressClaim{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bad-identity"},
			Spec: netboxv1alpha1.NetBoxIPAddressClaimSpec{
				NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{
					EndpointRef: "homelab", AllocationIdentity: identity,
				},
				PrefixRef: netboxv1alpha1.PrefixRef{Name: "home-lan"},
			},
		}

		if err := k8sClient.Create(context.Background(), claim); err == nil {
			t.Errorf("the api server accepted allocationIdentity %q", identity)
			_ = k8sClient.Delete(context.Background(), claim)
		}
	}
}

// TestClaimRequiresAPool and its sibling below cover the two fields with no useful default.
//
// A claim with no pool cannot be reported on usefully -- there is nothing to be exhausted or
// not allocatable -- so it is rejected rather than accepted and left Ready=False forever.
func TestClaimRequiresAPool(t *testing.T) {
	ns := newNamespace(t)

	claim := &netboxv1alpha1.NetBoxIPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "no-pool"},
		Spec: netboxv1alpha1.NetBoxIPAddressClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
		},
	}

	if err := k8sClient.Create(context.Background(), claim); err == nil {
		t.Error("the api server accepted a claim with no prefixRef")
	}
}

// TestClaimRequiresAnEndpoint: there is no cluster-wide default endpoint, so an omitted
// reference cannot be resolved into one.
func TestClaimRequiresAnEndpoint(t *testing.T) {
	ns := newNamespace(t)

	claim := &netboxv1alpha1.NetBoxIPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "no-endpoint"},
		Spec: netboxv1alpha1.NetBoxIPAddressClaimSpec{
			PrefixRef: netboxv1alpha1.PrefixRef{Name: "home-lan"},
		},
	}

	if err := k8sClient.Create(context.Background(), claim); err == nil {
		t.Error("the api server accepted a claim with no endpointRef")
	}
}

// TestClaimWaitsForItsEndpointWithoutAllocating is the whole controller path against a real
// API server, in the one state that needs no NetBox at all.
//
// It proves the wiring rather than the algorithm: the CRD is installed, the controller is
// registered by SetupClaimControllers, the claim is picked up, and the status the allocation
// engine writes is accepted by the API server -- which is the part a fake status writer
// cannot check.
func TestClaimWaitsForItsEndpointWithoutAllocating(t *testing.T) {
	ns := newNamespace(t)
	makeClaim(t, ns, "waiting")

	eventually(t, "Ready=False with WaitingForEndpoint", func() bool {
		claim := &netboxv1alpha1.NetBoxIPAddressClaim{}
		if err := k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: "waiting"}, claim); err != nil {
			return false
		}

		ready := readyCondition(claim.Status.Conditions)

		return ready != nil && ready.Reason == netboxv1alpha1.ReasonWaitingForEndpoint &&
			claim.Status.Address == "" && claim.Status.ObservedGeneration == claim.Generation
	})
}

// makeClaim creates a claim and waits for it to be readable.
//
// It returns nothing on purpose: every caller that edits the claim goes through updateClaim,
// which re-reads it. Handing back the object the Create was built from is how a test ends up
// updating a stale copy and asserting against the Conflict that follows.
func makeClaim(t *testing.T, ns, name string) {
	t.Helper()

	claim := &netboxv1alpha1.NetBoxIPAddressClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
		Spec: netboxv1alpha1.NetBoxIPAddressClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
			PrefixRef:       netboxv1alpha1.PrefixRef{Name: "home-lan"},
		},
	}
	if err := k8sClient.Create(context.Background(), claim); err != nil {
		t.Fatalf("creating the claim: %v", err)
	}

	// k8sClient reads through the manager's cache, and a Create goes straight to the API
	// server, so the object is not visible to the next Get for a few milliseconds. Waiting
	// here rather than in each caller: a NotFound read is indistinguishable from a rejection
	// at the call site, which is exactly how an admission assertion passes while proving
	// nothing.
	eventually(t, "the claim is visible through the cache", func() bool {
		return k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: name},
			&netboxv1alpha1.NetBoxIPAddressClaim{}) == nil
	})

	t.Cleanup(func() {
		stored := &netboxv1alpha1.NetBoxIPAddressClaim{}
		if err := k8sClient.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, stored); err != nil {
			return
		}

		// The claim takes a finalizer before it touches NetBox and drops it on its own
		// deletion pass, which needs no NetBox call -- so this cleanup completes even though
		// no NetBox exists in this test.
		_ = k8sClient.Delete(context.Background(), stored)
	})
}

// updateClaim mutates the stored claim and returns what the API server said.
//
// It re-reads before every attempt and retries a Conflict, because the claim's own controller
// is writing its status and its finalizer at the same time. Without that, a test asserting an
// admission rejection can pass on a Conflict instead -- proving nothing, intermittently.
func updateClaim(
	t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxIPAddressClaim),
) error {
	t.Helper()

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		claim := &netboxv1alpha1.NetBoxIPAddressClaim{}
		if err := k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: name}, claim); err != nil {
			return err
		}

		mutate(claim)

		return k8sClient.Update(context.Background(), claim)
	})
}

func readyCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == netboxv1alpha1.ConditionReady {
			return &conditions[i]
		}
	}

	return nil
}
