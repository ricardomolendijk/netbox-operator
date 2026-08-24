package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxtenants: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenants,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenants/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenants/finalizers,verbs=update

// NetBoxTenant's controller is one line, like NetBoxTag's: tenancy.Tenant's endpoint, its
// two natural-key candidates -- one of which pins `group_id` to null -- and its field map
// are all data on its Descriptor (internal/registry/tenancy_tenant.go), and every create,
// adopt, update and delete decision is the engine's (internal/reconciler). That includes
// the PROTECT-refused delete almost every IPAM kind can cause: it is a *netbox.ProtectedError
// the finalizer already knows how to report.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxTenant{}) }
