package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimInterfaceDescriptor()) }

// dcimInterfaceDescriptor is dcim.Interface as data.
//
// This registration is what makes `IPAssignment.interfaceRef` resolvable. The union member
// has named this Kind since NBO-011 and nothing registered it, so `ByObjectType` had no entry
// for `dcim.interface` and every use reported RefKindUnavailable; the ObjectType below is the
// whole of the fix -- the reverse index is built from it in Registry.Add, and
// internal/resolver's RefTargets picks the Kind up as a watch target from there
// (docs/concepts/generic-refs.md, NBO-019).
//
// It is also the largest field map in the registry, and deliberately unremarkable for it:
// three self-referential foreign keys, seven choice columns, two decimals-as-strings, a
// to-many VLAN list and thirteen read-only columns, and none of it is a line of engine code.
func dcimInterfaceDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxInterface"),
		Endpoint:   "dcim/interfaces",
		ObjectType: "dcim.interface",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.ComponentModel is a NetBoxModel, not a PrimaryModel (docs/netbox-schema.md ->
		// dcim.ComponentModel, bases) -- so there is no `comments` column here -- and
		// NetBoxModel still mixes in both TagsMixin and CustomFieldsMixin, so the provenance
		// stamp applies in full.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimInterfaceFields(),

		// One candidate, and it comes from the parent model rather than from this one:
		// `dcim.Interface` lists no meta.constraints of its own -- its only table-level line
		// is `meta.ordering: ('device', CollateAsChar('_name'))` -- and
		// `dcim.ComponentModel` carries
		// `models.UniqueConstraint(fields=('device', 'name'),
		// name='%(app_label)s_%(class)s_unique_device_name')`
		// (docs/netbox-schema.md -> dcim.Interface, dcim.ComponentModel meta.constraints).
		//
		// Two things follow. `device_id` is never omitted: the pair is unique per device and
		// `eth0` is the most-reused interface name there is, so a lookup without it would
		// adopt another device's interface on the first reconcile. And there is no `Lower()`
		// here, unlike both of dcim.Device's own constraints, so the name filter is exact --
		// `Eth0` and `eth0` are two interfaces on one device to NetBox and must be two to the
		// operator.
		//
		// There is no second candidate. Both halves are required fields, so there is no state
		// in which one is missing and a different identity applies.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "device_id", Spec: "deviceRef"},
					{Filter: "name", Spec: "name"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// Four references deferred conditionally, never unconditionally.
		//
		// `lag`, `parent` and `bridge` are the three self-references NBO-030 is about: two
		// interfaces of one device naming each other cannot both be created with the reference
		// in place, so the field comes out of the create and goes in a follow-up PATCH -- but
		// only when it does not already resolve. Unconditional deferral would turn every
		// ordinary sub-interface and every ordinary LAG member into two writes with a visible
		// intermediate state where it is briefly top-level or unbonded, which is the failure
		// DeferIfUnresolved exists for (NBO-015). Each is independent of the other two: a
		// sub-interface of a bonded parent defers `parent` and includes `lag`, or the reverse,
		// according to what has actually been created.
		//
		// `qinq_svlan` is not a self-reference and is deferred for the neighbouring reason: a
		// Q-in-Q service VLAN is usually applied in the same pass as the interfaces that carry
		// it, and NetBox cross-validates it against `mode`, so a create carrying an unresolved
		// reference fails where one that waits succeeds.
		//
		// None of the four is matched on by the natural key, so an unconditional deferral would
		// be legal here too (validateDeferred); conditional is chosen on the merits rather than
		// forced. A pair that genuinely cannot be ordered -- `a.parent = b`, `b.parent = a` --
		// is a cycle, and NBO-016's walk reports RefCycle and writes nothing rather than
		// deferring forever.
		Deferred: []DeferredField{
			{APIField: "lag", Mode: DeferIfUnresolved},
			{APIField: "parent", Mode: DeferIfUnresolved},
			{APIField: "bridge", Mode: DeferIfUnresolved},
			{APIField: "qinq_svlan", Mode: DeferIfUnresolved},
		},

		ReadOnly: dcimInterfaceReadOnly(),

		// The device is the containment parent, which is the same thing `on_delete=CASCADE`
		// says on the NetBox side: `dcim.ComponentModel.device` is the one cascading foreign
		// key this model has (docs/netbox-schema.md -> dcim.ComponentModel,
		// `device ForeignKey REQ -> dcim.Device on_delete=CASCADE`), so `kubectl delete nbdev`
		// takes its hand-written interfaces with it in the same namespace
		// (docs/decisions/0003-ownership-and-references.md rule 4).
		//
		// It is also the only candidate that would pass validateContainment. `parent` is
		// `RESTRICT`, `lag`, `bridge`, `untagged_vlan`, `qinq_svlan` and `vrf` are all
		// `SET_NULL`, and `module` -- which is out of scope -- is the only other CASCADE. So
		// the choice ADR-0003 rule 4 would otherwise leave open is closed by the schema.
		//
		// M5 replaces this with a *controller* owner reference for interfaces the operator
		// materialises from a device's inline list (NBO-034); a hand-written one stays a
		// non-controller owner, because two controllers on one object is the one thing
		// Kubernetes will not allow.
		ContainmentRef: "deviceRef",
	}
}

// dcimInterfaceFields is this kind's spec-to-column map.
//
// Extracted from the descriptor for length, not because anything about it is dynamic. It is
// still a literal.
//
// The entries that earn the explicit table: `mgmtOnly` -> `mgmt_only`, `markConnected` ->
// `mark_connected`, `rfRole` -> `rf_role`, `rfChannelFrequency` -> `rf_channel_frequency`,
// `poeMode` -> `poe_mode`, `taggedVLANs` -> `tagged_vlans`, `qinqSVLANRef` -> `qinq_svlan`,
// `txPower` -> `tx_power` and `wwn` -> `wwn`. A camelCase-to-snake_case convention needs an
// acronym list for four of them and gets `taggedVLANs` wrong as `tagged_v_l_a_ns` -- and
// NetBox ignores a field name it does not know rather than rejecting it, so every one of those
// would write nothing and report success.
//
// `taggedVLANs` is the only ClassRefMany, and the class is the one declaration of both its
// cardinality and its comparison rule: M2MFields() derives the order-independent id-set
// comparison from it (NBO-088).
//
// CascadeOnDelete is true on `deviceRef` alone. Every other reference here is `SET_NULL`,
// `RESTRICT` or `PROTECT` (docs/netbox-schema.md -> dcim.Interface, dcim.BaseInterface), and
// declaring the flag truthfully is what makes ContainmentRef enforceable rather than a
// convention.
func dcimInterfaceFields() []Field {
	return []Field{
		{Spec: "name", API: "name"},
		{Spec: "label", API: "label"},
		{Spec: "type", API: "type"},
		{Spec: "enabled", API: "enabled"},
		{Spec: "mgmtOnly", API: "mgmt_only"},
		{Spec: "markConnected", API: "mark_connected"},
		{Spec: "mtu", API: "mtu"},
		{Spec: "speed", API: "speed"},
		{Spec: "duplex", API: "duplex"},
		{Spec: "wwn", API: "wwn"},
		{Spec: "mode", API: "mode"},
		{Spec: "rfRole", API: "rf_role"},
		{Spec: "rfChannel", API: "rf_channel"},
		{Spec: "poeMode", API: "poe_mode"},
		{Spec: "poeType", API: "poe_type"},
		{Spec: "description", API: "description"},

		// EmptyIsNull on both: nullable DecimalFields, so an emptied one is sent as JSON null
		// rather than as the empty string DRF would reject as a number (#170). `txPower`,
		// `mtu` and `speed` need nothing here -- they are pointers in the spec, so an omitted
		// one is absent from the payload rather than empty in it.
		{Spec: "rfChannelFrequency", API: "rf_channel_frequency", EmptyIsNull: true},
		{Spec: "rfChannelWidth", API: "rf_channel_width", EmptyIsNull: true},

		{Spec: "txPower", API: "tx_power"},

		{
			Spec: "deviceRef", API: "device", Class: ClassRefOne,
			Target: netboxv1alpha1.DeviceRef{}.TargetGVK(), CascadeOnDelete: true,
		},
		{
			Spec: "lagRef", API: "lag", Class: ClassRefOne,
			Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(),
		},
		{
			Spec: "parentRef", API: "parent", Class: ClassRefOne,
			Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(),
		},
		{
			Spec: "bridgeRef", API: "bridge", Class: ClassRefOne,
			Target: netboxv1alpha1.InterfaceRef{}.TargetGVK(),
		},
		{
			Spec: "untaggedVLANRef", API: "untagged_vlan", Class: ClassRefOne,
			Target: netboxv1alpha1.VLANRef{}.TargetGVK(),
		},
		{
			Spec: "taggedVLANs", API: "tagged_vlans", Class: ClassRefMany,
			Target: netboxv1alpha1.VLANRef{}.TargetGVK(),
		},
		{
			Spec: "qinqSVLANRef", API: "qinq_svlan", Class: ClassRefOne,
			Target: netboxv1alpha1.VLANRef{}.TargetGVK(),
		},
		{
			Spec: "vrfRef", API: "vrf", Class: ClassRefOne,
			Target: netboxv1alpha1.VRFRef{}.TargetGVK(),
		},
	}
}

// dcimInterfaceReadOnly are the columns the operator must never write, in three groups.
//
// The four every ChangeLoggedModel carries, plus:
//
// `_name`, `_site`, `_location`, `_rack` and `_path` -- the underscore-prefixed caches.
// `_name` is a NaturalOrderingField NetBox derives from `name` so that `eth10` sorts after
// `eth9`; `_site`, `_location` and `_rack` are denormalised from the device
// (docs/netbox-schema.md -> dcim.Interface, dcim.ComponentModel); `_path` is the cable path
// NetBox recomputes from the cable graph (dcim.PathEndpoint). Every one is maintained
// server-side and dropped on write, so an entry in the field map for any would be a PATCH the
// operator repeats forever.
//
// `cable`, `cable_end`, `cable_connector` and `cable_positions` -- writable columns that
// belong to another Kind. NetBoxCable (NBO-049) owns the cable graph, and a cable is created
// from its own endpoints rather than by an interface claiming one, so these are read-only here
// rather than absent: an interface that adopted a cabled peer must not PATCH the cable away.
// `cable_terminations` beside them is a GenericRelation.
//
// `ip_addresses`, `mac_addresses`, `fhrp_group_assignments`, `tunnel_terminations`,
// `l2vpn_terminations` and `inventory_items` -- GenericRelations, the far end of somebody
// else's generic FK, which is to say a query rather than a column. `ip_addresses` is the one
// worth naming twice: it is the reverse of the `IPAssignment` union this Kind exists to make
// resolvable, so the direction matters. An address points at an interface; an interface does
// not list its addresses.
func dcimInterfaceReadOnly() []string {
	return []string{
		"created", "last_updated", "url", "display",
		"_name", "_site", "_location", "_rack", "_path",
		"cable", "cable_end", "cable_connector", "cable_positions", "cable_terminations",
		"ip_addresses", "mac_addresses", "fhrp_group_assignments",
		"tunnel_terminations", "l2vpn_terminations", "inventory_items",
	}
}
