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
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvrfs,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvrfs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvrfs/finalizers,verbs=update

// NetBoxVRF's controller is one line too, and that is this ticket's real claim: the first
// kind with two to-many references, an ordered pair of natural keys and a fallback key that
// is not unique needed no engine code at all. Every one of those is data on its Descriptor
// (internal/registry/ipam_vrf.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVRF{}) }
