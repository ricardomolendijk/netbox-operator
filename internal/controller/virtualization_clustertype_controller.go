package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxclustertypes: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclustertypes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclustertypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclustertypes/finalizers,verbs=update

// NetBoxClusterType's controller is one line. The kind is the smallest one in the catalogue
// -- an OrganizationalModel with no own columns -- so this file is nothing but the kind's name
// and its RBAC (internal/registry/virtualization_clustertype.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxClusterType{}) }
