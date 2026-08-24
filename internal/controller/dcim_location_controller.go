package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// create and delete are here for one caller: the child materialiser, which brings an inline
// entry into existence as a real CR and deletes it when the entry goes (NBO-032). The verbs
// are on every object kind rather than on the ones that are children today, because which
// kinds a parent declares is per-kind data and a list here would be a second place to
// remember it. Nothing else in the operator creates or deletes a CR, and the spec of one it
// did not create is still off limits (ADR-0005 §1, internal/controller/specguard.go).
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxlocations,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxlocations/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxlocations/finalizers,verbs=update

// NetBoxLocation's controller is one line like every other kind's, and it is the interesting
// case: this kind has a required reference, a self-referential one, a choice column and a
// containment parent, and none of those four is code. They are entries on its Descriptor
// (internal/registry/dcim_location.go), and the engine (internal/reconciler) reads them
// without knowing which kind it is holding.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxLocation{}) }
