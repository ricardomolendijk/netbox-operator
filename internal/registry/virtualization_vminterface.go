package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(virtualizationVMInterfaceDescriptor()) }

// virtualizationVMInterfaceDescriptor is virtualization.VMInterface as data.
//
// This registration is what makes `IPAssignment.vmInterfaceRef` resolvable. The union member
// has named this Kind since NBO-011 and nothing registered it, so `ByObjectType` had no entry
// for `virtualization.vminterface` and every use reported RefKindUnavailable; the ObjectType
// below is the whole of the fix -- the reverse index is built from it in Registry.Add, and
// internal/resolver's RefTargets picks the Kind up as a watch target from there
// (docs/concepts/generic-refs.md, NBO-019).
//
// The endpoint is `virtualization/interfaces`, which is the reason Descriptor.Endpoint is
// looked up rather than pluralised from the model name.
func virtualizationVMInterfaceDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVMInterface"),
		Endpoint:   "virtualization/interfaces",
		ObjectType: "virtualization.vminterface",
		Scope:      apiextensionsv1.NamespaceScoped,

		// virtualization.ComponentModel is a NetBoxModel, not a PrimaryModel
		// (docs/netbox-schema.md -> virtualization.ComponentModel, bases) -- so there is no
		// `comments` column here -- and NetBoxModel still mixes in both TagsMixin and
		// CustomFieldsMixin, so the provenance stamp applies in full.
		Taggable:        true,
		CustomFieldable: true,

		// `taggedVLANs` -> `tagged_vlans` is the only ClassRefMany, and the class is the one
		// declaration of both its cardinality and its comparison rule: M2MFields() derives
		// the order-independent id-set comparison from it (NBO-088).
		//
		// `primary_mac_address` is absent: NBO-048 owns the Kind it points at, and
		// `mac_addresses` is a GenericRelation rather than a column at all.
		//
		// `vlan_translation_policy` used to be listed here as absent for the same reason.
		// NBO-068 landed NetBoxVLANTranslationPolicy, so it is an ordinary reference now.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "enabled", API: "enabled"},
			{Spec: "mtu", API: "mtu"},
			{Spec: "mode", API: "mode"},
			{Spec: "description", API: "description"},
			{
				Spec: "virtualMachineRef", API: "virtual_machine", Class: ClassRefOne,
				Target: netboxv1alpha1.VirtualMachineRef{}.TargetGVK(),
				// virtualization.ComponentModel.virtual_machine is on_delete=CASCADE
				// (docs/netbox-schema.md), so deleting the VM takes its interfaces and disks with it
				// server-side -- which makes it a legal containment parent, and makes the owner
				// reference load-bearing: without it the child CR outlives its row and the
				// create-if-absent step recreates what NetBox deleted.
				CascadeOnDelete: true,
			},
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.VMInterfaceRef{}.TargetGVK(),
			},
			{
				Spec: "bridgeRef", API: "bridge", Class: ClassRefOne,
				Target: netboxv1alpha1.VMInterfaceRef{}.TargetGVK(),
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
			{
				Spec: "vlanTranslationPolicyRef", API: "vlan_translation_policy", Class: ClassRefOne,
				Target: netboxv1alpha1.VLANTranslationPolicyRef{}.TargetGVK(),
				// `vlan_translation_policy (BaseInterface) ForeignKey ->
				// ipam.VLANTranslationPolicy on_delete=PROTECT` (docs/netbox-schema.md).
				// PROTECT, so not eligible to be the containment parent, and no Deferred
				// entry: a policy has no dependency on the interface pointing at it, so
				// there is no ordering to solve.
			},
		},

		// One candidate, and it comes from the parent model rather than from this one:
		// `virtualization.VMInterface` lists no meta.constraints of its own, and
		// `virtualization.ComponentModel` carries
		// `UniqueConstraint(fields=('virtual_machine', 'name'),
		// name='%(app_label)s_%(class)s_unique_virtual_machine_name')`
		// (docs/netbox-schema.md -> virtualization.ComponentModel, meta.constraints).
		//
		// Two things follow. `virtual_machine_id` is never omitted: the pair is unique per VM
		// and `eth0` is the most-reused interface name there is, so a lookup without it would
		// adopt another VM's interface on the first reconcile. And there is no `Lower()` here,
		// unlike all four of virtualization.VirtualMachine's constraints, so the name filter
		// is exact -- `Eth0` and `eth0` are two interfaces on one VM to NetBox and must be
		// two to the operator.
		//
		// There is no second candidate. Both halves are required fields, so there is no state
		// in which one is missing and a different identity applies.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "virtual_machine_id", Spec: "virtualMachineRef"},
					{Filter: "name", Spec: "name"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// Three references deferred conditionally, never unconditionally.
		//
		// `parent` and `bridge` are self-references: two interfaces of one VM naming each
		// other cannot both be created with the reference in place, so the field comes out of
		// the create and goes in a follow-up PATCH -- but only when it does not already
		// resolve. Unconditional deferral would turn every ordinary sub-interface into two
		// writes with a visible intermediate state where it is briefly top-level, which is
		// the failure DeferIfUnresolved exists for (NBO-015).
		//
		// `qinq_svlan` is not a self-reference and is deferred for the neighbouring reason: a
		// Q-in-Q service VLAN is usually applied in the same pass as the interfaces that
		// carry it, and NetBox cross-validates it against `mode`, so a create carrying an
		// unresolved reference fails where one that waits succeeds.
		//
		// None of the three is matched on by the natural key, so an unconditional deferral
		// would be legal here too (validateDeferred); conditional is chosen on the merits
		// rather than forced.
		Deferred: []DeferredField{
			{APIField: "parent", Mode: DeferIfUnresolved},
			{APIField: "bridge", Mode: DeferIfUnresolved},
			{APIField: "qinq_svlan", Mode: DeferIfUnresolved},
		},

		// The four columns every ChangeLoggedModel carries, plus `_name` and the five reverse
		// relations.
		//
		// `_name` is a NaturalOrderingField NetBox derives from `name` so that `eth10` sorts
		// after `eth9` (docs/netbox-schema.md -> virtualization.VMInterface). `ip_addresses`,
		// `mac_addresses`, `fhrp_group_assignments`, `tunnel_terminations` and
		// `l2vpn_terminations` are GenericRelations -- the far end of somebody else's generic
		// FK, which is to say a query rather than a column. NetBox ignores every one of them
		// on write, so an entry in the field map for any would be a PATCH the operator repeats
		// forever.
		ReadOnly: []string{
			"created", "last_updated", "url", "display", "_name",
			"ip_addresses", "mac_addresses", "fhrp_group_assignments",
			"tunnel_terminations", "l2vpn_terminations",
		},

		// The VM is the containment parent, which is the same thing `on_delete=CASCADE` says
		// on the NetBox side: `kubectl delete nbvm` takes its hand-written interfaces with it
		// in the same namespace (docs/decisions/0003-ownership-and-references.md rule 4).
		// M5 replaces this with a *controller* owner reference for interfaces the operator
		// materialises from a VM's inline list (NBO-032); a hand-written one stays a
		// non-controller owner, because two controllers on one object is the one thing
		// Kubernetes will not allow.
		ContainmentRef: "virtualMachineRef",
	}
}
