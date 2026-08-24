package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxplatforms: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxplatforms,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxplatforms/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxplatforms/finalizers,verbs=update

// NetBoxPlatform's controller is one line, like NetBoxTenantGroup's: dcim.Platform's identity
// is keyed on `manufacturer` rather than on `parent`, and its `parent` is therefore deferred --
// both are data on its Descriptor (internal/registry/dcim_platform.go), and every create,
// adopt, update and delete decision is the engine's (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxPlatform{}) }
