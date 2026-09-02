package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxipsecprofiles: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecprofiles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipsecprofiles/finalizers,verbs=update

// NetBoxIPSecProfile's controller is one line. Its two required references, its single `name`
// candidate and its field map are data on its Descriptor
// (internal/registry/vpn_ipsecprofile.go); waiting for a reference that does not resolve yet
// is the engine's (internal/resolver, internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIPSecProfile{}) }
