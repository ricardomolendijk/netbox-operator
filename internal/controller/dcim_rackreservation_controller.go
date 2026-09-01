package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxrackreservations: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrackreservations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrackreservations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrackreservations/finalizers,verbs=update

// NetBoxRackReservation's controller is one line. The `units` array compared order-sensitively,
// the required `rackRef` that is also the containment parent, and the identity NetBox does not
// enforce are entries on its Descriptor (internal/registry/dcim_rackreservation.go); the owner
// reference that makes `kubectl delete netboxrack` collect the reservations is
// internal/reconciler reading ContainmentRef.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRackReservation{}) }
