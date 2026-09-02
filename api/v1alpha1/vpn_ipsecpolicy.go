package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxIPSecPolicySpec describes one vpn.IPSecPolicy: the phase 2 policy a peer offers,
// built from one or more NetBoxIPSecProposals.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// **Nothing here is secret-valued.** The pre-shared key in the `vpn` app is a column on
// `vpn.IKEPolicy` and nowhere else (docs/netbox-schema.md -> vpn.IKEPolicy,
// vpn.IPSecPolicy), so this kind ships complete.
//
// **Identity is `name`**, `CharField REQ UNIQUE len=100`, with no `meta.constraints` on the
// model. One candidate, no pin.
type NetBoxIPSecPolicySpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the policy's identity, and it is unique across the whole NetBox install
	// (docs/netbox-schema.md -> vpn.IPSecPolicy, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Proposals is the set of IPSec proposals this policy offers.
	//
	// A to-many reference: every element is resolved to a NetBox id and the field is written
	// as the whole list, because NetBox replaces a many-to-many wholesale on PATCH -- there is
	// no add or remove verb. So the listed set *is* the set, and reordering it produces no
	// write (docs/concepts/drift.md).
	//
	// **Optional in the CRD and required by NetBox**, for the reason NetBoxIKEPolicy's
	// identically shaped field gives: the column is
	// `proposals ManyToManyField -> vpn.IPSecProposal` with no `blank=True`
	// (docs/netbox-schema.md -> vpn.IPSecPolicy), so NetBox refuses to create a policy without
	// one -- but a spec omission means "do not manage this relation", and a required CRD field
	// would make adopting a policy somebody else's proposals belong to impossible.
	//
	// `minItems: 1` bounds the declared list rather than requiring one: `[]` would ask NetBox
	// to clear a relation it refuses to leave empty.
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of the
	// payload and the object reports RefsResolved=False naming the element that failed.
	//
	// MaxItems is the project standard 256 (docs/concepts/references.md, "A list needs a
	// bound").
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	Proposals []IPSecProposalRef `json:"proposals,omitempty"`

	// PFSGroup is the Diffie-Hellman group used for perfect forward secrecy
	// (docs/netbox-schema.md -> vpn.IPSecPolicy, `pfs_group PositiveSmallIntegerField
	// choices=DHGroupChoices`).
	//
	// A pointer rather than a bare DHGroup, because the column is `blank=True, null=True` and
	// an integer has no empty member to stand for "unset": a policy without PFS is an
	// ordinary policy, and `0` is not a DH group. Omitting the field leaves NetBox's own
	// value alone; setting it to `null` explicitly clears the column.
	//
	// The same DHGroup type as NetBoxIKEProposal's `group`, because it is the same NetBox
	// ChoiceSet (`DHGroupChoices`, `netbox/vpn/choices.py:155`) -- see api/v1alpha1/
	// vpn_crypto.go for why one ChoiceSet gets exactly one Go type.
	// +optional
	PFSGroup *DHGroup `json:"pfsGroup,omitempty"`

	// Description is free text shown next to the policy. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the policy's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxIPSecPolicy is one vpn.IPSecPolicy in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbipsecpol
// +kubebuilder:printcolumn:name="PFS Group",type=integer,JSONPath=`.spec.pfsGroup`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPSecPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPSecPolicySpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxIPSecPolicy) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxIPSecPolicy) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxIPSecPolicyList is a list of NetBoxIPSecPolicy.
// +kubebuilder:object:root=true
type NetBoxIPSecPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPSecPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPSecPolicy{}, &NetBoxIPSecPolicyList{})
}
