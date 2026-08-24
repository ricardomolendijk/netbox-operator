package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimDeviceTypeDescriptor()) }

// dcimDeviceTypeDescriptor is dcim.DeviceType as data.
//
// The kind whose identity is scoped by a *required* reference to another kind. Both
// constraints start at `manufacturer` (docs/netbox-schema.md ->
// dcim.DeviceType.meta.constraints):
//
//	UniqueConstraint(fields=('manufacturer', 'model'), name='..._unique_manufacturer_model')
//	UniqueConstraint(fields=('manufacturer', 'slug'),  name='..._unique_manufacturer_slug')
//
// Neither is conditional, so there is no null pin to write: `manufacturer` is `REQ` and a
// manufacturer-less device type is not a state NetBox has. The consequence is the
// dcim.Location shape rather than the ipam.Prefix one -- with `manufacturerRef` unresolved
// **no** candidate is applicable, so the object writes nothing at all rather than being
// created without the field.
func dcimDeviceTypeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxDeviceType"),
		Endpoint:   "dcim/device-types",
		ObjectType: "dcim.devicetype",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.DeviceType is a PrimaryModel (docs/netbox-schema.md -> dcim.DeviceType,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp. ImageAttachmentsMixin and WeightMixin, the other two
		// bases, contribute a GenericRelation and the out-of-scope weight columns.
		Taggable:        true,
		CustomFieldable: true,

		// `uHeight` needs no field class: NetBox returns a DecimalField as a string padded to
		// its decimal_places ("1.00" for a spec that said "1.0"), and
		// internal/netbox/drift.go's scalarEqual compares two numeric strings numerically.
		// dcim.Site's latitude proved that. Nor do the two choice columns: a choice comes
		// back as {"value","label"} and unwrapNested reduces it by the absence of an "id"
		// key.
		//
		// `front_image` and `rear_image` are absent: they are ImageFields, uploaded as
		// multipart form data and returned as URLs, so no JSON payload can write one.
		Fields: []Field{
			{Spec: "model", API: "model"},
			{Spec: "slug", API: "slug"},
			{Spec: "partNumber", API: "part_number"},
			{Spec: "uHeight", API: "u_height"},
			{Spec: "excludeFromUtilization", API: "exclude_from_utilization"},
			{Spec: "isFullDepth", API: "is_full_depth"},
			// EmptyIsNull on both choice columns: they are `blank=True, null=True`, and
			// NetBox's serializer returns `null` for an unset choice rather than `""`. An
			// emptied field sent as `""` would differ from the value read back on every
			// pass, which is a PATCH loop (#170, as on dcim.Site's coordinates).
			{Spec: "subdeviceRole", API: "subdevice_role", EmptyIsNull: true},
			{Spec: "airflow", API: "airflow", EmptyIsNull: true},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// Written as `manufacturer`, filtered as `manufacturer_id`: the field map carries
			// the write name and the natural keys below carry the filter name.
			{
				Spec: "manufacturerRef", API: "manufacturer", Class: ClassRefOne,
				Target: netboxv1alpha1.ManufacturerRef{}.TargetGVK(),
			},
			{
				Spec: "defaultPlatformRef", API: "default_platform", Class: ClassRefOne,
				Target: netboxv1alpha1.PlatformRef{}.TargetGVK(),
			},
		},

		// Two candidates, both scoped by `manufacturer_id`, and this pair *is* a fallback
		// chain -- unlike dcim.Region's, where exactly one candidate is ever applicable.
		// `model` and `slug` are both required, so both candidates apply whenever
		// `manufacturerRef` resolves and the second is reached only when the first matched
		// nothing.
		//
		// That is safe here for a reason specific to these constraints: `(manufacturer,
		// model)` is unique in the database, so an object the second candidate finds is the
		// same make and model the spec describes, and creating a second one would be a 409
		// rather than a duplicate. Adopting it and PATCHing the slug is strictly better than
		// failing every reconcile.
		//
		// `slug` leads because it is the stable identifier: a marketing rename edits `model`,
		// and looking up by `slug` first keeps that a PATCH rather than a second object.
		//
		// `manufacturerRef` is not deferred, and cannot be: both candidates match on it, so
		// stripping it from a create would mean the lookup asked a different question from
		// the create it decided on (registry.ErrDeferredNaturalKey).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "slug", Spec: "slug"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "model", Spec: "model"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No containment ref. `manufacturer` is the required foreign key, but it is
		// `on_delete=PROTECT` (docs/netbox-schema.md -> dcim.DeviceType): NetBox refuses to
		// delete a manufacturer while a device type points at it, so nothing cascades and
		// there is no server-side deletion for an owner reference to mirror
		// (docs/decisions/0003-ownership-and-references.md rule 4). `default_platform` is
		// SET_NULL, which is not containment either.

		// The four columns every ChangeLoggedModel carries, plus every CounterCacheField this
		// model declares and WeightMixin's `_abs_weight` (docs/netbox-schema.md, preamble:
		// `_`-prefixed columns and every CounterCacheField are denormalised caches NetBox
		// maintains itself). Listed rather than left implicit even though no spec field maps
		// onto one: the list is what makes a future field map that does reach for
		// `device_count` a boot failure (ErrFieldReadOnly) instead of a payload NetBox
		// silently drops and the engine re-sends forever.
		ReadOnly: []string{
			"created", "last_updated", "url", "display",
			"console_port_template_count", "console_server_port_template_count",
			"power_port_template_count", "power_outlet_template_count",
			"interface_template_count", "front_port_template_count",
			"rear_port_template_count", "device_bay_template_count",
			"module_bay_template_count", "inventory_item_template_count",
			"device_count", "_abs_weight",
		},
	}
}
