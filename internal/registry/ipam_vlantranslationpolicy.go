package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamVLANTranslationPolicyDescriptor()) }

// ipamVLANTranslationPolicyDescriptor is ipam.VLANTranslationPolicy as data.
//
// **The identity is hand-declared, and this is the derivation.** The committed IR records
// `natural_keys: []` for this model (hack/testdata/ir-4.6.8.json.gz ->
// ipam.VLANTranslationPolicy), which is not a statement that the model has no identity: the
// extractor builds that list from `meta.constraints` alone, and ipam.VLANTranslationPolicy
// declares none -- its only table-level line is `meta.ordering: ('name',)`. The uniqueness is
// one level down, on the column:
//
//	name  CharField  REQ UNIQUE len=100
//
// (docs/netbox-schema.md -> ipam.VLANTranslationPolicy; the same fact is in the IR as
// `fields[name].sql.unique: true`.) internal/registry/coverage_test.go's irSQL.Unique comment
// says this in general terms -- "the uniqueness `natural_keys` does not carry ... a model whose
// identity is one UNIQUE column has an empty one" -- and dcim.Interface carries a hand-written
// NaturalKeys block for the neighbouring reason, its constraint living on an abstract base.
// One candidate, no null pin, nothing conditional.
//
// Getting this wrong is not a missing feature, it is a silent adoption of somebody else's
// object (#206, #216), so it is worth naming what was *not* used. There is no `slug` column on
// this model at all, unlike every OrganizationalModel in the catalogue, so `name` is not a
// second-best choice -- it is the only unique column there is. And `description` is not a
// second candidate: a kind gets one identity, and `name` is required and unique, so a lookup
// that found nothing means the policy does not exist and should be created.
//
// The filter is registered: `name` is in `VLANTranslationPolicyFilterSet`'s `meta.fields`
// (`('id', 'name', 'description')`, NetBox 4.6.8 `netbox/ipam/filtersets.py:1167`), which the
// IR records as a `MultiValueCharFilter` with `lookup_expr: exact`.
//
// **No provenance stamp, and it is the serializer that decides that.** The model is a
// PrimaryModel, so it mixes in TagsMixin and CustomFieldsMixin like any other; but
// `VLANTranslationPolicySerializer.Meta.fields` is written out longhand as
// `('id', 'url', 'display', 'name', 'description', 'display', 'rules', 'owner', 'comments')`
// (NetBox 4.6.8, `netbox/ipam/api/serializers_/vlans.py:123`) and neither `tags` nor
// `custom_fields` survives it. Both flags are therefore false, and coverage_test.go checks
// them against exactly that serializer rather than against the model. The consequence is real
// and worth stating: this kind cannot be adopted by stamp, only by natural key, so the
// `name` above is the whole of how the operator recognises its own policy.
func ipamVLANTranslationPolicyDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVLANTranslationPolicy"),
		Endpoint:   "ipam/vlan-translation-policies",
		ObjectType: "ipam.vlantranslationpolicy",
		Scope:      apiextensionsv1.NamespaceScoped,

		// False for the reason the doc comment gives -- the serializer drops both columns --
		// and not because the model lacks the mixins. Flipping either would make every write
		// carry a field NetBox ignores, which is a silent no-op rather than an error.
		Taggable:        false,
		CustomFieldable: false,

		// Three columns. `owner` is the fourth writable one and has no Kind (users/* is an
		// excluded endpoint), and `rules` is the reverse relation, read-only on the serializer
		// and materialised as child CRs instead.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, from the column-level UNIQUE. See the doc comment for the derivation
		// and for why there is no second.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// No RetainOnDelete, and it is the ipam.VLANGroup exception rather than an oversight
		// (#176, #186). The rule decision #176 turns on is whether deletion destroys *state*: a
		// translation policy is a table of rewrites, recreated verbatim from the manifest, and
		// deleting one frees no address, no VLAN ID and no range. It belongs with the
		// configuration kinds, and it has to: the ticket's own acceptance criterion is that
		// deleting the policy CR leaves nothing behind in NetBox, which Retain would make
		// impossible.
		//
		// Deleting it is still often refused, and by design -- both interface kinds point here
		// with `on_delete=PROTECT`, so a policy in use comes back as
		// Deleting=False, Reason=Protected rather than taking the interfaces with it
		// (docs/concepts/deletion.md).

		// No ContainmentRef. The only foreign key on this model is `owner`, which the operator
		// does not map, so there is no FK the server cascades and therefore no containment
		// parent (docs/decisions/0003-ownership-and-references.md rule 4). The cascade in this
		// pair runs the other way: it is the *rule* that names this policy as its parent.

		// `url` and `display` are the hyperlinks every serializer carries. `created` and
		// `last_updated` are deliberately absent from this list, unlike on every other kind:
		// they are not in this serializer's `Meta.fields` at all, so there is no column here to
		// guard against. `rules` is the one that matters -- it is declared
		// `VLANTranslationRuleSerializer(many=True, read_only=True)`
		// (netbox/ipam/api/serializers_/vlans.py:123), so a write to it is dropped in silence
		// and would be re-sent on every reconcile.
		ReadOnly: []string{"url", "display", "rules"},
	}
}
