package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IPAddressStatus is one value of NetBox's IPAddressStatusChoices.
//
// docs/netbox-schema.md -> ipam.IPAddress records the column as
// `status CharField len=50 def=UNRESOLVED:IPAddressStatusChoices.STATUS_ACTIVE
// choices=IPAddressStatusChoices` -- the choice *class*, not its members, because the AST
// walk cannot evaluate a Django choice class (the same limitation SiteStatus documents).
// These five are read from `netbox/ipam/choices.py`, `IPAddressStatusChoices`, in the 4.6.8
// tree the digest was taken from.
//
// +kubebuilder:validation:Enum=active;reserved;deprecated;dhcp;slaac
type IPAddressStatus string

const (
	// IPAddressStatusActive is an address in use, and NetBox's own default.
	IPAddressStatusActive IPAddressStatus = "active"

	// IPAddressStatusReserved is an address set aside and not yet in use.
	IPAddressStatusReserved IPAddressStatus = "reserved"

	// IPAddressStatusDeprecated is an address being retired.
	IPAddressStatusDeprecated IPAddressStatus = "deprecated"

	// IPAddressStatusDHCP is an address handed out by DHCP rather than assigned.
	IPAddressStatusDHCP IPAddressStatus = "dhcp"

	// IPAddressStatusSLAAC is an address a host gave itself by IPv6 autoconfiguration.
	IPAddressStatusSLAAC IPAddressStatus = "slaac"
)

// IPAddressRole is one value of NetBox's IPAddressRoleChoices.
//
// A string on this model and a foreign key on its neighbours, which is the trap this type
// exists to make visible: docs/netbox-schema.md gives `ipam.IPAddress.role` as
// `CharField len=50 choices=IPAddressRoleChoices`, while `ipam.Prefix.role` and
// `ipam.VLAN.role` are `ForeignKey -> ipam.Role`. Same JSON key, different type, adjacent
// kinds -- so this kind has `role` and those get `roleRef`. The engine already treats the
// two differently without being told (a choice column compares on `.value`, a related
// field on `.id` -- internal/netbox/drift.go, unwrapNested), but a reader has to be.
//
// The empty value is in the enum on purpose: NetBox's column is blank-able, so `""` is the
// cleared value and the field is one of the three-state ones
// (docs/concepts/field-ownership.md). Without it there would be no way to say "this address
// has no role" once one had been set.
//
// The values are read from `netbox/ipam/choices.py`, `IPAddressRoleChoices`, in the 4.6.8
// tree. Six of the eight -- everything but `loopback` and `secondary` -- exist *in order
// to* be duplicated: a VRRP virtual address appears once per participating router. See
// spec.allowDuplicate.
//
// +kubebuilder:validation:Enum="";loopback;secondary;anycast;vip;vrrp;hsrp;glbp;carp
type IPAddressRole string

const (
	// IPAddressRoleLoopback is a device's loopback address, normally a /32 or /128.
	IPAddressRoleLoopback IPAddressRole = "loopback"

	// IPAddressRoleSecondary is an additional address on an interface.
	IPAddressRoleSecondary IPAddressRole = "secondary"

	// IPAddressRoleAnycast is an address announced from several places at once.
	IPAddressRoleAnycast IPAddressRole = "anycast"

	// IPAddressRoleVIP is a virtual address in front of a service.
	IPAddressRoleVIP IPAddressRole = "vip"

	// IPAddressRoleVRRP is a VRRP virtual address, shared by the routers in the group.
	IPAddressRoleVRRP IPAddressRole = "vrrp"

	// IPAddressRoleHSRP is Cisco's equivalent of VRRP.
	IPAddressRoleHSRP IPAddressRole = "hsrp"

	// IPAddressRoleGLBP is Cisco's load-balancing first-hop redundancy protocol.
	IPAddressRoleGLBP IPAddressRole = "glbp"

	// IPAddressRoleCARP is the BSD first-hop redundancy protocol.
	IPAddressRoleCARP IPAddressRole = "carp"
)

// NetBoxIPAddressSpec describes one ipam.IPAddress.
//
// There is no `fromPrefixRef`, no `prefixLength` and no allocation mode. "Give me a free
// address" is NetBoxIPAddressClaim, a separate kind
// (docs/decisions/0004-claims-first-allocation.md, NBO-036); this kind states an address it
// was given.
//
// Two columns ipam.IPAddress has are deliberately absent. `tenant`
// (docs/netbox-schema.md -> ipam.IPAddress, `tenant ForeignKey -> tenancy.Tenant`) waits on
// NBO-021's `tenantRef`, because a field that is accepted and never written reports success
// while doing nothing. `nat_outside` is not a column at all -- it is the reverse accessor of
// the `nat_inside` self-FK -- so there is nothing to write and no drift to detect.
//
// `tags` and `customFields` are absent for the same reason they are absent from
// NetBoxSite: they are the envelope's own columns until a kind declares them, and no
// shipped kind does yet.
type NetBoxIPAddressSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Address is the address and its mask, `10.0.20.1/24` or `2001:db8::1/64`.
	//
	// Host bits are **preserved**, and that is the whole point of an ipam.IPAddress: it
	// records a host *and* the prefix the host sits in. NetBoxPrefix masks (NBO-024); this
	// kind must not, so no rule here sets or clears the host portion. A `/32` or a `/128`
	// is equally valid and is the normal way to record a loopback, so nothing requires the
	// host portion to be set either.
	//
	// The pattern is deliberately loose: it fixes the shape (`<address>/<0-128>`) and the
	// character set, and leaves the rest to NetBox, which validates an IPAddressField
	// properly and answers a bad one with a 400 that becomes Ready=False, Reason=Invalid
	// carrying NetBox's own message. A stricter regex here would be a second, worse
	// implementation of an IP parser, and every disagreement between it and NetBox would
	// reject an address NetBox accepts.
	// +kubebuilder:validation:MinLength=4
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:Pattern=`^[0-9A-Fa-f.:]+/([0-9]|[1-9][0-9]|1[01][0-9]|12[0-8])$`
	Address string `json:"address"`

	// AllowDuplicate says that this address may legitimately exist in NetBox more than
	// once, so several objects matching the natural key is not an error
	// (decision #177).
	//
	// NetBox decides whether a duplicate is allowed, through configuration this operator
	// does not own: `ipam.VRF.enforce_unique` defaults to true
	// (docs/netbox-schema.md -> ipam.VRF), and the global table depends on the instance's
	// ENFORCE_GLOBAL_UNIQUE. Where duplicates are permitted they are often the point --
	// anycast, and the VRRP/HSRP/GLBP/CARP virtual addresses, which exist once per
	// participating router.
	//
	// Setting it changes this object's identity. Without it, the natural key is the address
	// and its VRF, and two matches are a Conflict naming both. With it, the key is the
	// address, its VRF **and this CR's provenance stamp**: the operator claims the matching
	// object whose `k8s_uid` custom field holds this CR's own metadata.uid, and creates
	// another only when every match provably belongs to somebody else.
	//
	// Two consequences worth knowing before setting it:
	//
	//   - it requires the endpoint's spec.managedBy (docs/operations/provenance.md).
	//     Without the stamp there is nothing that could tell two identical addresses apart,
	//     and an object the operator could not recognise again is refused rather than
	//     created.
	//   - a match with **no** stamp -- an address created before the operator, or by
	//     another tool -- is refused rather than duplicated. "I cannot tell which of these
	//     is mine" is the worst moment to make another one. To take such an address over,
	//     unset this field and use spec.onConflict: Adopt.
	// +optional
	AllowDuplicate bool `json:"allowDuplicate,omitempty"`

	// VRFRef is the VRF this address belongs to. Unset means the global table, which is a
	// different identity rather than a missing filter -- see the natural keys in
	// internal/registry/ipam_ipaddress.go and docs/concepts/lookups.md.
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// Status is the address's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status IPAddressStatus `json:"status,omitempty"`

	// Role is what the address is for. A string here and a reference on NetBoxPrefix and
	// NetBoxVLAN -- see IPAddressRole.
	//
	// Three-state like the other optional fields: leaving `role` out leaves NetBox's own
	// value alone, and `role: ""` clears it -- which is why the empty value is one of the
	// enum's members (docs/concepts/field-ownership.md). It carries no tri-state sentence of
	// its own, because the schema check that reads those treats any enum as rejecting the
	// empty value and this enum accepts it.
	// +optional
	Role IPAddressRole `json:"role,omitempty"`

	// AssignedObject is what the address is attached to: a device interface, a
	// virtual-machine interface, or an FHRP group.
	//
	// The polymorphic pair `(assigned_object_type, assigned_object_id)`, written atomically
	// or not at all (docs/concepts/generic-refs.md). At most one member may be set and none
	// is legal: both columns are nullable, so an unassigned address is an ordinary state
	// rather than an error -- the `REQ` the schema digest prints against the
	// `assigned_object` row is an extractor artefact, and believing it would make half of a
	// normal apply illegal.
	//
	// None of the three target kinds exists yet (NBO-029, NBO-030, NBO-055). Until they do,
	// `name` mode reports RefsResolved=False, Reason=RefKindUnavailable naming the field and
	// the missing kind, while `slug`, `lookup` and `id` resolve against NetBox directly and
	// work today -- which is how an address is attached to an interface a human made.
	//
	// Omit it to leave NetBox's own assignment alone; write `assignedObject: {}` to clear
	// both columns. The two are different intents (docs/concepts/field-ownership.md).
	// +optional
	AssignedObject *IPAssignment `json:"assignedObject,omitempty"`

	// NatInsideRef is the address this one is the outside NAT address for
	// (docs/netbox-schema.md -> ipam.IPAddress, `nat_inside ForeignKey -> ipam.IPAddress
	// on_delete=SET_NULL`).
	//
	// Self-referential, so two addresses applied together resolve on the second pass: the
	// reference is left out of the create, reported on RefsResolved, and PATCHed in when the
	// target has an id. Two addresses each naming the other is a ring the resolver reports
	// as RefsResolved=False, Reason=RefCycle rather than requeueing forever (NBO-016).
	//
	// NetBox does not constrain the address families of a NAT pair, so a v6 nat_inside on a
	// v4 address -- NAT64 -- is accepted here too. The operator adds no validation NetBox
	// does not have.
	// +optional
	NatInsideRef *IPAddressRef `json:"natInsideRef,omitempty"`

	// DNSName is the hostname this address resolves to
	// (docs/netbox-schema.md -> ipam.IPAddress, `dns_name CharField len=255`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=255
	// +optional
	DNSName string `json:"dnsName,omitempty"`

	// Description is free text shown next to the address. Inherited from PrimaryModel, so
	// docs/netbox-schema.md lists it under the base rather than under ipam.IPAddress.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the address's long-form notes field. Also inherited from PrimaryModel,
	// and a TextField rather than a CharField: it has no max_length, so there is no
	// MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxIPAddress is one ipam.IPAddress in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// spec.deletionPolicy defaults to Retain on this kind, which is decision #176: deleting an
// ipam.IPAddress frees the address for reallocation, and if a claim allocated it that is
// destructive and has no undo. The default is data on the kind's Descriptor
// (registry.Descriptor.RetainOnDelete) rather than a CRD marker, because the field is
// declared once on the shared envelope -- see docs/concepts/deletion.md for the table.
//
// There is no ASSIGNED printer column. Rendering `dcim.interface/42` needs the *resolved*
// pair, and a resolved generic FK is not recorded on the status (NBO-019); a JSONPath into
// the spec could only ever show one of the three members.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbip
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.spec.address`
// +kubebuilder:printcolumn:name="VRF",type=string,JSONPath=`.spec.vrfRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="Role",type=string,JSONPath=`.spec.role`
// +kubebuilder:printcolumn:name="DNS",type=string,JSONPath=`.spec.dnsName`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPAddress struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPAddressSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxIPAddress) NetBoxSpec() *NetBoxObjectSpec { return &a.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxIPAddress) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxIPAddressList is a list of NetBoxIPAddress.
// +kubebuilder:object:root=true
type NetBoxIPAddressList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPAddress `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPAddress{}, &NetBoxIPAddressList{})
}
