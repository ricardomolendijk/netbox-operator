package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxclusters: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclusters,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclusters/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxclusters/finalizers,verbs=update

// NetBoxCluster's controller is one line, the same one NetBoxPrefix's is, and that is what
// NBO-028 tests. The second kind NetBox's 4.2 scope change broke in netbox-populator -- a
// polymorphic foreign key with four caches that must never be written, three ordinary foreign
// keys one of which is required, a choice column, and a two-candidate natural key with a
// pinned null -- is entirely data on its Descriptor
// (internal/registry/virtualization_cluster.go). This file names the kind and carries its
// RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxCluster{}) }
