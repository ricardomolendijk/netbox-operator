package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so that adding a kind is a new file and never an edit to shared
// logic (CONTRIBUTING.md, "Extensibility"). MustRegister panics on a duplicate GVK: that
// is a programming error, and it must stop the process at boot rather than surface as a
// reconcile failure hours later.
func init() { MustRegister(extrasTagDescriptor()) }

// extrasTagDescriptor is extras.Tag as data.
//
// It is the simplest real kind there is -- one scalar natural key, no foreign keys at all,
// and one many-to-many that points at Django ContentTypes rather than at NetBox objects --
// which is why it is the kind the engine is proved against first.
//
// Named for the app as well as the model, because every descriptor in this package shares
// one namespace and the models do not: `dcim.Role` does not exist but `ipam.Role` and
// `dcim.DeviceRole` both do, `dcim.Module` and `dcim.ModuleType` sit next to
// `dcim.ModuleBay`, and a bare modelDescriptor() would start colliding well before the
// hundred-and-twentieth kind.
func extrasTagDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxTag"),
		Endpoint:   "extras/tags",
		ObjectType: "extras.tag",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Neither Taggable nor CustomFieldable, and left at false explicitly rather than
		// omitted, because it is the fact worth writing down: extras.Tag's bases are
		// CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel and
		// django-taggit's TagBase (docs/netbox-schema.md -> extras.Tag) -- no TagsMixin and
		// no CustomFieldsMixin. A tag cannot be tagged, so a NetBoxTag is a managed object
		// that carries no provenance stamp at all, which is the case
		// docs/operations/provenance.md calls out and NetBoxSweep (NBO-046) must never
		// delete.
		Taggable:        false,
		CustomFieldable: false,

		// The bridge between the two vocabularies every other list here uses: CR spec
		// names on the left, NetBox API names on the right. `objectTypes` -> `object_types`
		// is the entry that earns the table -- no camelCase-to-snake_case convention gets
		// `wirelessLANs` or `primaryIP4Ref` right, and NetBox ignores a field name it does
		// not know rather than rejecting it, so a wrong one writes nothing and reports
		// success.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "color", API: "color"},
			{Spec: "description", API: "description"},
			{Spec: "weight", API: "weight"},
			// Not a reference of any cardinality. extras.Tag.object_types is a
			// ManyToManyField onto contenttypes.ContentType (docs/netbox-schema.md ->
			// extras.Tag), so its API values are `app_label.model` strings rather than
			// NetBox object ids: a resolver told to resolve them would go looking for a CR
			// named `dcim.device`, which cannot exist. The class also picks the comparison
			// -- an order-independent string set, because NetBox does not preserve M2M
			// order.
			{Spec: "objectTypes", API: "object_types", Class: ClassObjectTypeList},
		},

		// One candidate is enough here, which is unusual. `slug` is column-unique on
		// django-taggit's TagBase, so it identifies at most one tag on its own: there is
		// no conditional constraint to express as a second candidate and no parent to pin
		// to null.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries and the operator must never
		// write (docs/netbox-schema.md, preamble). Writing one does not fail -- it
		// silently no-ops, so the next reconcile finds the same difference and PATCHes
		// again, forever.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
