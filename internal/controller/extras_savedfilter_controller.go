package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsavedfilters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsavedfilters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsavedfilters/finalizers,verbs=update

// One line. The two natural-key candidates that make a slug rename work are data on the
// Descriptor (internal/registry/extras_savedfilter.go), not logic here.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxSavedFilter{}) }
