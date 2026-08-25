package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(tenancyContactRoleDescriptor()) }

// tenancyContactRoleDescriptor is tenancy.ContactRole as data.
//
// An OrganizationalModel with no columns of its own (docs/netbox-schema.md ->
// tenancy.ContactRole, "no own columns"), so this is the smallest shape in the group:
// `slug` is globally UNIQUE at the column level (netbox/netbox/models/__init__.py:232-236),
// which makes the identity one candidate with no null pin and nothing conditional about it.
//
// Worth stating next to its neighbour rather than left implicit: tenancy.ContactGroup shares
// the same app, the same file and almost the same fields, and cannot use `slug` at all --
// NestedGroupModel's `slug` has no UNIQUE. The base class is what decides, not the app.
//
// This kind has **no foreign keys at all**, so it has no containment parent and cannot have
// one. The reference that points *at* it, `ContactAssignment.role`, is `on_delete=PROTECT`
// (docs/netbox-schema.md -> tenancy.ContactAssignment), so deleting a role that is in use is
// refused by NetBox rather than cascading.
func tenancyContactRoleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxContactRole"),
		Endpoint:   "tenancy/contact-roles",
		ObjectType: "tenancy.contactrole",
		Scope:      apiextensionsv1.NamespaceScoped,

		// tenancy.ContactRole is an OrganizationalModel (docs/netbox-schema.md ->
		// tenancy.ContactRole, bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// `slug` alone, from the column-level UNIQUE. `name` is column-unique too and
		// deliberately is not a candidate: a kind gets one identity, and `slug` is the one the
		// spec calls the role's identifier -- so a rename that collides is NetBox's own 409
		// reported as Invalid rather than an adoption under the other candidate.
		//
		// The filter is registered: `Meta.fields = ('id', 'name', 'slug', 'description')` on
		// ContactRoleFilterSet (NetBox 4.6.8, netbox/tenancy/filtersets.py:74).
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. No `_`-prefixed caches and no
		// counter: an OrganizationalModel has no tree and this one has no reverse relation the
		// serializer counts.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
