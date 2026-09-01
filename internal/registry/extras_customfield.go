package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasCustomFieldDescriptor()) }

// extrasCustomFieldDescriptor is extras.CustomField as data.
//
// The one kind the operator was already a writer of before it had a CRD, and the two
// declarations that say so are the interesting part of this file:
//
//   - ReservedKeySpec: `name`, so a CR naming one of the provenance bootstrap's own
//     definitions is refused rather than becoming a second writer of it.
//   - DataLossOnDelete: true, so deleting one is blocked until somebody says the loss is
//     acceptable.
//
// Everything else here is an ordinary column table. That is the point: the collision is a
// fact about this NetBox model, so it is data on its descriptor rather than a branch in the
// engine (docs/custom-fields.md).
func extrasCustomFieldDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCustomField"),
		Endpoint:   "extras/custom-fields",
		ObjectType: "extras.customfield",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Neither, and stated rather than omitted: the bases are `CloningMixin,
		// ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel` (docs/netbox-schema.md ->
		// extras.CustomField) with no TagsMixin and no CustomFieldsMixin. A custom field
		// cannot carry custom fields, so a NetBoxCustomField is a managed object with no
		// provenance stamp at all -- the case docs/operations/provenance.md calls out and
		// NetBoxSweep (NBO-046) must never delete.
		//
		// CustomFieldable being false also keeps this kind out of the `object_types` list the
		// bootstrap derives, which is the right answer twice over: the operator does not stamp
		// custom fields onto custom fields, and a kind that appeared in that list would have
		// the bootstrap widen `k8s_uid` to cover `extras.customfield` -- a column NetBox would
		// then offer on the very objects this Kind manages.
		Taggable:        false,
		CustomFieldable: false,

		// `name` is what the provenance bootstrap looks its own four definitions up by
		// (internal/provenance/bootstrap.go, bootstrapField). Declaring it here is what makes
		// a CR for `k8s_uid` report `Ready=False, Reason=ReservedByOperator` and write
		// nothing, instead of quietly becoming the second writer of the object every stamped
		// object in the cluster depends on. Which *names* are reserved is the endpoint's
		// business, not this file's: see provenance.Config.Reserved.
		ReservedKeySpec: "name",

		// Deleting one strips this field's stored value from every object in NetBox that has
		// it, on a `pre_delete` signal, with nothing `PROTECT`-ed to refuse the delete
		// (netbox/extras/signals.py:59-68 calling
		// netbox/extras/models/customfields.py:387-401). So the engine's usual safety net --
		// send the DELETE and report NetBox's refusal -- cannot fire here, and the refusal has
		// to be the operator's: `Deleting=False, Reason=DataLossBlocked` until the
		// `netbox.kubeforge.org/allow-data-loss` annotation says otherwise.
		DataLossOnDelete: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "label", API: "label"},
			{Spec: "groupName", API: "group_name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{Spec: "type", API: "type"},
			{Spec: "filterLogic", API: "filter_logic"},
			{Spec: "uiVisible", API: "ui_visible"},
			{Spec: "uiEditable", API: "ui_editable"},
			{Spec: "required", API: "required"},
			{Spec: "unique", API: "unique"},
			{Spec: "isCloneable", API: "is_cloneable"},
			{Spec: "searchWeight", API: "search_weight"},
			{Spec: "weight", API: "weight"},
			{Spec: "validationRegex", API: "validation_regex"},
			// DecimalFields, cleared with null rather than with the empty string: DRF parses
			// `""` as a number and rejects it, so without EmptyIsNull the cleared state would
			// be admissible and unwritable (#170, dcim.Site.latitude is the same case).
			{Spec: "validationMinimum", API: "validation_minimum", EmptyIsNull: true},
			{Spec: "validationMaximum", API: "validation_maximum", EmptyIsNull: true},
			// A ManyToManyField onto contenttypes.ContentType, so its values are
			// `app_label.model` strings rather than NetBox object ids: a resolver told to
			// resolve them would go looking for a CR named `dcim.device`, which cannot exist.
			// The class also picks the comparison -- an order-independent string set, because
			// NetBox does not preserve M2M order.
			{Spec: "objectTypes", API: "object_types", Class: ClassObjectTypeList},
			// *Not* an ObjectTypeList, and the difference is the cardinality rather than the
			// vocabulary: `related_object_type` is a `ForeignKey -> contenttypes.ContentType`
			// (docs/netbox-schema.md -> extras.CustomField), so it holds one content type and
			// not a set of them. It still travels as `app_label.model` in both directions,
			// because that is how ContentTypeField renders a ContentType
			// (netbox/netbox/api/fields.py:102-122) -- so a plain value compared as a string,
			// which is what ClassValue does. Cleared with null, since the serializer is
			// `required=False, allow_null=True` with no `allow_blank`.
			{Spec: "relatedObjectType", API: "related_object_type", EmptyIsNull: true},
			// JSONFields. `default` is the one that earns the class most visibly: NetBox's own
			// help text is "must be a JSON value ... encapsulate strings with double quotes",
			// so `{"id": 3}` is a legal default for a `json` field and the scalar rule would
			// unwrap it to `3` and PATCH forever.
			{Spec: "default", API: "default", Class: ClassJSON},
			{Spec: "relatedObjectFilter", API: "related_object_filter", Class: ClassJSON},
			{Spec: "validationSchema", API: "validation_schema", Class: ClassJSON},
			// `on_delete=PROTECT` (netbox/extras/models/customfields.py:236-243), so
			// CascadeOnDelete is false and this is not a containment parent: deleting the
			// choice set while a field still uses it is refused by NetBox, which the choice
			// set reports as `Deleting=False, Reason=Protected` until the last field using it
			// is gone.
			{
				Spec: "choiceSetRef", API: "choice_set", Class: ClassRefOne,
				Target: netboxv1alpha1.CustomFieldChoiceSetRef{}.TargetGVK(),
			},
		},

		// One candidate. `name` is column-unique (docs/netbox-schema.md -> extras.CustomField,
		// `name CharField REQ UNIQUE len=50`), and an exact lookup is right rather than
		// case-insensitive: the column has no `Lower('name')` constraint, and the CRD already
		// restricts the spec field to lowercase, so there is no case for `__ie` to reconcile.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// The four every ChangeLoggedModel carries, plus `data_type`: a read-only
		// SerializerMethodField derived from `type`
		// (netbox/extras/api/serializers_/customfields.py:56, 82-94). Listing it is what stops
		// a later addition mapping a spec field onto a column NetBox computes -- which does
		// not fail, it silently no-ops, and the next reconcile finds the same difference and
		// PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "data_type"},
	}
}
