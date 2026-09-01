package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxmacaddresses: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmacaddresses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmacaddresses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmacaddresses/finalizers,verbs=update

// NetBoxMACAddress's controller is one line, and the second polymorphic kind to prove that:
// the narrowed `assignedObject` union, the natural key over its two column names and the
// containment owner reference chosen per resolved member are all descriptor data
// (internal/registry/dcim_macaddress.go). Neither the engine, the resolver nor this file
// learned anything about MAC addresses to make it work.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxMACAddress{}) }
