package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxikeproposals: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxikeproposals,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxikeproposals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxikeproposals/finalizers,verbs=update

// NetBoxIKEProposal's controller is one line. Its endpoint, its single `name` candidate and
// its field map are data on its Descriptor (internal/registry/vpn_ikeproposal.go), and every
// create, adopt, update and delete decision is the engine's (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIKEProposal{}) }
