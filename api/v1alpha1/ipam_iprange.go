package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IPRangeStatus is one value of NetBox's IPRangeStatusChoices.
//
// Three values, and the absent one is the interesting one: `IPRangeStatusChoices` has no
// `container` (netbox/ipam/choices.py, NetBox 4.6.8 -- `active`, `reserved`, `deprecated`).
// A range is not a container for anything; addresses inside it are inside it because of what
// they are. So the `status: container` guard that stops an address claim allocating out of a
// container prefix has no analogue here, and it is skipped rather than faked.
//
// +kubebuilder:validation:Enum=active;reserved;deprecated
type IPRangeStatus string

const (
	// IPRangeStatusActive is a range in service, and NetBox's own default.
	IPRangeStatusActive IPRangeStatus = "active"

	// IPRangeStatusReserved is a range set aside for future use.
	IPRangeStatusReserved IPRangeStatus = "reserved"

	// IPRangeStatusDeprecated is a range being retired.
	IPRangeStatusDeprecated IPRangeStatus = "deprecated"
)

// NetBoxIPRangeSpec describes one ipam.IPRange.
//
// A run of consecutive addresses, held as its two endpoints: `10.0.30.128/24` through
// `10.0.30.191/24` is 64 addresses, and NetBox stores exactly that -- two `IPAddressField`
// columns and a derived count. It is not a prefix: a range need not be aligned, need not be a
// power of two long, and cannot contain child prefixes. What it is for is the block of a
// network somebody else hands out -- a DHCP scope, a load-balancer pool, an address space
// delegated to another team -- recorded so that this operator and that somebody do not both
// hand out `10.0.30.150`.
//
// **There is no `size` field, and that is not an omission.** `ipam.IPRange.size` is
// `editable=False` and computed in `save()` as `end - start + 1` (netbox/ipam/models/ip.py,
// NetBox 4.6.8), so it is not in the write serializer at all: a `size` in a payload is
// silently dropped. It is `REQ` in the schema digest because the *column* is not nullable,
// which is the trap docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest describes
// for a different pair of columns. The two endpoints are the input; the count is NetBox's
// answer, and this kind reports it in status rather than accepting it in spec.
//
// There is no scope union either -- `ipam.IPRange` is `ContactsMixin, PrimaryModel` with no
// `CachedScopeMixin` (docs/netbox-schema.md -> ipam.IPRange) -- and no `parentRef`: a range's
// place in the hierarchy is arithmetic, exactly as a prefix's is.
//
// Like NetBoxPrefix, `deletionPolicy` defaults to `Retain` here (decision #176): deleting a
// range frees every address in it for reallocation at once and destroys the record of who the
// block belonged to. The default is data on the kind's Descriptor
// (registry.Descriptor.RetainOnDelete) rather than a CRD marker, because the field is declared
// once on the embedded NetBoxObjectSpec and redeclaring it here makes controller-gen emit a
// schema the API server rejects -- so `kubectl explain` prints no default and
// docs/concepts/deletion.md carries the table.
type NetBoxIPRangeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// StartAddress is the first address in the range, with a mask
	// (docs/netbox-schema.md -> ipam.IPRange, `start_address IPAddressField REQ`).
	//
	// The mask is required by NetBox and must match `endAddress`'s: `IPRange.clean()` rejects
	// a pair whose prefix lengths differ, and the UI writes the containing prefix's length.
	// It is not a network -- `10.0.30.128/24` is an address inside `10.0.30.0/24`, and its
	// host bits are the whole point -- which is why there is no masked-form validation here of
	// the kind NetBoxPrefix.prefix carries.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="startAddress must be an address with a mask, IPv4 or IPv6, for example 10.0.30.128/24 or fd00:10::100/64"
	StartAddress string `json:"startAddress"`

	// EndAddress is the last address in the range, inclusive
	// (docs/netbox-schema.md -> ipam.IPRange, `end_address IPAddressField REQ`).
	//
	// Inclusive, so a range from `.128` to `.191` is 64 addresses and not 63. Equal to
	// `startAddress` is legal and is a one-address range.
	//
	// The ordering and family checks are NetBox's: `IPRange.clean()` refuses an end below the
	// start, a family mismatch and a mask mismatch, and refuses a range overlapping another in
	// the same VRF. None of those is re-implemented as CEL, because CEL cannot see the other
	// ranges and a rule that caught two of the four would read as if it caught all of them.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=43
	// +kubebuilder:validation:XValidation:rule="isCIDR(self)",message="endAddress must be an address with a mask, IPv4 or IPv6, for example 10.0.30.191/24 or fd00:10::1ff/64"
	EndAddress string `json:"endAddress"`

	// VRFRef puts the range in a VRF (docs/netbox-schema.md -> ipam.IPRange,
	// `vrf ForeignKey -> ipam.VRF on_delete=PROTECT`).
	//
	// Part of this kind's identity, and more sharply than for a prefix: NetBox's overlap
	// check is `filter(vrf=self.vrf)`, so the same block of addresses may legitimately be a
	// range once globally and once in every VRF. The lookup either matches `vrf_id` against a
	// value or pins it to null, never omits it
	// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
	// +optional
	VRFRef *VRFRef `json:"vrfRef,omitempty"`

	// TenantRef assigns the range to a tenant (docs/netbox-schema.md -> ipam.IPRange,
	// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`).
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// RoleRef marks what the range is for -- DHCP, load balancers, guests
	// (docs/netbox-schema.md -> ipam.IPRange, `role ForeignKey -> ipam.Role
	// on_delete=SET_NULL`).
	// +optional
	RoleRef *RoleRef `json:"roleRef,omitempty"`

	// Status is the range's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status IPRangeStatus `json:"status,omitempty"`

	// MarkPopulated stops NetBox creating ipam.IPAddress objects inside the range
	// (docs/netbox-schema.md -> ipam.IPRange, `mark_populated BooleanField def=False`).
	//
	// The flag for a block something else owns: a DHCP scope's leases are not NetBox's to
	// enumerate, and marking the range populated says so rather than leaving a hole that looks
	// free. A pointer because of the `def=False`: a plain bool cannot tell "not managed" from
	// "managed as false", so adopting a range somebody had marked populated would silently
	// clear it on the first reconcile.
	// +optional
	MarkPopulated *bool `json:"markPopulated,omitempty"`

	// MarkUtilized forces NetBox to report the range as 100% utilised regardless of what is
	// inside it (docs/netbox-schema.md -> ipam.IPRange, `mark_utilized BooleanField
	// def=False`). A pointer for the same reason as MarkPopulated.
	//
	// It is also the one flag that stops a NetBoxIPAddressClaim allocating out of this range:
	// the flag means the free space here is not really free, and NetBox's `available-ips` view
	// hands out an address anyway, so honouring it is this operator's job.
	// +optional
	MarkUtilized *bool `json:"markUtilized,omitempty"`

	// Description is free text shown next to the range. Declared on PrimaryModel rather than
	// on ipam.IPRange (docs/netbox-schema.md -> ipam.IPRange, `description (PrimaryModel)
	// CharField len=200`); an inherited column is as writable as a declared one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in NetBox.
	// The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the range's long-form notes field. Also inherited from PrimaryModel, and a
	// TextField rather than a CharField: it has no max_length, so there is no MaxLength marker
	// to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxIPRange is one ipam.IPRange in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// There is no SIZE printer column, for the reason there is no `size` field: NetBox derives it
// and the operator never sends it, so a column reading the spec would be empty on every object.
// What a human wants side by side is where the block starts, where it ends, and whether NetBox
// agrees.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrange
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.spec.startAddress`
// +kubebuilder:printcolumn:name="End",type=string,JSONPath=`.spec.endAddress`
// +kubebuilder:printcolumn:name="VRF",type=string,JSONPath=`.spec.vrfRef.name`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Adopted",type=boolean,JSONPath=`.status.adopted`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPRange struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPRangeSpec  `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxIPRange) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxIPRange) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxIPRangeList is a list of NetBoxIPRange.
// +kubebuilder:object:root=true
type NetBoxIPRangeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPRange `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPRange{}, &NetBoxIPRangeList{})
}
