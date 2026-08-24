package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxclustergroups: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclustergroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclustergroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclustergroups/finalizers,verbs=update

// NetBoxClusterGroup's controller is one line, and the same line as NetBoxClusterType's:
// two kinds that differ only in their descriptor's endpoint need no code that differs at all
// (internal/registry/virtualization_clustergroup.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxClusterGroup{}) }
