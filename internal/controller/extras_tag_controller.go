package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxtags: the operator reads CRs and writes their status
// and finalizers, and nothing else. Inline children (NBO-032) are the first thing that
// will need create, and they can ask for it then.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtags,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtags/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtags/finalizers,verbs=update

// NetBoxTag's controller is one line, and that is the deliverable: extras.Tag's endpoint,
// natural key, field map and comparison rules are all data on its Descriptor
// (internal/registry/extras_tag.go), and every create, adopt, update and delete decision
// is the engine's (internal/reconciler). This file exists to name the kind and to carry
// its RBAC, because there is nothing else about it that is per-kind.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxTag{}) }
