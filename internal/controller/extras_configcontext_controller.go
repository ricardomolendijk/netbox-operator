package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigcontexts,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigcontexts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigcontexts/finalizers,verbs=update

// One line for the kind with thirteen to-many references. Every one of them is a
// ClassRefMany entry on the Descriptor and none of them is visible here
// (internal/registry/extras_configcontext.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxConfigContext{}) }
