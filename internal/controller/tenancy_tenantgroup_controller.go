package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxtenantgroups: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenantgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenantgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenantgroups/finalizers,verbs=update

// NetBoxTenantGroup's controller is one line, like NetBoxTag's: tenancy.TenantGroup's
// endpoint, its single natural key, its deferred self-reference and its read-only MPTT
// caches are all data on its Descriptor (internal/registry/tenancy_tenantgroup.go), and
// every create, adopt, update and delete decision is the engine's (internal/reconciler).
// Being the second NestedGroupModel with a different identity from the first changed
// nothing here -- the identity is data too.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxTenantGroup{}) }
