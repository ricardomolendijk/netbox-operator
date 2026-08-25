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
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlangroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlangroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlangroups/finalizers,verbs=update

// NetBoxVLANGroup's controller is one line, the same one NetBoxPrefix's is -- and this is the
// kind that had to be built for that to stay true. Its identity is `(scope_type, scope_id,
// slug)`, a natural key over a polymorphic pair, which no Descriptor could state before #180:
// the two column names now appear in the key like any other filter
// (internal/registry/ipam_vlangroup.go), and neither the engine nor this file learned anything
// about scopes to make it work.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVLANGroup{}) }
