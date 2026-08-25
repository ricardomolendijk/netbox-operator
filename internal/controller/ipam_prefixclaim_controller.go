package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxprefixclaims: the operator reads claims and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprefixclaims/finalizers,verbs=update

// NetBoxPrefixClaim's controller is one line, the same one NetBoxIPAddressClaim's is.
// Everything specific to carving a child prefix out of a container -- which advisory-locked
// sub-path, which allocation parameter, which field of the answer is the result, which pool
// states are unexpected -- is data on its ClaimDescriptor
// (internal/registry/claim_ipam_prefix.go). This file exists to name the kind and to carry its
// RBAC.
func init() { registerClaimKind(&netboxv1alpha1.NetBoxPrefixClaim{}) }
