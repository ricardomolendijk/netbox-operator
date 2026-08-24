package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(virtualizationVirtualDiskDescriptor()) }

// virtualizationVirtualDiskDescriptor is virtualization.VirtualDisk as data.
//
// The smallest descriptor in the registry: four fields, one candidate, no deferral and no
// generic FK. It is here as the third member of the VM triple, and it is worth reading next
// to virtualization_virtualmachine.go for the contrast -- same engine, same shape of file,
// an order of magnitude less to say.
func virtualizationVirtualDiskDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVirtualDisk"),
		Endpoint:   "virtualization/virtual-disks",
		ObjectType: "virtualization.virtualdisk",
		Scope:      apiextensionsv1.NamespaceScoped,

		// A ComponentModel, so a NetBoxModel with TagsMixin and CustomFieldsMixin and no
		// `comments` (docs/netbox-schema.md -> virtualization.ComponentModel, bases).
		Taggable:        true,
		CustomFieldable: true,

		// `size` is the only column virtualization.VirtualDisk declares itself; the other
		// three are inherited from virtualization.ComponentModel and are as writable as a
		// declared one.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "size", API: "size"},
			{Spec: "description", API: "description"},
			{
				Spec: "virtualMachineRef", API: "virtual_machine", Class: ClassRefOne,
				Target: netboxv1alpha1.VirtualMachineRef{}.TargetGVK(),
			},
		},

		// The same candidate as NetBoxVMInterface's, from the same place: VirtualDisk lists
		// only `meta.ordering` of its own, and the constraint is
		// `UniqueConstraint(fields=('virtual_machine', 'name'))` on
		// virtualization.ComponentModel (docs/netbox-schema.md ->
		// virtualization.ComponentModel, meta.constraints). Unique per VM rather than
		// globally, no `Lower()`, so `virtual_machine_id` is always sent and the name filter
		// is exact.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "virtual_machine_id", Spec: "virtualMachineRef"},
					{Filter: "name", Spec: "name"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. VirtualDisk has no `_name`: the
		// NaturalOrderingField is declared on VMInterface rather than on ComponentModel, and
		// `meta.ordering: ('virtual_machine', 'name')` here orders on the plain column
		// (docs/netbox-schema.md -> virtualization.VirtualDisk).
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// The VM is the containment parent, on exactly NetBoxVMInterface's terms.
		ContainmentRef: "virtualMachineRef",
	}
}
