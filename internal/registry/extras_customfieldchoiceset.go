package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasCustomFieldChoiceSetDescriptor()) }

// extrasCustomFieldChoiceSetDescriptor is extras.CustomFieldChoiceSet as data.
//
// The first kind with a JSONField column, and the reason ClassJSON exists: `choice_colors`
// is a document, and comparing a document with the scalar rule would unwrap any object
// carrying an `id` or a `value` key -- which is how NetBox renders a foreign key and a choice
// on read -- and then never settle.
func extrasCustomFieldChoiceSetDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCustomFieldChoiceSet"),
		Endpoint:   "extras/custom-field-choice-sets",
		ObjectType: "extras.customfieldchoiceset",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Neither, and stated rather than omitted: the bases are `CloningMixin,
		// ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel` (docs/netbox-schema.md ->
		// extras.CustomFieldChoiceSet) with no TagsMixin and no CustomFieldsMixin. NetBox
		// ignores a column it does not know rather than rejecting it, so writing `tags` here
		// would not fail -- the value would vanish, the next read would find it absent, and
		// the engine would PATCH it again forever.
		Taggable:        false,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			// `null` and not `""`. The serializer is
			// `ChoiceField(choices=..., required=False, allow_null=True)` with `allow_blank`
			// left at false, and its `to_internal_value` raises "This field may not be blank"
			// on the empty string (netbox/extras/api/serializers_/customfields.py:20-24,
			// netbox/netbox/api/fields.py:64-68). So `baseChoices: ""` has to be *sent* as
			// JSON null or the cleared state would be admissible and unwritable (#170).
			{Spec: "baseChoices", API: "base_choices", EmptyIsNull: true},
			// An ordered array rather than a set. NetBox concatenates the base set and these
			// in the order given and only re-sorts at read time when `order_alphabetically`
			// is set (netbox/extras/models/customfields.py:974-987), so the order the spec
			// lists them in *is* data -- comparing it order-independently would silently
			// ignore a reordering the user asked for. The elements are themselves
			// two-element `[value, label]` arrays, which sameOrderedList compares element by
			// element without caring how deep they go.
			{Spec: "extraChoices", API: "extra_choices", Class: ClassArray},
			// A JSONField, and the reason ClassJSON exists. `{"active": "green"}` is a
			// document, and the scalar rule would unwrap `{"value": ...}` and `{"id": ...}`
			// out of one -- so a colour map keyed on a choice value called `id` would be
			// compared as that colour against the whole map and PATCH forever.
			{Spec: "choiceColors", API: "choice_colors", Class: ClassJSON},
			{Spec: "orderAlphabetically", API: "order_alphabetically"},
		},

		// One candidate. `name` is column-unique (docs/netbox-schema.md ->
		// extras.CustomFieldChoiceSet, `name CharField REQ UNIQUE len=100`), so it identifies
		// at most one choice set on its own: no conditional constraint to express as a second
		// candidate, and no parent to pin to null.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// The four every ChangeLoggedModel carries, plus `choices_count`: a read-only
		// `IntegerField` on the serializer (netbox/extras/api/serializers_/customfields.py:34)
		// that reads back as the size of the combined base-plus-extra list. It is not in
		// Fields, so nothing would write it -- it is listed so that a later addition cannot,
		// which is the only thing ReadOnly is for.
		ReadOnly: []string{"created", "last_updated", "url", "display", "choices_count"},
	}
}
