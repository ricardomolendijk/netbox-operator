package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxdeviceroles: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdeviceroles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdeviceroles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxdeviceroles/finalizers,verbs=update

// NetBoxDeviceRole's controller is one line, like NetBoxRegion's: dcim.DeviceRole's endpoint,
// its two natural-key candidates -- one of which pins `parent_id` to null -- and its field map
// are all data on its Descriptor (internal/registry/dcim_devicerole.go), and every create,
// adopt, update and delete decision is the engine's (internal/reconciler). The self-referential
// `parentRef`, its cycle check and its owner reference are engine behaviour too, and needed no
// code here.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxDeviceRole{}) }
