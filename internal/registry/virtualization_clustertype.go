package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(virtualizationClusterTypeDescriptor()) }

// virtualizationClusterTypeDescriptor is virtualization.ClusterType as data.
//
// The simplest shape a NetBox object kind has: an OrganizationalModel with no own columns at
// all (docs/netbox-schema.md -> virtualization.ClusterType), so three mapped fields, one
// natural key, and nothing else. It ships here rather than with NBO-029 because
// virtualization.Cluster's `type` is `REQ` and `PROTECT`ed -- a cluster cannot be created
// without one, and the type cannot be deleted while one exists.
func virtualizationClusterTypeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxClusterType"),
		Endpoint:   "virtualization/cluster-types",
		ObjectType: "virtualization.clustertype",
		Scope:      apiextensionsv1.NamespaceScoped,

		// An OrganizationalModel mixes in both TagsMixin and CustomFieldsMixin
		// (docs/netbox-schema.md -> netbox.OrganizationalModel), so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
		},

		// `slug` alone, from the column-level `REQ UNIQUE` the base class declares. `name`
		// carries the same UNIQUE and deliberately is not a second candidate: a kind gets one
		// identity, and a second candidate on an equally-unique column would only ever be
		// reached when the first matched nothing -- which for a unique column means the
		// object does not exist and should be created.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. No `_`-prefixed cache and no
		// CounterCacheField on this model; the serializer's `cluster_count` is a
		// RelatedObjectCountField -- returned, never accepted -- and is not listed for the
		// same reason ipam.VRF's `prefix_count` is not: this list guards the field map, and
		// no spec field maps onto it.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
