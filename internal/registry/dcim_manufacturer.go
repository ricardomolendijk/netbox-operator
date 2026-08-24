package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimManufacturerDescriptor()) }

// dcimManufacturerDescriptor is dcim.Manufacturer as data.
//
// The simplest descriptor in the registry, and the reason it is worth reading: the model has
// **no entry of its own** in the digest's Models section beyond its inherited columns -- it
// declares none -- so every field below comes from `OrganizationalModel` and the endpoint
// comes from the endpoint map (`dcim/manufacturers` -> `dcim.Manufacturer`).
//
// One natural-key candidate and no null pin. `dcim.Manufacturer` declares no
// `meta.constraints` at all and carries column-level `UNIQUE` on both `name` and `slug`
// (docs/netbox-schema.md -> dcim.Manufacturer), so uniqueness is global and there is no
// conditional constraint to express -- the same shape as tenancy.TenantGroup and the opposite
// of dcim.DeviceRole's, which is in this ticket too.
func dcimManufacturerDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxManufacturer"),
		Endpoint:   "dcim/manufacturers",
		ObjectType: "dcim.manufacturer",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Manufacturer is an OrganizationalModel (docs/netbox-schema.md ->
		// dcim.Manufacturer, bases), which mixes in both TagsMixin and CustomFieldsMixin, so
		// it carries the whole provenance stamp. ContactsMixin, the other base, contributes
		// only reverse relations and no columns.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
		},

		// `slug` alone, from the column-level UNIQUE. `name` is column-unique too and
		// deliberately is not a candidate: a kind gets one identity, and `slug` is the one
		// the spec calls the manufacturer's identifier.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries and the operator must never write
		// (docs/netbox-schema.md, preamble). The counts this model's serializer returns
		// (`devicetype_count`, `platform_count`, ...) are equally unwritable and are not
		// listed because this list guards the field map -- a column no spec field maps onto
		// cannot reach a payload, so listing it would document rather than prevent.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
