package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcontacts: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontacts,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontacts/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontacts/finalizers,verbs=update

// NetBoxContact's controller is one line. The two things that make this kind unusual -- a
// lookup key no constraint backs, and a group relationship that is a many-to-many rather than
// a foreign key -- are a NaturalKey and a FieldClass on the Descriptor
// (internal/registry/tenancy_contact.go), so neither reaches this file.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxContact{}) }
