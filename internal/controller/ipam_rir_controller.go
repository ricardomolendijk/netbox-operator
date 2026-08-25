package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxrirs: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrirs,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrirs/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrirs/finalizers,verbs=update

// NetBoxRIR's controller is one line, the same one NetBoxTag's is. An OrganizationalModel with one column of its own, the root of the allocation registry three other kinds require -- all of it data on its Descriptor (internal/registry/ipam_rir.go). This file exists to name the kind and to carry its RBAC.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRIR{}) }
