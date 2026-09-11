package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxprovidernetworks: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprovidernetworks,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprovidernetworks/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprovidernetworks/finalizers,verbs=update

// NetBoxProviderNetwork's controller is one line. Its `(provider, name)` candidate and its
// field map are data on its Descriptor (internal/registry/circuits_providernetwork.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxProviderNetwork{}) }
