package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxiprangeclaims: the operator reads claims and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxiprangeclaims,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxiprangeclaims/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxiprangeclaims/finalizers,verbs=update

// NetBoxIPRangeClaim's controller is one line, and that is the claim this ticket makes: the
// kind whose allocation NetBox does not serialise needs no controller of its own, no second
// engine and no branch in the first one. What is different about it -- placement computed
// client-side, an overlap rejection retried, contention reported apart from exhaustion -- is
// data on its ClaimDescriptor (internal/registry/claim_ipam_iprange.go) and one guard clause in
// the client (netbox.PlaceRange). This file exists to name the kind and to carry its RBAC.
func init() { registerClaimKind(&netboxv1alpha1.NetBoxIPRangeClaim{}) }
