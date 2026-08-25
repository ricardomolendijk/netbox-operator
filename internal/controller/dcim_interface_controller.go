package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxinterfaces: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxinterfaces,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxinterfaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxinterfaces/finalizers,verbs=update

// NetBoxInterface's controller is one line, and it is the same line as every other kind's even
// though this is the largest spec in the catalogue: three self-referential foreign keys, seven
// choice columns, a to-many VLAN list and twenty read-only columns are all data on its
// Descriptor (internal/registry/dcim_interface.go). Registering it is also what makes
// `IPAssignment.interfaceRef` resolve, and that took no code either -- the reverse index is
// built from Descriptor.ObjectType.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxInterface{}) }
