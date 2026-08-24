package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxcontactgroups: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactgroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactgroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxcontactgroups/finalizers,verbs=update

// NetBoxContactGroup's controller is one line. Being the third NestedGroupModel with a third
// distinct identity -- `(parent, name)` with no conditional variant behind the null-pinned
// candidate -- changed nothing here: the constraint, the pin and its column class are all data
// on the Descriptor (internal/registry/tenancy_contactgroup.go).
func init() { registerObjectKind(&netboxv1alpha1.NetBoxContactGroup{}) }
