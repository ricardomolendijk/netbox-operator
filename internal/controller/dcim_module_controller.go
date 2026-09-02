package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxmodules: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmodules,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmodules/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmodules/finalizers,verbs=update

// NetBoxModule's controller is one line. The one-to-one bay identity, the bay owner reference
// and the three required references are all Descriptor data
// (internal/registry/dcim_module.go), and "one module per bay" is enforced by NetBox's own
// unique index rather than by anything here (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxModule{}) }
