package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxipranges: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipranges,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipranges/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxipranges/finalizers,verbs=update

// NetBoxIPRange's controller is one line, the same one NetBoxPrefix's is. The kind whose
// natural key is a three-column tuple with a pinned null and whose `size` column must never be
// written is still entirely data on its Descriptor (internal/registry/ipam_iprange.go). This
// file exists to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxIPRange{}) }
