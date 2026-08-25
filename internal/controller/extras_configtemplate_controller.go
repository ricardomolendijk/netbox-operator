package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigtemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigtemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxconfigtemplates/finalizers,verbs=update

// One line. That this is the first kind stamped with a tag and no custom fields is two
// booleans on the Descriptor (internal/registry/extras_configtemplate.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxConfigTemplate{}) }
