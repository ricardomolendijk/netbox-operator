package v1alpha1

import (
	"path/filepath"
	"testing"
)

// vmCRD is the generated NetBoxVirtualMachine CRD, which is where both assertions below read
// the shape the API server will actually enforce.
var vmCRD = filepath.Join("..", "..", "config", "crd", "bases",
	"netbox.kubeforge.org_netboxvirtualmachines.yaml")

// TestVMInlineListBoundsAreTheDocumentedOnes reads the four bounds off the generated CRD.
//
// A kubebuilder marker cannot read a Go constant, so each number is a literal in a doc comment
// and the arithmetic that justifies it is in the prose beside it. This is what stops the two
// from drifting: a bound raised without redoing the cost multiplication fails here, next to the
// sentence that says why the product matters.
//
// The interface and disk bounds are statements about the world -- 32 is past every hypervisor's
// own vNIC limit. The two nested ones are statements about CEL cost, which the API server
// charges at the *product* of both levels' maxima.
func TestVMInlineListBoundsAreTheDocumentedOnes(t *testing.T) {
	t.Parallel()

	nodes := schemaNodes(t, vmCRD)

	for at, want := range map[string]float64{
		"v1alpha1.spec.interfaces":               32,
		"v1alpha1.spec.interfaces[].taggedVLANs": 32,
		"v1alpha1.spec.interfaces[].addresses":   16,
		"v1alpha1.spec.disks":                    32,
	} {
		node, found := nodes[at]
		if !found {
			t.Errorf("%s is not in the generated schema", at)

			continue
		}

		if got := node["maxItems"]; got != want {
			t.Errorf("%s has maxItems %v, want %v (see the cost arithmetic in "+
				"api/v1alpha1/virtualization_virtualmachine_inline.go)", at, got, want)
		}
	}
}

// TestVMInlineListsRejectADuplicateKeyWhereTheyCan is the split the two list shapes force.
//
// `interfaces` and `disks` are map lists keyed on `name`, so the API server rejects a duplicate
// for nothing -- where the obvious CEL rule is quadratic in the bound. `addresses` cannot be
// one: an entry's key is its `address` when it states one and its pool when it says
// `claimFrom`, so there is no single property to key on, and the materialiser's collision check
// is what catches a duplicate there instead (`ChildrenReady=False, Reason=Conflict`, and
// nothing written at all).
func TestVMInlineListsRejectADuplicateKeyWhereTheyCan(t *testing.T) {
	t.Parallel()

	nodes := schemaNodes(t, vmCRD)

	for at, key := range map[string]string{
		"v1alpha1.spec.interfaces": "name",
		"v1alpha1.spec.disks":      "name",
	} {
		node, found := nodes[at]
		if !found {
			t.Fatalf("%s is not in the generated schema", at)
		}

		if got := node["x-kubernetes-list-type"]; got != "map" {
			t.Errorf("%s has list type %v, want map: a duplicate key would then be a Conflict "+
				"discovered at reconcile rather than a rejection at apply", at, got)
		}

		keys, _ := node["x-kubernetes-list-map-keys"].([]any)
		if len(keys) != 1 || keys[0] != key {
			t.Errorf("%s is keyed on %v, want [%s]", at, keys, key)
		}
	}

	addresses, found := nodes["v1alpha1.spec.interfaces[].addresses"]
	if !found {
		t.Fatal("spec.interfaces[].addresses is not in the generated schema")
	}

	if got := addresses["x-kubernetes-list-type"]; got != "atomic" {
		t.Errorf("addresses has list type %v, want atomic: an entry that says claimFrom has no "+
			"`address` for a map key to read", got)
	}
}
