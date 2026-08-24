package controller

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Neither create nor delete on netboxsitegroups: the operator reads CRs and writes their
// status and finalizers, and nothing else.
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsitegroups,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsitegroups/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=netbox.kubeforge.org,resources=netboxsitegroups/finalizers,verbs=update

// NetBoxSiteGroup's controller is one line, like NetBoxRegion's: dcim.SiteGroup's endpoint,
// its two natural-key candidates, its field map and its read-only columns are all data on
// its Descriptor (internal/registry/dcim_sitegroup.go), and every create, adopt, update and
// delete decision is the engine's (internal/reconciler). A second self-referential kind
// needed no engine change at all, which is the claim this file is evidence for.
func init() { registerObjectKind(&netboxv1alpha1.NetBoxSiteGroup{}) }
