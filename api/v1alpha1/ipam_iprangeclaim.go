package v1alpha1

import (
	"fmt"
	"math/big"
	"net/netip"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition reasons and Event reasons for the one claim kind whose allocation is not
// serialised by NetBox.
const (
	// ReasonAllocationContended is on Allocated and on Ready: every placement this pass
	// computed was rejected because another writer created an overlapping range in between.
	//
	// **Not the same as PoolExhausted, and conflating them would send a human looking for
	// space that exists.** Exhausted means the pool is full: widen it or free something.
	// Contended means the space is there and somebody else got this candidate first, which
	// resolves on its own. It is nonetheless reported rather than retried forever: a pool that
	// keeps saying this has more claims competing for it than it has room for, and that is
	// worth knowing.
	//
	// Only NetBoxIPRangeClaim can report it. The advisory-locked endpoints the other claim
	// kinds use cannot contend -- NetBox serialises them, so a loser is told the pool is
	// exhausted or handed a different object.
	ReasonAllocationContended = "AllocationContended"

	// EventAllocationContended is the Event beside ReasonAllocationContended.
	EventAllocationContended = "AllocationContended"
)

// RangeAlignment is how a NetBoxIPRangeClaim's start address may be chosen.
//
// +kubebuilder:validation:Enum=Any;PowerOfTwo
type RangeAlignment string

const (
	// RangeAlignmentAny takes the first placement that leaves room, wherever it falls: the
	// densest packing of a partially-occupied parent.
	RangeAlignmentAny RangeAlignment = "Any"

	// RangeAlignmentPowerOfTwo starts the range on a multiple of the next power of two greater
	// than or equal to `size`.
	//
	// It exists because an unaligned DHCP pool is a bug report waiting to happen in every
	// downstream config generator: 64 addresses from `10.0.30.64` is `10.0.30.64/26` in a
	// dnsmasq or Kea config, and 64 addresses from `10.0.30.13` is a hand-written pair of
	// bounds that somebody eventually gets wrong. It is a *placement* input, not a NetBox
	// field, so it never reaches a payload -- NetBox has no opinion about where a range
	// starts.
	RangeAlignmentPowerOfTwo RangeAlignment = "PowerOfTwo"
)

// NetBoxIPRangeClaimSpec asks for one free run of consecutive addresses inside a prefix.
//
// "Reserve me 64 consecutive addresses in 10.0.30.0/24", and unlike its two sibling kinds
// there is no NetBox endpoint that answers that question: 4.6.8 has no `available-ranges`, and
// `POST ipam/ip-ranges/{id}/available-ips/` allocates an address *out of* a range, the
// opposite operation. So the placement is computed by this operator and committed with an
// ordinary POST, and what keeps two claims from being handed the same block is
// `ipam.IPRange.clean()` refusing to save a range that overlaps another in the same VRF. See
// the type comment on NetBoxIPRangeClaim.
type NetBoxIPRangeClaimSpec struct {
	NetBoxClaimSpec `json:",inline"`

	// ParentPrefixRef is the prefix to reserve inside: a NetBoxPrefix, resolved by name, slug,
	// lookup or id like any other reference, and subject to the same NetBoxRefGrant check when
	// it crosses a namespace.
	//
	// The parent supplies two things beyond its bounds. Its VRF, which the created range is
	// given explicitly -- a plain POST inherits nothing, and a range in the wrong VRF is
	// checked for overlap against the wrong set of ranges. And its mask, which both endpoints
	// are written with, because NetBox requires the two to match.
	//
	// Immutable, and this kind has more reason for that than most: repointing it while keeping
	// the claim's name is the mistake the ReclaimedOutsidePool guard exists for.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="parentPrefixRef is immutable; a claim allocates once, so pointing it at another prefix is a new claim"
	ParentPrefixRef PrefixRef `json:"parentPrefixRef"`

	// Size is how many consecutive addresses to reserve, counted inclusively: 64 means a range
	// whose end address is 63 above its start.
	//
	// Required and immutable. Growing a range is not this claim doing more of what it did --
	// the addresses above it may not be free -- so it is a different claim.
	//
	// Capped at 65536 so that the `size` column NetBox derives stays a number a human reads
	// rather than one they parse. NetBox's own ceiling is 2^32-1 (`IPRange.clean`), which for
	// an IPv6 parent is a range nobody meant to ask for and for an IPv4 one is more addresses
	// than exist.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=65536
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="size is immutable; a claim allocates once, so a different size is a new claim"
	Size int32 `json:"size"`

	// Alignment is where in the parent's free space the range may start.
	//
	// Defaulted to `Any`, which packs. `PowerOfTwo` is what a DHCP pool usually wants; see
	// RangeAlignmentPowerOfTwo. It changes only which candidate is chosen, never whether the
	// server accepts it, so switching it on a claim that has already allocated does nothing --
	// which is why it is not immutable and why the CRD accepts an edit that has no effect.
	// +kubebuilder:default=Any
	// +optional
	Alignment RangeAlignment `json:"alignment,omitempty"`

	// MarkPopulated stops NetBox creating ipam.IPAddress objects inside the reserved range
	// (docs/netbox-schema.md -> ipam.IPRange, `mark_populated BooleanField def=False`).
	//
	// Usually the point of reserving one: the block is handed out by a DHCP server, whose
	// leases are not NetBox's to enumerate. A pointer because of the `def=False` -- a plain
	// bool cannot tell "not managed" from "managed as false".
	//
	// One of the two pass-through fields this kind carries, and the two are exactly the fields
	// that describe *the reservation itself* rather than the thing reserved. Everything else
	// about the created range -- description, role, tenant, status -- is the desired state of a
	// NetBox object, which belongs to a NetBoxIPRange CR.
	// +optional
	MarkPopulated *bool `json:"markPopulated,omitempty"`

	// MarkUtilized forces NetBox to report the range as 100% utilised
	// (docs/netbox-schema.md -> ipam.IPRange, `mark_utilized BooleanField def=False`).
	// A pointer for the same reason as MarkPopulated.
	// +optional
	MarkUtilized *bool `json:"markUtilized,omitempty"`
}

// NetBoxIPRangeClaimStatus is what the claim reserved, and everything needed to prove it was
// reserved once.
type NetBoxIPRangeClaimStatus struct {
	NetBoxClaimStatus `json:",inline"`

	// StartAddress is the first address of the reserved range, as NetBox stored it.
	//
	// **Immutable, and the one field that must never be lost.** The allocation engine's first
	// guard clause reads it: while it holds a value nothing allocates again, ever. It is
	// written only after a read-after-write that proves the range exists, carries this claim's
	// allocation identity, and starts inside the parent prefix.
	// +optional
	StartAddress string `json:"startAddress,omitempty"`

	// EndAddress is the last address of the reserved range, inclusive.
	//
	// Derived from StartAddress and Size rather than read back separately, and it cannot
	// disagree with NetBox: the client refuses the allocation unless the `size` NetBox computed
	// from its own two endpoints equals the size that was asked for, so a start address and a
	// size pin the end address exactly.
	// +optional
	EndAddress string `json:"endAddress,omitempty"`

	// Size is how many addresses the reserved range covers, as NetBox counts them.
	// +optional
	Size int32 `json:"size,omitempty"`
}

// NetBoxIPRangeClaim reserves one run of consecutive addresses inside a prefix, once.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// **The safety argument is the server's rejection, not a lock, and that is the whole of what
// makes this kind different.** There is no advisory-locked endpoint to ask, so two claims can
// and do compute the same placement. NetBox is still the arbiter: `IPRange.clean()` rejects a
// range overlapping another in the same VRF (netbox/ipam/models/ip.py), and every API write
// runs it, because NetBox's ValidatedModelSerializer calls `full_clean()` before saving. The
// loser gets a 400, recomputes from fresh state and tries again -- up to five times in one
// reconcile -- and reports AllocationContended if it keeps losing. No client-side mutex is
// taken, and none would help: the other writer may be another cluster, or a human in the UI.
//
// The consequence a reader should carry away is that this kind can say "contended" and the
// other two cannot. docs/concepts/claims.md says why in more words.
//
// **Placement is arithmetic, never enumeration.** The occupied set is the other *ranges* in
// the parent's VRF, of which there are a handful, and never the addresses inside them: an IPv6
// /64 parent has 2^64 addresses and only one of those two numbers can be asked about.
// Individual ipam.IPAddress objects inside a candidate range are not a conflict -- NetBox
// permits a range to contain addresses -- so they are not consulted, and a range allocation
// issues no request to `available-ips` and none to `ipam/ip-addresses`.
//
// **A claim always retains its NetBox object.** There is no `deletionPolicy` field, for the
// reason NetBoxIPAddressClaim's type comment gives.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbiprangeclaim;nbrngc
// +kubebuilder:printcolumn:name="Start",type=string,JSONPath=`.status.startAddress`
// +kubebuilder:printcolumn:name="End",type=string,JSONPath=`.status.endAddress`
// +kubebuilder:printcolumn:name="Size",type=integer,JSONPath=`.status.size`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPRangeClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPRangeClaimSpec   `json:"spec,omitempty"`
	Status NetBoxIPRangeClaimStatus `json:"status,omitempty"`
}

// ClaimSpec returns the engine-owned part of the spec.
func (c *NetBoxIPRangeClaim) ClaimSpec() *NetBoxClaimSpec { return &c.Spec.NetBoxClaimSpec }

// ClaimStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxIPRangeClaim) ClaimStatus() *NetBoxClaimStatus { return &c.Status.NetBoxClaimStatus }

// Allocated returns the reserved range's start address, and is the allocation engine's first
// guard clause: non-empty means never allocate again, ever.
func (c *NetBoxIPRangeClaim) Allocated() string { return c.Status.StartAddress }

// SetAllocated records the reserved range.
//
// The only one of the three claim kinds whose allocated value is not a single field, and the
// one place in this operator where a status value is computed rather than read back. The
// arithmetic is safe because it is not a second opinion: the range was created with both
// endpoints in one POST, and the client refuses the result unless NetBox's own derived `size`
// -- which NetBox computes as `end - start + 1` -- equals the size that was requested. A start
// address and that size therefore pin the end address, and re-reading it would confirm a value
// already proven.
//
// A start address that does not parse leaves the two derived fields empty rather than writing
// a value nobody can act on. It cannot happen: the engine verified the same string against the
// parent prefix before this is called.
func (c *NetBoxIPRangeClaim) SetAllocated(value string) {
	c.Status.StartAddress = value
	c.Status.Size = c.Spec.Size
	c.Status.EndAddress = endAddressOf(value, c.Spec.Size)
}

// endAddressOf is `start + size - 1`, rendered with the mask the start address carried.
//
// The mask is preserved because NetBox requires the two endpoints to declare the same prefix
// length (`IPRange.clean`), so the end address of `10.0.30.128/24` plus 64 is
// `10.0.30.191/24` and not `10.0.30.191/32`.
func endAddressOf(start string, size int32) string {
	if size < 1 {
		return ""
	}

	prefix, err := netip.ParsePrefix(start)
	if err != nil {
		return ""
	}

	addr := prefix.Addr().Unmap()
	width := addr.BitLen() / 8

	end := new(big.Int).SetBytes(addr.AsSlice())
	end.Add(end, big.NewInt(int64(size)-1))

	if end.BitLen() > addr.BitLen() {
		return ""
	}

	last, ok := netip.AddrFromSlice(end.FillBytes(make([]byte, width)))
	if !ok {
		return ""
	}

	return fmt.Sprintf("%s/%d", last, prefix.Bits())
}

// NetBoxIPRangeClaimList is a list of NetBoxIPRangeClaim.
// +kubebuilder:object:root=true
type NetBoxIPRangeClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPRangeClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPRangeClaim{}, &NetBoxIPRangeClaimList{})
}
