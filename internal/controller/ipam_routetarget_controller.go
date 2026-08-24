package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxroutetargets: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxroutetargets,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxroutetargets/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxroutetargets/finalizers,verbs=update

// NetBoxRouteTarget's controller is one line, like every other kind's: the endpoint, the
// natural key and the field map are data on its Descriptor
// (internal/registry/ipam_routetarget.go), and being the far end of two many-to-many
// relations adds nothing here -- those are declared and written on ipam.VRF.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRouteTarget{}) }
