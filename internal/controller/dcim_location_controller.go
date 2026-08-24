package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxlocations: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxlocations,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxlocations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxlocations/finalizers,verbs=update

// NetBoxLocation's controller is one line like every other kind's, and it is the interesting
// case: this kind has a required reference, a self-referential one, a choice column and a
// containment parent, and none of those four is code. They are entries on its Descriptor
// (internal/registry/dcim_location.go), and the engine (internal/reconciler) reads them
// without knowing which kind it is holding.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxLocation{}) }
