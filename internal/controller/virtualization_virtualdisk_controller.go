package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// `create` and `delete` on netboxvirtualdisks, because a NetBoxVirtualMachine's `spec.disks`
// materialises these as owned children (NBO-033). See the note on
// virtualization_vminterface_controller.go for why the verbs live on each materialisable
// Kind's own marker rather than on the parent's.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualdisks,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualdisks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualdisks/finalizers,verbs=update

// NetBoxVirtualDisk's controller is one line, and its Descriptor is the smallest in the
// registry (internal/registry/virtualization_virtualdisk.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVirtualDisk{}) }
