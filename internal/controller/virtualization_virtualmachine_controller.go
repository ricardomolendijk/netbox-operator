package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvirtualmachines: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualmachines,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualmachines/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvirtualmachines/finalizers,verbs=update

// NetBoxVirtualMachine's controller is one line, the same one NetBoxTag's is, and that is
// the claim NBO-029 tests hardest. The kind with four conditional UniqueConstraints, a
// case-insensitive lookup, seven foreign keys, two unconditionally deferred one-to-ones and
// two counter caches is still entirely data on its Descriptor
// (internal/registry/virtualization_virtualmachine.go). This file exists to name the kind and
// to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVirtualMachine{}) }
