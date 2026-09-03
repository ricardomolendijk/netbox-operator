package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxtunnels: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtunnels,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtunnels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtunnels/finalizers,verbs=update

// NetBoxTunnel's controller is one line. The conditional identity -- `(group_id, name)` when a
// group is named and `name` with `?group_id=null` when none is -- is two NaturalKey entries on
// its Descriptor (internal/registry/vpn_tunnel.go), and choosing between them is
// NaturalKey.Applicable's job rather than this file's. No logic here.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxTunnel{}) }
