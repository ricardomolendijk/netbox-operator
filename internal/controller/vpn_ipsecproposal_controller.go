package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxipsecproposals: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecproposals,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecproposals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecproposals/finalizers,verbs=update

// NetBoxIPSecProposal's controller is one line. Its endpoint, its single `name` candidate and
// its field map are data on its Descriptor (internal/registry/vpn_ipsecproposal.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIPSecProposal{}) }
