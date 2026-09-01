package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxfhrpgroupassignments: the operator reads CRs and
// writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxfhrpgroupassignments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxfhrpgroupassignments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxfhrpgroupassignments/finalizers,verbs=update

// NetBoxFHRPGroupAssignment's controller is one line, the same one NetBoxTag's is. A bare
// ChangeLoggedModel: no tags, no custom fields, no provenance stamp, a generic-FK pair in
// its natural key and a CASCADE-ed containment parent -- still one line
// (internal/registry/ipam_fhrpgroupassignment.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxFHRPGroupAssignment{}) }
