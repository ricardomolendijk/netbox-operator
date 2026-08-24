package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxregions: the operator reads CRs and writes their
// status and finalizers, and nothing else. Inline children (NBO-032) are the first thing
// that will need create, and they can ask for it then.
// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxregions,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxregions/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.populator.io,resources=netboxregions/finalizers,verbs=update

// NetBoxRegion's controller is one line, like NetBoxTag's: dcim.Region's endpoint, its two
// natural-key candidates, its field map and its read-only columns are all data on its
// Descriptor (internal/registry/dcim_region.go), and every create, adopt, update and
// delete decision is the engine's (internal/reconciler). That holds even though this is
// the first kind with a self-referential reference -- the reference is data too.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRegion{}) }
