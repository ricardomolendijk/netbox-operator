package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxprefixes: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixes/finalizers,verbs=update

// NetBoxPrefix's controller is one line, the same one NetBoxTag's and NetBoxSite's are, and
// that is the whole claim NBO-024 tests. The kind NetBox's scope change broke in
// netbox-populator -- a polymorphic foreign key, three ordinary foreign keys, two tri-state
// booleans, a two-candidate natural key with a pinned null and six read-only columns -- is
// still entirely data on its Descriptor (internal/registry/ipam_prefix.go). This file exists
// to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxPrefix{}) }
