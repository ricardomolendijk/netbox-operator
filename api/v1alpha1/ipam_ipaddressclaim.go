package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Condition types set on a claim.
//
// Two, not the engine's five: a claim has no drift to detect, no natural key to adopt by
// and no spec to be Synced with. What it has is one irreversible fact and one summary.
const (
	// ConditionAllocated is the irreversible fact: NetBox handed this claim an object.
	//
	// **Once True it is never set False.** It is a historical statement rather than a
	// liveness signal -- the address was allocated, and no later event un-allocates it --
	// which is why there is no Degraded condition here
	// (docs/decisions/0004-claims-first-allocation.md).
	//
	// False carries why nothing has been allocated *yet*. An exhausted pool is one of
	// those: a claim that failed to allocate has allocated nothing, and its next pass is
	// still its first allocation. "Never re-allocate" is not "never allocate".
	ConditionAllocated = "Allocated"
)

// Condition reasons for a claim. As with the engine's vocabulary the set is deliberately
// small and closed: each one is documented in docs/reference/netboxipaddressclaim.md and
// keyed on by whoever automates around it.
const (
	// ReasonAddressAllocated is on Allocated and on Ready: NetBox allocated an object out
	// of the pool on this pass or an earlier one.
	ReasonAddressAllocated = "AddressAllocated"

	// ReasonReclaimedByIdentity is on Allocated: an object already carrying this claim's
	// allocation identity was found and adopted rather than a second one allocated.
	//
	// This is the reason a rebuilt cluster reports, and the reason a crash between the POST
	// and the status write reports. They are the same event from NetBox's point of view --
	// the object exists and says whose it is -- which is why they are one code path and one
	// reason rather than a recovery mode.
	ReasonReclaimedByIdentity = "ReclaimedByIdentity"

	// ReasonAllocationPending is on Allocated and on Ready: nothing has been allocated yet
	// and nothing is wrong. The first pass of every claim reports it.
	ReasonAllocationPending = "AllocationPending"

	// ReasonPoolExhausted is on Allocated and on Ready: NetBox has no free object in the
	// pool.
	//
	// Not terminal. The claim is not misconfigured, so a terminal failure would sit there
	// after somebody widened the prefix, waiting for a human to touch the object. It waits
	// on a fixed ten-minute timer *and* on a watch of its pool
	// (docs/decisions/0004-claims-first-allocation.md, exhaustion). The two are not
	// redundant: widening the prefix is a change to a Kubernetes object and the watch sees
	// it, freeing an address inside NetBox is not and only the timer catches it.
	//
	// The message names the pool and states its utilisation, because a reader told only
	// "exhausted" goes and looks the prefix up by hand.
	ReasonPoolExhausted = "PoolExhausted"

	// ReasonPoolNotAllocatable is on Allocated and on Ready: the resolved pool is one this
	// operator refuses to allocate out of.
	//
	// Two causes, both of them the NetBox operator having said something about the prefix
	// that `available-ips` does not honour on its own:
	//
	//   - `mark_utilized`. It only forces NetBox's utilisation gauge to 100%; the
	//     allocation endpoint still hands out an address. The flag means "the free space
	//     here is not really free -- it is delegated to DHCP or to another IPAM", so
	//     honouring it has to be this operator's job.
	//   - `status: container`. A container's free space is subdivided by child prefixes
	//     rather than populated by addresses, so a bare address out of one is almost always
	//     a mistake. The escape hatch is a child prefix, or a NetBoxIPAddress with an
	//     explicit address.
	//
	// There is no override flag for either.
	ReasonPoolNotAllocatable = "PoolNotAllocatable"

	// ReasonReclaimedOutsidePool is on Allocated and on Ready: an object carrying this
	// claim's identity exists and is not inside the pool the claim now names.
	//
	// The claim refuses rather than allocating a second one, because two objects carrying
	// one identity is the state that makes every future reclaim ambiguous -- and accepting
	// the out-of-pool object would make prefixRef a lie. A human either deletes the stale
	// NetBox object or sets spec.allocationIdentity.
	//
	// This failure mode exists *because* the identity is deterministic, and it is the price
	// of docs/decisions/0005-gitops-coexistence.md section 3: repointing a claim, renaming a
	// prefix, or reusing a claim name for a different purpose all reach it, where a
	// UID-keyed identity never could.
	ReasonReclaimedOutsidePool = "ReclaimedOutsidePool"

	// ReasonAllocationConflict is on Allocated and on Ready: more than one NetBox object
	// carries this claim's allocation identity.
	//
	// The claim never allocates and never deletes. Two objects sharing one identity means a
	// previous over-allocation, and the operator cannot prove which of them is unused -- a
	// NIC or a DNS record may be pointing at either. The message names every match, because
	// the next step is a human choosing.
	ReasonAllocationConflict = "AllocationConflict"

	// ReasonIdempotencyKeyUnavailable is on Allocated and on Ready: this endpoint has no
	// place to store an allocation identity, so the claim will not allocate at all.
	//
	// Zero POSTs, deliberately. The engine's provenance stamp is optional; for a claim an
	// identity store is mandatory, because without it a lost HTTP response is
	// unrecoverable and every retry burns another address. There is no unsafe override.
	ReasonIdempotencyKeyUnavailable = "IdempotencyKeyUnavailable"
)

// Event reasons emitted by the claim controller.
const (
	// EventAllocated is NetBox having handed out an object. It names the address and the
	// pool.
	EventAllocated = "Allocated"

	// EventAllocationReclaimed is an existing object adopted by identity instead of a
	// second one allocated.
	//
	// It names both UIDs when the object's k8s_uid is not this claim's, which is the only
	// signal that exists for "a different claim has held this name": the handover is
	// legitimate on a rebuilt cluster and suspicious when two claims are given one name over
	// time, and the two are indistinguishable from inside the operator.
	EventAllocationReclaimed = "AllocationReclaimed"

	// EventPoolExhausted is the pool having no free object.
	EventPoolExhausted = "PoolExhausted"

	// EventPoolNotAllocatable is a pool this operator refuses to allocate out of.
	EventPoolNotAllocatable = "PoolNotAllocatable"

	// EventReclaimedOutsidePool is an object carrying this claim's identity from outside
	// the pool.
	EventReclaimedOutsidePool = "ReclaimedOutsidePool"

	// EventAllocationConflict is two or more objects carrying one identity.
	EventAllocationConflict = "AllocationConflict"

	// EventAddressRetained is a claim being deleted while its NetBox object stays.
	//
	// The whole of the garbage-collection *reporting* path: the operator is about to stop
	// tracking an object it can still name, so it says so, once, with everything a human
	// needs to find it -- and never deletes it
	// (https://github.com/ricardomolendijk/netbox-operator/issues/182). Retaining is what
	// makes "rebuild from Git, keep my addresses" a guarantee rather than a coincidence;
	// this Event is what stops the retained object from being invisible.
	EventAddressRetained = "AddressRetained"
)

// NetBoxIPAddressClaimSpec asks for one free address out of a pool.
//
// It is deliberately much smaller than NetBoxIPAddress's spec, and the difference is the
// whole design: a claim's job is to *get* an address, and the desired state of the address
// it got belongs to a NetBoxIPAddress CR
// (docs/decisions/0004-claims-first-allocation.md). So there is no dnsName, no role, no
// description and no assignedObject here. A field the claim could write once at allocation
// and could never correct afterwards would be a field that lies the first time somebody
// edits it, and this operator does not ship those -- it would take a declarative reconcile
// of the allocated object to make one honest, which is exactly what NetBoxIPAddress is
// (NBO-025) and what the claim will materialise as its child once that kind lands
// (NBO-032).
type NetBoxIPAddressClaimSpec struct {
	NetBoxClaimSpec `json:",inline"`

	// PrefixRef is the pool to allocate out of: a NetBoxPrefix, resolved by name, slug,
	// lookup or id like any other reference, and subject to the same NetBoxRefGrant check
	// when it crosses a namespace.
	//
	// Immutable. "Point this claim at a different prefix" is a different claim, and a CEL
	// rule on the field is a better contract than a controller comparing the spec against
	// status after the fact: the API server rejects the edit, so there is no window in which
	// the claim's spec and its allocated address disagree.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="prefixRef is immutable; a claim allocates once, so pointing it at another prefix is a new claim"
	PrefixRef PrefixRef `json:"prefixRef"`
}

// NetBoxClaimSpec is the part of every claim CR's spec that the allocation engine owns.
//
// Kinds embed it inline, so its fields are ordinary spec fields -- and NBO-064's prefix and
// ip-range claims embed the same one, which is what makes the engine generic over
// registry.ClaimDescriptor rather than over a Kind.
//
// Deliberately *not* NetBoxObjectSpec. Two of that struct's three fields mean nothing to a
// claim: `onConflict` is about adopting an object that matches a natural key, and a claim
// has no natural key -- it adopts by allocation identity and by nothing else -- while
// `deletionPolicy` is fixed at Retain (see NetBoxIPAddressClaim). A field that is accepted
// and ignored is worse than a field that is not there.
type NetBoxClaimSpec struct {
	// EndpointRef names the NetBoxEndpoint to allocate through, in this claim's own
	// namespace.
	//
	// Declared here rather than inherited from NetBoxObjectSpec, because two of that
	// struct's three fields mean nothing to a claim. `onConflict` is about adopting an
	// object that matches a natural key, and a claim has no natural key -- it adopts by
	// allocation identity and by nothing else. `deletionPolicy` is fixed at Retain, see the
	// type comment on NetBoxIPAddressClaim. A field that is accepted and ignored is worse
	// than a field that is not there.
	//
	// It also participates in the allocation identity: the same claim pointed at a different
	// NetBox is a different allocation, and asking a second NetBox for the object of the
	// first one would find nothing and allocate twice.
	// +kubebuilder:validation:MinLength=1
	EndpointRef string `json:"endpointRef"`

	// AllocationIdentity overrides the derived allocation identity.
	//
	// Unset -- the normal case -- the identity is derived from the endpoint's URL, this
	// claim's namespace, its Kind and its name, which is what makes a claim re-applied to a
	// rebuilt cluster reclaim the same address
	// (docs/decisions/0005-gitops-coexistence.md section 3). Set it to the previous
	// identity to carry an address across a *rename*, which the derived value cannot
	// survive by construction.
	//
	// Immutable once set, and settable on a claim that has one already only in the sense
	// that adding it is allowed and changing it is not: an identity that moves is a claim
	// pointed at somebody else's address.
	//
	// Absent and empty mean the same thing here -- derive it -- so this field has two states
	// rather than the three of docs/concepts/field-ownership.md. Nothing is written to NetBox
	// from it directly, so there is no NetBox value for an empty string to clear.
	// +optional
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:Pattern=`^[a-z0-9]+$`
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="allocationIdentity is immutable; changing it points this claim at a different allocation"
	AllocationIdentity string `json:"allocationIdentity,omitempty"`
}

// AllocationPool is the pool a claim allocated out of, as it was resolved.
//
// Recorded so that "which prefix did this address come from" is answerable from the claim
// alone, after the fact and after the reference has been repointed or the prefix renamed.
// The spec holds the *intent*; this holds what it resolved to.
type AllocationPool struct {
	// Display is the pool as a human reads it: a prefix in CIDR notation.
	// +optional
	Display string `json:"display,omitempty"`

	// Endpoint is the pool's NetBox REST path, `ipam/prefixes`.
	// +optional
	Endpoint string `json:"endpoint,omitempty"`

	// ID is the pool's NetBox primary key.
	// +optional
	ID int64 `json:"id,omitempty"`
}

// NetBoxIPAddressClaimStatus is what the claim allocated, and everything needed to prove it
// was allocated once.
type NetBoxIPAddressClaimStatus struct {
	NetBoxClaimStatus `json:",inline"`

	// Address is the allocated address in CIDR notation.
	//
	// **Immutable, and the one field that must never be lost.** It is written after
	// read-after-write verification and never rewritten: while it holds a value the
	// reconciler's first guard clause returns before anything can allocate again. If the
	// NetBox object behind it is deleted out from under a live claim the operator does not
	// pick a new address -- by then something outside Kubernetes is using this one, a NIC's
	// static configuration or a DNS record or a firewall rule, and silently moving it turns
	// a NetBox bookkeeping accident into a live network change nobody asked for
	// (docs/decisions/0004-claims-first-allocation.md).
	// +optional
	Address string `json:"address,omitempty"`
}

// NetBoxClaimStatus is the part of every claim CR's status that the allocation engine owns.
//
// Everything about *how* an object was obtained, which is identical for all three claim
// kinds. What was obtained is not -- an address claim reports `address` and NBO-064's prefix
// claim will report `prefix` -- so the allocated value stays on the kind's own status, where
// its name means something and its printer column can say so.
type NetBoxClaimStatus struct {
	// NetBoxID is the allocated object's NetBox primary key. Zero until an allocation is
	// proven server-side: a DryRun endpoint reports what it would have done and writes
	// nothing here.
	// +optional
	NetBoxID int64 `json:"netboxID,omitempty"`

	// URL is the allocated object's absolute NetBox URL, as returned by the API.
	// +optional
	URL string `json:"url,omitempty"`

	// Pool is the pool this claim allocated out of, as resolved at allocation time.
	// +optional
	Pool *AllocationPool `json:"pool,omitempty"`

	// AllocationIdentity is the identity written into the allocated object's custom field,
	// and the key every future reclaim searches by.
	//
	// Reported because it is the one value that makes a leaked allocation findable: paste it
	// into NetBox's custom-field filter and the object it belongs to comes back, whether or
	// not the CR still exists.
	// +optional
	AllocationIdentity string `json:"allocationIdentity,omitempty"`

	// ClaimUID is the metadata.uid of the claim that allocated this object.
	//
	// Recorded for debugging and for exactly one decision: an allocated object whose
	// k8s_uid is not the current claim's is a reclaim by a re-created claim, which is
	// legitimate and is worth an Event naming both. The identity, not this, is what
	// reclaims -- a UID is regenerated precisely when the old address is most wanted back.
	// +optional
	ClaimUID string `json:"claimUID,omitempty"`

	// AllocatedAt is when the address was first allocated or reclaimed. Never rewritten
	// afterwards.
	// +optional
	AllocatedAt *metav1.Time `json:"allocatedAt,omitempty"`

	// Provenance is the stamp the allocated object carries in NetBox: the tag and the
	// custom fields written with the allocating POST, as they were written.
	// +optional
	Provenance *ProvenanceStatus `json:"provenance,omitempty"`

	// ObservedGeneration is the spec generation this status refers to. Always set, because
	// `kubectl wait` lies without it.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions follow the standard Kubernetes vocabulary.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// NetBoxIPAddressClaim asks NetBox for one free address out of a prefix, once.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// **A claim always retains its NetBox object.** There is no `deletionPolicy` field: a
// single-valued knob is not one, and Retain is the value that makes the deterministic
// identity worth having -- delete the claim, re-apply the same manifest, get the same
// address back. What stops that from being a silent leak is that the operator *reports*
// it: deleting a claim emits the AddressRetained Event naming the address, the NetBox id and
// the identity, and never deletes anything
// (https://github.com/ricardomolendijk/netbox-operator/issues/182). To free the address,
// delete the NetBox object.
//
// The printer columns are the three things a human wants side by side: what was asked for,
// what was handed out, and whether it stuck.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbipclaim;nbipc
// +kubebuilder:printcolumn:name="Address",type=string,JSONPath=`.status.address`
// +kubebuilder:printcolumn:name="Prefix",type=string,JSONPath=`.status.pool.display`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPAddressClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPAddressClaimSpec   `json:"spec,omitempty"`
	Status NetBoxIPAddressClaimStatus `json:"status,omitempty"`
}

// ClaimSpec returns the engine-owned part of the spec.
func (c *NetBoxIPAddressClaim) ClaimSpec() *NetBoxClaimSpec { return &c.Spec.NetBoxClaimSpec }

// ClaimStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxIPAddressClaim) ClaimStatus() *NetBoxClaimStatus {
	return &c.Status.NetBoxClaimStatus
}

// Allocated returns the allocated address, and is the allocation engine's first guard
// clause: non-empty means never allocate again, ever.
func (c *NetBoxIPAddressClaim) Allocated() string { return c.Status.Address }

// SetAllocated records the allocated address.
//
// The one per-kind accessor the engine needs, because the *name* of the allocated value is
// per-kind while nothing about how it was obtained is. A shared `status.result` would be a
// field nobody could read without already knowing the Kind, and a printer column headed
// RESULT.
func (c *NetBoxIPAddressClaim) SetAllocated(value string) { c.Status.Address = value }

// NetBoxIPAddressClaimList is a list of NetBoxIPAddressClaim.
// +kubebuilder:object:root=true
type NetBoxIPAddressClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPAddressClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPAddressClaim{}, &NetBoxIPAddressClaimList{})
}
