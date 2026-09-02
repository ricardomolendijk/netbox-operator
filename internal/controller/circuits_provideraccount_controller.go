package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxprovideraccounts: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprovideraccounts,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprovideraccounts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxprovideraccounts/finalizers,verbs=update

// NetBoxProviderAccount's controller is one line. Its `(provider, account)` candidate, and the
// second constraint it deliberately does not carry, are data on its Descriptor
// (internal/registry/circuits_provideraccount.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxProviderAccount{}) }
