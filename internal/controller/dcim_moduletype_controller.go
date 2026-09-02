package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxmoduletypes: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmoduletypes,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmoduletypes/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmoduletypes/finalizers,verbs=update

// NetBoxModuleType's controller is one line. The required `manufacturerRef` the only natural
// key starts at, the `attributes` column's rename from `attribute_data` and its JSON
// comparison rule, and the nine read-only counters are all entries on its Descriptor
// (internal/registry/dcim_moduletype.go); "write nothing until the manufacturer resolves" is
// the engine reading them (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxModuleType{}) }
