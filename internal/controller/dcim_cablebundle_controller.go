package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcablebundles: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcablebundles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcablebundles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcablebundles/finalizers,verbs=update

// NetBoxCableBundle's controller is one line. The kind is a PrimaryModel with one column of
// its own, so this file is nothing but the kind's name and its RBAC
// (internal/registry/dcim_cablebundle.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCableBundle{}) }
