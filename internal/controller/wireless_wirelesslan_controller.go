package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxwirelesslans: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslans,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslans/finalizers,verbs=update

// NetBoxWirelessLAN's controller is one line, the same one NetBoxPrefix's and NetBoxCluster's
// are. The fourth kind on CachedScopeMixin adds one call to registry.ScopeFK and a four-entry
// cascade table (internal/registry/wireless_wirelesslan.go); the scope union, the content-type
// spellings, the cache list and the paired drift are already written down once.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxWirelessLAN{}) }
