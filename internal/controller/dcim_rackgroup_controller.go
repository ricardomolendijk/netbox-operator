package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxrackgroups: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrackgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrackgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxrackgroups/finalizers,verbs=update

// NetBoxRackGroup's controller is one line, and the interesting part is what is *not* here: no
// tree walk and no cycle check, because dcim.RackGroup is an OrganizationalModel with no
// `parent` column at all (internal/registry/dcim_rackgroup.go). Had it been the
// NestedGroupModel its name suggests, the self-reference, its cycle check and its owner
// reference would still have been engine behaviour and this file would read the same.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxRackGroup{}) }
