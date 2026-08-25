package registry

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per claim kind, so adding one is a new file and never an edit to shared logic.
func init() { MustRegisterClaim(netBoxIPRangeClaimDescriptor()) }

// netBoxIPRangeClaimDescriptor is "reserve N consecutive addresses inside a prefix" as data.
//
// The kind with no allocation endpoint. NetBox 4.6.8 offers exactly three allocation paths
// (netbox/ipam/api/urls.py): `prefixes/{id}/available-ips/`,
// `prefixes/{id}/available-prefixes/` and `ip-ranges/{id}/available-ips/`. None of them places
// a *range*, so `place-ip-range` is not a NetBox URL: the placement is computed by this
// operator and committed with a plain `POST ipam/ip-ranges/`, and the guarantee it leans on is
// `IPRange.clean()` rejecting an overlap rather than an advisory lock (netbox.PlaceRange).
//
// Every other line here is the same shape as the other two kinds', which is the property that
// matters: the identity, the search, the read-after-write, the exhaustion tier and the
// reporting are shared code, and only the mechanism differs.
func netBoxIPRangeClaimDescriptor() ClaimDescriptor {
	return ClaimDescriptor{
		GVK:      netboxv1alpha1.GroupVersion.WithKind("NetBoxIPRangeClaim"),
		Endpoint: "ipam/ip-ranges",

		// The model the claim allocates, which is also NetBoxIPRange's -- and is here for the
		// provenance bootstrap: the custom field carrying the allocation identity has to list
		// `ipam.iprange` in its object_types or the creating POST is a 400 naming a field the
		// user can see in the UI.
		ObjectType: "ipam.iprange",

		// The pool is a *prefix*, not a range: this claim reserves space inside a network. The
		// created ipam.IPRange is the result, and `ipam/ip-ranges` above is where it lives.
		Pool: Field{
			Spec:   "parentPrefixRef",
			Class:  ClassRefOne,
			Target: netboxv1alpha1.PrefixRef{}.TargetGVK(),
		},
		PoolValueField: "prefix",
		PoolSubPath:    "place-ip-range",

		// The placement inputs. Both API names are `@`-prefixed because neither is a NetBox
		// column: `size` is derived by NetBox from the two endpoints and would be silently
		// dropped, and `alignment` is not a NetBox concept at all. The client removes them from
		// the body and refuses to send one that still carries an `@` key
		// (netbox.PlacementSize, netbox.PlacementAlignment).
		RequestFields: []Field{
			{Spec: "size", API: "@size"},
			{Spec: "alignment", API: "@alignment"},
		},

		// The parent's `mark_utilized`, for the same reason as the other two kinds: the flag
		// says the free space here is not really free.
		PoolMustNotBeTrue: []string{"mark_utilized"},

		// Nothing is refused. A range inside a container prefix is ordinary -- a DHCP scope in
		// a network that is subdivided elsewhere -- and a range inside an `active` prefix is the
		// common case, so there is neither a refusal nor an expectation to record here. The
		// contrast with the address claim's single-entry list is the point: these are two
		// lists of data, not two branches.
		PoolForbiddenStatus: nil,
		PoolExpectedStatus:  nil,

		// The first address, which is what the engine's guard clause reads and what its
		// read-after-write proves is inside the parent. The end address and the size are
		// derived on the CR from this and `spec.size`, which NetBox's own derived `size` has
		// already been checked against (NetBoxIPRangeClaim.SetAllocated).
		ResultField: "start_address",

		// ipam.IPRange is a PrimaryModel (docs/netbox-schema.md -> ipam.IPRange, bases), so an
		// ordinary create carries the whole provenance stamp and the allocation identity in the
		// same call -- which is what makes a lost response recoverable on the one path that has
		// no lock behind it.
		Taggable:        true,
		CustomFieldable: true,
	}
}
