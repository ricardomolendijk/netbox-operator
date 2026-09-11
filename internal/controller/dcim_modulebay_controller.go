package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxmodulebays: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmodulebays,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmodulebays/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmodulebays/finalizers,verbs=update

// NetBoxModuleBay's controller is one line. The three-column constraint, the null-pinned
// `(device, name)` fallback for a chassis bay, the device owner reference and the read-only
// MPTT `parent` are all Descriptor data (internal/registry/dcim_modulebay.go), so this file
// carries no logic.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxModuleBay{}) }
