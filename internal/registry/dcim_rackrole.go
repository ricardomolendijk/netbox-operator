package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimRackRoleDescriptor()) }

// dcimRackRoleDescriptor is dcim.RackRole as data.
//
// The plainest identity in NBO-051, and the derivation is the one worth reading, because
// dcim.RackRole declares **no `meta.constraints` at all** (docs/netbox-schema.md ->
// dcim.RackRole; its `meta.ordering` is `('name',)` and there is nothing else). So the key
// comes from the base class's column-level unique instead:
//
//	slug (OrganizationalModel)  SlugField  REQ UNIQUE len=100
//
// One candidate, no null pin, no reference in the key. Identical to dcim.Manufacturer's and
// tenancy.ContactRole's, and it is `OrganizationalModel` rather than the app that decides
// that: `NestedGroupModel.slug` carries no UNIQUE, which is why every nested-group kind has a
// `(parent, name)` key instead.
//
// The filter is registered: `slug` is in `RackRoleFilterSet.meta_fields`
// (`('id', 'name', 'slug', 'color', 'description')`, NetBox 4.6.8
// `netbox/dcim/filtersets.py:328`).
func dcimRackRoleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRackRole"),
		Endpoint:   "dcim/rack-roles",
		ObjectType: "dcim.rackrole",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.RackRole is an OrganizationalModel (docs/netbox-schema.md -> dcim.RackRole,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `color` needs no field class: a ColorField is six hex digits over the wire, a plain
		// string the drift comparison handles as one. dcim.DeviceRole proved that.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "color", API: "color"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// `name` is UNIQUE too and deliberately not a second candidate: a kind gets one
		// identity, `slug` is the stable one, and a colliding rename comes back as NetBox's
		// own 409 rather than being adopted under the other key.
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: dcim.RackRole has no foreign key at all bar `owner`, which has no
		// Kind, so there is nothing that could be a containment parent
		// (docs/decisions/0003-ownership-and-references.md rule 4). The reference pointing *at*
		// it is `Rack.role ForeignKey -> dcim.RackRole on_delete=PROTECT`, so deleting a role
		// in use is refused rather than cascading, reported here as
		// Deleting=False, Reason=Protected.

		// The four columns every ChangeLoggedModel carries, plus the CounterCacheField
		// dcim.RackRole declares. NetBox maintains `rack_count` from the racks pointing here
		// and refuses an attempt to set it (docs/netbox-schema.md, preamble on every
		// CounterCacheField), so writing it silently no-ops and the engine would PATCH it
		// forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "rack_count"},
	}
}
