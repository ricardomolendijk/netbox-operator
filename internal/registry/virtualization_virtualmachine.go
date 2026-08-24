package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(virtualizationVirtualMachineDescriptor()) }

// virtualizationVirtualMachineDescriptor is virtualization.VirtualMachine as data.
//
// The most intricate natural key in the project, and still nothing but a list. See
// NaturalKeys below for the four UniqueConstraints it is derived from and the reading of
// each; the rest of this kind -- seven foreign keys, two deferred one-to-ones, two choice
// columns, a decimal-as-string and two counter caches -- is ordinary.
func virtualizationVirtualMachineDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVirtualMachine"),
		Endpoint:   "virtualization/virtual-machines",
		ObjectType: "virtualization.virtualmachine",
		Scope:      apiextensionsv1.NamespaceScoped,

		// virtualization.VirtualMachine is a PrimaryModel (docs/netbox-schema.md ->
		// virtualization.VirtualMachine, bases), which mixes in both TagsMixin and
		// CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `roleRef` -> `role` is the entry that earns an explicit table twice over: the
		// spec name says nothing about which of NetBox's two role models it is, and the
		// answer is `dcim.DeviceRole` rather than the `ipam.Role` that `RoleRef` names
		// (docs/netbox-schema.md -> virtualization.VirtualMachine, `role ForeignKey ->
		// dcim.DeviceRole`). `startOnBoot` -> `start_on_boot` and `primaryIP4Ref` ->
		// `primary_ip4` are the two a camelCase convention would get wrong -- and NetBox
		// ignores a field name it does not know rather than rejecting it, so either would
		// write nothing and report success.
		//
		// `virtual_machine_type`, `config_template` and `local_context_data` are absent:
		// NBO-060 and NBO-059 own them, and a field that is accepted and writes nothing is
		// worse than a field that is not there.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "status", API: "status"},
			{Spec: "startOnBoot", API: "start_on_boot"},
			{Spec: "vcpus", API: "vcpus", EmptyIsNull: true},
			{Spec: "memory", API: "memory"},
			{Spec: "disk", API: "disk"},
			{Spec: "serial", API: "serial"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "clusterRef", API: "cluster", Class: ClassRefOne,
				Target: netboxv1alpha1.ClusterRef{}.TargetGVK(),
			},
			{
				Spec: "siteRef", API: "site", Class: ClassRefOne,
				Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
			},
			{
				Spec: "deviceRef", API: "device", Class: ClassRefOne,
				Target: netboxv1alpha1.DeviceRef{}.TargetGVK(),
			},
			{
				Spec: "roleRef", API: "role", Class: ClassRefOne,
				Target: netboxv1alpha1.DeviceRoleRef{}.TargetGVK(),
			},
			{
				Spec: "platformRef", API: "platform", Class: ClassRefOne,
				Target: netboxv1alpha1.PlatformRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
			{
				Spec: "primaryIP4Ref", API: "primary_ip4", Class: ClassRefOne,
				Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
			},
			{
				Spec: "primaryIP6Ref", API: "primary_ip6", Class: ClassRefOne,
				Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
			},
		},

		NaturalKeys: virtualizationVirtualMachineKeys(),

		UpdateStrategy: UpdatePatch,

		// The ring this exists for is `VM -> IPAddress -> VMInterface -> VM`: an address is
		// assigned to an interface, the interface belongs to the VM, the VM points back at
		// the address. No apply order satisfies it, so DeferAlways rather than
		// DeferIfUnresolved -- there is no first pass in which either field could resolve,
		// and IfUnresolved would spend a reconcile discovering that every time.
		//
		// Legal because neither column is matched on by any candidate above: stripping them
		// from the create cannot change the identity the lookup decided on
		// (validateDeferred, NBO-015).
		Deferred: []DeferredField{
			{APIField: "primary_ip4", Mode: DeferAlways},
			{APIField: "primary_ip6", Mode: DeferAlways},
		},

		// The four columns every ChangeLoggedModel carries, plus the two CounterCacheFields
		// (docs/netbox-schema.md -> virtualization.VirtualMachine, `interface_count
		// CounterCacheField`, `virtual_disk_count CounterCacheField`). NetBox maintains both
		// from the child rows and ignores an attempt to set either, so writing one does not
		// fail -- it silently no-ops, the next reconcile finds the same difference, and the
		// operator PATCHes forever.
		ReadOnly: []string{
			"created", "last_updated", "url", "display",
			"interface_count", "virtual_disk_count",
		},

		// `clusterRef` and not "clusterRef, or siteRef when there is no cluster", which is
		// what NBO-029's spec asks for. ContainmentRef is one field because Kubernetes
		// garbage collection waits for *every* owner, so it cannot be conditional without
		// becoming a func on a Descriptor -- and nothing in a Descriptor is a func (see this
		// package's comment). One parent, the primary one: a cluster is what a VM runs on,
		// and `on_delete=PROTECT` on `cluster` means NetBox refuses to delete the cluster
		// while the VM is there, so the two sides agree about which object is the parent.
		//
		// The cost is that a site-only VM takes no owner reference and reports
		// ParentOwned=False. That is visible in the status rather than silent, and a
		// per-Kind conditional parent is a change to the shared Descriptor, which is out of
		// scope for one kind (docs/decisions/0003-ownership-and-references.md rule 4).
		ContainmentRef: "clusterRef",
	}
}

// virtualizationVirtualMachineKeys are this kind's lookup candidates, in priority order.
//
// Extracted from the descriptor because it is the longest candidate list in the registry and
// the reasoning behind it is the longest too -- not because anything about it is dynamic. It
// is still a literal.
//
// Five candidates, four of them read straight off meta.constraints
// (docs/netbox-schema.md -> virtualization.VirtualMachine):
//
//  1. `UniqueConstraint(Lower('name'), 'cluster', 'tenant',
//     name='..._unique_name_cluster_tenant')` -- unconditional.
//  2. `UniqueConstraint(Lower('name'), 'cluster', name='..._unique_name_cluster',
//     condition=Q(tenant__isnull=True))`.
//  3. `UniqueConstraint(Lower('name'), 'device', 'tenant',
//     name='..._unique_name_device_tenant',
//     condition=Q(cluster__isnull=True, device__isnull=False))`.
//  4. `UniqueConstraint(Lower('name'), 'device', name='..._unique_name_device',
//     condition=Q(cluster__isnull=True, device__isnull=False, tenant__isnull=True))`.
//
// Every condition becomes a NullField and not an omitted filter, which is what makes
// the list an ordered set of *identities* rather than a fallback chain: candidate 2
// asserts `tenantRef` was never declared, so a VM whose tenant has not been created
// yet matches nothing and the engine waits instead of adopting the tenant-less VM of
// the same name and PATCHing a tenant onto somebody else's row
// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
// Candidates 3 and 4 carry the constraints' own `cluster__isnull=True` for the same
// reason, and their `device__isnull=False` needs no pin: Applicable already requires
// `deviceRef` to have resolved.
//
// All four filter `name__ie` and not `name`, because all four constraints are over
// `Lower('name')`. An exact filter would report `dns` absent while NetBox holds
// `DNS` in that cluster, and the create that followed would be answered with a 400 --
// a loop where the lookup and the write disagree about what exists.
//
// Candidate 5 is **not** a constraint and is labelled as such. `clean()` accepts a VM
// with a site and neither cluster nor device
// (`netbox/virtualization/models/virtualmachines.py` lines 291-295), and no unique
// constraint covers that shape -- so without a candidate such a VM could never
// establish identity and would sit at WaitingForKey forever, which is the worst of
// the available outcomes. `(name, site)` with cluster and device pinned null is the
// convention NBO-029's spec names as the third lookup order. It does not make the
// pair unique: two site-only VMs sharing a name still match, and that is reported as
// a Conflict naming both ids rather than resolved by taking the first row. Tenant is
// deliberately not in it -- there is no constraint to derive a tenant-qualified
// variant from, and a Conflict is the honest answer to an ambiguity NetBox itself
// does not resolve.
func virtualizationVirtualMachineKeys() []NaturalKey {
	return []NaturalKey{

		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "cluster_id", Spec: "clusterRef"},
				{Filter: "tenant_id", Spec: "tenantRef"},
			},
		},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "cluster_id", Spec: "clusterRef"},
			},
			NullFields: []NullField{{Filter: "tenant_id", Spec: "tenantRef"}},
		},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "device_id", Spec: "deviceRef"},
				{Filter: "tenant_id", Spec: "tenantRef"},
			},
			NullFields: []NullField{{Filter: "cluster_id", Spec: "clusterRef"}},
		},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "device_id", Spec: "deviceRef"},
			},
			NullFields: []NullField{
				{Filter: "cluster_id", Spec: "clusterRef"},
				{Filter: "tenant_id", Spec: "tenantRef"},
			},
		},
		{
			Fields: []KeyField{
				{Filter: "name", Spec: "name", Lookup: LookupIExact},
				{Filter: "site_id", Spec: "siteRef"},
			},
			NullFields: []NullField{
				{Filter: "cluster_id", Spec: "clusterRef"},
				{Filter: "device_id", Spec: "deviceRef"},
			},
		},
	}
}
