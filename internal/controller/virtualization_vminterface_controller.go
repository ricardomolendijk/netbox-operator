package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvminterfaces: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvminterfaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvminterfaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvminterfaces/finalizers,verbs=update

// NetBoxVMInterface's controller is one line. Registering the kind is also what makes
// `IPAssignment.vmInterfaceRef` resolve for the first time -- through
// registry.ByObjectType, not through anything here
// (internal/registry/virtualization_vminterface.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVMInterface{}) }
