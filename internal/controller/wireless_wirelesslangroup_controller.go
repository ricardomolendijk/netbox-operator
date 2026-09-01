package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxwirelesslangroups: the operator reads CRs and writes
// their status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslangroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslangroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxwirelesslangroups/finalizers,verbs=update

// NetBoxWirelessLANGroup's controller is one line, the same one NetBoxTenantGroup's is --
// which is the whole claim of the third NestedGroupModel: the constraint lines differ, the
// natural key differs, and the code does not (internal/registry/wireless_wirelesslangroup.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxWirelessLANGroup{}) }
