package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(circuitsCircuitTypeDescriptor()) }

// circuitsCircuitTypeDescriptor is circuits.CircuitType as data.
//
// The `OrganizationalModel` derivation, two base classes deep. circuits.CircuitType declares no
// columns and no `meta.constraints` of its own -- docs/netbox-schema.md records it as "(no own
// columns -- every field is inherited from BaseCircuitType)" with `meta.ordering: ('name',)` and
// nothing else, and the IR records `natural_keys: []`. `circuits.BaseCircuitType` adds one
// column, `color`, over `OrganizationalModel`, which is where the uniques are:
//
//	name (OrganizationalModel)  CharField  REQ UNIQUE len=100
//	slug (OrganizationalModel)  SlugField  REQ UNIQUE len=100
//
// One candidate, no pin, no reference. The dcim.RackRole and tenancy.ContactRole derivation, and
// it is `OrganizationalModel` rather than the app that decides it: `NestedGroupModel.slug`
// carries no UNIQUE, which is why every nested-group kind has a `(parent, name)` key instead.
//
// NBO-057's ticket claims this kind has "no model entry in the schema". It does --
// docs/netbox-schema.md -> circuits.CircuitType -- and the IR carries it as a full kind with an
// endpoint, a filterset and a write path. The fields below are read from those two artefacts,
// not inferred from the base class.
//
// The filter is registered: `slug` is in `CircuitTypeFilterSet.meta_fields` (NetBox 4.6.8,
// `circuits/filtersets.py:163`).
func circuitsCircuitTypeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCircuitType"),
		Endpoint:   "circuits/circuit-types",
		ObjectType: "circuits.circuittype",
		Scope:      apiextensionsv1.NamespaceScoped,

		// An OrganizationalModel through BaseCircuitType (docs/netbox-schema.md, bases), which
		// mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance
		// stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `color` needs no field class: a ColorField is six hex digits over the wire, a plain
		// string the drift comparison handles as one. dcim.DeviceRole and dcim.RackRole proved
		// that. No EmptyIsNull either -- the column is `blank=True` and not nullable
		// (hack/testdata/ir-4.6.8.json.gz -> circuits.CircuitType, field `color`), so `""` is
		// the value that clears it and a `null` would be rejected.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "color", API: "color"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// `name` is UNIQUE too and deliberately not a second candidate: a kind gets one
		// identity, `slug` is the stable one, and a colliding rename comes back as NetBox's own
		// 409 rather than being adopted under the other key.
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: this kind has no foreign key at all bar `owner`, which has no
		// Kind. The reference pointing *at* it is `Circuit.type ForeignKey REQ ->
		// circuits.CircuitType on_delete=PROTECT`, so deleting a type in use is refused rather
		// than cascading, reported here as Deleting=False, Reason=Protected.

		// The four columns every ChangeLoggedModel carries, plus the counter the serializer
		// returns.
		ReadOnly: []string{"created", "last_updated", "url", "display", "circuit_count"},
	}
}
