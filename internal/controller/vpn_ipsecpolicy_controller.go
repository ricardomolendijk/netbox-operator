package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxipsecpolicies: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecpolicies,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecpolicies/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecpolicies/finalizers,verbs=update

// NetBoxIPSecPolicy's controller is one line. Its endpoint, its single `name` candidate, its
// to-many `proposals` relation and its field map are data on its Descriptor
// (internal/registry/vpn_ipsecpolicy.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIPSecPolicy{}) }
