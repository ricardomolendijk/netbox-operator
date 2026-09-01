package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Create and delete on netboxipaddresses, unlike almost every other kind: this is the second
// kind the operator *materialises*, from the `addresses` nested under a NetBoxDevice's inline
// interfaces (NBO-034, ADR-0003 rule 5). The grant belongs in the child kind's own file for
// the reason NetBoxInterface's does -- see internal/controller/dcim_interface_controller.go --
// and it does not widen what the operator may write to a spec.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddresses,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddresses/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipaddresses/finalizers,verbs=update

// NetBoxIPAddress's controller is one line, the same one NetBoxTag's and NetBoxSite's are,
// and this is the kind that makes the claim worth something: a polymorphic foreign key, a
// self-referential one, an identity with no database constraint behind it and a duplicate
// rule of its own are all data on its Descriptor (internal/registry/ipam_ipaddress.go).
// This file exists to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIPAddress{}) }
