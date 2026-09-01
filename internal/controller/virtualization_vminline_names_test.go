package controller

import (
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestVirtualMachineChildNamesTrackMetadataName is the rule that keeps a rename in NetBox from
// churning Kubernetes: **the prefix is `metadata.name`, never `spec.name`.**
//
// A Kubernetes name is immutable, so a child name derived from it never changes under a live
// object. Deriving it from the NetBox name instead would mean that renaming a VM in NetBox
// deleted and recreated every child CR beneath it -- and in NetBox every interface, address and
// disk with them, at new ids. The fixture's two names differ on purpose, and the derived names
// follow the Kubernetes one.
func TestVirtualMachineChildNamesTrackMetadataName(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "web-01", func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
		withInlineTree(vm)
		// The NetBox name, which is not the CR's name and must reach no derived name.
		vm.Spec.Name = "web01.example.net"
	})

	eventually(t, "the virtual machine to be Ready with its three children", func() bool {
		return virtualMachineIsReady(ns, "web-01") && len(childrenOf(ns, "web-01")) == 3
	})

	children := childrenOf(ns, "web-01")

	for path, want := range map[string]string{
		"spec.interfaces[eth0]":                          "web-01-eth0",
		"spec.interfaces[eth0].addresses[10.20.0.10/24]": "web-01-eth0-ip-10-20-0-10-24",
		"spec.disks[scsi0]":                              "web-01-disk-scsi0",
	} {
		if got := children[path].Name; got != want {
			t.Errorf("%s materialised %q, want %q derived from metadata.name", path, got, want)
		}
	}

	ready := childrenReadyOf(ns, "web-01")
	if ready.Reason != netboxv1alpha1.ReasonAllReady {
		t.Errorf("ChildrenReady = %s/%s, want True/AllReady: %s", ready.Status, ready.Reason, ready.Message)
	}
}
