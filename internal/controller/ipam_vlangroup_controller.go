package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvlangroups: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlangroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlangroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlangroups/finalizers,verbs=update

// NetBoxVLANGroup's controller is one line, the same one NetBoxPrefix's is -- and this is the
// kind that had to be built for that to stay true. Its identity is `(scope_type, scope_id,
// slug)`, a natural key over a polymorphic pair, which no Descriptor could state before #180:
// the two column names now appear in the key like any other filter
// (internal/registry/ipam_vlangroup.go), and neither the engine nor this file learned anything
// about scopes to make it work.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVLANGroup{}) }
