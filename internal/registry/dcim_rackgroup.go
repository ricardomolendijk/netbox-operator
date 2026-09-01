package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimRackGroupDescriptor()) }

// dcimRackGroupDescriptor is dcim.RackGroup as data.
//
// The kind whose name says NestedGroupModel and whose base class says otherwise. NBO-051's
// ticket table asks for `(parent, slug)` plus a `parent IS NULL` variant and an MPTT cycle
// check; the schema at 4.6.8 supports none of it:
//
//	## dcim.RackGroup   (dcim/models/racks.py)
//	   bases: OrganizationalModel
//	   (no own columns — every field is inherited from OrganizationalModel)
//	   meta.ordering: ('name',)
//
// (docs/netbox-schema.md -> dcim.RackGroup.) No `parent`, no MPTT base, no `site`, and no
// `meta.constraints`. The serializer agrees -- its write path is `('id', 'url',
// 'display_url', 'display', 'name', 'slug', 'description', 'owner', 'comments', 'tags',
// 'custom_fields', 'created', 'last_updated', 'rack_count')`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.RackGroup.write_path) -- and so does the filterset,
// whose `meta_fields` are `('id', 'name', 'slug', 'description')` with no `parent_id` declared
// (`netbox/dcim/filtersets.py:320`).
//
// So the identity is `slug` alone, off `OrganizationalModel`'s column-level unique, exactly as
// dcim.RackRole's is. A `parent_id` pin here would be a filter NetBox does not register, which
// `BaseFilterSet` drops silently -- the lookup would then match every group of that slug and
// adopt one.
//
// The ticket's "schema gap: dcim.RackGroup has an endpoint but no model entry" is stale: the
// entry exists at 4.6.8 and says `OrganizationalModel`. Reported on the PR.
func dcimRackGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRackGroup"),
		Endpoint:   "dcim/rack-groups",
		ObjectType: "dcim.rackgroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.RackGroup is an OrganizationalModel (docs/netbox-schema.md -> dcim.RackGroup,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate. `name` is UNIQUE too and is not a second one, for the reason
		// dcim.RackRole's descriptor gives.
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: the model has no foreign key bar `owner`. `Rack.group` points at
		// it with `on_delete=PROTECT`, so deleting a group in use is refused rather than
		// cascading.

		// The four columns every ChangeLoggedModel carries, plus the CounterCacheField the
		// serializer returns and the API refuses.
		ReadOnly: []string{"created", "last_updated", "url", "display", "rack_count"},
	}
}
