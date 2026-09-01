// The inline child sugar of ADR-0003 rule 5, for NetBoxVirtualMachine: the three entry
// structs its `interfaces`, `addresses` and `disks` lists carry, and the two capabilities the
// engine reads them through.
//
// A file of its own rather than an addition to virtualization_virtualmachine.go, so that
// "adding a kind is adding files" stays true of adding *sugar* to one as well
// (CONTRIBUTING.md, "Extensibility"). The two spec fields themselves have to live in the spec
// struct, because Go has nowhere else to put a struct field; everything that reads them is
// here.
//
// **Every field below is optional, and every child kind is fully usable standalone.** That is
// not a style choice, it is the term on which the sugar is allowed into v1alpha1 at all: an
// optional field nobody set can be removed at a version boundary without breaking anyone, and
// a materialised child is identified by its marker rather than by its parent's spec, so
// children already materialised survive their parent losing the field that declared them
// (docs/decisions/0003-ownership-and-references.md rule 5).
//
// The inline forms deliberately do not mirror the longhand specs. `parentRef`, `bridgeRef`,
// `qinqSVLANRef`, `natInsideRef`, `comments`, `tags` and `customFields` are all absent, and an
// interface or address that needs one is written as its own CR. Inline covers the common case;
// the standalone kind stays the complete one, which is what keeps the sugar from growing into
// a copy of three other specs that has to be kept in step with them
// (docs/decisions/0004-claims-first-allocation.md).
package v1alpha1

import (
	"slices"
	"sort"
	"strconv"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// The spec field names and the discriminators the derived name and the owned-by path are built
// from. `interfaces` owns the VM's own namespace of keys and takes no discriminator -- `dns-eth0`
// reads as the interface of `dns` -- and every other set takes one, because two child Kinds
// under one parent must not derive one name from one key.
//
// `interfacesField`, `addressesField` and `addressDiscriminator` are NetBoxDevice's
// (dcim_device_inline.go) and are reused rather than redeclared: they are the same two spec
// fields spelled the same way, and one definition is what keeps two parents' derived names
// consistent with each other.
const (
	// disksField is the VM's own inline list, which a device has no counterpart for.
	disksField = "disks"

	// claimDiscriminator names a materialised NetBoxIPAddressClaim:
	// `dns-eth0-claim-mgmt-net`. One inline `addresses` list materialises two Kinds, and an
	// entry of each sharing a key would otherwise derive one name.
	claimDiscriminator = "claim"

	// diskDiscriminator names a materialised NetBoxVirtualDisk: `dns-disk-scsi0`. Without it a
	// VM with a disk and an interface both called `scsi0` would derive one name for two
	// objects.
	diskDiscriminator = "disk"
)

// InlineVMInterface is one entry of NetBoxVirtualMachine.spec.interfaces: a NetBoxVMInterface
// the VM materialises and owns.
//
// `virtualMachineRef` is absent, and that is the rule rather than an omission: the
// materialiser sets it from the parent, so a field the user cannot meaningfully set does not
// exist here instead of existing and being ignored.
//
// **An inline field has two states where a longhand one has three, and it follows from
// `omitempty` rather than from a decision here.** The child is written with server-side apply,
// `omitempty` drops `description: ""` from the request, no manager claims the field, and the
// child's own pass therefore reads it as *absent* -- "leave NetBox alone" rather than "clear"
// (docs/concepts/field-ownership.md). An inline entry can set a value and leave one alone;
// clearing one is a longhand NetBoxVMInterface, where the field is the user's own and the
// distinction survives. The same is true of every optional string on the two entry types below.
//
// +kubebuilder:validation:XValidation:rule="!has(self.addresses) || self.addresses.filter(a, has(a.primary) && a.primary && has(a.address) && !a.address.contains(':')).size() <= 1",message="at most one inline IPv4 address per interface may set primary"
// +kubebuilder:validation:XValidation:rule="!has(self.addresses) || self.addresses.filter(a, has(a.primary) && a.primary && has(a.address) && a.address.contains(':')).size() <= 1",message="at most one inline IPv6 address per interface may set primary"
type InlineVMInterface struct {
	// Name is the interface's name, and **this entry's key**: it is what the derived child
	// name and the owned-by path are both built from, so changing it prunes `dns-eth0` and
	// materialises `dns-eth1` -- which in NetBox is a delete and a create with a new id, and
	// takes the interface's addresses with it.
	//
	// Case-sensitive, like the longhand kind's: `virtualization.ComponentModel`'s constraint
	// is `(virtual_machine, name)` with no `Lower()`, so `Eth0` and `eth0` are two interfaces
	// on one VM (docs/netbox-schema.md -> virtualization.ComponentModel). The derived *CR*
	// name is lowercased by slugify either way, so those two entries derive one name and are
	// reported as a Conflict rather than silently merged.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// Enabled is whether the interface is administratively up. A pointer for the reason
	// NetBoxVMInterface.spec.enabled is one: the column defaults to true, so a plain bool
	// could not tell "not managed" from "managed as false".
	// +optional
	Enabled *bool `json:"enabled,omitempty"`

	// MTU is the interface's maximum transmission unit, bounded at NetBox's own validators.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	// +optional
	MTU *int32 `json:"mtu,omitempty"`

	// Mode is how the interface treats 802.1Q tags. The same enum the longhand kind carries,
	// and the same absence of a default: an unset mode is a real state in NetBox.
	// +optional
	Mode InterfaceMode `json:"mode,omitempty"`

	// VRFRef is the VRF the interface's addresses live in.
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// UntaggedVLANRef is the interface's native VLAN.
	// +optional
	UntaggedVLANRef *VLANRef `json:"untaggedVLANRef,omitempty"`

	// TaggedVLANs are the VLANs carried tagged on this interface.
	//
	// Bounded at 32 rather than the 256 the longhand kind allows, and the reason is CEL cost
	// rather than NetBox: this list is *nested* inside `interfaces`, so the API server costs
	// its rules at the product of the two maxima, and a bound that reads fine on its own can
	// make the whole CRD unloadable one level down (docs/concepts/references.md, "A list needs
	// a bound"). A trunk enumerating more than 32 VLANs inline wants `mode: tagged-all`, or
	// the interface written as its own NetBoxVMInterface.
	// +kubebuilder:validation:MaxItems=32
	// +optional
	TaggedVLANs []VLANRef `json:"taggedVLANs,omitempty"`

	// Description is free text shown next to the interface. An inline field has two states
	// where a longhand one has three -- see the note on this type.
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Addresses are the IP addresses on this interface, each materialised as its own CR and
	// assigned to the interface this entry declares.
	//
	// Not a map list, unlike `interfaces` and `disks`: an entry's key is its `address` when it
	// states one and its pool when it says `claimFrom`, so there is no single property the API
	// server could key the list on. Two entries deriving one key are caught by the
	// materialiser instead, which reports `ChildrenReady=False, Reason=Conflict` naming both
	// and writes nothing at all (docs/concepts/inline-children.md).
	//
	// Bounded at 16 for the nesting reason `taggedVLANs` is bounded at 32. Sixteen addresses
	// on one virtual interface is already unusual; the seventeenth is written as its own
	// NetBoxIPAddress with `assignedObject.vmInterfaceRef` naming the materialised interface.
	// +kubebuilder:validation:MaxItems=16
	// +listType=atomic
	// +optional
	Addresses []InlineVMAddress `json:"addresses,omitempty"`
}

// InlineVMAddress is one entry of NetBoxVirtualMachine.spec.interfaces[].addresses: either an
// address the manifest states, or one allocated out of a pool.
//
// `assignedObject` is absent for the reason `virtualMachineRef` is absent from the interface
// entry -- the materialiser sets it, to the interface child this address is nested under.
//
// `tenantRef` is absent for a different reason, and it is worth naming: NetBoxIPAddress has no
// `tenantRef` either. `ipam.IPAddress.tenant` waits on NBO-021, and an inline field the child
// CR could not carry would be accepted and silently dropped.
//
// **`allowDuplicate` is absent, and this one is a safety property rather than a scope
// decision.** The flag makes the provenance stamp part of an address's identity; a stamped
// child that loses `status.id` -- a status write lost to a restart, a restore -- then finds no
// match it can claim and **creates a second address**
// (internal/reconciler/duplicate.go, https://github.com/ricardomolendijk/netbox-operator/issues/167).
// A materialised child is exactly the object most likely to be re-created from an unchanged
// manifest, so the field it would be dangerous on is the field it does not have. An address
// that legitimately exists twice -- anycast, a VRRP virtual address -- is written as its own
// NetBoxIPAddress, where a human has said so deliberately.
//
// +kubebuilder:validation:XValidation:rule="has(self.address) != has(self.claimFrom)",message="an inline address states exactly one of address or claimFrom"
// +kubebuilder:validation:XValidation:rule="!has(self.primary) || !self.primary || has(self.address)",message="primary needs a literal address: an inline claimFrom does not materialise an address CR for the VM to point at yet (NBO-036)"
// +kubebuilder:validation:XValidation:rule="!has(self.claimFrom) || (!has(self.status) && !has(self.role) && !has(self.vrfRef) && !has(self.dnsName) && !has(self.description))",message="status, role, vrfRef, dnsName and description describe an address, not a request for one: NetBoxIPAddressClaim carries none of them, so an inline claimFrom that set one would report success and write nothing"
type InlineVMAddress struct {
	// Address is the address and its mask, `10.20.0.10/24` or `2001:db8::1/64`, and **this
	// entry's key including the mask**: `/24` and `/25` of one host are two NetBox objects and
	// two CRs, so they are two keys and two derived names.
	//
	// The same loose pattern the longhand kind carries: it fixes the shape and the character
	// set and leaves the rest to NetBox, because a stricter regex here would be a second,
	// worse IP parser and every disagreement between the two would reject an address NetBox
	// accepts.
	// +kubebuilder:validation:MinLength=4
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:Pattern=`^[0-9A-Fa-f.:]+/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$`
	// +optional
	Address string `json:"address,omitempty"`

	// ClaimFrom allocates the address out of a pool instead of stating it, by materialising a
	// real NetBoxIPAddressClaim child.
	//
	// Sugar over the claim kind rather than a second allocation path: the same Kind, the same
	// controller, the same advisory-locked POST, so there is exactly one place an address is
	// ever allocated (docs/decisions/0004-claims-first-allocation.md).
	// +optional
	ClaimFrom *InlineAddressClaim `json:"claimFrom,omitempty"`

	// Primary makes this address the VM's `primary_ip4` or `primary_ip6`, chosen by the
	// address family.
	//
	// **Not a NetBox column on this object.** It is a statement about the *VM*: the column is
	// `virtualization.VirtualMachine.primary_ip4`, and the value is the id of this address. It
	// therefore reaches NetBox as a deferred field on the parent -- the VM's POST omits it and
	// a follow-up PATCH lands it once this child has an id -- and never as a write to the VM's
	// spec, which is what ADR-0005 §1 forbids and what Argo CD would revert on its next sync.
	//
	// At most one per family per VM, across every interface rather than per interface. Two of
	// one family, or one beside an explicit `spec.primaryIP4Ref`, is refused: two sources of
	// truth for one column is not something to resolve by precedence.
	//
	// Two states, not three: absent and `false` both mean "not the primary address", and there
	// is no NetBox value for an empty one to clear.
	// +optional
	Primary bool `json:"primary,omitempty"`

	// Status is the address's lifecycle state. Not defaulted here, unlike the longhand kind's:
	// a default would put the column in every materialised child's spec whether or not the
	// inline entry said anything, which is the opposite of "spec omission means do not manage".
	// +optional
	Status IPAddressStatus `json:"status,omitempty"`

	// Role is what the address is for. A string here and a reference on NetBoxPrefix and
	// NetBoxVLAN, exactly as on the longhand kind -- see IPAddressRole.
	// +optional
	Role IPAddressRole `json:"role,omitempty"`

	// VRFRef is the VRF this address belongs to. Unset means the global table, which is a
	// different identity rather than a missing filter (docs/concepts/lookups.md).
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// DNSName is the hostname this address resolves to.
	// +kubebuilder:validation:MaxLength=255
	// +optional
	DNSName string `json:"dnsName,omitempty"`

	// Description is free text shown next to the address.
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// InlineAddressClaim is the pool an inline address is allocated out of: the nested `claimFrom`
// of ADR-0004.
//
// Nested rather than a flat `fromPrefixRef`, and that is the whole point of the shape: a claim
// may one day allocate out of an ip-range as well as a prefix (NBO-064), and a flat spelling
// generalises only by growing a second sibling key that is mutually exclusive with the first.
// `claimFrom` generalises by adding a member -- `claimFrom: {ipRangeRef: ...}` -- with
// exactly-one-of enforced inside one field instead of across several
// (docs/decisions/0004-claims-first-allocation.md#the-inline-key-is-claimfrom-and-it-is-nested).
//
// One member today, and `prefixRef` is therefore required rather than part of a `== 1` union.
// NBO-064 adds the second member, relaxes this to optional and adds the union rule; relaxing a
// required field is not a breaking change, and a one-member union whose rule nothing can
// violate is a rule nobody can read.
type InlineAddressClaim struct {
	// PrefixRef is the prefix to allocate out of, and **the claim entry's key**: the derived
	// child name is `<vm>-<interface>-claim-<pool>`.
	//
	// The pool rather than an index or a counter, because determinism here is what makes a
	// cluster rebuild give back the same address. A claim's allocation identity is derived
	// from `(endpoint, namespace, kind, name)`
	// (docs/decisions/0005-gitops-coexistence.md), the name is derived from this key, so a VM
	// deleted and re-applied from the same manifest reclaims what it had. The cost is that two
	// inline entries claiming from one pool on one interface derive one name, which is
	// reported as a Conflict; the second one is written as its own NetBoxIPAddressClaim.
	PrefixRef PrefixRef `json:"prefixRef"`
}

// InlineVirtualDisk is one entry of NetBoxVirtualMachine.spec.disks: a NetBoxVirtualDisk the
// VM materialises and owns.
//
// Note what this does *not* do to `spec.disk`. NetBox fills the VM's `disk` column from the
// aggregate of its virtual disks when it is null and **rejects** a value that disagrees with
// that aggregate (`netbox/virtualization/models/virtualmachines.py` lines 330-341, NetBox
// 4.6.8), so `spec.disk` beside these is a loud 400 reported as `Ready=False, Reason=Invalid`
// naming both numbers -- not a PATCH loop, and not something the operator has to guard against
// by refusing the combination. Setting both consistently works and is legal, which is why
// there is no rule here forbidding it.
type InlineVirtualDisk struct {
	// Name is the disk's name, and **this entry's key**. Case-sensitive, like the interface's.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	Name string `json:"name"`

	// Size is the disk's size, in whatever unit the NetBox instance's `DISK_BASE_UNIT` names.
	// Required, because `virtualization.VirtualDisk.size` is: a disk of no stated size is not a
	// thing to let through admission, and `size: 0` is legal and explicit.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	Size int32 `json:"size"`

	// Description is free text shown next to the disk. Two states, not three -- see the note on
	// InlineVMInterface.
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// InlineChildren returns the child CRs this VM's spec declares, as a tree.
//
// The first implementation of InlineParent in the project, and the shape every later one
// follows: read the spec, build the objects, return. Pure -- it is called on every reconcile,
// it reads no API server and caches nothing -- and it names no sibling by anything but the
// deterministic ChildName, which is why that function lives in this package.
//
// Three Kinds and four sets. The two address sets share the `addresses` field and differ in
// their discriminator, because one inline list materialises both NetBoxIPAddress and
// NetBoxIPAddressClaim and the discriminator is what keeps their derived names apart.
//
// Empty sets are returned rather than skipped, and that is not laziness: a VM whose inline
// list has just been emptied has no desired child left to read a Kind off, so the pruner falls
// back to the Kinds recorded in `status.children` -- and it costs nothing to be explicit about
// what this parent could declare.
func (v *NetBoxVirtualMachine) InlineChildren() []InlineChildSet {
	interfaces := InlineChildSet{Field: interfacesField}

	for i := range v.Spec.Interfaces {
		iface := v.Spec.Interfaces[i]

		interfaces.Entries = append(interfaces.Entries, InlineChildEntry{
			Key:      iface.Name,
			Desired:  iface.child(v),
			Children: iface.addressSets(v),
		})
	}

	disks := InlineChildSet{Field: disksField, Discriminator: diskDiscriminator}

	for i := range v.Spec.Disks {
		disk := v.Spec.Disks[i]

		disks.Entries = append(disks.Entries, InlineChildEntry{Key: disk.Name, Desired: disk.child(v)})
	}

	return []InlineChildSet{interfaces, disks}
}

// child is the NetBoxVMInterface this entry declares, minus everything the materialiser owns.
//
// `endpointRef` and `deletionPolicy` are left empty on purpose: the materialiser inherits both
// from the parent unless the entry set them, and there is no inline field that sets them. An
// entry that needs a different endpoint from its VM's is not an entry, it is a separate CR.
func (i InlineVMInterface) child(v *NetBoxVirtualMachine) *NetBoxVMInterface {
	return &NetBoxVMInterface{
		Spec: NetBoxVMInterfaceSpec{
			VirtualMachineRef: VirtualMachineRef{Name: v.GetName()},
			Name:              i.Name,
			Enabled:           i.Enabled,
			MTU:               i.MTU,
			Mode:              i.Mode,
			VRFRef:            i.VRFRef,
			UntaggedVLANRef:   i.UntaggedVLANRef,
			// Cloned, not shared. The materialiser marshals the child for a server-side
			// apply and never writes into it, so sharing the parent's backing array would be
			// safe today -- and would make the next caller that does write one corrupt the
			// object it read the list from.
			TaggedVLANs: slices.Clone(i.TaggedVLANs),
			Description: i.Description,
		},
	}
}

// addressSets are the child sets nested under one interface entry: the addresses it states,
// and the claims it asks for.
//
// Two sets rather than one with a branch inside, because a set is exactly "a field, a
// discriminator, and some entries" and these two entry kinds differ in the discriminator. The
// materialiser recurses over whatever it is handed, so nesting depth is not baked into it and
// the path grows one segment per level either way.
func (i InlineVMInterface) addressSets(v *NetBoxVirtualMachine) []InlineChildSet {
	interfaceChild := ChildName(v.GetName(), []ChildSegment{{Field: interfacesField, Key: i.Name}})

	addresses := InlineChildSet{Field: addressesField, Discriminator: addressDiscriminator}
	claims := InlineChildSet{Field: addressesField, Discriminator: claimDiscriminator}

	for n := range i.Addresses {
		entry := i.Addresses[n]

		if entry.ClaimFrom != nil {
			claims.Entries = append(claims.Entries, InlineChildEntry{
				Key:     entry.ClaimFrom.key(),
				Desired: entry.claim(),
			})

			continue
		}

		addresses.Entries = append(addresses.Entries, InlineChildEntry{
			Key:     entry.Address,
			Desired: entry.child(interfaceChild),
		})
	}

	return []InlineChildSet{addresses, claims}
}

// child is the NetBoxIPAddress this entry declares.
//
// `assignedObject` names the *materialised interface's CR name*, resolved by the ordinary
// reference machinery through that CR's `status.id` -- which is the whole reason ChildName is
// exported from this package. Nothing here reads the API server: the name is derived from the
// parent's name and the entry's key, so an inline address knows what its sibling will be called
// before either exists.
func (a InlineVMAddress) child(interfaceChild string) *NetBoxIPAddress {
	return &NetBoxIPAddress{
		Spec: NetBoxIPAddressSpec{
			Address:        a.Address,
			VRFRef:         a.VRFRef,
			Status:         a.Status,
			Role:           a.Role,
			AssignedObject: &IPAssignment{VMInterfaceRef: &VMInterfaceRef{Name: interfaceChild}},
			DNSName:        a.DNSName,
			Description:    a.Description,
		},
	}
}

// claim is the NetBoxIPAddressClaim this entry declares.
//
// The claim carries no `assignedObject` and no `dnsName`, because NetBoxIPAddressClaim carries
// neither: a claim's job is to *get* an address, and the desired state of the address it got
// belongs to the NetBoxIPAddress the claim will materialise (NBO-036). So an inline `claimFrom`
// allocates and records an address and does not yet attach it to the interface it is written
// under -- exactly as a standalone claim does not, which is what keeps this sugar equivalent
// to the longhand it stands for rather than a better version of it.
func (a InlineVMAddress) claim() *NetBoxIPAddressClaim {
	return &NetBoxIPAddressClaim{Spec: NetBoxIPAddressClaimSpec{PrefixRef: *a.ClaimFrom.PrefixRef.DeepCopy()}}
}

// key is the entry key a claimFrom contributes: the pool, in whichever mode it names one.
//
// Deterministic in every mode, because the derived child name is derived from this and a
// claim's allocation identity is derived from that name. An `id` mode is prefixed so that
// `{id: 7}` and `{name: "7"}` are two keys: they are two different references, and one derived
// name for both would make a rewrite of the reference look like no change at all.
func (c InlineAddressClaim) key() string {
	ref := c.PrefixRef.AsObjectRef()

	switch {
	case ref.Name != "":
		return ref.Name
	case ref.Slug != "":
		return ref.Slug
	case ref.ID != nil:
		return "id-" + strconv.FormatInt(*ref.ID, 10)
	}

	// A lookup, flattened to sorted `key-value` pairs. Sorted because a map has no order and
	// an unordered key would derive a different name on different passes, which is a prune
	// and a create -- in NetBox, a released address and a newly allocated one.
	pairs := make([]string, 0, len(ref.Lookup))
	for name, value := range ref.Lookup {
		pairs = append(pairs, name+"-"+value)
	}
	sort.Strings(pairs)

	return strings.Join(pairs, "-")
}

// child is the NetBoxVirtualDisk this entry declares.
func (d InlineVirtualDisk) child(v *NetBoxVirtualMachine) *NetBoxVirtualDisk {
	return &NetBoxVirtualDisk{
		Spec: NetBoxVirtualDiskSpec{
			VirtualMachineRef: VirtualMachineRef{Name: v.GetName()},
			Name:              d.Name,
			Size:              d.Size,
			Description:       d.Description,
		},
	}
}

// DerivedRefs is the `primary` back-patch: the references this VM's inline addresses
// contribute to the VM's *own* payload.
//
// The only place in the operator where a child's identity flows back up into its parent's
// write, and the reason it is a derived reference rather than anything else is worth stating.
// The column is on the VM, the value is the id of an address the VM materialised, and neither
// of the two obvious mechanisms is available: the materialiser may not write
// `spec.primaryIP4Ref` (ADR-0005 §1 -- Argo CD would revert it and the two would fight at the
// shorter resync interval), and `status.children` records names rather than ids. So the
// reference is *derived*, on every pass, from data this object already has -- the child's name
// is deterministic -- and the id is read by the ordinary resolver from the child's own status.
//
// Everything downstream then needs no special case. `primaryIP4Ref` is an ordinary declared
// reference from the moment it is folded in, so it is stripped from the create, reported in
// `status.deferredPending`, and applied by the follow-up PATCH exactly as a hand-written one is
// (NBO-015) -- which is what makes the `VM -> IPAddress -> VMInterface -> VM` ring converge
// without a second write path.
//
// Pure, like InlineChildren(), and it returns an error rather than choosing: an explicit
// `spec.primaryIP4Ref` beside an inline IPv4 address marked `primary` is two answers to one
// question, and picking one by precedence would make the other a lie that nothing reports.
func (v *NetBoxVirtualMachine) DerivedRefs() ([]DerivedRef, error) {
	primaries, err := v.inlinePrimaries()
	if err != nil {
		return nil, err
	}

	out := make([]DerivedRef, 0, len(primaries))

	for _, primary := range primaries {
		if declared := v.declaredPrimary(primary.field); declared != nil {
			return nil, &derivedRefClash{
				field: primary.field, path: primary.path,
				why: "an inline address marked primary and an explicit reference are two sources of " +
					"truth for one netbox column",
			}
		}

		out = append(out, DerivedRef{Field: primary.field, Ref: ObjectRef{Name: primary.child}})
	}

	return out, nil
}

// inlinePrimary is one inline address that asked to be its VM's primary, resolved to the spec
// field it derives and the child CR it names.
type inlinePrimary struct {
	// field is the spec field the reference is derived under: `primaryIP4Ref` or
	// `primaryIP6Ref`, chosen by the address family.
	field string

	// path is the inline entry, in the owned-by-path spelling, so a Conflict message names the
	// thing the user wrote rather than the thing the operator derived.
	path string

	// child is the materialised NetBoxIPAddress's CR name.
	child string
}

// inlinePrimaries are the VM's inline primary addresses, at most one per family.
//
// The cross-interface check, and it is here rather than only in CEL because it is the half CEL
// is least able to carry: the rule is over a nested list comprehension, whose cost the API
// server charges at the product of both maxima, and a rule that fails to install is worse than
// one that is enforced twice. The CRD carries the per-interface half, which is cheap; this
// carries both.
func (v *NetBoxVirtualMachine) inlinePrimaries() ([]inlinePrimary, error) {
	var out []inlinePrimary

	for i := range v.Spec.Interfaces {
		iface := v.Spec.Interfaces[i]

		for n := range iface.Addresses {
			entry := iface.Addresses[n]
			if !entry.Primary || entry.Address == "" {
				continue
			}

			path := []ChildSegment{
				{Field: interfacesField, Key: iface.Name},
				{Field: addressesField, Discriminator: addressDiscriminator, Key: entry.Address},
			}

			found := inlinePrimary{
				field: primaryRefField(entry.Address),
				path:  ChildPath(path),
				child: ChildName(v.GetName(), path),
			}

			if first := indexOfField(out, found.field); first >= 0 {
				return nil, &derivedRefClash{
					field: found.field, path: out[first].path, other: found.path,
					why: "at most one inline address per family may set primary, across every " +
						"interface of one virtual machine",
				}
			}

			out = append(out, found)
		}
	}

	return out, nil
}

// declaredPrimary is the explicit reference the spec wrote for one derived field, or nil.
//
// A switch on the field name rather than reflection, because there are exactly two fields and
// the alternative reads a struct tag to find a value the caller is holding a name for.
func (v *NetBoxVirtualMachine) declaredPrimary(field string) *IPAddressRef {
	if field == primaryIP6RefField {
		return v.Spec.PrimaryIP6Ref
	}

	return v.Spec.PrimaryIP4Ref
}

// The two spec fields a `primary` inline address can derive. Named constants because the
// derived reference, the Conflict message and registry.Field.Spec all have to agree on the
// spelling, and `primaryIP4Ref` is one a camelCase convention gets wrong.
const (
	primaryIP4RefField = "primaryIP4Ref"
	primaryIP6RefField = "primaryIP6Ref"
)

// primaryRefField is the spec field an address of this family derives.
//
// The family is read off the literal rather than parsed: an IPv6 address contains a colon and
// an IPv4 address cannot, which is the same test the CRD's own CEL rules make -- and using two
// different tests would let admission and the controller disagree about which column an
// address belongs in.
func primaryRefField(address string) string {
	if strings.Contains(address, ":") {
		return primaryIP6RefField
	}

	return primaryIP4RefField
}

// indexOfField is the first entry deriving field, or -1.
func indexOfField(entries []inlinePrimary, field string) int {
	return slices.IndexFunc(entries, func(entry inlinePrimary) bool { return entry.field == field })
}

// Compile-time proof that the VM implements both halves of the sugar. A capability the engine
// reaches by type assertion is a contract nothing else checks, so a signature drifting out of
// shape would otherwise show up as a VM that quietly materialises nothing.
var (
	_ InlineParent    = (*NetBoxVirtualMachine)(nil)
	_ InlineRefParent = (*NetBoxVirtualMachine)(nil)
	_ client.Object   = (*NetBoxVMInterface)(nil)
	_ client.Object   = (*NetBoxIPAddress)(nil)
	_ client.Object   = (*NetBoxIPAddressClaim)(nil)
	_ client.Object   = (*NetBoxVirtualDisk)(nil)
)
