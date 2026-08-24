package registry

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per claim kind, so adding one is a new file and never an edit to shared logic.
func init() { MustRegisterClaim(netBoxIPAddressClaimDescriptor()) }

// netBoxIPAddressClaimDescriptor is "allocate one ipam.IPAddress out of an ipam.Prefix" as
// data.
//
// The whole of what makes this claim kind different from NBO-064's two: which sub-path to
// POST to, which field of the answer is the result, and which states of the pool are a
// refusal. Everything else -- the identity, the search, the read-after-write, the
// exhaustion tier, the reporting -- is in internal/reconciler and is the same for all
// three, which is the property that keeps there being exactly one allocation engine
// (docs/decisions/0004-claims-first-allocation.md).
func netBoxIPAddressClaimDescriptor() ClaimDescriptor {
	return ClaimDescriptor{
		GVK:      netboxv1alpha1.GroupVersion.WithKind("NetBoxIPAddressClaim"),
		Endpoint: "ipam/ip-addresses",

		// The model the claim allocates, not the claim itself: a NetBoxIPAddressClaim is a
		// Kubernetes object with no NetBox counterpart of its own. It is here so the
		// provenance bootstrap declares `ipam.ipaddress` on the custom field carrying the
		// allocation identity -- without which the first allocating POST on a fresh NetBox
		// is a 400.
		ObjectType: "ipam.ipaddress",

		Pool: Field{
			Spec:   "prefixRef",
			Class:  ClassRefOne,
			Target: netboxv1alpha1.PrefixRef{}.TargetGVK(),
		},
		PoolValueField: "prefix",
		PoolSubPath:    "available-ips",

		// `mark_utilized` only forces NetBox's utilisation gauge to 100%; `available-ips`
		// still hands out an address (docs/netbox-schema.md -> ipam.Prefix,
		// `mark_utilized BooleanField def=False`). The flag is the NetBox operator saying the
		// free space here is not really free -- it is delegated to DHCP or to another IPAM --
		// so refusing is the operator's job.
		PoolMustNotBeTrue: []string{"mark_utilized"},

		// A container's free space is subdivided by child prefixes rather than populated by
		// addresses, so a bare address out of one is almost always a mistake. There is no
		// override: the escape hatch is a child prefix (NBO-064) or a NetBoxIPAddress with
		// an explicit address.
		//
		// Deliberately *not* including `reserved` or `deprecated`. Both are ordinary
		// operational states -- a reserved prefix is one somebody is holding, and holding it
		// is done by allocating out of it -- and refusing them would make the operator
		// second-guess a decision the NetBox operator has already recorded.
		PoolForbiddenStatus: []string{string(netboxv1alpha1.PrefixStatusContainer)},

		ResultField: "address",

		// ipam.IPAddress is a PrimaryModel (docs/netbox-schema.md -> ipam.IPAddress,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin -- so the allocating
		// POST carries the whole provenance stamp *and* the allocation identity in one
		// atomic call.
		Taggable:        true,
		CustomFieldable: true,
	}
}
