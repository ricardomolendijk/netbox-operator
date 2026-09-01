package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// `create` and `delete` on netboxvminterfaces, unlike most kinds, and it is not this
// controller that needs them: a NetBoxVirtualMachine's `spec.interfaces` materialises these as
// owned children, so the materialiser applies and prunes them (NBO-033). The verbs go on this
// Kind's own marker rather than on the VM's, because controller-gen silently drops
// `resources=*` and the alternative is one group rule carrying a hand-maintained list of every
// materialisable Kind -- exactly the thing that goes stale when a Kind is added.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvminterfaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvminterfaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvminterfaces/finalizers,verbs=update

// NetBoxVMInterface's controller is one line. Registering the kind is also what makes
// `IPAssignment.vmInterfaceRef` resolve for the first time -- through
// registry.ByObjectType, not through anything here
// (internal/registry/virtualization_vminterface.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVMInterface{}) }
