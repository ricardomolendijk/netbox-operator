package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxservicetemplates: the operator reads CRs and writes
// their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxservicetemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxservicetemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxservicetemplates/finalizers,verbs=update

// NetBoxServiceTemplate's controller is one line, the same one NetBoxTag's is. ipam.Service
// minus its parent, with a database-backed identity where the other has a convention
// (internal/registry/ipam_servicetemplate.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxServiceTemplate{}) }
