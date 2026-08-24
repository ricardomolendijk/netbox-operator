package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxdevices: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevices,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevices/finalizers,verbs=update

// NetBoxDevice's controller is one line, and that is the claim NBO-030 tests hardest so far:
// dcim.Device has a three-candidate natural key of which the first comes from a column-level
// unique rather than from meta.constraints, two case-insensitive lookups, three unconditionally
// deferred one-to-ones and no containment parent at all -- and every one of those is data on
// its Descriptor (internal/registry/dcim_device.go). No file under internal/reconciler changed
// for it. This file exists to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxDevice{}) }
