package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(tenancyContactDescriptor()) }

// tenancyContactDescriptor is tenancy.Contact as data.
//
// The kind whose identity **no constraint of any kind backs**. tenancy.Contact declares no
// `meta.constraints`, no column UNIQUE and only an index on `name`
// (docs/netbox-schema.md -> tenancy.Contact, `meta.indexes:
// (models.Index(fields=('name',)),)`; netbox/tenancy/models/contacts.py:114-120). Two
// contacts named "NOC" are legal server state, so the single candidate below is a convention
// and an ambiguous match is a Conflict rather than an adoption -- the same position
// ipam.Prefix and ipam.IPAddress are in, and it is stated here because a reader who assumes
// `?name=` identifies one row will be wrong exactly once, expensively.
//
// It is also the only kind in the group whose *group* relationship is a many-to-many.
// `Contact.groups` is `ManyToManyField -> tenancy.ContactGroup`
// (netbox/tenancy/models/contacts.py:71-76), so a contact may sit in several groups and there
// is no single value a lookup filter could take -- which is why `groups` is outside the
// natural key even though `group_id` is a registered filter on this endpoint
// (netbox/tenancy/filtersets.py:80-85, a TreeNodeMultipleChoiceFilter over `groups`: it
// matches membership, not identity).
func tenancyContactDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxContact"),
		Endpoint:   "tenancy/contacts",
		ObjectType: "tenancy.contact",
		Scope:      apiextensionsv1.NamespaceScoped,

		// tenancy.Contact is a PrimaryModel (docs/netbox-schema.md -> tenancy.Contact,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "title", API: "title"},
			{Spec: "phone", API: "phone"},
			{Spec: "email", API: "email"},
			{Spec: "address", API: "address"},
			{Spec: "link", API: "link"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// ClassRefMany and not ClassRefOne: this is the M2M, so the value is compared as
			// an order-independent id set and written as the whole list. NetBox does not
			// preserve M2M order, so the order the spec lists them in is not data.
			//
			// No CascadeOnDelete. A many-to-many has no `on_delete` in either direction:
			// deleting a ContactGroup drops the join-table rows and leaves the contact
			// standing, so there is no server-side deletion for an owner reference to mirror
			// and this kind has no containment parent.
			{
				Spec: "groups", API: "groups", Class: ClassRefMany,
				Target: netboxv1alpha1.ContactGroupRef{}.TargetGVK(),
			},
		},

		// One candidate, and it is a convention rather than a constraint -- see the type
		// comment. `name` is a registered filter: `Meta.fields = ('id', 'name', 'title',
		// 'phone', 'email', 'address', 'link', 'description')` on ContactFilterSet
		// (NetBox 4.6.8, netbox/tenancy/filtersets.py:94-96).
		//
		// No null pin and nothing to pin: `groups` is outside the key, and pinning an M2M to
		// null is not a query NetBox offers.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		// No DuplicateSpec. Two contacts of one name is *not* a case where the operator picks
		// its own by the provenance stamp -- that is ipam.IPAddress's `allowDuplicate`, where
		// NetBox's data model requires the duplicate. Here a second row of the same name means
		// somebody declared the same contact twice, and the honest answer is Conflict.
		//
		// No ContainmentRef: `groups` is the only reference and a many-to-many cascades
		// nothing (see the field comment).
		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. tenancy.Contact declares no
		// `_`-prefixed cache and no CounterCacheField of its own.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
