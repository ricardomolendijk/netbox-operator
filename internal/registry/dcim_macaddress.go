package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/utils/ptr"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimMACAddressDescriptor()) }

// dcimMACAddressDescriptor is dcim.MACAddress as data.
//
// The second kind with a polymorphic foreign key on a shipped CRD, and the first whose union
// is *narrower* than an existing one. `MACAssignment` names `dcim.interface` and
// `virtualization.vminterface` and stops there, because that is what NetBox permits:
// `MACADDRESS_ASSIGNMENT_MODELS = Q(app_label='dcim', model='interface') |
// Q(app_label='virtualization', model='vminterface')` (netbox/dcim/constants.py:156-159),
// applied to the serializer's `assigned_object_type` queryset
// (netbox/dcim/api/serializers_/devices.py:318). `ipam.fhrpgroup` is legal for an IP address
// and illegal for a MAC.
//
// Reusing ipam.IPAddress's three-member `IPAssignment` and merely narrowing AllowedTypes
// here would not work: validateUnionTypes cross-checks every member whose Kind is registered
// against AllowedTypes and returns ErrMemberTypeNotAllowed, which fails the boot of the whole
// manager. It would pass today only because NetBoxFHRPGroup does not exist yet, and break the
// day NBO-055 adds it. What *is* reused is everything that matters: the two typed ref aliases,
// GenericFKSpec, the resolver's dispatch table, the atomic pair, the ref watches and the CEL
// shape (docs/concepts/generic-refs.md).
//
// The other half of this kind is that **NetBox enforces no identity for it at all.**
// dcim.MACAddress declares no `meta.constraints`, only two indexes -- `(mac_address, id)` for
// the default ordering and `(assigned_object_type, assigned_object_id)`
// (netbox/dcim/models/devices.py:1380-1385). Duplicate MACs are legal, including on one
// interface. The natural key below is a convention, and the engine already has exactly one
// answer for a convention that does not hold: netbox.Client.Get returns an *AmbiguousError
// naming every match rather than choosing (internal/netbox/client.go), the engine passes it
// through as Conflict, and nothing is written. No per-kind helper, and no DuplicateSpec
// either -- unlike ipam.IPAddress, nothing here asks for several rows to be legal at once.
func dcimMACAddressDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxMACAddress"),
		Endpoint:   "dcim/mac-addresses",
		ObjectType: "dcim.macaddress",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.MACAddress is a PrimaryModel (netbox/dcim/models/devices.py:1360), which mixes
		// in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `assignedObject` is not here. One spec field writes two columns, so it is declared
		// on GenericFKs below -- a Field maps one name to one name.
		Fields: []Field{
			{Spec: "macAddress", API: "mac_address"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// The `assigned_object` pair. No Cached columns: dcim.MACAddress maintains no
		// denormalised caches from it, unlike CachedScopeMixin's `_site` and friends.
		//
		// Both members cascade, and by the *first* of the two mechanisms ScopeFK's comment
		// describes: `mac_addresses` is a `GenericRelation` on dcim.Interface
		// (netbox/dcim/models/device_components.py:966-971) and on
		// virtualization.VMInterface (netbox/virtualization/models/virtualmachines.py:507-512),
		// so deleting either interface deletes its MAC addresses server-side. There is no
		// denormalised CASCADE column to read as well, because there is no cache here at all.
		GenericFKs: []GenericFKSpec{{
			TypeField:    "assigned_object_type",
			IDField:      "assigned_object_id",
			Spec:         "assignedObject",
			AllowedTypes: []string{"dcim.interface", "virtualization.vminterface"},
			Members: []GenericFKMember{
				{Spec: "interfaceRef", Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(), CascadeOnDelete: ptr.To(true)},
				{Spec: "vmInterfaceRef", Target: netboxv1alpha1.VMInterfaceRef{}.TargetGVK(), CascadeOnDelete: ptr.To(true)},
			},
		}},

		// Two candidates, and the pair is filtered by its two *column* names because a union
		// has no single value to offer one filter (#180, docs/concepts/generic-refs.md,
		// "Natural keys"). `MACAddressFilterSet` takes both: `assigned_object_type` is a
		// MultiValueContentTypeFilter and `assigned_object_id` an ordinary filter from
		// Meta.fields (netbox/dcim/filtersets.py:2031, :2086).
		//
		// The second candidate is the unattached MAC, and the pin is on the *id* half only.
		// The type half cannot be pinned: `assigned_object_type` is a ForeignKey to
		// contenttypes.ContentType filtered by MultiValueContentTypeFilter, for which NetBox
		// registers neither the sentinel nor `__empty`, and the sentinel is worse than dropped
		// -- it makes the request match nothing and the engine create a duplicate. Pinning the
		// paired `_id` asks the same question, because NetBox rejects one half of the pair
		// without the other (naturalkey.go, knownNullColumns). `assigned_object_id` is a plain
		// PositiveBigIntegerField (netbox/dcim/models/devices.py:1371-1374), hence
		// NullColumnNumeric and `?assigned_object_id__empty=true` rather than the sentinel a
		// foreign key takes.
		//
		// A `mac_address` filter with the assignment merely *omitted* is not a third
		// candidate: duplicate MACs across interfaces are the normal shape, so it would match
		// every copy of the address in NetBox and report Conflict where the narrower candidate
		// would have found the one row this CR describes.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "assigned_object_type", Spec: "assigned_object_type"},
					{Filter: "assigned_object_id", Spec: "assigned_object_id"},
					{Filter: "mac_address", Spec: "macAddress"},
				},
			},
			{
				Fields: []KeyField{{Filter: "mac_address", Spec: "macAddress"}},
				NullFields: []NullField{
					{Filter: "assigned_object_id", Spec: "assignedObject", Column: NullColumnNumeric},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// `assignedObject` is the containment parent: both union members cascade, so a MAC
		// gets a non-controller owner reference to whichever interface it actually resolved
		// through and `kubectl delete` on that interface takes the MAC with it
		// (ADR-0003 rule 4, and #214 for why the decision is per member and per pass rather
		// than per Kind).
		//
		// It is the only reference this Kind has, so there is no tiebreak to make. And it is
		// load-bearing rather than tidy: the *second* natural-key candidate stays applicable
		// when the union is undeclared, so a MAC CR outliving the interface NetBox
		// cascade-deleted would find nothing on `?mac_address=...&assigned_object_id__empty=true`
		// and the create-if-absent step would recreate it -- unattached, which is not what
		// anybody wrote. Exactly the resurrection #203 found on NetBoxTenantGroup.
		ContainmentRef: "assignedObject",

		// The four columns every ChangeLoggedModel carries. `is_primary` is a cached_property
		// on the model rather than a column (netbox/dcim/models/devices.py:1399-1404) and is
		// excluded by being absent from the spec: this list guards the field map, and a column
		// no spec field maps onto cannot reach a payload.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
