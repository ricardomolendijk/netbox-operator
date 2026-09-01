// Inline components on a NetBoxDevice: the sugar of ADR-0003 rule 5, as the data the
// materialiser consumes.
//
// A file of its own rather than more lines in dcim_device.go, because it is a different kind
// of declaration: everything here describes *another* Kind's spec, so it moves when
// NetBoxInterface or NetBoxIPAddress moves rather than when dcim.Device does.
//
// One inline list ships, `interfaces`, with `addresses` nested under it. `dcim.Device` has
// nine more component relations -- console ports, console server ports, power ports, power
// outlets, front ports, rear ports, device bays, module bays, inventory items -- and none of
// them has a Kind yet (NBO-052 for power, NBO-053 for modules and the rest). Declaring the
// field before the Kind exists would accept input the operator cannot honour, which is worse
// than not offering it: NetBox drops a field name it does not know rather than rejecting it,
// so the write would report success and store nothing. Each one arrives as one more
// InlineChildSet returned by the method below, plus its own longhand Kind, and no engine
// change.
package v1alpha1

// Bounds on the two inline lists, and on the one to-many reference nested inside them.
//
// The house bound for a to-many reference is 256 (docs/concepts/references.md, "A list needs
// a bound"), and these are the first lists where that number cannot simply be repeated:
// validation cost multiplies through every level, so the API server costs
// `spec.interfaces[].taggedVLANs[]`'s five ObjectRef rules at 128 x 128 rather than at either
// bound alone. The three chosen here multiply out to 16 384 costed reference items for the
// tagged-VLAN rule set and 2 048 for the addresses', against a measured single-field ceiling
// of 57 803 -- so the whole nested tree costs less than one top-level `[]ObjectRef` field
// would at the house bound, and #185 adding rules to ObjectRef cannot make it unaffordable.
//
// Each is also a statement about the API in its own right, which is the half the reference
// page states:
//
//   - **128 interfaces** (`spec.interfaces`, on NetBoxDeviceSpec): past every fixed-form
//     switch -- 48 or 64 ports plus uplinks and a management port -- and every server. A
//     modular chassis with more ports than that is not a device anybody hand-writes an inline
//     list for, and the longhand NetBoxInterface has no bound at all.
//   - **128 tagged VLANs** per inline interface, where the longhand kind allows 256: a trunk
//     enumerating more than 128 VLANs wants `mode: tagged-all`, which is one field instead of
//     a list.
//   - **16 addresses** per inline interface: an IPv4, an IPv6, and room for the VRRP/HSRP
//     virtuals and a secondary or two. An interface with more than sixteen addresses is real,
//     and it is a NetBoxIPAddress each.
//
// The numbers are literals in the markers below because a kubebuilder marker cannot read a
// Go constant. They are asserted against the generated CRDs by
// TestInlineListBoundsAreTheDocumentedOnes rather than left to agree by inspection.

// interfacesField, addressesField and addressDiscriminator are the three strings the derived
// name and the owned-by path are built from, written once because they appear in an
// annotation a human reads, in a name that must never change under a live object, and in the
// sibling lookup below.
const (
	interfacesField      = "interfaces"
	addressesField       = "addresses"
	addressDiscriminator = "ip"
)

// InlineInterface is one entry of NetBoxDeviceSpec.Interfaces: a dcim.Interface on this
// device, declared inline and materialised as a real NetBoxInterface CR.
//
// The fields are the longhand kind's own types -- InterfaceType, InterfaceMode, VLANRef --
// so every enum, pattern and length limit is declared once, in dcim_interface.go, and an
// inline entry cannot drift from the Kind it becomes. What is missing from this struct is
// the interesting part, and every absence is a decision:
//
//   - `deviceRef` and `assignedObject`, because the materialiser sets them from the parent.
//     A field the user cannot meaningfully set does not exist.
//   - `lagRef`, `parentRef` and `bridgeRef`, replaced by the sibling *keys* `lag`, `parent`
//     and `bridge` -- see InlineInterface.LAG.
//   - `markConnected`, `wwn`, `txPower`, `rfRole`, `rfChannel`, `rfChannelFrequency`,
//     `rfChannelWidth` and `qinqSVLANRef`. The inline form deliberately does not mirror the
//     longhand spec (ADR-0003 rule 5: the standalone kind stays the complete one), and every
//     one of those is either wireless-only or a column somebody setting up a chassis by hand
//     is not setting. Write a NetBoxInterface for an interface that needs them; nothing about
//     doing so is a downgrade, and it is one line longer.
type InlineInterface struct {
	// Name is the interface's name, and this entry's key.
	//
	// The key is what the child's derived name and its owned-by path are both built from, so
	// changing it prunes the old child and materialises a new one -- which in NetBox is a
	// delete and a create, taking the interface's addresses with it
	// (docs/concepts/inline-children.md).
	//
	// **Matched case-sensitively in NetBox and case-insensitively in a Kubernetes name**, and
	// that asymmetry has a consequence here. `dcim.ComponentModel`'s constraint is
	// `UniqueConstraint(fields=('device', 'name'))` with no `Lower()`, so `Eth0` and `eth0`
	// are two interfaces on one device (docs/reference/netboxinterface.md) -- but a derived
	// child name is slugified, which lowercases, so the two derive one CR name. The
	// materialiser reports that as `ChildrenReady=False, Reason=Conflict` and writes
	// **nothing at all**, rather than letting two entries overwrite each other on alternate
	// reconciles. Two interfaces that differ only in case are a pair the inline form cannot
	// express; write at least one of them as a NetBoxInterface.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// Type is the physical or virtual form of the interface.
	//
	// Required, because `dcim.Interface.type` is `REQ` (docs/netbox-schema.md ->
	// dcim.Interface, `type CharField REQ len=50 choices=InterfaceTypeChoices`) and there is
	// no sensible default: `1000base-t` would be a guess that silently creates the wrong
	// hardware.
	Type InterfaceType `json:"type"`

	// Label is the physical label on the port, where it differs from the name.
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Label string `json:"label,omitempty"`

	// Enabled is whether the interface is administratively up. A pointer, so that `false` is
	// distinguishable from unset (docs/concepts/field-ownership.md).
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MgmtOnly marks the interface as out-of-band management only. A pointer for the same
	// reason as Enabled.
	// +optional
	MgmtOnly *bool `json:"mgmtOnly,omitempty"`

	// MTU is the maximum transmission unit in bytes.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	// +optional
	MTU *int32 `json:"mtu,omitempty"`

	// Speed is the configured speed in kbps.
	// +kubebuilder:validation:Minimum=0
	// +optional
	Speed *int64 `json:"speed,omitempty"`

	// Duplex is the configured duplex mode.
	// +optional
	Duplex InterfaceDuplex `json:"duplex,omitempty"`

	// Mode is the 802.1Q tagging mode.
	// +optional
	Mode InterfaceMode `json:"mode,omitempty"`

	// PoEMode is whether the interface sources or draws power over Ethernet.
	// +optional
	PoEMode InterfacePoEMode `json:"poeMode,omitempty"`

	// PoEType is the PoE standard the interface implements.
	// +optional
	PoEType InterfacePoEType `json:"poeType,omitempty"`

	// LAG is the **key of a sibling inline interface** this one is a member of, not a
	// reference and not a CR name: write `lag: bond0` and the bond is the entry named
	// `bond0` in this same list.
	//
	// A key rather than an ObjectRef, and it is the one place the inline form is not simply a
	// shorter spelling of the longhand one. An ObjectRef would force the user to write
	// `lagRef: {name: <device>-bond0}`, which hardcodes the derived-name algorithm into their
	// manifest -- so the day a long device name starts taking the hash-suffix path
	// (api/v1alpha1/inline_children.go, ChildName), every such manifest would point at a name
	// that no longer exists and the child would sit at `RefNotFound`. A key is stable because
	// it is the user's own input; the cost is one lookup in InlineChildren.
	//
	// Resolved through the exported ChildName helper, so the child gets an ordinary
	// `lagRef: {name: ...}` and from there it is an ordinary deferred self-FK: `lag` is left
	// out of the child's POST and applied by a follow-up PATCH once the sibling has an id
	// (`DeferIfUnresolved`, internal/registry/dcim_interface.go, NBO-015). The order the two
	// entries appear in the list is therefore irrelevant.
	//
	// A key that names no sibling is **not silently dropped**: the child is written with a
	// `lagRef` naming a CR that does not exist, and reports `RefsResolved=False,
	// Reason=RefNotFound` naming it, which leaves the device `ChildrenReady=False,
	// Reason=PendingChildren`. Two entries naming each other are a ring, reported as
	// `RefCycle` on the children rather than deferred forever (NBO-016).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	LAG string `json:"lag,omitempty"`

	// Parent is the key of the sibling inline interface this one is a sub-interface of. The
	// same shape, the same resolution and the same deferral as LAG.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Parent string `json:"parent,omitempty"`

	// Bridge is the key of the sibling inline interface this one is bridged to. The same
	// shape, the same resolution and the same deferral as LAG.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +optional
	Bridge string `json:"bridge,omitempty"`

	// UntaggedVLANRef is the VLAN carried untagged on this interface.
	// +optional
	UntaggedVLANRef *VLANRef `json:"untaggedVLANRef,omitempty"`

	// TaggedVLANs are the VLANs carried tagged on this interface.
	//
	// Bounded at 128 rather than the longhand kind's 256: this list is nested inside
	// `interfaces`, so the API server costs its CEL rules at the product of the two bounds --
	// see the constants at the top of this file.
	// +kubebuilder:validation:MaxItems=128
	// +optional
	TaggedVLANs []VLANRef `json:"taggedVLANs,omitempty"`

	// VRFRef is the VRF this interface's addresses live in.
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// Description is free text shown next to the interface.
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Addresses are the ipam.IPAddress objects assigned to this interface, materialised as
	// NetBoxIPAddress children of the **device** -- the device is what declared them, and a
	// controller owner reference names the object that created another, not the object it
	// points at (ADR-0003 rule 3). The containment owner reference naming the interface is
	// added by the address's own pass, and the two coexist.
	//
	// A list with a key, so reordering it changes no child's name, path or resourceVersion.
	// +kubebuilder:validation:MaxItems=16
	// +listType=map
	// +listMapKey=address
	// +optional
	Addresses []InlineIPAddress `json:"addresses,omitempty"`
}

// InlineIPAddress is one entry of InlineInterface.Addresses: an ipam.IPAddress assigned to
// the interface it is nested under, materialised as a real NetBoxIPAddress CR.
//
// Four fields, against NetBoxIPAddressSpec's twelve, and the absences are the same kind of
// decision as on InlineInterface:
//
//   - `assignedObject` is set by the materialiser to the sibling interface child.
//   - `claimFrom` is absent. An inline address that names a pool instead of a literal
//     materialises a NetBoxIPAddressClaim, which is ADR-0004's single allocation code path
//     and NBO-036's ticket; `fromPrefixRef`, the spelling an older draft used, does not exist
//     and never will (docs/reference/netboxdevice.md).
//   - `allowDuplicate` is absent, deliberately and permanently. It makes the provenance stamp
//     part of the object's identity, so a materialised child that lost `status.id` would
//     create a *second* NetBox address rather than adopting its own
//     (internal/reconciler/duplicate.go, issue #167). An anycast or VRRP address that needs it
//     is written as its own NetBoxIPAddress.
//   - `tenantRef` is absent because NetBoxIPAddress has no `tenantRef` to carry it to: the
//     column waits on the longhand kind (api/v1alpha1/ipam_ipaddress.go). Accepting it here
//     would report success and write nothing.
//   - `role`, `natInsideRef`, `description` and `comments` are the longhand kind's, for the
//     reason InlineInterface leaves out the wireless columns.
type InlineIPAddress struct {
	// Address is the address and its mask, `10.0.20.10/24` or `2001:db8::1/64`, and this
	// entry's key.
	//
	// **The prefix length is part of the key**, so `10.0.20.10/24` and `10.0.20.10/25` are two
	// entries, two NetBox objects and two child CRs -- which is what NetBox holds, since an
	// ipam.IPAddress records a host and the prefix it sits in.
	//
	// The pattern and the length limits are the longhand kind's, so there is one definition
	// of what an address looks like (api/v1alpha1/ipam_ipaddress.go).
	// +kubebuilder:validation:MinLength=4
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:Pattern=`^[0-9A-Fa-f.:]+/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$`
	Address string `json:"address"`

	// Status is the address's lifecycle state. Undefaulted here although the longhand kind
	// defaults it to `active`: a default on the inline entry would put `status` into every
	// child's applied spec, so the child's own default -- which is NetBox's -- is what
	// applies, and there is exactly one place the value comes from.
	// +optional
	Status IPAddressStatus `json:"status,omitempty"`

	// VRFRef is the VRF this address belongs to. Unset means the global table, which is a
	// different identity rather than a missing filter (docs/concepts/lookups.md).
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// DNSName is the hostname this address resolves to.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	DNSName string `json:"dnsName,omitempty"`
}

// InlineChildren returns the child CRs this device's inline lists declare: one
// NetBoxInterface per entry of spec.interfaces, and one NetBoxIPAddress per address nested
// under each.
//
// The whole of the device's per-kind knowledge of child materialisation, and the whole of
// what NBO-034 adds to the reconcile path: the engine's side is one type assertion on
// InlineParent (internal/reconciler/children.go), so nothing below is reachable from a switch
// on Kind and nothing above it had to learn what a device is.
//
// Pure, as the interface requires: it builds objects out of the spec and reads nothing else.
// The one thing that looks like a lookup -- resolving a sibling key for `lag` -- is arithmetic
// on the parent's own name, not a read.
//
// The set is returned even when the list is empty, so that `interfaces: []` and an omitted
// `interfaces` are the same instruction. They are, for once: an inline list has no third
// state, because there is no NetBox column behind it to leave alone. Both mean "declare no
// children", and both prune the children a previous spec declared.
func (d *NetBoxDevice) InlineChildren() []InlineChildSet {
	interfaces := InlineChildSet{Field: interfacesField, Entries: make([]InlineChildEntry, 0, len(d.Spec.Interfaces))}

	for _, entry := range d.Spec.Interfaces {
		interfaces.Entries = append(interfaces.Entries, InlineChildEntry{
			Key:      entry.Name,
			Desired:  entry.child(d),
			Children: entry.addresses(d),
		})
	}

	return []InlineChildSet{interfaces}
}

// child renders one inline entry as the NetBoxInterface the materialiser will apply.
//
// Everything the materialiser owns is left off: the name, the namespace, the labels, the
// annotations and the owner references, plus `endpointRef` and `deletionPolicy`, which it
// inherits from the parent unless the entry set them -- and an inline entry cannot set them,
// which is why they are absent from InlineInterface rather than copied here.
func (in InlineInterface) child(d *NetBoxDevice) *NetBoxInterface {
	return &NetBoxInterface{
		Spec: NetBoxInterfaceSpec{
			// The parent's metadata.name, because that is what a `name`-mode reference
			// resolves through -- the CR, not the NetBox object. It is also immutable, so the
			// reference cannot go stale under a live child.
			DeviceRef: DeviceRef{Name: d.Name},

			Name:            in.Name,
			Type:            in.Type,
			Label:           in.Label,
			Enabled:         in.Enabled,
			MgmtOnly:        in.MgmtOnly,
			MTU:             in.MTU,
			Speed:           in.Speed,
			Duplex:          in.Duplex,
			Mode:            in.Mode,
			PoEMode:         in.PoEMode,
			PoEType:         in.PoEType,
			LagRef:          siblingInterfaceRef(d, in.LAG),
			ParentRef:       siblingInterfaceRef(d, in.Parent),
			BridgeRef:       siblingInterfaceRef(d, in.Bridge),
			UntaggedVLANRef: in.UntaggedVLANRef,
			TaggedVLANs:     in.TaggedVLANs,
			VRFRef:          in.VRFRef,
			Description:     in.Description,
		},
	}
}

// addresses renders one inline interface's nested address list as a child set.
//
// The discriminator is what keeps an address child from colliding with an interface child of
// the same key, and it is in the *name* rather than in the path: `<device>-eth0-ip-10-0-20-10-24`
// against `<device>-eth0`. Nil for an interface with no addresses, so an entry that declares
// none carries no empty set into the path.
func (in InlineInterface) addresses(d *NetBoxDevice) []InlineChildSet {
	if len(in.Addresses) == 0 {
		return nil
	}

	set := InlineChildSet{
		Field:         addressesField,
		Discriminator: addressDiscriminator,
		Entries:       make([]InlineChildEntry, 0, len(in.Addresses)),
	}

	for _, address := range in.Addresses {
		set.Entries = append(set.Entries, InlineChildEntry{
			Key:     address.Address,
			Desired: address.child(d, in.Name),
		})
	}

	return []InlineChildSet{set}
}

// child renders one inline address as the NetBoxIPAddress the materialiser will apply,
// assigned to the interface child its entry is nested under.
//
// `assignedObject.interfaceRef` is the generic-FK member that makes NetBox record
// `assigned_object_type: "dcim.interface"`, and it names the *sibling child CR* rather than
// the NetBox interface, so the pair is written atomically once that child has an id
// (docs/concepts/generic-refs.md).
func (a InlineIPAddress) child(d *NetBoxDevice, interfaceKey string) *NetBoxIPAddress {
	return &NetBoxIPAddress{
		Spec: NetBoxIPAddressSpec{
			Address: a.Address,
			Status:  a.Status,
			VRFRef:  a.VRFRef,
			DNSName: a.DNSName,
			AssignedObject: &IPAssignment{
				InterfaceRef: &InterfaceRef{Name: interfaceChildName(d, interfaceKey)},
			},
		},
	}
}

// siblingInterfaceRef turns a sibling *key* into the reference the child carries, and returns
// nil for an entry that named none.
//
// Nil rather than an empty ObjectRef: an empty reference is a value the API server rejects
// (objectref.go, "no mode at all"), and a nil pointer is what "this field is not declared"
// looks like everywhere else in a spec.
func siblingInterfaceRef(d *NetBoxDevice, key string) *InterfaceRef {
	if key == "" {
		return nil
	}

	return &InterfaceRef{Name: interfaceChildName(d, key)}
}

// interfaceChildName is the derived name of the interface child at one key of
// spec.interfaces, which is the one piece of the naming algorithm the device has to be able
// to compute for itself: an address's assignment and a LAG membership both have to name a
// sibling the same pass materialised.
//
// Through the exported ChildName helper rather than by string concatenation, so the sibling
// reference and the child's own name are one calculation and cannot disagree -- including on
// the truncate-and-hash path a long device name takes (api/v1alpha1/inline_children.go).
func interfaceChildName(d *NetBoxDevice, key string) string {
	return ChildName(d.Name, []ChildSegment{{Field: interfacesField, Key: key}})
}
