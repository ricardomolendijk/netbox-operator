package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomfields,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomfields/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcustomfields/finalizers,verbs=update

// NetBoxCustomField's controller is one line, like every other kind's, and that is the
// deliverable even for the kind whose NetBox objects the operator also writes itself: the
// reservation and the data-loss guard are both declarations on its Descriptor
// (internal/registry/extras_customfield.go, ReservedKeySpec and DataLossOnDelete), read by
// the engine's own guard clauses. Nothing about this kind is a branch on Kind, and nothing
// about it is per-kind code.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCustomField{}) }
