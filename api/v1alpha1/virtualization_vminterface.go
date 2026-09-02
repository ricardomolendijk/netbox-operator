package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// InterfaceMode is one value of NetBox's InterfaceModeChoices: how an interface treats
// 802.1Q tags.
//
// docs/netbox-schema.md -> virtualization.VMInterface records the column as
// `mode (BaseInterface) CharField len=50 choices=InterfaceModeChoices` -- the choice class
// and no members. The four values are read from `netbox/dcim/choices.py` lines 1543-1555,
// `InterfaceModeChoices`, in the same 4.6.8 tree the digest was taken from.
//
// Shared with dcim.Interface, because the column is: both inherit it from
// `dcim.BaseInterface`, so this type is declared once rather than per interface kind.
//
// +kubebuilder:validation:Enum=access;tagged;tagged-all;q-in-q
type InterfaceMode string

const (
	// InterfaceModeAccess carries one untagged VLAN.
	InterfaceModeAccess InterfaceMode = "access"

	// InterfaceModeTagged carries the tagged VLANs listed on the interface, plus an
	// optional untagged one.
	InterfaceModeTagged InterfaceMode = "tagged"

	// InterfaceModeTaggedAll carries every tagged VLAN, so `taggedVLANs` is not enumerated.
	InterfaceModeTaggedAll InterfaceMode = "tagged-all"

	// InterfaceModeQinQ is 802.1ad double tagging, where `qinqSVLANRef` is the service
	// VLAN.
	InterfaceModeQinQ InterfaceMode = "q-in-q"
)

// NetBoxVMInterfaceSpec describes one virtualization.VMInterface.
//
// The Kind that makes `IPAssignment.vmInterfaceRef` resolvable for the first time. That
// union member has been declared since NBO-011 and named a Kind nothing registered, so every
// use of it reported `RefKindUnavailable`; registering `virtualization.vminterface` puts it
// in the registry's reverse index, which is what turns the member into a resolvable target
// and a watch (internal/registry, ByObjectType; docs/concepts/generic-refs.md).
//
// There is no `macAddress` and no `primaryMACAddressRef`. NetBox 4.2 moved the MAC to
// `dcim.MACAddress` behind a generic FK, and this model's own entry lists only
// `mac_addresses GenericRelation` -- a reverse relation, never a column
// (docs/netbox-schema.md -> virtualization.VMInterface, dcim.BaseInterface). NBO-048 lands
// the Kind; a field accepted and silently dropped would be worse than one that is not there.
type NetBoxVMInterfaceSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// VirtualMachineRef is the VM this interface belongs to. Required, because NetBox's
	// column is: `virtual_machine ForeignKey REQ -> virtualization.VirtualMachine
	// on_delete=CASCADE` on virtualization.ComponentModel, which is where VMInterface
	// inherits it (docs/netbox-schema.md -> virtualization.ComponentModel).
	//
	// It is half the identity as well as the parent. `(virtual_machine, name)` is unique per
	// VM and not globally, so an unresolved reference means the operator cannot tell whether
	// this interface exists -- it waits rather than looking `eth0` up across every VM in
	// NetBox (docs/concepts/lookups.md).
	//
	// It is also the containment parent: deleting the NetBoxVirtualMachine takes its
	// hand-written interfaces with it, in the same namespace
	// (docs/decisions/0003-ownership-and-references.md rule 4). `on_delete=CASCADE` in
	// NetBox says the same thing about the other side of the mirror.
	VirtualMachineRef VirtualMachineRef `json:"virtualMachineRef"`

	// Name is the interface's name (docs/netbox-schema.md -> virtualization.VMInterface,
	// `name CharField REQ len=64`).
	//
	// Matched **case-sensitively**, unlike a VM's name: ComponentModel's constraint is
	// `UniqueConstraint(fields=('virtual_machine', 'name'))` with no `Lower()`
	// (docs/netbox-schema.md -> virtualization.ComponentModel, meta.constraints), so `Eth0`
	// and `eth0` are two interfaces on one VM and the lookup must not merge them.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// Enabled is whether the interface is administratively up (docs/netbox-schema.md ->
	// virtualization.VMInterface, `enabled (BaseInterface) BooleanField def=True`).
	//
	// A pointer, and the reason is the column's `def=True`. A plain bool cannot tell "not
	// managed" from "managed as false", so adopting an interface a human had disabled would
	// silently re-enable it on the first reconcile. Nil leaves NetBox's value alone; `false`
	// writes false (docs/concepts/field-ownership.md).
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MTU is the interface's maximum transmission unit (docs/netbox-schema.md ->
	// virtualization.VMInterface, `mtu (BaseInterface) PositiveIntegerField`).
	//
	// Bounded at NetBox's own validators, `INTERFACE_MTU_MIN` and `INTERFACE_MTU_MAX`
	// (`netbox/dcim/constants.py` lines 48-49, NetBox 4.6.8): 1 and 65536. A pointer, so
	// omitting it leaves NetBox's value alone rather than clearing it.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	// +optional
	MTU *int32 `json:"mtu,omitempty"`

	// Mode is how the interface treats 802.1Q tags.
	//
	// Not defaulted, unlike a VM's `status`. The column carries no `def=`
	// (docs/netbox-schema.md -> virtualization.VMInterface), and an unset mode is a real
	// state in NetBox -- an interface with no VLAN semantics at all -- so defaulting it
	// would make every adopted interface drift towards a mode nobody chose.
	//
	// It carries no tri-state note, and that is not an omission: an enum has no empty
	// member, so `mode: ""` is rejected at admission and there is no way to spell "clear
	// the mode NetBox holds" through this field. Omitting it leaves NetBox's value alone.
	// Every choice column in the project is in the same position -- the three states of
	// docs/concepts/field-ownership.md are two for an enum -- and the way back to an unset
	// mode is the NetBox UI.
	// +optional
	Mode InterfaceMode `json:"mode,omitempty"`

	// ParentRef is the interface this one is a sub-interface of (docs/netbox-schema.md ->
	// virtualization.VMInterface, `parent (BaseInterface) ForeignKey ->
	// virtualization.VMInterface on_delete=RESTRICT`).
	//
	// **Deferred when it does not resolve, and only then.** A self-reference between two
	// interfaces of the same VM cannot always be satisfied in one pass, so the field is left
	// out of the create and PATCHed once the parent exists. Conditionally rather than
	// always, because unconditional deferral would create the interface, then reparent it --
	// two writes and a visible intermediate state -- where including a reference that
	// already resolves is one (NBO-015).
	//
	// It is not part of the identity: `(virtual_machine, name)` is, so a deferred parent
	// cannot make the operator adopt the wrong row.
	// +optional
	ParentRef *VMInterfaceRef `json:"parentRef,omitempty"`

	// BridgeRef is the interface this one is bridged to (docs/netbox-schema.md ->
	// virtualization.VMInterface, `bridge (BaseInterface) ForeignKey ->
	// virtualization.VMInterface on_delete=SET_NULL`).
	//
	// A second self-reference, deferred on the same terms as `parentRef` and independently
	// of it. Bridging is symmetric in intent and not in the database: NetBox stores one
	// column per interface, so two interfaces bridged to each other are two CRs each naming
	// the other, and each defers until the other exists.
	// +optional
	BridgeRef *VMInterfaceRef `json:"bridgeRef,omitempty"`

	// UntaggedVLANRef is the interface's native VLAN (docs/netbox-schema.md ->
	// virtualization.VMInterface, `untagged_vlan (BaseInterface) ForeignKey -> ipam.VLAN
	// on_delete=SET_NULL`).
	// +optional
	UntaggedVLANRef *VLANRef `json:"untaggedVLANRef,omitempty"`

	// TaggedVLANs are the VLANs carried tagged on this interface (docs/netbox-schema.md ->
	// virtualization.VMInterface, `tagged_vlans (BaseInterface) ManyToManyField ->
	// ipam.VLAN`).
	//
	// A to-many reference with the three states every optional field has: omitting the field
	// leaves NetBox's own list alone, `[]` clears it, and a list replaces it. The order is
	// not data -- NetBox does not preserve it -- so the ids are sent sorted and deduplicated
	// and the comparison is order-independent (docs/concepts/drift.md).
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of
	// the payload and the object reports `RefsResolved=False` naming the element that
	// failed. Writing the ones that did resolve would be a full-list replacement with a
	// shorter list -- a deletion, reported as a success.
	//
	// MaxItems is not a NetBox limit and is not decoration: ObjectRef carries five CEL rules
	// (objectref.go) and the API server costs each one at the list's maximum length, so an
	// unbounded list of refs is rejected outright with "estimated rule cost exceeds budget".
	// 256 is the project's standard bound for a to-many reference (#187). A trunk that needs
	// more than 256 VLANs enumerated wants `mode: tagged-all`, which is one field instead of
	// a list.
	// +kubebuilder:validation:MaxItems=256
	// +optional
	TaggedVLANs []VLANRef `json:"taggedVLANs,omitempty"`

	// QinQSVLANRef is the service VLAN of an 802.1ad double-tagged interface
	// (docs/netbox-schema.md -> virtualization.VMInterface, `qinq_svlan (BaseInterface)
	// ForeignKey -> ipam.VLAN on_delete=SET_NULL`).
	//
	// Deferred when it does not resolve, like `parentRef`: a Q-in-Q service VLAN is
	// frequently created in the same apply as the interfaces that carry it, and NetBox
	// validates it against `mode: q-in-q` -- so a create that carried an unresolved
	// reference would fail where one that waits succeeds.
	// +optional
	QinQSVLANRef *VLANRef `json:"qinqSVLANRef,omitempty"`

	// VRFRef is the VRF the interface's addresses live in (docs/netbox-schema.md ->
	// virtualization.VMInterface, `vrf ForeignKey -> ipam.VRF on_delete=SET_NULL`).
	//
	// Declared on VMInterface itself rather than on BaseInterface, which is the one column
	// here that is: dcim.Interface has no `vrf` of its own.
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// VLANTranslationPolicyRef is the table of VLAN ID rewrites applied to this interface
	// (docs/netbox-schema.md -> virtualization.VMInterface, `vlan_translation_policy
	// (BaseInterface) ForeignKey -> ipam.VLANTranslationPolicy on_delete=PROTECT`).
	//
	// Inherited from BaseInterface, so it is the same column dcim.Interface carries and it
	// points at the same Kind -- one NetBoxVLANTranslationPolicy can be shared by a physical
	// interface and a VM interface at once. NBO-030 left it out because there was no Kind to
	// point at; NBO-068 lands it.
	//
	// Not deferred: a policy is a standalone object with no dependency on this interface, so
	// there is no ordering problem to solve. PROTECT, so it is not a containment parent
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	VLANTranslationPolicyRef *VLANTranslationPolicyRef `json:"vlanTranslationPolicyRef,omitempty"`

	// Description is free text shown next to the interface. Declared on
	// virtualization.ComponentModel rather than on VMInterface (docs/netbox-schema.md ->
	// virtualization.ComponentModel, `description CharField len=200`); an inherited column
	// is as writable as a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxVMInterface is one virtualization.VMInterface in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// It has no `comments` and no `tags`-bearing `PrimaryModel` base: `ComponentModel` is a
// plain `NetBoxModel`, so the writable envelope is smaller than a VM's
// (docs/netbox-schema.md -> virtualization.ComponentModel, bases). It does mix in
// TagsMixin and CustomFieldsMixin, so it carries the provenance stamp.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvmif
// +kubebuilder:printcolumn:name="VM",type=string,JSONPath=`.spec.virtualMachineRef.name`
// +kubebuilder:printcolumn:name="Enabled",type=boolean,JSONPath=`.spec.enabled`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVMInterface struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVMInterfaceSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (i *NetBoxVMInterface) NetBoxSpec() *NetBoxObjectSpec { return &i.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (i *NetBoxVMInterface) NetBoxStatus() *NetBoxObjectStatus { return &i.Status }

// NetBoxVMInterfaceList is a list of NetBoxVMInterface.
// +kubebuilder:object:root=true
type NetBoxVMInterfaceList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVMInterface `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVMInterface{}, &NetBoxVMInterfaceList{})
}
