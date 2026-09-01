package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcables: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcables,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcables/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcables/finalizers,verbs=update

// NetBoxCable's controller is one line, which is the claim NBO-049 was the test of: the kind
// whose update is *destructive* and whose identity is a to-many polymorphic pair needs no
// controller code, because both facts are data on its Descriptor
// (internal/registry/dcim_cable.go, `UpdateRecreate` and `GenericFKList`).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCable{}) }
