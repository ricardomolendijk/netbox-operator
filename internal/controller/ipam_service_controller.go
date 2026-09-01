package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxservices: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxservices,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxservices/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxservices/finalizers,verbs=update

// NetBoxService's controller is one line, the same one NetBoxTag's is. A polymorphic pair,
// a many-to-many and an ordered array on one object, all three as data
// (internal/registry/ipam_service.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxService{}) }
