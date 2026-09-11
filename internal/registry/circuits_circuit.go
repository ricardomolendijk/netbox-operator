package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(circuitsCircuitDescriptor()) }

// circuitsCircuitDescriptor is circuits.Circuit as data.
//
// **Two usable constraints, one declared candidate**, and that narrowing is the one judgement
// call in NBO-057's catalogue slice, so it is argued rather than asserted
// (docs/netbox-schema.md -> circuits.Circuit.meta.constraints):
//
//	UniqueConstraint(fields=('provider', 'cid'),         name='..._unique_provider_cid')
//	UniqueConstraint(fields=('provider_account', 'cid'), name='..._unique_provideraccount_cid')
//
// The IR records both as usable -- `unusable: null` on each
// (hack/testdata/ir-4.6.8.json.gz) -- so unlike circuits.ProviderAccount's second constraint,
// nothing forces the choice. What decides it is that the two are keyed on **different
// references**, which is not the dcim.DeviceType / dcim.RackType shape it superficially
// resembles.
//
// Candidates are tried in order and the second is reached only when the first matched nothing.
// The first is `(provider, cid)`, and `provider` is `REQ`, so it is applicable on every
// reconcile where `providerRef` resolves. For the second to fire at all, NetBox must hold no
// circuit with this provider and this cid -- and then match one with this *provider account* and
// this cid. Since `ProviderAccount.provider` is itself a foreign key, that object is by
// construction a circuit sold by a **different provider**. Adopting it means PATCHing
// `provider`, silently repointing somebody else's circuit. Declining means the create returns
// NetBox's own 409 naming `..._unique_provideraccount_cid`, which says what is wrong.
//
// A permanent 409 is a worse *loop* than an adoption, which is the argument dcim.RackType's
// fallback makes; it is not a worse *outcome* than repointing the wrong object, which is the
// class of defect behind #206 and #216. And the asymmetry matters here in a way it does not
// there: dcim.RackType's two candidates share their leading term, so its fallback can only ever
// find an object with the same manufacturer.
//
// Natural keys are Descriptor data rather than CRD shape, so this is reversible: adding the
// second candidate later is not a breaking change to a shipped `v1alpha1`. Shipping it now and
// discovering it adopts wrongly would not be reversible for the objects it touched.
//
// Every filter is registered (NetBox 4.6.8, `circuits/filtersets.py:171`): `provider_id` is
// declared on `CircuitFilterSet`, and `cid` is in its `meta_fields`. `provider_account_id` is
// declared too -- over the column `provider_account` -- so the discarded candidate is discarded
// on the argument above and not for want of a filter.
func circuitsCircuitDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCircuit"),
		Endpoint:   "circuits/circuits",
		ObjectType: "circuits.circuit",
		Scope:      apiextensionsv1.NamespaceScoped,

		// circuits.Circuit is a PrimaryModel (docs/netbox-schema.md, bases), so it mixes in
		// both TagsMixin and CustomFieldsMixin and carries the whole provenance stamp.
		// ContactsMixin, ImageAttachmentsMixin and DistanceMixin add a GenericRelation each and
		// DistanceMixin's two columns, which are mapped below.
		Taggable:        true,
		CustomFieldable: true,

		Fields: circuitsCircuitFields(),

		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "provider_id", Spec: "providerRef"},
					{Filter: "cid", Spec: "cid"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef. Every foreign key on this kind is `on_delete=PROTECT` --
		// `provider`, `provider_account`, `type` and `tenant` (docs/netbox-schema.md ->
		// circuits.Circuit) -- and validateContainment refuses a parent that does not cascade,
		// because an owner reference on a PROTECTed FK promises a cluster-side cascade NetBox
		// will refuse to perform (docs/decisions/0003-ownership-and-references.md rule 4).
		//
		// The `CASCADE` in this app runs the other way: `CircuitTermination.circuit` is
		// `on_delete=CASCADE`, so when that Kind ships it takes *this* one as its containment
		// parent. Nothing about that is a fact about this descriptor.

		// The four columns every ChangeLoggedModel carries, plus the three the model returns and
		// refuses to accept.
		//
		// `termination_a` and `termination_z` are here because they are exactly the trap this
		// list exists for: the IR records both as `read_only: true` and they *are* in the
		// serializer's write path, so a payload carrying one is accepted and dropped. Listing
		// them means ErrFieldReadOnly fails the boot if a future field map ever adds them,
		// rather than the engine PATCHing them forever. No spec field maps onto either, so no
		// request body this kind produces can contain them.
		//
		// `_abs_distance` is DistanceMixin's normalised metres, derived from `distance` and
		// `distance_unit` on every save (netbox/netbox/models/mixins.py:108-117), and the IR
		// records it as absent from the write path entirely.
		ReadOnly: []string{
			"created", "last_updated", "url", "display",
			"termination_a", "termination_z", "_abs_distance",
		},
	}
}

// circuitsCircuitFields is this kind's spec-to-column map.
//
// Extracted from the descriptor for length, not because anything about it is dynamic -- the
// dcimRackTypeFields shape.
//
// Three entries earn the explicit table on their own. `cid` is one of the few NetBox column
// names that is *not* what a Go field would be called, so the map is what stops it being
// written as `circuitId`. `installDate` and `terminationDate` are nullable DateFields, and
// NetBox rejects `""` for a DateField outright, so an emptied value has to go over the wire as
// null to clear rather than to fail (#170, the same handling ipam.Aggregate's `date_added`
// gets). `distance` is the same story for a nullable DecimalField; `distanceUnit` is a char
// column that takes the empty string, so it needs no flag.
//
// `commitRate` needs no EmptyIsNull: the CRD field is a `*int32`, so the only two states it has
// are absent and a number -- there is no empty value for the flag to translate.
func circuitsCircuitFields() []Field {
	return []Field{
		{Spec: "cid", API: "cid"},
		{Spec: "status", API: "status"},
		{Spec: "installDate", API: "install_date", EmptyIsNull: true},
		{Spec: "terminationDate", API: "termination_date", EmptyIsNull: true},
		{Spec: "commitRate", API: "commit_rate"},
		{Spec: "distance", API: "distance", EmptyIsNull: true},
		{Spec: "distanceUnit", API: "distance_unit"},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
		{
			// Half of the natural key, so not deferrable: the candidate matches on it, and
			// stripping it from a create would mean the lookup asked a different question from
			// the create it decided on (registry.ErrDeferredNaturalKey).
			Spec: "providerRef", API: "provider", Class: ClassRefOne,
			Target: netboxv1alpha1.ProviderRef{}.TargetGVK(),
		},
		{
			// Required by the column but not in the key, so an unresolved `typeRef` blocks the
			// write without making the lookup impossible.
			Spec: "typeRef", API: "type", Class: ClassRefOne,
			Target: netboxv1alpha1.CircuitTypeRef{}.TargetGVK(),
		},
		{
			// Optional, and not a natural-key candidate -- see the descriptor comment.
			Spec: "providerAccountRef", API: "provider_account", Class: ClassRefOne,
			Target: netboxv1alpha1.ProviderAccountRef{}.TargetGVK(),
		},
		{
			Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
			Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
		},
	}
}
