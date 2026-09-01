package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigcontextprofiles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigcontextprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigcontextprofiles/finalizers,verbs=update

// One line. That this is the only kind in `extras` carrying a whole provenance stamp is two
// booleans on the Descriptor (internal/registry/extras_configcontextprofile.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxConfigContextProfile{}) }
