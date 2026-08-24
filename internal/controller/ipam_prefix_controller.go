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
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixes,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixes/finalizers,verbs=update

// NetBoxPrefix's controller is one line, the same one NetBoxTag's and NetBoxSite's are, and
// that is the whole claim NBO-024 tests. The kind NetBox's scope change broke in
// netbox-populator -- a polymorphic foreign key, three ordinary foreign keys, two tri-state
// booleans, a two-candidate natural key with a pinned null and six read-only columns -- is
// still entirely data on its Descriptor (internal/registry/ipam_prefix.go). This file exists
// to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxPrefix{}) }
