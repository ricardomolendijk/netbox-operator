package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(tenancyContactGroupDescriptor()) }

// tenancyContactGroupDescriptor is tenancy.ContactGroup as data.
//
// The third NestedGroupModel, and the third *different* identity out of one base class --
// which is why plan.md §8.1's claim that every MPTT kind is `(parent, name)` plus a
// `parent IS NULL` variant is checked here rather than assumed:
//
//   - dcim.Region and dcim.SiteGroup declare `(parent, name)` *and* `(name)` conditioned on
//     `parent IS NULL` (netbox/dcim/models/sites.py:62-67 and :133-143).
//   - tenancy.TenantGroup declares no `meta.constraints` at all and puts column-level UNIQUE
//     on `name` and `slug`.
//   - tenancy.ContactGroup declares **one** constraint and no conditional variant:
//     `UniqueConstraint(fields=('parent', 'name'), name='..._unique_parent_name')`
//     (docs/netbox-schema.md -> tenancy.ContactGroup, `meta.constraints`;
//     netbox/tenancy/models/contacts.py:53-58).
//
// So the key is `name` and not `slug` -- NestedGroupModel's `slug` carries no UNIQUE
// (netbox/netbox/models/__init__.py:183-186) and this model adds none, so two contact groups
// may share one and a `slug` candidate would adopt whichever came back first.
//
// The second candidate below is therefore a **convention rather than a constraint**, and that
// is the one thing about this kind that differs from dcim.Region at runtime: with `parent`
// NULL, Postgres treats the rows as distinct and `unique_parent_name` does not fire, so two
// top-level contact groups named "NOC" are legal server state. The candidate pins the column
// anyway, because the alternative is worse in both directions -- omitting `parent_id` asks
// "this name under any parent", so every top-level group would adopt a nested one of the same
// name -- and more than one match is reported as a Conflict rather than resolved by taking the
// first (docs/concepts/lookups.md).
func tenancyContactGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxContactGroup"),
		Endpoint:   "tenancy/contact-groups",
		ObjectType: "tenancy.contactgroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// tenancy.ContactGroup is a NestedGroupModel (docs/netbox-schema.md ->
		// tenancy.ContactGroup, bases), which mixes in both TagsMixin and CustomFieldsMixin,
		// so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// A foreign key is written as `parent` and filtered as `parent_id`; the field
			// map carries the write name, the natural keys below carry the filter name.
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.ContactGroupRef{}.TargetGVK(),
				// `parent (NestedGroupModel) TreeForeignKey -> tenancy.ContactGroup
				// on_delete=CASCADE` (docs/netbox-schema.md -> tenancy.ContactGroup), which
				// is what makes this field the ContainmentRef below.
				CascadeOnDelete: true,
			},
		},

		// Two candidates, in this order, and the order is not a fallback: a nested group is
		// identified by the first and a top-level one by the second, and NaturalKey.Applicable
		// keeps them apart -- the second asserts `parentRef` was never declared, so a child
		// whose parent has not been created yet matches neither and the engine waits rather
		// than adopting an unrelated top-level group and reparenting somebody else's data.
		//
		// `parent_id` is a registered filter on this endpoint --
		// `parent_id = ModelMultipleChoiceFilter(queryset=ContactGroup.objects.all())`
		// (NetBox 4.6.8, netbox/tenancy/filtersets.py:34-38) -- and `name` comes from
		// `Meta.fields = ('id', 'name', 'slug', 'description')` (:67). The null pin is
		// spelled with the sentinel `?parent_id=null` rather than a suffix, because `parent`
		// is a foreign key and its filter is a model-choice one (NullColumnRef).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "parent_id", Spec: "parentRef"},
					{Filter: "name", Spec: "name"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},

		// No deferral, exactly as on dcim.Region and unlike tenancy.TenantGroup. `parent` is
		// matched on by candidate 1, so DeferAlways is refused at boot
		// (ErrDeferredNaturalKey), and DeferIfUnresolved would be dead data: a declared but
		// unresolved parent makes *neither* candidate applicable, so the engine never reaches
		// a create with the reference outstanding.
		//
		// `parentRef` is the containment parent, so a nested group gets a non-controller owner
		// reference to its parent and `kubectl delete` on the parent takes its children with
		// it (ADR-0003 rule 4). Not a stylistic choice: the FK is CASCADE, so NetBox deletes
		// the descendants server-side, and without the owner reference the child CR outlives
		// the row and the engine's create-if-absent step recreates what NetBox deliberately
		// deleted. It is also the only FK this kind has, so the cascade rule picks it with no
		// tiebreak.
		ContainmentRef: "parentRef",

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches and the counter the serializer returns. `_depth`, `_children` and
		// `contact_count` are maintained by NetBox (docs/netbox-schema.md, preamble on
		// `_`-prefixed columns; netbox/tenancy/api/serializers_/contacts.py:27); writing one
		// does not fail, it silently no-ops, so the next reconcile finds the same difference
		// and PATCHes again forever.
		ReadOnly: []string{
			"created", "last_updated", "url", "display", "_depth", "_children", "contact_count",
		},
	}
}
