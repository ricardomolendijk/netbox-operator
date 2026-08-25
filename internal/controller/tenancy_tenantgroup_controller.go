package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// create and delete are here for one caller: the child materialiser, which brings an inline
// entry into existence as a real CR and deletes it when the entry goes (NBO-032). The verbs
// are on every object kind rather than on the ones that are children today, because which
// kinds a parent declares is per-kind data and a list here would be a second place to
// remember it. Nothing else in the operator creates or deletes a CR, and the spec of one it
// did not create is still off limits (ADR-0005 §1, internal/controller/specguard.go).
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenantgroups,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenantgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxtenantgroups/finalizers,verbs=update

// NetBoxTenantGroup's controller is one line, like NetBoxTag's: tenancy.TenantGroup's
// endpoint, its single natural key, its deferred self-reference and its read-only MPTT
// caches are all data on its Descriptor (internal/registry/tenancy_tenantgroup.go), and
// every create, adopt, update and delete decision is the engine's (internal/reconciler).
// Being the second NestedGroupModel with a different identity from the first changed
// nothing here -- the identity is data too.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxTenantGroup{}) }
