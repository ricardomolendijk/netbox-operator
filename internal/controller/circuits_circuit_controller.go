package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcircuits: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcircuits,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcircuits/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcircuits/finalizers,verbs=update

// NetBoxCircuit's controller is one line. The single `(provider, cid)` candidate, the four
// references, the two nullable dates and the read-only `termination_a`/`termination_z` pair are
// all Descriptor data (internal/registry/circuits_circuit.go), so no engine change was needed
// and this file carries no logic.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCircuit{}) }
