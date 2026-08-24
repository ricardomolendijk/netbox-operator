package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/util/retry"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// The half of NBO-064's contract that only a real API server can prove. Everything about *how*
// a prefix is carved or a range is placed is unit-tested against a fake NetBox in
// internal/reconciler and internal/netbox, where the POST count is an assertion; what is left
// is what the CRD promises, and a CRD is only enforced by the thing that stores the object.

// TestPrefixClaimRequestIsImmutable is ADR-0004's immutability for the two fields that
// together *are* the request.
//
// Both matter for the same reason and in the same way: a claim allocates once, so an edit to
// either would leave the spec describing a request the status cannot be an answer to. Rejecting
// it at admission means there is no window in which the two disagree.
func TestPrefixClaimRequestIsImmutable(t *testing.T) {
	ns := newNamespace(t)
	claim := &netboxv1alpha1.NetBoxPrefixClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "tenant-a-net"},
		Spec: netboxv1alpha1.NetBoxPrefixClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
			ParentPrefixRef: netboxv1alpha1.PrefixRef{Name: "container-10-0"},
			PrefixLength:    26,
		},
	}

	if err := k8sClient.Create(context.Background(), claim); err != nil {
		t.Fatalf("creating the claim: %v", err)
	}

	// k8sClient reads through the manager's cache and a Create goes straight to the API
	// server, so the object is not visible to the next Get for a few milliseconds -- and a
	// NotFound is indistinguishable from a rejection at the assertion below, which is exactly
	// how an admission test passes while proving nothing.
	eventually(t, "the prefix claim is visible through the cache", func() bool {
		return k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: "tenant-a-net"},
			&netboxv1alpha1.NetBoxPrefixClaim{}) == nil
	})

	// Through a retry-on-conflict re-read, because the claim's own controller writes a
	// finalizer and a status underneath this test: a stale-object Conflict would pass an
	// "expected an error" assertion while proving nothing about admission.
	edits := map[string]func(*netboxv1alpha1.NetBoxPrefixClaim){
		"parentPrefixRef": func(c *netboxv1alpha1.NetBoxPrefixClaim) {
			c.Spec.ParentPrefixRef = netboxv1alpha1.PrefixRef{Name: "container-10-1"}
		},
		"prefixLength": func(c *netboxv1alpha1.NetBoxPrefixClaim) { c.Spec.PrefixLength = 27 },
	}

	for field, edit := range edits {
		t.Run(field, func(t *testing.T) {
			if err := updatePrefixClaim(t, ns, "tenant-a-net", edit); !apierrors.IsInvalid(err) {
				t.Errorf("editing %s gave %v, want an admission rejection", field, err)
			}
		})
	}
}

// updatePrefixClaim re-reads and edits one claim, retrying the Conflict its own controller
// causes by writing status.
func updatePrefixClaim(
	t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxPrefixClaim),
) error {
	t.Helper()

	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		claim := &netboxv1alpha1.NetBoxPrefixClaim{}
		if err := k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: name}, claim); err != nil {
			return err
		}

		mutate(claim)

		return k8sClient.Update(context.Background(), claim)
	})
}

// TestPrefixClaimBoundsThePrefixLengthStatically is deliberately the *weak* half of the check,
// and the test says so.
//
// The CRD can only bound what CEL can see, and CEL cannot see the parent: `4..128` covers both
// families because the family is the parent's. A /64 out of an IPv4 container and a /16 out of
// a /16 are both accepted here and refused by the controller with the two numbers named, which
// is why encoding half the rule as CEL would be worse than encoding none of it -- it would read
// as if it were the whole rule.
func TestPrefixClaimBoundsThePrefixLengthStatically(t *testing.T) {
	ns := newNamespace(t)

	for name, length := range map[string]int32{"zero": 0, "too long": 129} {
		t.Run(name, func(t *testing.T) {
			claim := &netboxv1alpha1.NetBoxPrefixClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bad-" + name},
				Spec: netboxv1alpha1.NetBoxPrefixClaimSpec{
					NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
					ParentPrefixRef: netboxv1alpha1.PrefixRef{Name: "container-10-0"},
					PrefixLength:    length,
				},
			}

			if err := k8sClient.Create(context.Background(), claim); err == nil {
				t.Errorf("the api server accepted prefixLength %d", length)
			}
		})
	}
}

// TestRangeClaimDefaultsTheAlignmentAndCapsTheSize covers the two fields that are this kind's
// own.
//
// The default is asserted because it is a promise about behaviour: `Any` packs, and a manifest
// that says nothing gets the packing rather than an empty string the client would have to
// interpret. The cap is asserted because `size` is the one field a typo turns into a range
// nobody meant -- `65536` and `655360` differ by a keystroke.
func TestRangeClaimDefaultsTheAlignmentAndCapsTheSize(t *testing.T) {
	ns := newNamespace(t)

	claim := &netboxv1alpha1.NetBoxIPRangeClaim{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "dhcp-pool"},
		Spec: netboxv1alpha1.NetBoxIPRangeClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
			ParentPrefixRef: netboxv1alpha1.PrefixRef{Name: "home-lan"},
			Size:            64,
		},
	}

	if err := k8sClient.Create(context.Background(), claim); err != nil {
		t.Fatalf("creating the claim: %v", err)
	}

	eventually(t, "the range claim is visible through the cache", func() bool {
		return k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: "dhcp-pool"},
			&netboxv1alpha1.NetBoxIPRangeClaim{}) == nil
	})

	if claim.Spec.Alignment != netboxv1alpha1.RangeAlignmentAny {
		t.Errorf("alignment defaulted to %q, want Any", claim.Spec.Alignment)
	}

	err := retry.RetryOnConflict(retry.DefaultRetry, func() error {
		edited := &netboxv1alpha1.NetBoxIPRangeClaim{}
		if err := k8sClient.Get(context.Background(),
			types.NamespacedName{Namespace: ns, Name: "dhcp-pool"}, edited); err != nil {
			return err
		}

		edited.Spec.Size = 32

		return k8sClient.Update(context.Background(), edited)
	})
	if !apierrors.IsInvalid(err) {
		t.Errorf("editing size gave %v, want an admission rejection", err)
	}

	for name, size := range map[string]int32{"zero": 0, "past the cap": 65537} {
		t.Run(name, func(t *testing.T) {
			bad := &netboxv1alpha1.NetBoxIPRangeClaim{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bad-" + name},
				Spec: netboxv1alpha1.NetBoxIPRangeClaimSpec{
					NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
					ParentPrefixRef: netboxv1alpha1.PrefixRef{Name: "home-lan"},
					Size:            size,
				},
			}

			if err := k8sClient.Create(context.Background(), bad); err == nil {
				t.Errorf("the api server accepted size %d", size)
			}
		})
	}
}

// TestIPRangeRejectsAnAddressWithoutAMask is the CEL rule that keeps a value NetBox would
// mis-store out of the API.
//
// Both endpoints carry a mask, and the two masks have to match: `IPRange.clean()` rejects a
// mismatched pair, and NetBox's own UI writes the containing prefix's length. A bare
// `10.0.30.128` is not a NetBox address column value, and accepting it here would turn a
// one-line rejection into a 400 with a field name nobody wrote.
func TestIPRangeRejectsAnAddressWithoutAMask(t *testing.T) {
	ns := newNamespace(t)

	cases := map[string][2]string{
		"no mask on the start": {"10.0.30.128", "10.0.30.191/24"},
		"no mask on the end":   {"10.0.30.128/24", "10.0.30.191"},
		"not an address":       {"dhcp-pool", "10.0.30.191/24"},
	}

	for name, pair := range cases {
		t.Run(name, func(t *testing.T) {
			rng := &netboxv1alpha1.NetBoxIPRange{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "bad-range"},
				Spec: netboxv1alpha1.NetBoxIPRangeSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					StartAddress:     pair[0],
					EndAddress:       pair[1],
				},
			}

			if err := k8sClient.Create(context.Background(), rng); err == nil {
				t.Errorf("the api server accepted %v", pair)
				_ = k8sClient.Delete(context.Background(), rng)
			}
		})
	}
}

// TestIPRangeAcceptsAnAddressInsideItsPrefix is the other direction, and it is the assertion
// that stops the rule above from being tightened into nonsense.
//
// `10.0.30.128/24` has host bits set and is *correct*: it is an address inside 10.0.30.0/24,
// which is exactly what NetBox stores. The masked-form rule NetBoxPrefix.prefix carries would
// reject it, which is why this kind does not carry that rule.
func TestIPRangeAcceptsAnAddressInsideItsPrefix(t *testing.T) {
	ns := newNamespace(t)

	rng := &netboxv1alpha1.NetBoxIPRange{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: "dhcp-clients"},
		Spec: netboxv1alpha1.NetBoxIPRangeSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			StartAddress:     "10.0.30.128/24",
			EndAddress:       "10.0.30.191/24",
		},
	}

	if err := k8sClient.Create(context.Background(), rng); err != nil {
		t.Fatalf("an address with host bits set was rejected: %v", err)
	}

	// The range's own controller takes a finalizer before it touches NetBox and drops it on
	// its own deletion pass, so this cleanup completes without a NetBox behind it.
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), rng) })

	if rng.Spec.Status != netboxv1alpha1.IPRangeStatusActive {
		t.Errorf("status defaulted to %q, want active", rng.Spec.Status)
	}
}
