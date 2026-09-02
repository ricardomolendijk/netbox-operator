package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimModuleTypeDescriptor()) }

// dcimModuleTypeDescriptor is dcim.ModuleType as data.
//
// The dcim.RackType and dcim.DeviceType shape with one constraint instead of two
// (docs/netbox-schema.md -> dcim.ModuleType.meta.constraints):
//
//	UniqueConstraint(fields=('manufacturer', 'model'), name='..._unique_manufacturer_model')
//
// This is the one kind in #54's table whose natural key the committed IR does supply directly:
// `hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleType.natural_keys` carries the pair, with
// `manufacturer_id` and `model` as the filters and no null pin. Nothing is hand-derived here.
//
// There is no second candidate, and the absence is a schema fact rather than a choice:
// dcim.RackType and dcim.DeviceType each have a `(manufacturer, slug)` constraint to fall back
// to, and `dcim.ModuleType` has no `slug` column at all. So a model rename is a new object to
// the lookup rather than a PATCH -- worth knowing, and not something the operator can improve
// on without inventing a key NetBox does not enforce.
//
// The constraint is unconditional and `manufacturer` is `REQ`, so there is no null pin to
// write: with `manufacturerRef` unresolved no candidate is applicable and the object writes
// nothing at all rather than being created without the field.
//
// Both filters are registered: `manufacturer_id` is declared on `ModuleTypeFilterSet` and
// `model` is in its `meta_fields` (hack/testdata/ir-4.6.8.json.gz ->
// dcim.ModuleType.filters).
func dcimModuleTypeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxModuleType"),
		Endpoint:   "dcim/module-types",
		ObjectType: "dcim.moduletype",
		Scope:      apiextensionsv1.NamespaceScoped,

		// A PrimaryModel (docs/netbox-schema.md -> dcim.ModuleType, bases), so it mixes in both
		// TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		// ImageAttachmentsMixin and WeightMixin, the other two bases, contribute a
		// GenericRelation and three columns respectively.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimModuleTypeFields(),

		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "model", Spec: "model"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef. `manufacturer` and `profile` are both `on_delete=PROTECT`
		// (docs/netbox-schema.md -> dcim.ModuleType): NetBox refuses to delete either while a
		// module type points at it, so nothing cascades and there is no server-side deletion
		// for an owner reference to mirror
		// (docs/decisions/0003-ownership-and-references.md rule 4).

		ReadOnly: dcimModuleTypeReadOnly(),
	}
}

// dcimModuleTypeFields is this kind's spec-to-column map.
//
// Two entries earn the explicit table on their own.
//
// `attributes` is the one that would silently do nothing if it were guessed. The *model*
// column is `attribute_data` and the *serializer* exposes it as `attributes`
// (hack/testdata/api-schema-4.6.8.json.gz -> ModuleTypeSerializer, `declared.attributes` with
// serializer_field `AttributesField`; the IR marks `attribute_data` as not in the write path
// for the same reason). NetBox drops a field name it does not know rather than rejecting it,
// so writing `attribute_data` would report success and set nothing. It is ClassJSON because
// the column is a JSONField whose value is a whole document: compared as a scalar, the
// unwrapping rule would strip any `{"value": ...}` an attribute document happens to contain
// and the operator would PATCH forever (registry.ClassJSON).
//
// `partNumber` -> `part_number` is a camelCase-to-snake_case pair a convention gets right and
// is written out anyway, because the table is the whole map or it is not a map.
//
// `airflow`, `weight` and `weightUnit` all carry EmptyIsNull: all three columns are
// `blank=True, null=True` (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleType), NetBox returns
// an unset choice and an unset decimal as `null` rather than as `""`, and DRF rejects `""` as
// a number outright. An emptied field sent as the empty string would differ from the value
// read back on every pass, or fail the write (#170).
func dcimModuleTypeFields() []Field {
	return []Field{
		{Spec: "model", API: "model"},
		{Spec: "partNumber", API: "part_number"},
		{Spec: "airflow", API: "airflow", EmptyIsNull: true},
		{Spec: "attributes", API: "attributes", Class: ClassJSON},
		{Spec: "weight", API: "weight", EmptyIsNull: true},
		{Spec: "weightUnit", API: "weight_unit", EmptyIsNull: true},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
		// Written as `manufacturer`, filtered as `manufacturer_id`: the field map carries the
		// write name and the natural key carries the filter name. PROTECT, so no cascade to
		// declare -- which is what leaves this kind without a containment parent.
		{
			Spec: "manufacturerRef", API: "manufacturer", Class: ClassRefOne,
			Target: netboxv1alpha1.ManufacturerRef{}.TargetGVK(),
		},
		// PROTECT as well, so no cascade either.
		{
			Spec: "profileRef", API: "profile", Class: ClassRefOne,
			Target: netboxv1alpha1.ModuleTypeProfileRef{}.TargetGVK(),
		},
	}
}

// dcimModuleTypeReadOnly are the columns the operator must never write.
//
// The four every ChangeLoggedModel carries, then the nine CounterCacheFields, then
// WeightMixin's cache.
//
// The counters are the reason this list is long. NetBox maintains `module_count` from the
// modules pointing at this type and one `*_template_count` per component template kind from
// the templates pointing at it (docs/netbox-schema.md, preamble on every CounterCacheField),
// and every one of them is in the serializer's write path
// (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleType.write_path). NetBox ignores a write to
// one instead of refusing it, so with the column in ReadOnly a field map that ever reaches for
// it fails Validate at boot (ErrFieldReadOnly) rather than PATCHing forever.
//
// `_abs_weight` is WeightMixin's normalised-grams cache, which the IR records as absent from
// the write path entirely, and `images` is an ImageAttachmentsMixin GenericRelation -- the
// reverse of somebody else's foreign key rather than a column.
func dcimModuleTypeReadOnly() []string {
	return []string{
		"created", "last_updated", "url", "display",
		"module_count",
		"console_port_template_count", "console_server_port_template_count",
		"power_port_template_count", "power_outlet_template_count",
		"interface_template_count", "front_port_template_count",
		"rear_port_template_count", "module_bay_template_count",
		"_abs_weight",
	}
}
