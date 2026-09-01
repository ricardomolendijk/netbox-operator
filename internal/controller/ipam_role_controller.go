package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxroles: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxroles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxroles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxroles/finalizers,verbs=update

// NetBoxRole's controller is one line, the same one NetBoxTag's is. The Kind RoleRef has
// pointed at since NBO-024, and not dcim.DeviceRole: separate models, separate endpoints,
// separate aliases (internal/registry/ipam_role.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRole{}) }
