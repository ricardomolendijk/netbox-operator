package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimModuleTypeProfileDescriptor()) }

// dcimModuleTypeProfileDescriptor is dcim.ModuleTypeProfile as data.
//
// Two writable columns of its own -- `name` and `schema` -- over a plain PrimaryModel, and the
// simplest identity in the module block.
//
// **The key is hand-declared, and the IR is why.** `dcim.ModuleTypeProfile` has no
// `meta.constraints` line at all (docs/netbox-schema.md -> dcim.ModuleTypeProfile), so the
// extractor emits `natural_keys: []` for it -- the same extractor gap #274 records for the
// power kinds, arriving here from the other direction: there the UNIQUE was declared on an
// abstract base, and here it is declared on the column. `name CharField REQ UNIQUE len=100` is
// a database-level unique index either way, and it is the only one this model has. The
// derivation is dcim.Manufacturer's and dcim.RackRole's, with `name` where those have `slug`,
// because this model has no `slug` column.
//
// `name` is registered as a filter: it is in `ModuleTypeProfileFilterSet`'s `meta_fields`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleTypeProfile.filters, `from: meta.fields`).
func dcimModuleTypeProfileDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxModuleTypeProfile"),
		Endpoint:   "dcim/module-type-profiles",
		ObjectType: "dcim.moduletypeprofile",
		Scope:      apiextensionsv1.NamespaceScoped,

		// A PrimaryModel (docs/netbox-schema.md -> dcim.ModuleTypeProfile, bases), so it mixes
		// in both TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			// ClassJSON, not ClassValue. The scalar comparison unwraps any JSON object
			// carrying an `id` or a `value` key, because that is how NetBox renders a foreign
			// key and a choice on read -- and a JSON Schema document routinely contains a
			// `"value"` property, so comparing one as a scalar would never settle
			// (registry.ClassJSON, netbox.FieldRules.JSON).
			{Spec: "schema", API: "schema", Class: ClassJSON},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate. `name` is required, so there is no state in which it is missing and
		// a different identity applies, and there is no second unique column to fall back to.
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "name", Spec: "name"}}},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: the model's only foreign key is `owner`, which is PROTECT and has
		// no Kind (docs/decisions/0003-ownership-and-references.md rule 4).

		// The four columns every ChangeLoggedModel carries. This model declares no
		// CounterCacheField and no cache of its own.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
