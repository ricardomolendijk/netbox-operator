package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxvlans: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlans,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlans/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxvlans/finalizers,verbs=update

// NetBoxVLAN's controller is one line, the same one NetBoxTag's and NetBoxPrefix's are. The
// kind that writes `site` as a real foreign key while the kind next to it must never write it
// at all -- five ordinary foreign keys, one of them deferred and self-referencing, a
// three-candidate natural key with two pinned nulls, and two choice columns -- is still
// entirely data on its Descriptor (internal/registry/ipam_vlan.go). This file exists to name
// the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxVLAN{}) }
