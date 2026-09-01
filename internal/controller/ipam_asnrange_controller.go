package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxasnranges: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxasnranges,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxasnranges/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxasnranges/finalizers,verbs=update

// NetBoxASNRange's controller is one line, the same one NetBoxTag's is. A span of ASNs with
// a PROTECT-ed tenant, which is the reference that makes a NetBoxTenant refuse to delete
// (internal/registry/ipam_asnrange.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxASNRange{}) }
