package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(circuitsProviderAccountDescriptor()) }

// circuitsProviderAccountDescriptor is circuits.ProviderAccount as data.
//
// **Two constraints, one candidate**, and the discarded one is the point of this kind
// (docs/netbox-schema.md -> circuits.ProviderAccount.meta.constraints):
//
//	UniqueConstraint(fields=('provider', 'account'), name='..._unique_provider_account')
//	UniqueConstraint(fields=('provider', 'name'),    name='..._unique_provider_name',
//	                 condition=~Q(name=''))
//
// The second carries a `condition=` that is **not a null pin**. A null pin the operator can
// reproduce -- `?location_id=null` is a filter NetBox understands, which is how NBO-051's
// dcim.Rack expresses `location IS NULL`. The condition on the second constraint is not that:
// it excludes rows whose `name` is the empty string, and there is no NetBox filter for "and this
// column is not the empty string". A candidate that drops the condition would match the
// *unconstrained* set, and on a kind that adopts what it finds that is #206 and #216 exactly.
//
// The extractor reaches the same conclusion independently and records it, which is why this is
// evidence rather than an opinion:
//
//	"unusable": "constraint condition is more than a null pin: ['name']"
//	                                (hack/testdata/ir-4.6.8.json.gz -> circuits.ProviderAccount)
//
// and docs/coverage.md carries the row `circuits.ProviderAccount | ... | unusable | constraint
// condition is more than a null pin: ['name']`.
//
// Both filters behind the surviving candidate are registered: `provider_id` is declared on
// `ProviderAccountFilterSet` and `account` is in its `meta_fields` (NetBox 4.6.8,
// `circuits/filtersets.py:103`).
func circuitsProviderAccountDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxProviderAccount"),
		Endpoint:   "circuits/provider-accounts",
		ObjectType: "circuits.provideraccount",
		Scope:      apiextensionsv1.NamespaceScoped,

		// circuits.ProviderAccount is a PrimaryModel (docs/netbox-schema.md, bases), so it
		// mixes in both TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "account", API: "account"},
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "providerRef", API: "provider", Class: ClassRefOne,
				Target: netboxv1alpha1.ProviderRef{}.TargetGVK(),
				// PROTECT, so no cascade to declare. Stated by omission rather than by a false
				// flag: CascadeOnDelete is read off the Django field's own `on_delete`.
			},
		},

		// One candidate, and `providerRef` is not deferred and cannot be: the candidate matches
		// on it, so stripping it from a create would mean the lookup asked a different question
		// from the create it decided on (registry.ErrDeferredNaturalKey). With `providerRef`
		// unresolved there is no applicable candidate at all, so the object writes nothing
		// rather than being created without its required column.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "provider_id", Spec: "providerRef"},
					{Filter: "account", Spec: "account"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef. `provider` is the required foreign key, but it is
		// `on_delete=PROTECT` (docs/netbox-schema.md -> circuits.ProviderAccount): NetBox
		// refuses to delete a provider while an account points at it, so nothing cascades and
		// there is no server-side deletion for an owner reference to mirror
		// (docs/decisions/0003-ownership-and-references.md rule 4).

		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
