package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxsites: the operator reads CRs and writes their status
// and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsites,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsites/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsites/finalizers,verbs=update

// NetBoxSite's controller is one line, the same one NetBoxTag's is, and that is the whole
// claim NBO-009 tests: dcim.Site's endpoint, natural key, field map and comparison rules
// are all data on its Descriptor (internal/registry/dcim_site.go), and every create, adopt,
// update and delete decision is the engine's (internal/reconciler). A choice column and two
// decimals did not change that. This file exists to name the kind and to carry its RBAC,
// because there is nothing else about it that is per-kind.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxSite{}) }
