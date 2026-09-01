package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxasns: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxasns,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxasns/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxasns/finalizers,verbs=update

// NetBoxASN's controller is one line, the same one NetBoxTag's is. The one kind whose
// identity is a number rather than a name or a slug -- no name column, no slug column, and
// a unique 32-bit ASN (internal/registry/ipam_asn.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxASN{}) }
