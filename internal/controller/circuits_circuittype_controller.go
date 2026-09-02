package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcircuittypes: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcircuittypes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcircuittypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcircuittypes/finalizers,verbs=update

// NetBoxCircuitType's controller is one line. Its endpoint, its single `slug` candidate and its
// field map are data on its Descriptor (internal/registry/circuits_circuittype.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCircuitType{}) }
