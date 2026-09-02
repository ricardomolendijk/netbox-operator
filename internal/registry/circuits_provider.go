package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(circuitsProviderDescriptor()) }

// circuitsProviderDescriptor is circuits.Provider as data.
//
// The identity derivation is the one worth reading, because circuits.Provider declares **no
// `meta.constraints` at all** (docs/netbox-schema.md -> circuits.Provider; its `meta` carries
// only `ordering: ['name']`), and the committed IR agrees -- `natural_keys: []`
// (hack/testdata/ir-4.6.8.json.gz). So the key comes from the model's own column-level uniques
// instead:
//
//	name  CharField  REQ UNIQUE len=100
//	slug  SlugField  REQ UNIQUE len=100
//
// One candidate, no null pin, no reference in the key. Identical to dcim.Site's, dcim.Manufacturer's
// and NBO-051's dcim.RackRole -- and note that here it is the *model* rather than a base class
// that declares the two uniques, so this is the same shape arrived at by a different route.
//
// `name` is UNIQUE too and deliberately not a second candidate: a kind gets one identity, `slug`
// is the stable one, and a colliding rename comes back as NetBox's own 409 rather than being
// adopted under the other key.
//
// The filter is registered: `slug` is in `ProviderFilterSet.meta_fields` (NetBox 4.6.8,
// `circuits/filtersets.py:38`, recorded in the IR as `from: meta.fields`). Checked rather than
// assumed for the reason #206 exists -- django-filter drops a parameter it does not recognise
// and answers with the *unfiltered* set, so a guessed filter name is a lookup that matches every
// provider in NetBox.
func circuitsProviderDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxProvider"),
		Endpoint:   "circuits/providers",
		ObjectType: "circuits.provider",
		Scope:      apiextensionsv1.NamespaceScoped,

		// circuits.Provider is a PrimaryModel (docs/netbox-schema.md -> circuits.Provider,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp. ContactsMixin, the other base, contributes only a GenericRelation.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				// The one to-many reference on this kind. ClassRefMany rather than
				// ClassRefOne is what makes the comparison an order-independent id set:
				// NetBox does not preserve M2M order, so an order-sensitive comparison would
				// PATCH forever (fields.go, ClassRefMany).
				//
				// No CascadeOnDelete: `asns` is a ManyToManyField with no `on_delete` at all
				// (hack/testdata/ir-4.6.8.json.gz -> circuits.Provider, field `asns`,
				// `ref.on_delete: null`), so deleting an ASN removes the row from the join
				// table and leaves the provider standing. Nothing to mirror as an owner
				// reference, and a to-many containment parent is refused at boot anyway
				// (ErrContainmentToMany).
				Spec: "asns", API: "asns", Class: ClassRefMany,
				Target: netboxv1alpha1.ASNRef{}.TargetGVK(),
			},
		},

		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: circuits.Provider has no foreign key at all bar `owner`, which has
		// no Kind, so there is nothing that could be a containment parent
		// (docs/decisions/0003-ownership-and-references.md rule 4). Every reference pointing
		// *at* it -- ProviderAccount.provider, ProviderNetwork.provider, Circuit.provider -- is
		// `on_delete=PROTECT`, so deleting a provider in use is refused rather than cascading,
		// reported here as Deleting=False, Reason=Protected.
		//
		// RetainOnDelete is left false: a provider is configuration a manifest can recreate,
		// not allocated state, so `deletionPolicy` defaults to Delete (#176,
		// docs/concepts/deletion.md).

		// The four columns every ChangeLoggedModel carries, plus the counter this serializer
		// returns. NetBox maintains `circuit_count` from the circuits pointing here and refuses
		// an attempt to set it (docs/netbox-schema.md, preamble on every CounterCacheField), so
		// writing it silently no-ops and the engine would PATCH it forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "circuit_count"},
	}
}
