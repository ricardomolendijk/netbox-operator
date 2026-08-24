package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxdevicetypes: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevicetypes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevicetypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevicetypes/finalizers,verbs=update

// NetBoxDeviceType's controller is one line, like NetBoxLocation's, and for the same reason:
// "no candidate is applicable until the required reference resolves, so write nothing" is a
// property of the Descriptor's natural keys (internal/registry/dcim_devicetype.go), not a
// branch anywhere here. The decimal `u_height`, the two choice columns and the eleven
// CounterCacheFields are equally data.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxDeviceType{}) }
