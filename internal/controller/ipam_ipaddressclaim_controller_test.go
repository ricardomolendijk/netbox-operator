package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
	claim := makeClaim(t, ns, "dns-eth0")

	claim.Spec.PrefixRef = netboxv1alpha1.PrefixRef{Name: "other-lan"}

	err := k8sClient.Update(context.Background(), claim)
	if err == nil {
		t.Fatal("the api server accepted a repointed prefixRef; a claim allocates once, so this" +
			" has to be rejected at admission")
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
	claim := makeClaim(t, ns, "dns-eth0")

	claim.Spec.AllocationIdentity = "9f2c41b7ae05d813"
	if err := k8sClient.Update(context.Background(), claim); err != nil {
		t.Fatalf("setting allocationIdentity on a claim that had none: %v", err)
	}

	claim.Spec.AllocationIdentity = "0000000000000000"
	if err := k8sClient.Update(context.Background(), claim); err == nil {
		t.Fatal("the api server accepted a changed allocationIdentity")
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

// makeClaim creates a claim and returns it as stored, so a test can edit the object the API
// server actually has.
func makeClaim(t *testing.T, ns, name string) *netboxv1alpha1.NetBoxIPAddressClaim {
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

	return claim
}

func readyCondition(conditions []metav1.Condition) *metav1.Condition {
	for i := range conditions {
		if conditions[i].Type == netboxv1alpha1.ConditionReady {
			return &conditions[i]
		}
	}

	return nil
}
