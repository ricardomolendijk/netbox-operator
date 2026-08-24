package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxVirtualDiskSpec describes one virtualization.VirtualDisk.
//
// The smallest kind in the catalogue: `virtualization.VirtualDisk` declares exactly one
// column of its own, `size PositiveIntegerField REQ`, and inherits the rest from
// `virtualization.ComponentModel` (docs/netbox-schema.md -> virtualization.VirtualDisk,
// virtualization.ComponentModel). Everything interesting about it is on the other end of
// `virtualMachineRef`.
//
// **The interaction with `NetBoxVirtualMachine.spec.disk`.** A VM's `disk` must equal the
// sum of its virtual disks' sizes, or NetBox rejects the write:
// `VirtualMachine.clean()` fills `disk` from the aggregate when it is null and raises
// `ValidationError` when it is set and disagrees
// (`netbox/virtualization/models/virtualmachines.py` lines 330-341, NetBox 4.6.8). So use
// one or the other. Setting both consistently works, setting both inconsistently is a 400
// on the *VM* reported as `Ready=False, Reason=Invalid` naming both numbers, and setting
// only these lets NetBox compute the total.
type NetBoxVirtualDiskSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// VirtualMachineRef is the VM this disk belongs to. Required, because NetBox's column
	// is: `virtual_machine ForeignKey REQ -> virtualization.VirtualMachine
	// on_delete=CASCADE` on virtualization.ComponentModel (docs/netbox-schema.md ->
	// virtualization.ComponentModel).
	//
	// Half the identity and the containment parent, on the same terms as
	// NetBoxVMInterface's: `(virtual_machine, name)` is unique per VM rather than globally,
	// so an unresolved reference means the operator waits instead of looking `disk0` up
	// across every VM in NetBox.
	VirtualMachineRef VirtualMachineRef `json:"virtualMachineRef"`

	// Name is the disk's name (docs/netbox-schema.md -> virtualization.ComponentModel,
	// `name CharField REQ len=64`).
	//
	// Case-sensitive: ComponentModel's constraint carries no `Lower()`, unlike all four of
	// virtualization.VirtualMachine's.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// Size is the disk's size (docs/netbox-schema.md -> virtualization.VirtualDisk,
	// `size PositiveIntegerField REQ`).
	//
	// Required, and therefore not a pointer: the three states an optional field has do not
	// apply to a column NetBox will not accept as null, and a disk of no stated size is not
	// a thing to let through admission. `size: 0` is legal and explicit.
	//
	// The unit is whatever the NetBox instance's `DISK_BASE_UNIT` says -- MB or MiB
	// (`netbox/virtualization/forms/model_forms.py` line 498, NetBox 4.6.8) -- and it is the
	// same unit as `NetBoxVirtualMachine.spec.disk`, which is what makes the two comparable
	// at all.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	Size int32 `json:"size"`

	// Description is free text shown next to the disk. Declared on
	// virtualization.ComponentModel (docs/netbox-schema.md ->
	// virtualization.ComponentModel, `description CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxVirtualDisk is one virtualization.VirtualDisk in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvdisk
// +kubebuilder:printcolumn:name="VM",type=string,JSONPath=`.spec.virtualMachineRef.name`
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.spec.size`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVirtualDisk struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVirtualDiskSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (d *NetBoxVirtualDisk) NetBoxSpec() *NetBoxObjectSpec { return &d.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (d *NetBoxVirtualDisk) NetBoxStatus() *NetBoxObjectStatus { return &d.Status }

// NetBoxVirtualDiskList is a list of NetBoxVirtualDisk.
// +kubebuilder:object:root=true
type NetBoxVirtualDiskList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVirtualDisk `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVirtualDisk{}, &NetBoxVirtualDiskList{})
}
