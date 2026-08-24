package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VirtualMachineStatus is one value of NetBox's VirtualMachineStatusChoices.
//
// docs/netbox-schema.md -> virtualization.VirtualMachine records the column as
// `status CharField len=50 def=UNRESOLVED:VirtualMachineStatusChoices.STATUS_ACTIVE
// choices=VirtualMachineStatusChoices` -- the choice *class*, not its members, and a `def=`
// the AST walk could not evaluate (that is #65's job). The seven values below are read from
// `netbox/virtualization/choices.py` lines 32-51, `VirtualMachineStatusChoices`, in the same
// 4.6.8 tree the digest was taken from.
//
// It is not dcim.Site's set and not ipam.Prefix's: a VM is `offline`, `active`, `planned`,
// `staged`, `failed`, `decommissioning` or `paused` -- there is no `retired` and no
// `container`, and `paused` exists nowhere else.
//
// +kubebuilder:validation:Enum=offline;active;planned;staged;failed;decommissioning;paused
type VirtualMachineStatus string

const (
	// VirtualMachineStatusOffline is a VM that exists and is not running.
	VirtualMachineStatusOffline VirtualMachineStatus = "offline"

	// VirtualMachineStatusActive is a running VM, and NetBox's own default.
	VirtualMachineStatusActive VirtualMachineStatus = "active"

	// VirtualMachineStatusPlanned is a VM that does not exist yet.
	VirtualMachineStatusPlanned VirtualMachineStatus = "planned"

	// VirtualMachineStatusStaged is a VM built and not yet in service.
	VirtualMachineStatusStaged VirtualMachineStatus = "staged"

	// VirtualMachineStatusFailed is a VM that is broken.
	VirtualMachineStatusFailed VirtualMachineStatus = "failed"

	// VirtualMachineStatusDecommissioning is a VM being retired.
	VirtualMachineStatusDecommissioning VirtualMachineStatus = "decommissioning"

	// VirtualMachineStatusPaused is a suspended VM: it exists, holds its memory, and is
	// not executing.
	VirtualMachineStatusPaused VirtualMachineStatus = "paused"
)

// VirtualMachineStartOnBoot is one value of NetBox's VirtualMachineStartOnBootChoices.
//
// A `CharField len=32`, not a boolean, and that is the whole reason this type exists: the
// digest shows `start_on_boot CharField len=32
// def=UNRESOLVED:VirtualMachineStartOnBootChoices.STATUS_OFF
// choices=VirtualMachineStartOnBootChoices` (docs/netbox-schema.md ->
// virtualization.VirtualMachine), and the three values are read from
// `netbox/virtualization/choices.py` lines 54-65. The third one, `laststate`, is what a
// boolean could not express -- resume whatever the VM was doing when the host went down --
// so a `startOnBoot: bool` would silently make a hypervisor's most common setting
// unrepresentable.
//
// +kubebuilder:validation:Enum=on;off;laststate
type VirtualMachineStartOnBoot string

const (
	// VirtualMachineStartOnBootOn starts the VM when its host boots.
	VirtualMachineStartOnBootOn VirtualMachineStartOnBoot = "on"

	// VirtualMachineStartOnBootOff leaves the VM stopped, and is NetBox's own default.
	VirtualMachineStartOnBootOff VirtualMachineStartOnBoot = "off"

	// VirtualMachineStartOnBootLastState restores whatever state the VM was in.
	VirtualMachineStartOnBootLastState VirtualMachineStartOnBoot = "laststate"
)

// NetBoxVirtualMachineSpec describes one virtualization.VirtualMachine.
//
// The kind with the most intricate identity in the catalogue so far: four UniqueConstraints,
// three of them conditional, all four over `Lower('name')` (docs/netbox-schema.md ->
// virtualization.VirtualMachine, meta.constraints). See
// internal/registry/virtualization_virtualmachine.go for the ordered candidate list they
// become, and docs/reference/netboxvirtualmachine.md for the reading of each one.
//
// **`disk` is not a field the operator has to defend.** NBO-029's spec carried a hypothesis
// that NetBox recomputes `disk` from a VM's virtual disks, which would make `spec.disk`
// alongside a NetBoxVirtualDisk a PATCH loop and therefore something the operator must
// refuse. The source says otherwise: `VirtualMachine.clean()` sets `disk` from the aggregate
// only when it is `None`, and raises `ValidationError` when it is set and disagrees
// (`netbox/virtualization/models/virtualmachines.py` lines 330-341, NetBox 4.6.8). So the
// server rejects the contradiction loudly, the operator reports `Ready=False,
// Reason=Invalid` carrying NetBox's own message, and there is no drift loop to guard
// against. No guard clause ships, because there is nothing left for it to prevent.
//
// `deletionPolicy` keeps the envelope's `Delete` default, which is right here and is not on
// the IPAM kinds: a deleted VM record is re-creatable from the manifest that described it,
// and a prefix's history is not (docs/reference/netboxprefix.md).
//
// +kubebuilder:validation:XValidation:rule="has(self.clusterRef) || has(self.siteRef) || has(self.deviceRef)",message="a virtual machine must set at least one of clusterRef, siteRef or deviceRef"
type NetBoxVirtualMachineSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the VM's name (docs/netbox-schema.md -> virtualization.VirtualMachine,
	// `name CharField REQ len=64`).
	//
	// Matched case-insensitively when the operator goes looking for it, because all four
	// unique constraints are over `Lower('name')`: `DNS` and `dns` in one cluster are one
	// object to NetBox, and an exact filter would make them two to the operator -- a lookup
	// that says "absent" followed by a create NetBox answers with a 400
	// (docs/concepts/lookups.md).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// ClusterRef is the cluster the VM runs on (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `cluster ForeignKey -> virtualization.Cluster
	// on_delete=PROTECT`).
	//
	// Nullable in the database and part of the identity when set. It is also this kind's
	// containment parent: deleting the NetBoxCluster takes its VMs with it, and their
	// interfaces and disks after them (docs/decisions/0003-ownership-and-references.md
	// rule 4).
	//
	// NetBoxCluster lands with NBO-028. Until it does, `clusterRef: {name: ...}` reports
	// `RefsResolved=False, Reason=RefKindUnavailable` and nothing is written -- the
	// manifest is right and the operator is short a Kind.
	// +optional
	ClusterRef *ClusterRef `json:"clusterRef,omitempty"`

	// SiteRef is the site the VM is at (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `site ForeignKey -> dcim.Site on_delete=PROTECT`).
	//
	// Neither this nor `clusterRef` is `REQ` in the database, and NetBox's `clean()` still
	// requires one of the three -- `site`, `cluster` or `device`
	// (`netbox/virtualization/models/virtualmachines.py` lines 291-295, NetBox 4.6.8). That
	// is a validation rule no schema digest can show, so the CRD's CEL rule enforces it at
	// admission rather than letting every such VM fail on its first write.
	//
	// NBO-029's spec table says "at least one of `clusterRef` / `siteRef`". The source says
	// three, `device` included, and the CEL rule follows the source: a VM pinned to a
	// standalone host device with no cluster and no site is legal in NetBox, and rejecting
	// it at admission would make a working manifest un-appliable.
	//
	// Setting both this and `clusterRef` is legal, and NetBox additionally requires the
	// site to match the cluster's (`clean()` lines 297-303). That comparison needs the
	// cluster's own `_site`, which only the server has, so it stays server-side and
	// surfaces as `Ready=False, Reason=Invalid` carrying NetBox's message.
	// +optional
	SiteRef *SiteRef `json:"siteRef,omitempty"`

	// DeviceRef pins the VM to the host device it runs on (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `device ForeignKey -> dcim.Device on_delete=PROTECT`).
	//
	// Part of the identity when there is no cluster: two of the four unique constraints are
	// over `(Lower(name), device, ...)` and both are conditional on `cluster IS NULL AND
	// device IS NOT NULL`, so a device-hosted VM has a different natural key from a
	// cluster-hosted one rather than the same key with a filter missing.
	//
	// NetBox refuses a device assignment that contradicts the device's own cluster -- a
	// device that belongs to a cluster requires that cluster named explicitly, and a device
	// in a different cluster is rejected (`clean()` lines 313-327). Both need the device's
	// row, so both stay server-side.
	//
	// NetBoxDevice lands with NBO-030. `deviceRef: {id: 42}` works today, because an id
	// needs the target's endpoint and not its CR; `deviceRef: {name: ...}` reports
	// RefKindUnavailable until the Kind ships.
	// +optional
	DeviceRef *DeviceRef `json:"deviceRef,omitempty"`

	// RoleRef is the VM's functional role (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `role ForeignKey -> dcim.DeviceRole
	// on_delete=PROTECT`).
	//
	// A **DCIM** device role. There is no virtualization-specific role model, which is the
	// easy thing to assume and the reason `dcim.DeviceRole.vm_role` exists and defaults to
	// true. It is not `RoleRef` either -- that alias is `ipam.Role`, a different model at a
	// different endpoint carried by prefixes and VLANs.
	// +optional
	RoleRef *DeviceRoleRef `json:"roleRef,omitempty"`

	// PlatformRef is the operating system running on the VM (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `platform ForeignKey -> dcim.Platform
	// on_delete=SET_NULL`).
	// +optional
	PlatformRef *PlatformRef `json:"platformRef,omitempty"`

	// TenantRef is the tenant the VM belongs to (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `tenant ForeignKey -> tenancy.Tenant
	// on_delete=PROTECT`).
	//
	// Part of the identity, and the reason the candidate list has four entries rather than
	// two: `(Lower(name), cluster, tenant)` is one constraint and `(Lower(name), cluster)
	// WHERE tenant IS NULL` is another, so the same name in the same cluster is a different
	// object per tenant *and* a tenant-less VM is its own identity rather than a missing
	// filter (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
	//
	// It is not a containment parent: a VM outliving a tenant re-assignment is normal, and
	// exactly one containment owner is allowed
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Status is the VM's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status VirtualMachineStatus `json:"status,omitempty"`

	// StartOnBoot is what the hypervisor does with this VM when the host boots. Defaulted
	// to NetBox's own `off` for the reason `status` is.
	// +kubebuilder:default=off
	// +optional
	StartOnBoot VirtualMachineStartOnBoot `json:"startOnBoot,omitempty"`

	// VCPUs is the number of virtual CPUs, with up to two decimal places
	// (docs/netbox-schema.md -> virtualization.VirtualMachine, `vcpus DecimalField
	// decimal(6,2)`).
	//
	// A string, and not a float64. NetBox returns a DecimalField as a JSON string and
	// canonicalises it, so `"2"` comes back as `"2.00"`: a float would make the round trip
	// through binary and produce `2.0000000000000004` for values that have an exact decimal
	// form, and the drift comparison already compares numerically, so `"2"` against
	// `"2.00"` is no change and no PATCH (docs/concepts/drift.md).
	//
	// Cleared with `null` rather than with `""`, like dcim.Site's latitude: DRF parses an
	// empty string as a number and rejects it, which would make `vcpus: ""`
	// admission-legal and unwritable. Set it to `""` to clear the value in NetBox; omit it
	// to leave NetBox's own alone (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=9
	// +kubebuilder:validation:XValidation:rule="self == '' || self.matches('^[0-9]{1,4}(\\\\.[0-9]{1,2})?$')",message="vcpus must be a decimal with at most 4 integer and 2 fractional digits, for example 2 or 2.50"
	// +optional
	VCPUs string `json:"vcpus,omitempty"`

	// Memory is the VM's RAM in megabytes (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `memory PositiveIntegerField`).
	//
	// The column carries no unit -- NetBox's own form labels it from the instance's
	// `RAM_BASE_UNIT` setting, so the number means MB or MiB depending on the server
	// (`netbox/virtualization/forms/model_forms.py` line 292, NetBox 4.6.8). The operator
	// writes the integer it is given either way; it is not a conversion the CRD can do.
	//
	// A pointer, because a plain int cannot tell "not managed" from "managed as zero" and
	// adopting a VM whose memory a human had set would silently clear it on the first
	// reconcile. Nil leaves NetBox's value alone; `0` writes zero
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	Memory *int32 `json:"memory,omitempty"`

	// Disk is the VM's total disk in megabytes (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `disk PositiveIntegerField`) -- MB or MiB per the
	// instance's `DISK_BASE_UNIT`, as with `memory`. Not gigabytes, which NBO-029's spec
	// table has; NetBox's own form derives the label from the same setting as RAM's.
	//
	// Set it or use NetBoxVirtualDisk, never both with different totals. NetBox fills
	// `disk` from the aggregate of a VM's virtual disks when it is null, and **rejects** a
	// value that disagrees with that aggregate
	// (`netbox/virtualization/models/virtualmachines.py` lines 330-341, NetBox 4.6.8) --
	// so the contradiction is a 400 reported as `Ready=False, Reason=Invalid` naming both
	// numbers, not a PATCH loop, and not something the operator has to guard.
	//
	// A pointer for the same reason `memory` is one.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	Disk *int32 `json:"disk,omitempty"`

	// Serial is the VM's serial number (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `serial CharField len=50`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Serial string `json:"serial,omitempty"`

	// PrimaryIP4Ref is the VM's primary IPv4 address (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `primary_ip4 OneToOneField -> ipam.IPAddress
	// on_delete=SET_NULL`).
	//
	// **Deferred, unconditionally.** The dependency ring is `VM -> IPAddress ->
	// VMInterface -> VM`: the address is assigned to an interface, the interface belongs to
	// the VM, and the VM points back at the address. No apply order breaks it, so the field
	// is stripped from the create and written by a follow-up PATCH once the reference
	// resolves. In between the VM reports `Ready=False,
	// Reason=DeferredFieldPending` and names the field in `status.deferredPending`, which
	// is what makes `kubectl wait --for=condition=Ready` on a VM mean something
	// (docs/concepts/object-lifecycle.md, NBO-015).
	//
	// NetBoxIPAddress lands with NBO-025.
	// +optional
	PrimaryIP4Ref *IPAddressRef `json:"primaryIP4Ref,omitempty"`

	// PrimaryIP6Ref is the VM's primary IPv6 address (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `primary_ip6 OneToOneField -> ipam.IPAddress
	// on_delete=SET_NULL`).
	//
	// A separate column and a separately deferred field: it behaves exactly like
	// `primaryIP4Ref` and resolves independently, so a VM whose v6 address is missing still
	// gets its v4 one written.
	// +optional
	PrimaryIP6Ref *IPAddressRef `json:"primaryIP6Ref,omitempty"`

	// Description is free text shown next to the VM. Declared on PrimaryModel rather than
	// on virtualization.VirtualMachine (docs/netbox-schema.md ->
	// virtualization.VirtualMachine, `description (PrimaryModel) CharField len=200`); an
	// inherited column is as writable as a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the VM's long-form notes field. Also inherited from PrimaryModel, and a
	// TextField rather than a CharField: it has no max_length, so there is no MaxLength
	// marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxVirtualMachine is one virtualization.VirtualMachine in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// `PRIMARY-IP` reads `.status.deferredPending` rather than the spec, and that is
// deliberate: the question a VM's primary address raises is not "what did you ask for" but
// "has it been written yet", and the spec cannot answer it. An empty column means nothing is
// outstanding.
//
// There is no `spec.macAddress` and no `spec.primaryMACAddressRef`. NetBox 4.2 moved the MAC
// to its own model, `dcim.MACAddress` with a generic FK, and `virtualization.VMInterface`
// carries only `mac_addresses GenericRelation` (docs/netbox-schema.md ->
// virtualization.VMInterface, dcim.BaseInterface). NBO-048 lands the Kind; until then the
// field is absent rather than accepted and dropped.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvm
// +kubebuilder:printcolumn:name="Cluster",type=string,JSONPath=`.spec.clusterRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="Primary-IP",type=string,JSONPath=`.status.deferredPending`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVirtualMachine struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVirtualMachineSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus       `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (v *NetBoxVirtualMachine) NetBoxSpec() *NetBoxObjectSpec { return &v.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (v *NetBoxVirtualMachine) NetBoxStatus() *NetBoxObjectStatus { return &v.Status }

// NetBoxVirtualMachineList is a list of NetBoxVirtualMachine.
// +kubebuilder:object:root=true
type NetBoxVirtualMachineList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVirtualMachine `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVirtualMachine{}, &NetBoxVirtualMachineList{})
}
