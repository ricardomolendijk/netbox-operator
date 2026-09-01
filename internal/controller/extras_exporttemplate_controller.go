package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxexporttemplates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxexporttemplates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxexporttemplates/finalizers,verbs=update

// One line. That `name` is not unique in NetBox, and that an ambiguous lookup is therefore a
// Conflict rather than a guess, is a property of the Descriptor's natural key
// (internal/registry/extras_exporttemplate.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxExportTemplate{}) }
