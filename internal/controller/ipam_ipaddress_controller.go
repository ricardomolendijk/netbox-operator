package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxipaddresses: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddresses,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddresses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddresses/finalizers,verbs=update

// NetBoxIPAddress's controller is one line, the same one NetBoxTag's and NetBoxSite's are,
// and this is the kind that makes the claim worth something: a polymorphic foreign key, a
// self-referential one, an identity with no database constraint behind it and a duplicate
// rule of its own are all data on its Descriptor (internal/registry/ipam_ipaddress.go).
// This file exists to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIPAddress{}) }
