package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomlinks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomlinks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomlinks/finalizers,verbs=update

// One line, because everything about extras.CustomLink is data on its Descriptor
// (internal/registry/extras_customlink.go). This file names the kind and carries its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCustomLink{}) }
