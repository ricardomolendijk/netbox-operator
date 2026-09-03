package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxIPSecProposalSpec describes one vpn.IPSecProposal: the set of algorithms one peer
// offers for the IPSec security association -- phase 2.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// **Nothing here is secret-valued** (docs/netbox-schema.md -> vpn.IPSecProposal, every
// column), so unlike vpn.IKEPolicy this kind ships complete.
//
// **The two algorithm columns are optional here and required on vpn.IKEProposal.** That is
// NetBox's asymmetry rather than an oversight: `encryption_algorithm` is `REQ` on the IKE
// proposal and `blank=True, null=True` on this one, because an AH-only association encrypts
// nothing and an AEAD cipher authenticates as it encrypts. Both fields therefore use the
// shared enums' empty member, and NetBox's `clean()` is the authority on which combinations
// are usable.
//
// **Identity is `name`**, `CharField REQ UNIQUE len=100`, with no `meta.constraints` on the
// model. One candidate, no pin.
type NetBoxIPSecProposalSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the proposal's identity, and it is unique across the whole NetBox install
	// (docs/netbox-schema.md -> vpn.IPSecProposal, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// EncryptionAlgorithm is the phase 2 cipher (docs/netbox-schema.md -> vpn.IPSecProposal,
	// `encryption_algorithm CharField choices=EncryptionAlgorithmChoices`).
	//
	// Optional, because the column is `blank=True, null=True`: an AH-only proposal offers
	// integrity without encryption. No `MinLength` here, unlike NetBoxIKEProposal's field of
	// the same type -- that is the one difference between the two columns.
	//
	// Unset leaves NetBox's own value alone; `""` clears it -- two different instructions the
	// operator tells apart from metadata.managedFields (docs/concepts/field-ownership.md).
	// The wording differs from the other clearable fields here only because this one carries
	// an enum, exactly as NetBoxRack.formFactor's does.
	//
	// Cleared as `null` rather than as an empty string, because NetBox returns `null` for an
	// unset choice and a payload of `""` would differ from the value read back on every pass
	// (#170).
	// +optional
	EncryptionAlgorithm EncryptionAlgorithm `json:"encryptionAlgorithm,omitempty"`

	// AuthenticationAlgorithm is the phase 2 HMAC (docs/netbox-schema.md ->
	// vpn.IPSecProposal, `authentication_algorithm CharField
	// choices=AuthenticationAlgorithmChoices`).
	//
	// Optional for the same reason, and the mirror case: an AEAD cipher such as
	// `aes-256-gcm` supplies its own integrity.
	// +optional
	AuthenticationAlgorithm AuthenticationAlgorithm `json:"authenticationAlgorithm,omitempty"`

	// SALifetimeSeconds is how long a phase 2 security association lives, in seconds
	// (docs/netbox-schema.md -> vpn.IPSecProposal, `sa_lifetime_seconds
	// PositiveIntegerField`).
	//
	// A pointer, so omitting it leaves NetBox's value alone rather than clearing it. The
	// upper bound is Django's `PositiveIntegerField` ceiling, 2147483647; NetBox declares no
	// validator of its own on the column.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	SALifetimeSeconds *int64 `json:"saLifetimeSeconds,omitempty"`

	// SALifetimeData is how much traffic a phase 2 security association carries before it is
	// rekeyed, in kilobytes (docs/netbox-schema.md -> vpn.IPSecProposal, `sa_lifetime_data
	// PositiveIntegerField`).
	//
	// The second half of a lifetime pair NetBox keeps as two independent nullable columns:
	// either, both or neither may be set, and the operator writes what the spec declares
	// without inferring one from the other.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	SALifetimeData *int64 `json:"saLifetimeData,omitempty"`

	// Description is free text shown next to the proposal. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the proposal's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxIPSecProposal is one vpn.IPSecProposal in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbipsecprop
// +kubebuilder:printcolumn:name="Encryption",type=string,JSONPath=`.spec.encryptionAlgorithm`
// +kubebuilder:printcolumn:name="Authentication",type=string,JSONPath=`.spec.authenticationAlgorithm`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPSecProposal struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPSecProposalSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus      `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxIPSecProposal) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxIPSecProposal) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxIPSecProposalList is a list of NetBoxIPSecProposal.
// +kubebuilder:object:root=true
type NetBoxIPSecProposalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPSecProposal `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPSecProposal{}, &NetBoxIPSecProposalList{})
}
