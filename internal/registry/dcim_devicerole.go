package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimDeviceRoleDescriptor()) }

// dcimDeviceRoleDescriptor is dcim.DeviceRole as data.
//
// A NestedGroupModel in 4.6.8 -- the model gained a `parent` -- and one that really does need
// the null-pinned variant. It is not the base class that decides that, it is the constraint
// list (docs/netbox-schema.md -> dcim.DeviceRole.meta.constraints), which declares four
// constraints in two pairs:
//
//	UniqueConstraint(fields=('parent', 'name'), name='..._parent_name')
//	UniqueConstraint(fields=('name',), name='..._name', condition=Q(parent__isnull=True))
//	UniqueConstraint(fields=('parent', 'slug'), name='..._parent_slug')
//	UniqueConstraint(fields=('slug',), name='..._slug', condition=Q(parent__isnull=True))
//
// tenancy.TenantGroup is the same base class with no `meta.constraints` at all and therefore
// no pin; getting the two the wrong way round would either make a nested group's slug
// unfindable or let every top-level role adopt an unrelated one. The `parent__isnull=True`
// conditions above are the whole evidence, and they are the reason candidate 2 exists.
//
// The `slug` pair is the identity and the `name` pair deliberately is not: a kind gets one
// identity, and `slug` is the stable one. A rename that collides therefore comes back as
// NetBox's own 409 -- reported as Ready=False/Invalid -- rather than being adopted under the
// other candidate.
func dcimDeviceRoleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxDeviceRole"),
		Endpoint:   "dcim/device-roles",
		ObjectType: "dcim.devicerole",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.DeviceRole is a NestedGroupModel (docs/netbox-schema.md -> dcim.DeviceRole,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `config_template` is absent deliberately: extras.ConfigTemplate has no Kind yet
		// (NBO-059), and a field the CRD accepts and the payload drops reports success while
		// writing nothing.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "color", API: "color"},
			{Spec: "vmRole", API: "vm_role"},
			{Spec: "description", API: "description"},
			// A foreign key is written as `parent` and filtered as `parent_id`; the field
			// map carries the write name, the natural keys below carry the filter name.
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.DeviceRoleRef{}.TargetGVK(),
				// The self-reference is on_delete=CASCADE (docs/netbox-schema.md), so it is a
				// legal containment parent: deleting a parent takes its children with it
				// server-side, and without the owner reference a child CR would outlive its
				// row and the create-if-absent step would recreate what NetBox deleted.
				// NBO-193 makes an undeclared cascade a boot failure rather than a
				// convention, which is what caught this.
				CascadeOnDelete: true,
			},
		},

		// Two candidates, in constraint order. Not a fallback chain: a nested role is
		// identified by the first and a top-level one by the second, and
		// NaturalKey.Applicable is what keeps them apart -- the second asserts `parentRef`
		// was never declared, so a child whose parent has not been created yet matches
		// neither and the engine waits. Falling through would find an unrelated top-level
		// role of that slug, adopt it, and the follow-up PATCH would reparent somebody
		// else's data (NBO-015).
		//
		// The second pins `parent_id__isnull=true` rather than omitting `parent_id`.
		// Omitting it asks "this slug under any parent", so every top-level role would match
		// every role of that slug anywhere in the tree (docs/concepts/lookups.md).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "parent_id", Spec: "parentRef"},
					{Filter: "slug", Spec: "slug"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "slug", Spec: "slug"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef", Column: NullColumnRef}},
			},
		},

		// No Deferred entry for `parent`, unlike tenancy.TenantGroup and dcim.Platform, and
		// the difference is these constraints. Deferral only ever fires for a *declared but
		// unresolved* reference, and in that state neither candidate here is applicable --
		// candidate 1 needs the id, candidate 2 asserts the field was never declared -- so
		// the engine waits and there is nothing to defer. Declaring it anyway would be dead
		// configuration that reads as though a child role were created top-level first, which
		// is the very thing candidate 2's pin exists to prevent. Same shape as dcim.Region
		// and dcim.SiteGroup.

		UpdateStrategy: UpdatePatch,

		// `parentRef` is the containment parent: `dcim.DeviceRole.parent` is a TreeForeignKey
		// with `on_delete=CASCADE` (docs/netbox-schema.md -> dcim.DeviceRole), so deleting a
		// role in NetBox deletes its descendants server-side. Without the owner reference a
		// child CR would outlive the row it described and the engine's create-if-absent step
		// would recreate the role NetBox deliberately deleted
		// (docs/decisions/0003-ownership-and-references.md rule 4, as on dcim.Region).
		ContainmentRef: "parentRef",

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does not
		// fail, it silently no-ops, so the next reconcile finds the same difference and
		// PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
