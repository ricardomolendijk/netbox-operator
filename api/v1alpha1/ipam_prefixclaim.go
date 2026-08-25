package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Event reasons emitted for a claim whose pool is in a state worth mentioning but not
// refusing.
const (
	// EventPoolUnexpectedStatus is an allocation out of a pool whose `status` is not one the
	// claim kind expects: a child prefix carved out of an `active` prefix rather than a
	// `container`.
	//
	// A Warning and not a refusal, and the asymmetry is deliberate: the same
	// `status: container` that stops a NetBoxIPAddressClaim dead is what a
	// NetBoxPrefixClaim expects to find, so neither can be a rule in shared code and the
	// prefix claim's expectation is data on its descriptor
	// (registry.ClaimDescriptor.PoolExpectedStatus). Subdividing a network that is already in
	// service is unusual rather than wrong, and refusing it would overrule a decision the
	// NetBox operator has already recorded.
	EventPoolUnexpectedStatus = "PoolUnexpectedStatus"
)

// NetBoxPrefixClaimSpec asks for one free child prefix out of a container.
//
// "Carve me a /26 out of 10.0.0.0/16", and the answer is whichever /26 NetBox's
// `available-prefixes` view hands out under its advisory lock. Deliberately small, for the
// reason NetBoxIPAddressClaimSpec is: a claim's job is to *get* a prefix, and the desired
// state of the prefix it got belongs to a NetBoxPrefix CR
// (docs/decisions/0004-claims-first-allocation.md). So there is no `description`, no `role`
// and no `scope` here -- a field the claim could write once at allocation and never correct
// afterwards would be a field that lies the first time somebody edits it.
//
// **There is no `vrfRef` either, and that one is worth stating.** NetBox's
// `AvailablePrefixesView.prep_object_data` sets `'vrf': parent.vrf.pk if parent.vrf else None`
// on every requested prefix (netbox/ipam/api/views.py, NetBox 4.6.8), *overwriting* whatever
// the request carried. The child therefore always lands in the parent's VRF, a `vrfRef` on
// this kind could not change that, and a field that is accepted and ignored is worse than a
// field that is not there. `status.pool` records which prefix it came from, and the VRF is
// that prefix's.
type NetBoxPrefixClaimSpec struct {
	NetBoxClaimSpec `json:",inline"`

	// ParentPrefixRef is the container to carve out of: a NetBoxPrefix, resolved by name,
	// slug, lookup or id like any other reference, and subject to the same NetBoxRefGrant
	// check when it crosses a namespace.
	//
	// Immutable. "Carve this claim out of a different container" is a different claim, and a
	// CEL rule is a better contract than a controller comparing spec against status after the
	// fact: the API server rejects the edit, so there is no window in which the claim's spec
	// and its allocated prefix disagree.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="parentPrefixRef is immutable; a claim allocates once, so pointing it at another container is a new claim"
	ParentPrefixRef PrefixRef `json:"parentPrefixRef"`

	// PrefixLength is the mask length of the child prefix to allocate: 26 for a /26.
	//
	// Required, immutable, and the one field that makes this request a request -- NetBox's
	// `PrefixLengthSerializer` rejects a body without it by name.
	//
	// The bounds here are the static ones only. `4..128` covers both families because the
	// family is a property of the *parent*, which CEL cannot see: a `prefixLength: 64` on an
	// IPv4 container is rejected by the controller with a message naming both, and by NetBox
	// itself with `Invalid prefix length (64) for IPv4`. Likewise a length that is not longer
	// than the parent's own mask -- a /16 out of a /16 -- is a controller guard, because the
	// comparison needs the resolved parent. Encoding either as CEL would mean encoding half
	// of it and reading as if it were all of it.
	// +kubebuilder:validation:Minimum=4
	// +kubebuilder:validation:Maximum=128
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="prefixLength is immutable; a claim allocates once, so a different size is a new claim"
	PrefixLength int32 `json:"prefixLength"`
}

// NetBoxPrefixClaimStatus is what the claim allocated, and everything needed to prove it was
// allocated once.
type NetBoxPrefixClaimStatus struct {
	NetBoxClaimStatus `json:",inline"`

	// Prefix is the allocated child prefix in CIDR notation.
	//
	// **Immutable, and the one field that must never be lost.** Written after
	// read-after-write verification -- which proves the prefix is inside the parent and has
	// the requested mask length -- and never rewritten: while it holds a value the
	// reconciler's first guard clause returns before anything can allocate again. If the
	// NetBox object behind it is deleted out from under a live claim the operator does not
	// pick a new prefix: by then something outside Kubernetes is using this one, a router's
	// static configuration or somebody's firewall rules, and silently moving it turns a NetBox
	// bookkeeping accident into a live network change nobody asked for
	// (docs/decisions/0004-claims-first-allocation.md).
	// +optional
	Prefix string `json:"prefix,omitempty"`
}

// NetBoxPrefixClaim asks NetBox for one free child prefix out of a container, once.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// **The safety argument is the server's lock.** `POST ipam/prefixes/{id}/available-prefixes/`
// runs inside NetBox's own `advisory_lock('available-prefixes')` (netbox/ipam/api/views.py,
// NetBox 4.6.8), so two controller workers -- or two clusters -- cannot be handed the same
// /26. This operator takes no client-side lock and does not serialise per pool, because a
// client-side lock would be unnecessary here and useless across two clusters. Its sibling
// NetBoxIPRangeClaim has no such endpoint and therefore a different argument; see
// docs/concepts/claims.md.
//
// **A claim always retains its NetBox object.** There is no `deletionPolicy` field: a
// single-valued knob is not one, and Retain is what makes the deterministic identity worth
// having -- delete the claim, re-apply the same manifest, get the same prefix back. What stops
// that from being a silent leak is that the operator reports it, once, with the id and the
// identity needed to find it again.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbprefixclaim;nbpfxc
// +kubebuilder:printcolumn:name="Prefix",type=string,JSONPath=`.status.prefix`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.status.pool.display`
// +kubebuilder:printcolumn:name="Length",type=integer,JSONPath=`.spec.prefixLength`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxPrefixClaim struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxPrefixClaimSpec   `json:"spec,omitempty"`
	Status NetBoxPrefixClaimStatus `json:"status,omitempty"`
}

// ClaimSpec returns the engine-owned part of the spec.
func (c *NetBoxPrefixClaim) ClaimSpec() *NetBoxClaimSpec { return &c.Spec.NetBoxClaimSpec }

// ClaimStatus returns the engine-owned part of the status, for the engine to write.
func (c *NetBoxPrefixClaim) ClaimStatus() *NetBoxClaimStatus { return &c.Status.NetBoxClaimStatus }

// Allocated returns the allocated prefix, and is the allocation engine's first guard clause:
// non-empty means never allocate again, ever.
func (c *NetBoxPrefixClaim) Allocated() string { return c.Status.Prefix }

// SetAllocated records the allocated prefix.
//
// The per-kind half of the claim interface, and the reason it is per-kind: the *name* of the
// allocated value differs -- `address` on an address claim, `prefix` here -- while nothing
// about how it was obtained does. A shared `status.result` would be a field nobody could read
// without already knowing the Kind, and a printer column headed RESULT.
func (c *NetBoxPrefixClaim) SetAllocated(value string) { c.Status.Prefix = value }

// NetBoxPrefixClaimList is a list of NetBoxPrefixClaim.
// +kubebuilder:object:root=true
type NetBoxPrefixClaimList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxPrefixClaim `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxPrefixClaim{}, &NetBoxPrefixClaimList{})
}
