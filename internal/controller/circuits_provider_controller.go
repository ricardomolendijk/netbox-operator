package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxproviders: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxproviders,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxproviders/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxproviders/finalizers,verbs=update

// NetBoxProvider's controller is one line. Its endpoint, its single `slug` candidate, its
// `asns` to-many reference and its field map are data on its Descriptor
// (internal/registry/circuits_provider.go), and every create, adopt, update and delete decision
// is the engine's (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxProvider{}) }
