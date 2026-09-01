package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxaggregates: the operator reads CRs and writes their status and
// finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxaggregates,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxaggregates/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxaggregates/finalizers,verbs=update

// NetBoxAggregate's controller is one line, the same one NetBoxTag's is. A kind with no
// uniqueness constraint at all, whose (prefix, rir) lookup is a convention and whose
// ambiguous match is a Conflict (internal/registry/ipam_aggregate.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxAggregate{}) }
