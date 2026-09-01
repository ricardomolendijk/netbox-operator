package registry

import (
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimRackTypeDescriptor()) }

// dcimRackTypeDescriptor is dcim.RackType as data.
//
// The dcim.DeviceType shape, field for field where the identity is concerned. Both constraints
// start at `manufacturer` (docs/netbox-schema.md -> dcim.RackType.meta.constraints):
//
//	UniqueConstraint(fields=('manufacturer', 'model'), name='..._unique_manufacturer_model')
//	UniqueConstraint(fields=('manufacturer', 'slug'),  name='..._unique_manufacturer_slug')
//
// Neither is conditional, so there is no null pin to write: `manufacturer` is `REQ` and a
// manufacturer-less rack type is not a state NetBox has. With `manufacturerRef` unresolved
// **no** candidate is applicable, so the object writes nothing at all rather than being
// created without the field.
//
// One difference from dcim.DeviceType worth recording, because it changes nothing and looks
// like it should: `slug` here is *also* `UNIQUE` at the column level, so `(manufacturer, slug)`
// is stricter than the database needs. The filter still carries `manufacturer_id`, because a
// candidate that drops a filter matches more objects rather than fewer and the constraint names
// the pair.
//
// Both filters are registered: `manufacturer_id` is declared on `RackTypeFilterSet` and `model`
// and `slug` are in its `meta_fields` (NetBox 4.6.8, `netbox/dcim/filtersets.py:336`).
func dcimRackTypeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRackType"),
		Endpoint:   "dcim/rack-types",
		ObjectType: "dcim.racktype",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.RackType derives from RackBase, which is a PrimaryModel
		// (docs/netbox-schema.md -> dcim.RackType and dcim.RackBase, bases), so it mixes in
		// both TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		// ImageAttachmentsMixin, the other base, contributes only a GenericRelation.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimRackTypeFields(),

		// Two candidates, both scoped by `manufacturer_id`, and this pair *is* a fallback chain
		// -- the dcim.DeviceType argument applies unchanged. `model` and `slug` are both
		// required, so both candidates apply whenever `manufacturerRef` resolves and the second
		// is reached only when the first matched nothing. That is safe because `(manufacturer,
		// model)` is unique in the database, so an object the second candidate finds is the same
		// make and model the spec describes and creating a second one would be a 409 rather
		// than a duplicate: adopting it and PATCHing the slug beats failing every reconcile.
		//
		// `slug` leads because it is the stable identifier: a marketing rename edits `model`,
		// and looking up by `slug` first keeps that a PATCH rather than a second object.
		//
		// `manufacturerRef` is not deferred, and cannot be: both candidates match on it, so
		// stripping it from a create would mean the lookup asked a different question from the
		// create it decided on (registry.ErrDeferredNaturalKey).
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

		// No ContainmentRef. `manufacturer` is the required foreign key, but it is
		// `on_delete=PROTECT` (docs/netbox-schema.md -> dcim.RackType): NetBox refuses to delete
		// a manufacturer while a rack type points at it, so nothing cascades and there is no
		// server-side deletion for an owner reference to mirror
		// (docs/decisions/0003-ownership-and-references.md rule 4).

		// The four columns every ChangeLoggedModel carries, the CounterCacheField this model
		// declares, and RackBase's two weight caches.
		ReadOnly: append([]string{
			"created", "last_updated", "url", "display", "rack_count",
		}, rackBaseReadOnly...),
	}
}

// dcimRackTypeFields is this kind's spec-to-column map: the four columns dcim.RackType declares
// over RackBase, its required manufacturer, and RackBase's twelve dimensions.
//
// Extracted from the descriptor for length, not because anything about it is dynamic -- the
// dcimDeviceFields shape. `manufacturerRef` is written as `manufacturer` and filtered as
// `manufacturer_id`: the field map carries the write name and the natural keys carry the filter
// name.
//
// `formFactor` carries no EmptyIsNull, unlike its twin on dcim.Rack. The two columns differ:
// this one is NOT NULL with no default and the CRD makes the field required, so there is no
// empty value for the flag to translate (docs/netbox-schema.md -> dcim.RackType,
// `form_factor CharField REQ len=50`).
func dcimRackTypeFields() []Field {
	return slices.Concat([]Field{
		{Spec: "model", API: "model"},
		{Spec: "slug", API: "slug"},
		{Spec: "formFactor", API: "form_factor"},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
		{
			Spec: "manufacturerRef", API: "manufacturer", Class: ClassRefOne,
			Target: netboxv1alpha1.ManufacturerRef{}.TargetGVK(),
			// PROTECT, so no cascade to declare. Stated by omission rather than by a false
			// flag: CascadeOnDelete is read off the Django field's own `on_delete`.
		},
	}, rackBaseFields())
}
