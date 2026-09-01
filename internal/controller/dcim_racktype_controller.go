package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxracktypes: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxracktypes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxracktypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxracktypes/finalizers,verbs=update

// NetBoxRackType's controller is one line. The required `manufacturerRef` that both natural
// keys start at, the fallback from `(manufacturer, slug)` to `(manufacturer, model)`, and the
// twelve RackBase dimension columns are all entries on its Descriptor
// (internal/registry/dcim_racktype.go); "write nothing until the manufacturer resolves" is the
// engine reading them (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRackType{}) }
