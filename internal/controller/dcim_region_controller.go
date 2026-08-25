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
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxregions,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxregions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxregions/finalizers,verbs=update

// NetBoxRegion's controller is one line, like NetBoxTag's: dcim.Region's endpoint, its two
// natural-key candidates, its field map and its read-only columns are all data on its
// Descriptor (internal/registry/dcim_region.go), and every create, adopt, update and
// delete decision is the engine's (internal/reconciler). That holds even though this is
// the first kind with a self-referential reference -- the reference is data too.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRegion{}) }
