package admission

import (
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// yes is the address of a true, for an owner reference's two flags.
var yes = true

// generatedAddress is a NetBoxIPAddress as the materialiser writes one: a controller owner
// reference naming the parent that built it.
func generatedAddress(ns, name string, mutate ...func(*netboxv1alpha1.NetBoxIPAddress)) *netboxv1alpha1.NetBoxIPAddress {
	address := &netboxv1alpha1.NetBoxIPAddress{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: ns, Name: name,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: netboxv1alpha1.GroupVersion.String(),
				Kind:       "NetBoxVirtualMachine",
				Name:       "dns",
				UID:        "8f1cf000-0000-0000-0000-000000000001",
				Controller: &yes, BlockOwnerDeletion: &yes,
			}},
		},
		Spec: netboxv1alpha1.NetBoxIPAddressSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: endpointName},
			Address:          "10.20.0.10/24",
		},
	}

	for _, m := range mutate {
		m(address)
	}

	return address
}

// TestDuplicateFlagOnAGeneratedChildIsDenied is issue #167 refused at admission.
//
// `spec.allowDuplicate` makes the provenance stamp part of an address's identity, so a stamped
// object that loses `status.id` matches nothing it can claim and the engine's create-if-absent
// step allocates a *second* address. A materialised child is the object most exposed to that,
// because it is re-created from an unchanged manifest by design -- which is the whole point of
// deriving its name deterministically.
//
// Neither half of this can be CEL: which spec field declares duplicates is
// `Descriptor.DuplicateSpec`, which no schema sees, and whether the operator generated the
// object is a controller owner reference, while a CRD rule at the root sees only
// `metadata.name`.
func TestDuplicateFlagOnAGeneratedChildIsDenied(t *testing.T) {
	ns := newNamespace(t)

	message := refuses(t, generatedAddress(ns, "dns-eth0-ip-10-20-0-10-24",
		func(a *netboxv1alpha1.NetBoxIPAddress) { a.Spec.AllowDuplicate = true }))

	for _, want := range []string{
		"spec.allowDuplicate", "NetBoxVirtualMachine dns", "issue #167",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("rejection message %q does not mention %q", message, want)
		}
	}
}

// TestDuplicateFlagIsAdmittedWhereItIsDeliberate holds the negative, which is the whole reason
// the flag exists: an anycast or VRRP address that a human declared is unaffected. The rule
// keys on the *controller* owner reference, which a hand-written CR does not have.
func TestDuplicateFlagIsAdmittedWhereItIsDeliberate(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, generatedAddress(ns, "vrrp-vip", func(a *netboxv1alpha1.NetBoxIPAddress) {
		a.Spec.AllowDuplicate = true
		a.OwnerReferences = nil
	}))
}

// TestAGeneratedChildWithoutTheFlagIsAdmitted is the other negative, and it is the one that
// matters most in practice: every materialised address in the cluster takes this path on every
// apply, so a rule that refused them all would break the feature it protects.
func TestAGeneratedChildWithoutTheFlagIsAdmitted(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, generatedAddress(ns, "dns-eth0-ip-10-20-0-11-24",
		func(a *netboxv1alpha1.NetBoxIPAddress) { a.Spec.Address = "10.20.0.11/24" }))
}

// TestContainmentOwnershipDoesNotCountAsGenerated is the distinction the whole rule rests on.
// ADR-0003 has the operator set two kinds of owner reference: a *controller* one on a child it
// materialised, and a *non-controller* one on a hand-written CR whose containment parent
// happens to be in the same namespace. Only the first means "the operator created this", and
// treating the second as generated would refuse a legitimate anycast address the moment its
// interface was in the same namespace.
func TestContainmentOwnershipDoesNotCountAsGenerated(t *testing.T) {
	ns := newNamespace(t)

	mustCreate(t, generatedAddress(ns, "anycast-vip", func(a *netboxv1alpha1.NetBoxIPAddress) {
		a.Spec.Address = "10.20.0.12/24"
		a.Spec.AllowDuplicate = true
		a.OwnerReferences[0].Controller = nil
		a.OwnerReferences[0].BlockOwnerDeletion = nil
	}))
}
