package reconciler

import (
	"errors"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestDuplicateFlagOnAMaterialisedChildStopsThePass is the reconcile-time backstop for issue
// #167, and it is what keeps the admission webhook's `failurePolicy: Ignore` honest: with the
// webhook down the hand edit is admitted, and the object then refuses to write rather than
// creating a second NetBox row.
//
// Refused *before* the lookup, so the count of NetBox calls is zero either way -- the flag
// changes what the natural key means, and a key the operator cannot trust is not one to act on.
func TestDuplicateFlagOnAMaterialisedChildStopsThePass(t *testing.T) {
	generated := fakeObject()
	generated.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: netboxv1alpha1.GroupVersion.String(),
		Kind:       "NetBoxVirtualMachine",
		Name:       "dns",
		UID:        "8f1cf000-0000-0000-0000-000000000001",
		Controller: boolPtr(true), BlockOwnerDeletion: boolPtr(true),
	}}

	p := &pass{
		obj:  generated,
		desc: registry.Descriptor{DuplicateSpec: "allowDuplicate"},
		spec: specFields{"allowDuplicate": true},
	}

	_, err := p.duplicate(nil, nil)
	if !errors.Is(err, errDuplicateOnGeneratedChild) {
		t.Fatalf("duplicate() = %v, want errDuplicateOnGeneratedChild", err)
	}

	if out := classify(err, testResync); out.reason != netboxv1alpha1.ReasonInvalid {
		t.Errorf("classify() reason = %s, want %s", out.reason, netboxv1alpha1.ReasonInvalid)
	}
}

// TestDuplicateFlagIsUntouchedOnAHandWrittenObject is the negative that makes the rule usable.
// A containment owner reference is *non-controller* (ADR-0003 rule 4) and goes on ordinary
// hand-written CRs, so keying on "any owner reference in this group" would refuse a legitimate
// anycast address the moment its interface moved into the same namespace.
func TestDuplicateFlagIsUntouchedOnAHandWrittenObject(t *testing.T) {
	cases := map[string][]metav1.OwnerReference{
		"no owner at all": nil,
		"a containment owner reference, which is not a controller one": {{
			APIVersion: netboxv1alpha1.GroupVersion.String(),
			Kind:       "NetBoxVMInterface",
			Name:       "eth0",
			UID:        "8f1cf000-0000-0000-0000-000000000002",
		}},
		"a controller reference from outside this api group": {{
			APIVersion: "apps/v1", Kind: "StatefulSet", Name: "db",
			UID: "8f1cf000-0000-0000-0000-000000000003",
			// Controller, and irrelevant: it is not one of ours.
			Controller: boolPtr(true), BlockOwnerDeletion: boolPtr(true),
		}},
	}

	for name, owners := range cases {
		t.Run(name, func(t *testing.T) {
			obj := fakeObject()
			obj.OwnerReferences = owners

			p := &pass{
				obj:  obj,
				desc: registry.Descriptor{DuplicateSpec: "allowDuplicate"},
				spec: specFields{"allowDuplicate": true},
			}

			// stampIdentifies() is false on this fixture -- it carries no provenance stamp --
			// so the *next* guard is the one that answers. Which guard that is, is the
			// assertion: anything but errDuplicateOnGeneratedChild means the object was not
			// treated as the operator's own output.
			if _, err := p.duplicate(nil, nil); errors.Is(err, errDuplicateOnGeneratedChild) {
				t.Errorf("duplicate() treated a hand-written object as materialised: %v", err)
			}
		})
	}
}

// boolPtr is &v for a bool literal, which an owner reference's two flags need.
func boolPtr(v bool) *bool { return &v }
