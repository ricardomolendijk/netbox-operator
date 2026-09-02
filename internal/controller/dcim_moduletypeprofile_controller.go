package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxmoduletypeprofiles: the operator reads CRs and writes
// their status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmoduletypeprofiles,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmoduletypeprofiles/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxmoduletypeprofiles/finalizers,verbs=update

// NetBoxModuleTypeProfile's controller is one line. Its endpoint, its single `name` candidate
// -- hand-declared from the column-level UNIQUE, because the model has no meta.constraints for
// the extractor to read -- and the JSON `schema` column's comparison rule are all data on its
// Descriptor (internal/registry/dcim_moduletypeprofile.go), and every create, adopt, update and
// delete decision is the engine's (internal/reconciler).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxModuleTypeProfile{}) }
