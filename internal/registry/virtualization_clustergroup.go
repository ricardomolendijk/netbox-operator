package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(virtualizationClusterGroupDescriptor()) }

// virtualizationClusterGroupDescriptor is virtualization.ClusterGroup as data.
//
// Identical to virtualization.ClusterType bar the endpoint and the object type, and that is
// the honest description rather than a missing abstraction: `bases: ContactsMixin,
// OrganizationalModel` (docs/netbox-schema.md -> virtualization.ClusterGroup) and its only own
// entries are two GenericRelations, which are reverse relations rather than columns.
//
// The group matters to virtualization.Cluster out of proportion to its size: `(group, name)`
// is the first of the two constraints in the Cluster's `meta.constraints`, so setting a
// cluster's group is what makes that cluster's lookup unambiguous.
func virtualizationClusterGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxClusterGroup"),
		Endpoint:   "virtualization/cluster-groups",
		ObjectType: "virtualization.clustergroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
		},

		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
