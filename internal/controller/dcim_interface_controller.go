package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Create and delete on netboxinterfaces, unlike almost every other kind: this is one of the
// two the operator *materialises* from a NetBoxDevice's inline `interfaces` list (NBO-034,
// ADR-0003 rule 5), so the materialiser has to be able to bring one into existence and to
// prune one whose inline entry is gone. The grant is here, in the child kind's own file,
// because that is where childWriter's contract puts it: controller-gen accepts no wildcard
// resource, so the alternative is a hand-maintained list of every materialisable kind next to
// the writer, which goes stale the first time a kind is added
// (internal/controller/objectcontroller.go).
//
// It does not widen what the operator may write to a *spec*: specGuard admits a spec write
// only for an object carrying a controller owner reference the operator set, and the
// materialiser refuses to write to a name it does not own at all
// (internal/reconciler/children.go).
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxinterfaces,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxinterfaces/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxinterfaces/finalizers,verbs=update

// NetBoxInterface's controller is one line, and it is the same line as every other kind's even
// though this is the largest spec in the catalogue: three self-referential foreign keys, seven
// choice columns, a to-many VLAN list and twenty read-only columns are all data on its
// Descriptor (internal/registry/dcim_interface.go). Registering it is also what makes
// `IPAssignment.interfaceRef` resolve, and that took no code either -- the reverse index is
// built from Descriptor.ObjectType.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxInterface{}) }
