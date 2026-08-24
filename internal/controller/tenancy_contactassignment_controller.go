package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcontactassignments: the operator reads CRs and writes
// their status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactassignments,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactassignments/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactassignments/finalizers,verbs=update

// NetBoxContactAssignment's controller is one line too, and that is the claim this kind tests
// hardest. It is the widest generic-FK union in the catalogue, the first `REQ` pair to ship,
// the first identity built from a polymorphic pair *and* two ordinary references, and the first
// containment parent that is a union on a Kind made of nothing but references -- and every one
// of those is data on the Descriptor (internal/registry/tenancy_contactassignment.go). The
// resolver dispatches on the union member's JSON name through GenericFKSpec.Members, and the
// owner reference is decided per pass from the member the object resolved through, so neither
// internal/resolver nor internal/reconciler needed a line for it.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxContactAssignment{}) }
