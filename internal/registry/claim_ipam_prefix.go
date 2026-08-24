package registry

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per claim kind, so adding one is a new file and never an edit to shared logic.
func init() { MustRegisterClaim(netBoxPrefixClaimDescriptor()) }

// netBoxPrefixClaimDescriptor is "carve one child ipam.Prefix out of a container" as data.
//
// The easy one of NBO-064's two kinds, and it is easy for exactly one reason:
// `POST ipam/prefixes/{id}/available-prefixes/` is advisory-locked
// (`AvailablePrefixesView.advisory_lock_key = 'available-prefixes'`, netbox/ipam/api/views.py)
// just like `available-ips`, so its safety story is NBO-036's with a different sub-path and a
// different result field. Nothing in internal/reconciler knows this kind exists.
func netBoxPrefixClaimDescriptor() ClaimDescriptor {
	return ClaimDescriptor{
		GVK:      netboxv1alpha1.GroupVersion.WithKind("NetBoxPrefixClaim"),
		Endpoint: "ipam/prefixes",

		// The model the claim allocates. It is also NetBoxPrefix's own ObjectType, and that is
		// not a conflict: a ClaimDescriptor is not a Descriptor and claims no Kind's endpoint --
		// Registry.Add would reject a second claimant of `ipam.prefix`, which is why claims have
		// a registry of their own (docs/decisions/0004-claims-first-allocation.md).
		ObjectType: "ipam.prefix",

		Pool: Field{
			Spec:   "parentPrefixRef",
			Class:  ClassRefOne,
			Target: netboxv1alpha1.PrefixRef{}.TargetGVK(),
		},
		PoolValueField: "prefix",
		PoolSubPath:    "available-prefixes",

		// The allocation parameter, and the whole of what makes this request a request.
		// NetBox's PrefixLengthSerializer refuses a body without it by name, and refuses a
		// string by type -- so it passes through as the integer the CRD declares.
		RequestFields: []Field{{Spec: "prefixLength", API: "prefix_length"}},

		// And it is the length checked against the resolved parent before anything is POSTed:
		// a /16 asked for out of a /16 is accepted by NetBox and creates a duplicate of the
		// container (see RequestLengthField).
		RequestLengthField: "prefix_length",

		// `mark_utilized` means the same thing here as it does for an address claim: NetBox's
		// utilisation gauge is forced to 100% while `available-prefixes` still hands out a
		// child, so the flag is the NetBox operator saying the free space is delegated
		// elsewhere and honouring it is this operator's job.
		PoolMustNotBeTrue: []string{"mark_utilized"},

		// **Empty, and that is the asymmetry NBO-064 exists to get right.** `status: container`
		// is a refusal for a NetBoxIPAddressClaim -- a container's free space is subdivided by
		// child prefixes rather than populated by addresses -- and it is what this kind expects
		// to find, because subdividing is what it does. The same value cannot be a rule in
		// shared code, which is why both lists are data.
		//
		// Nothing else is refused either. NetBox's `available-prefixes` view does not consult
		// `status` at all (`get_available_prefixes` subtracts the child prefixes and nothing
		// more), so a refusal here would be this operator inventing a rule the server does not
		// have.
		PoolForbiddenStatus: nil,

		// Allocating out of a non-container is legitimate and unusual: somebody is subdividing
		// a network that is already in service. So it proceeds and emits a Warning naming what
		// was noticed, rather than refusing a decision the NetBox operator has already
		// recorded.
		PoolExpectedStatus: []string{string(netboxv1alpha1.PrefixStatusContainer)},

		ResultField: "prefix",

		// ipam.Prefix is a PrimaryModel (docs/netbox-schema.md -> ipam.Prefix, bases), which
		// mixes in both TagsMixin and CustomFieldsMixin -- and `AvailablePrefixesView` passes
		// the request body through to the full PrefixSerializer after injecting `prefix` and
		// `vrf`, so the provenance stamp and the allocation identity ride along on the atomic
		// call.
		Taggable:        true,
		CustomFieldable: true,
	}
}
