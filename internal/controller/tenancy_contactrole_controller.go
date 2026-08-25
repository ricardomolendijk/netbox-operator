package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcontactroles: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactroles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactroles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactroles/finalizers,verbs=update

// NetBoxContactRole's controller is one line, like NetBoxClusterType's: an OrganizationalModel
// with no columns of its own, keyed on a globally unique `slug` and holding no references at
// all (internal/registry/tenancy_contactrole.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxContactRole{}) }
