package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(circuitsProviderNetworkDescriptor()) }

// circuitsProviderNetworkDescriptor is circuits.ProviderNetwork as data.
//
// The simplest identity in the provider family, and the useful contrast with
// circuits.ProviderAccount's: one constraint, unconditional, so one candidate and no pin
// (docs/netbox-schema.md -> circuits.ProviderNetwork.meta.constraints):
//
//	UniqueConstraint(fields=('provider', 'name'), name='..._unique_provider_name')
//
// The name is the same `..._unique_provider_name` circuits.ProviderAccount's *unusable* second
// constraint carries, and the difference is the whole of it: this one has no `condition=`
// clause, so it can be reproduced as a filter pair exactly as written. The extractor records it
// with `unusable: null` (hack/testdata/ir-4.6.8.json.gz).
//
// `service_id` is deliberately not a candidate and not a tiebreak: it carries no UNIQUE of any
// kind, so a filter on it can match any number of rows.
//
// Both filters are registered: `provider_id` is declared on `ProviderNetworkFilterSet` and
// `name` is in its `meta_fields` (NetBox 4.6.8, `circuits/filtersets.py:133`).
func circuitsProviderNetworkDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxProviderNetwork"),
		Endpoint:   "circuits/provider-networks",
		ObjectType: "circuits.providernetwork",
		Scope:      apiextensionsv1.NamespaceScoped,

		// circuits.ProviderNetwork is a PrimaryModel and nothing else -- the one kind in this
		// family with no ContactsMixin (docs/netbox-schema.md, bases) -- so it mixes in both
		// TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `serviceId` -> `service_id` is the entry that earns an explicit table: NetBox ignores
		// a field name it does not know rather than rejecting it, so `serviceId` on the wire
		// would write nothing and report success.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "serviceId", API: "service_id"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "providerRef", API: "provider", Class: ClassRefOne,
				Target: netboxv1alpha1.ProviderRef{}.TargetGVK(),
				// PROTECT, so no cascade to declare.
			},
		},

		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "provider_id", Spec: "providerRef"},
					{Filter: "name", Spec: "name"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: `provider` is `on_delete=PROTECT`, so nothing cascades
		// (docs/decisions/0003-ownership-and-references.md rule 4).

		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
