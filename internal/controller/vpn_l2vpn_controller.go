package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxl2vpns: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxl2vpns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxl2vpns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxl2vpns/finalizers,verbs=update

// NetBoxL2VPN's controller is one line. Its two route-target relations are ClassRefMany
// entries on its Descriptor (internal/registry/vpn_l2vpn.go); the set diff, the sorted id list
// and the order-independent comparison are the engine's and are shared with ipam.VRF unchanged.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxL2VPN{}) }
