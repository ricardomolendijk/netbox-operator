package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxmanufacturers: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmanufacturers,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmanufacturers/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmanufacturers/finalizers,verbs=update

// NetBoxManufacturer's controller is one line: dcim.Manufacturer's endpoint, its single
// slug-keyed natural key and its three-field map are all data on its Descriptor
// (internal/registry/dcim_manufacturer.go), and every create, adopt, update and delete
// decision is the engine's (internal/reconciler). That includes the PROTECT-refused delete a
// manufacturer with device types or platforms causes: it is a *netbox.ProtectedError the
// finalizer already knows how to report.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxManufacturer{}) }
