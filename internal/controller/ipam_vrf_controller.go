package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvrfs: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvrfs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvrfs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvrfs/finalizers,verbs=update

// NetBoxVRF's controller is one line too, and that is this ticket's real claim: the first
// kind with two to-many references, an ordered pair of natural keys and a fallback key that
// is not unique needed no engine code at all. Every one of those is data on its Descriptor
// (internal/registry/ipam_vrf.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVRF{}) }
