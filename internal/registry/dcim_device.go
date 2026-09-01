package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimDeviceDescriptor()) }

// dcimDeviceDescriptor is dcim.Device as data.
//
// The kind with **no containment parent at all**, and it is the first one where that is the
// interesting fact rather than an absence. ADR-0003 rule 4 as amended by #202: the
// containment parent is whichever FK the *server* cascades, and dcim.Device has none. Every
// one of its foreign keys is `PROTECT` or `SET_NULL` (docs/netbox-schema.md -> dcim.Device):
//
//	device_type  PROTECT     role      PROTECT     site   PROTECT     tenant   PROTECT
//	platform     SET_NULL    cluster   SET_NULL    oob_ip SET_NULL    primary_ip4/6 SET_NULL
//
// NBO-030's spec names `siteRef` as the single containment ref. That is what the earlier
// reading of rule 4 said -- the required FK -- and validateContainment refuses it at boot
// (ErrContainmentNotCascade), because `site` is `PROTECT`: NetBox declines to delete a site
// that still has devices, so an owner reference there would promise a cluster-side cascade
// with no server-side counterpart. Garbage collection would remove the CR, the finalizer's
// DELETE would be refused, and the row would outlive the object that described it. #202
// removed exactly this `siteRef` from the device fixture for exactly this reason. The
// consequence -- no cascade from a NetBoxSite to its NetBoxDevices -- is not a gap: NetBox
// refuses that deletion anyway (docs/concepts/ownership.md).
//
// `clusterRef` is the containment-*shaped* reference the spec's design note argues about, and
// the argument settles the same way from the other end: `SET_NULL` leaves the device row alive
// with the column cleared, so it does not cascade either. Two owner references would have been
// wrong for the reason the note gives -- Kubernetes garbage collection waits for *every* owner
// -- and here the question does not arise.
func dcimDeviceDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxDevice"),
		Endpoint:   "dcim/devices",
		ObjectType: "dcim.device",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Device is a PrimaryModel (docs/netbox-schema.md -> dcim.Device, bases), which
		// mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance
		// stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimDeviceFields(),

		NaturalKeys: dcimDeviceKeys(),

		UpdateStrategy: UpdatePatch,

		// The ring this exists for is `Device -> IPAddress -> Interface -> Device`: an address
		// is assigned to an interface, the interface belongs to the device, the device points
		// back at the address. No apply order satisfies it, so DeferAlways rather than
		// DeferIfUnresolved -- there is no first pass in which any of the three could resolve,
		// and IfUnresolved would spend a reconcile discovering that every time.
		//
		// Legal because none of the three columns is matched on by any candidate below:
		// stripping them from the create cannot change the identity the lookup decided on
		// (validateDeferred, NBO-015).
		Deferred: []DeferredField{
			{APIField: "primary_ip4", Mode: DeferAlways},
			{APIField: "primary_ip6", Mode: DeferAlways},
			{APIField: "oob_ip", Mode: DeferAlways},
		},

		// The four columns every ChangeLoggedModel carries, the ten CounterCacheFields, and
		// `services`.
		//
		// Ten and not the eleven NBO-030's spec counts: `console_port_count`,
		// `console_server_port_count`, `power_port_count`, `power_outlet_count`,
		// `interface_count`, `front_port_count`, `rear_port_count`, `device_bay_count`,
		// `module_bay_count` and `inventory_item_count` are what dcim.Device declares
		// (docs/netbox-schema.md -> dcim.Device; `netbox/dcim/models/devices.py` lines
		// 694-733). The eleventh in the spec's count is dcim.DeviceType's `device_count`,
		// which lives on the other model.
		//
		// NetBox maintains every counter from the child rows and ignores an attempt to set
		// one, so writing it does not fail -- it silently no-ops, the next reconcile finds the
		// same difference, and the operator PATCHes forever. `services` is a GenericRelation,
		// which is the far end of somebody else's foreign key: a query rather than a column,
		// and dropped on write the same way.
		ReadOnly: []string{
			"created", "last_updated", "url", "display", "services",
			"console_port_count", "console_server_port_count",
			"power_port_count", "power_outlet_count", "interface_count",
			"front_port_count", "rear_port_count", "device_bay_count",
			"module_bay_count", "inventory_item_count",
		},

		// No ContainmentRef -- see the doc comment. Every FK on dcim.Device is PROTECT or
		// SET_NULL, so there is nothing for an owner reference to mirror.
	}
}

// dcimDeviceFields is this kind's spec-to-column map.
//
// Extracted from the descriptor for length, not because anything about it is dynamic. It is
// still a literal.
//
// Four entries earn the explicit table on their own. `deviceTypeRef` -> `device_type` and
// `assetTag` -> `asset_tag` are the camelCase-to-snake_case pairs a convention gets right and
// `primaryIP4Ref` -> `primary_ip4` and `oobIPRef` -> `oob_ip` are the ones it gets wrong
// (`primary_i_p4`, `oob_i_p`) -- and NetBox ignores a field name it does not know rather than
// rejecting it, so either would write nothing and report success.
//
// `roleRef` -> `role` earns it twice over: the spec name says nothing about which of NetBox's
// two role models it is, and the answer is `dcim.DeviceRole` rather than the `ipam.Role` that
// `RoleRef` names (docs/netbox-schema.md -> dcim.Device, `role ForeignKey REQ ->
// dcim.DeviceRole`).
//
// `location`, `rack`, `position`, `face`, `virtual_chassis`, `vc_position`, `vc_priority`,
// `config_template` and `local_context_data` are all absent: NBO-048, NBO-051, NBO-053 and
// NBO-059 own the Kinds behind them, and a field that is accepted and writes nothing is worse
// than a field that is not there.
//
// `spec.interfaces` is absent for a different reason, and it is the one absence here that is
// not a "not yet": there is no `interfaces` column on dcim.Device to map it to. The foreign
// key points the other way -- `dcim.Interface.device` -- so the inline list produces child CRs
// that each write their own NetBox object, and nothing about it reaches this payload
// (NBO-034, api/v1alpha1/dcim_device_inline.go). The engine excludes it from the payload from
// the parent's own InlineChildren() rather than from a list here, so this descriptor states
// the fact once, by not mentioning it (internal/reconciler/payload.go,
// dropInlineChildren).
//
// CascadeOnDelete is false on every reference here, which is the fact the doc comment turns
// on. It is declared by omission rather than written out ten times because false is the zero
// value; the flag being *absent* on every FK of this kind is the same statement as the flag
// being false, and validateContainment reads it either way.
func dcimDeviceFields() []Field {
	return []Field{
		{Spec: "name", API: "name"},
		{Spec: "serial", API: "serial"},
		{Spec: "assetTag", API: "asset_tag", EmptyIsNull: true},
		{Spec: "status", API: "status"},
		{Spec: "airflow", API: "airflow"},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},

		// EmptyIsNull on both coordinates: they are the only nullable non-text columns this
		// kind writes, so `latitude: ""` has to be sent as JSON null rather than as the empty
		// string DRF would reject as a number (#170).
		{Spec: "latitude", API: "latitude", EmptyIsNull: true},
		{Spec: "longitude", API: "longitude", EmptyIsNull: true},

		{
			Spec: "deviceTypeRef", API: "device_type", Class: ClassRefOne,
			Target: netboxv1alpha1.DeviceTypeRef{}.TargetGVK(),
		},
		{
			Spec: "roleRef", API: "role", Class: ClassRefOne,
			Target: netboxv1alpha1.DeviceRoleRef{}.TargetGVK(),
		},
		{
			Spec: "siteRef", API: "site", Class: ClassRefOne,
			Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
		},
		{
			Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
			Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
		},
		{
			Spec: "platformRef", API: "platform", Class: ClassRefOne,
			Target: netboxv1alpha1.PlatformRef{}.TargetGVK(),
		},
		{
			Spec: "clusterRef", API: "cluster", Class: ClassRefOne,
			Target: netboxv1alpha1.ClusterRef{}.TargetGVK(),
		},
		{
			Spec: "primaryIP4Ref", API: "primary_ip4", Class: ClassRefOne,
			Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
		},
		{
			Spec: "primaryIP6Ref", API: "primary_ip6", Class: ClassRefOne,
			Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
		},
		{
			Spec: "oobIPRef", API: "oob_ip", Class: ClassRefOne,
			Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
		},
	}
}

// dcimDeviceKeys are this kind's lookup candidates, in priority order.
//
// Three, from two different kinds of uniqueness declaration. dcim.Device's full
// meta.constraints list is four entries (docs/netbox-schema.md -> dcim.Device):
//
//  1. `models.UniqueConstraint(Lower('name'), 'site', 'tenant',
//     name='%(app_label)s_%(class)s_unique_name_site_tenant')` -- unconditional.
//  2. `models.UniqueConstraint(Lower('name'), 'site',
//     name='%(app_label)s_%(class)s_unique_name_site', condition=Q(tenant__isnull=True),
//     violation_error_message=_('Device name must be unique per site.'))`.
//  3. `models.UniqueConstraint(fields=('rack', 'position', 'face'),
//     name='%(app_label)s_%(class)s_unique_rack_position_face')` -- **unreachable**. All
//     three columns are out of scope for this Kind (dcim.Rack is NBO-051, and `position` and
//     `face` are meaningless without it), so no candidate can be built from it. A candidate
//     that could never be applicable is worse than none: the engine would wait forever for an
//     identity it cannot construct.
//  4. `models.UniqueConstraint(fields=('virtual_chassis', 'vc_position'),
//     name='%(app_label)s_%(class)s_unique_virtual_chassis_vc_position')` -- unreachable for
//     the same reason. dcim.VirtualChassis is NBO-053.
//
// Candidate 1 below is not in that list at all, and is labelled as such: it comes from the
// *column*, `asset_tag CharField UNIQUE len=50` (docs/netbox-schema.md -> dcim.Device;
// `netbox/dcim/models/devices.py` lines 555-562, `unique=True`). A column-level unique is as
// binding as a table-level one, and this is the only single-column key dcim.Device has, so it
// is the strongest and goes first. It is also the one key whose collision is cluster-wide
// rather than site-local, which is why two CRs in two namespaces claiming one asset tag are
// one device and a Conflict.
//
// Candidates 2 and 3 filter `name__ie` and not `name`, because both constraints are over
// `Lower('name')`. An exact filter would report `sw1` absent while NetBox holds `SW1` at that
// site, and the create that followed would be answered with a 400 -- a loop where the lookup
// and the write disagree about what exists (docs/concepts/lookups.md).
//
// `site_id` is never omitted from either. Device names are unique **per site**, so a lookup
// without it finds the wrong device or several, and `sw1` is the most-reused device name there
// is. Candidate 3's `tenant_id` null pin is the constraint's own `condition=Q(tenant__isnull=
// True)`, and it is load-bearing rather than tidy: omitted, a device whose tenant has not been
// created yet would match by name and site, adopt the tenant-less device, and the follow-up
// PATCH would move somebody else's device into this tenant. Pinned, such a device matches
// nothing -- candidate 2 needs the tenant resolved, candidate 3 needs it never declared -- and
// the engine waits, which is the correct outcome
// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
//
// An empty `assetTag` -- the explicit clear -- makes candidate 1 inapplicable rather than
// matching every device without one: filterValue treats the empty string as no value
// (internal/reconciler/payload.go). Candidates 2 and 3 do not pin `asset_tag`, so they stay
// applicable and the device is still identifiable.
func dcimDeviceKeys() []NaturalKey {
	return []NaturalKey{
		{
			Fields: []KeyField{{Filter: "asset_tag", Spec: "assetTag"}},
		},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "site_id", Spec: "siteRef"},
				{Filter: "tenant_id", Spec: "tenantRef"},
			},
		},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "site_id", Spec: "siteRef"},
			},
			NullFields: []NullField{{Filter: "tenant_id", Spec: "tenantRef", Column: NullColumnRef}},
		},
	}
}
