package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamIPAddressDescriptor()) }

// ipamIPAddressDescriptor is ipam.IPAddress as data.
//
// The first kind with a polymorphic foreign key on a shipped CRD, and the first whose
// identity is not a uniqueness constraint at all: ipam.IPAddress has **no**
// `meta.constraints` (docs/netbox-schema.md -> ipam.IPAddress lists only indexes -- an
// `(address, id)` index, a host-cast index, and one on the assignment pair). So there is no
// database uniqueness to key on, and the natural keys below are a convention that NetBox
// enforces at the application layer through `ipam.VRF.enforce_unique` -- or does not enforce
// at all, which is what spec.allowDuplicate exists for.
func ipamIPAddressDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIPAddress"),
		Endpoint:   "ipam/ip-addresses",
		ObjectType: "ipam.ipaddress",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.IPAddress is a PrimaryModel (docs/netbox-schema.md -> ipam.IPAddress,
		// bases: ContactsMixin, PrimaryModel), which mixes in both TagsMixin and
		// CustomFieldsMixin, so it carries the whole provenance stamp. Load-bearing twice
		// over here: the stamp is also this kind's identity under spec.allowDuplicate.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain. Deleting an address frees it for
		// reallocation, and if a claim allocated it (ADR-0004) that hands somebody else an
		// address this cluster believes it owns.
		RetainOnDelete: true,

		// Decision #177, answered B. The spec field that makes several matches legal, and
		// substitutes the provenance stamp for the uniqueness NetBox may not be enforcing.
		DuplicateSpec: "allowDuplicate",

		// CR spec names on the left, NetBox API names on the right.
		//
		// `role` is a value and not a reference, which is the one entry in this table worth
		// reading twice: the same JSON key is a `ForeignKey -> ipam.Role` on ipam.Prefix and
		// ipam.VLAN (docs/netbox-schema.md), so those kinds get `roleRef` with
		// ClassRefOne and this one gets a choice string.
		//
		// `assignedObject` is not here at all. One spec field writes two columns, so it is
		// declared on GenericFKs below -- a Field maps one name to one name.
		Fields: []Field{
			{Spec: "address", API: "address"},
			{Spec: "status", API: "status"},
			{Spec: "role", API: "role"},
			{Spec: "dnsName", API: "dns_name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "vrfRef", API: "vrf", Class: ClassRefOne,
				Target: netboxv1alpha1.VRFRef{}.TargetGVK(),
			},
			// Self-referential, and deliberately *not* in Deferred. The engine already
			// leaves an unresolved reference out of the payload, so DeferIfUnresolved would
			// strip nothing (deferral.strip) and change no write -- while a deferral does
			// change one thing: resolver.blocking stops following the edge, and a mutual
			// pair of addresses each naming the other would then be created rather than
			// reported. NBO-025 asks for the opposite, RefCycle, so the field is an
			// ordinary reference and the cycle check sees it.
			{
				Spec: "natInsideRef", API: "nat_inside", Class: ClassRefOne,
				Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
			},
		},

		// The `assigned_object` pair, with the three targets NetBox accepts. No Cached
		// columns: ipam.IPAddress maintains no denormalised caches from this pair, unlike
		// CachedScopeMixin's `_site` and friends.
		GenericFKs: []GenericFKSpec{{
			TypeField:    "assigned_object_type",
			IDField:      "assigned_object_id",
			Spec:         "assignedObject",
			AllowedTypes: []string{"dcim.interface", "virtualization.vminterface", "ipam.fhrpgroup"},
			Members: []GenericFKMember{
				{Spec: "interfaceRef", Target: netboxv1alpha1.InterfaceRef{}.TargetGVK()},
				{Spec: "vmInterfaceRef", Target: netboxv1alpha1.VMInterfaceRef{}.TargetGVK()},
				{Spec: "fhrpGroupRef", Target: netboxv1alpha1.FHRPGroupRef{}.TargetGVK()},
			},
		}},

		// Two candidates, and the second is why NullField exists. `vrf_id` merely *omitted*
		// matches the same address in every VRF, so a global address would adopt a
		// per-VRF one -- and putting `10.0.10.1/24` in a per-house VRF is the normal shape
		// rather than a corner case.
		//
		// The assignment is not a third, narrower candidate, although ipam.IPAddress's own
		// index `(assigned_object_type, assigned_object_id)` invites one: a natural key
		// filtering on a polymorphic pair needs two filters and a resolved union offers no
		// single value, so params() would refuse such a candidate loudly
		// (docs/concepts/generic-refs.md, "It is not usable in a natural key yet"). Two
		// VRRP addresses sharing an address and a VRF are therefore told apart by
		// spec.allowDuplicate and the provenance stamp, which is what decision #177
		// answered -- not by their assignment.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "address", Spec: "address"},
					{Filter: "vrf_id", Spec: "vrfRef"},
				},
			},
			{
				Fields: []KeyField{{Filter: "address", Spec: "address"}},
				NullFields: []NullField{
					// A foreign key: NetBox registers only negation on an FK filter, so the pin
					// is the sentinel value rather than a suffix (NBO-206).
					{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. This model has no `_`-prefixed
		// caches and no CounterCacheFields, and `nat_outside` -- the reverse accessor of
		// `nat_inside` -- is excluded by being absent from the spec rather than by being
		// listed here: this list guards the field map, and a column no spec field maps onto
		// cannot reach a payload.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
