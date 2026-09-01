package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxdevices: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevices,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdevices/finalizers,verbs=update

// The first InlineParent in the catalogue, and its controller did not change to become one
// (NBO-034). A NetBoxDevice's `spec.interfaces` materialises NetBoxInterface children and
// NetBoxIPAddress grandchildren, and the whole of the operator's knowledge of that is one
// method next to the spec struct (api/v1alpha1/dcim_device_inline.go): the engine asks every
// object it reconciles whether it implements InlineParent, so there is no registration here,
// no watch to add, and nothing in this file that says "device" twice.
//
// The RBAC the materialiser needs -- create and delete on the two child kinds -- is on those
// kinds' own controller files, not here. A parent granting itself rights over another kind
// would be a second place to keep the list of materialisable kinds
// (internal/controller/objectcontroller.go, childWriter).
//
// NetBoxDevice's controller is one line, and that is the claim NBO-030 tests hardest so far:
// dcim.Device has a three-candidate natural key of which the first comes from a column-level
// unique rather than from meta.constraints, two case-insensitive lookups, three unconditionally
// deferred one-to-ones and no containment parent at all -- and every one of those is data on
// its Descriptor (internal/registry/dcim_device.go). No file under internal/reconciler changed
// for it. This file exists to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxDevice{}) }
