package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxfhrpgroups: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxfhrpgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxfhrpgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxfhrpgroups/finalizers,verbs=update

// NetBoxFHRPGroup's controller is one line, the same one NetBoxTag's is. The kind IPAssignment.fhrpGroupRef has pointed at since NBO-025, with two closed choice enums and no auth_key field (internal/registry/ipam_fhrpgroup.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxFHRPGroup{}) }
