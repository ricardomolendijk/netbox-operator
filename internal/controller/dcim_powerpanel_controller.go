package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxpowerpanels: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxpowerpanels,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxpowerpanels/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxpowerpanels/finalizers,verbs=update

// NetBoxPowerPanel's controller is one line. Its `(site, name)` identity is a single
// natural-key candidate on its Descriptor (internal/registry/dcim_powerpanel.go), and waiting
// for `siteRef` before writing anything is the engine's behaviour for a reference in a
// natural key -- shared with every other kind whose identity reads one.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxPowerPanel{}) }
