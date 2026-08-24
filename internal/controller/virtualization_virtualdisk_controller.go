package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvirtualdisks: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualdisks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualdisks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualdisks/finalizers,verbs=update

// NetBoxVirtualDisk's controller is one line, and its Descriptor is the smallest in the
// registry (internal/registry/virtualization_virtualdisk.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVirtualDisk{}) }
