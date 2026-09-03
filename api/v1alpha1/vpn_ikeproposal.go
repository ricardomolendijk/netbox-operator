package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// AuthenticationMethod is one value of NetBox's AuthenticationMethodChoices: how the two
// peers prove their identity to each other during IKE phase 1.
//
// One user only -- `vpn.IKEProposal.authentication_method` (docs/netbox-schema.md ->
// vpn.IKEProposal, `authentication_method CharField REQ choices=AuthenticationMethodChoices`)
// -- so unlike the three sets in vpn_crypto.go this one is declared next to its kind. The
// four members are `netbox/vpn/choices.py:93` at 4.6.8.
//
// Closed: the class declares no `key`, so no deployment's `FIELD_CHOICES` can add a member
// this enum would reject (`netbox/utilities/choices.py:23-35`, and
// `hack/testdata/ir-4.6.8.json.gz` -> enums.AuthenticationMethodChoices).
//
// No empty member: the column is `REQ` with no `blank=True`, so there is no clearing intent
// to express.
//
// +kubebuilder:validation:Enum="preshared-keys";"certificates";"rsa-signatures";"dsa-signatures"
type AuthenticationMethod string

const (
	// AuthenticationMethodPresharedKeys authenticates with a pre-shared key. The key itself
	// lives on the *policy* rather than the proposal, and the operator does not write it --
	// see NetBoxIKEPolicy.
	AuthenticationMethodPresharedKeys AuthenticationMethod = "preshared-keys"

	// AuthenticationMethodCertificates authenticates with X.509 certificates.
	AuthenticationMethodCertificates AuthenticationMethod = "certificates"

	// AuthenticationMethodRSASignatures authenticates with RSA signatures.
	AuthenticationMethodRSASignatures AuthenticationMethod = "rsa-signatures"

	// AuthenticationMethodDSASignatures authenticates with DSA signatures.
	AuthenticationMethodDSASignatures AuthenticationMethod = "dsa-signatures"
)

// NetBoxIKEProposalSpec describes one vpn.IKEProposal: the set of algorithms one peer offers
// for IKE phase 1.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// **No secret here, and none possible.** A proposal names algorithms; the pre-shared key that
// `preshared-keys` implies is a column on `vpn.IKEPolicy`, which is where the operator's
// refusal to hold a secret inline is documented. This kind is safe to ship complete because
// nothing on it is secret-valued (docs/netbox-schema.md -> vpn.IKEProposal, every column).
//
// **Identity is `name`, and NetBox enforces it.** `name CharField REQ UNIQUE len=100` -- a
// column-level UNIQUE rather than a `meta.constraints` entry, of which this model has none.
// So one candidate, no pin, and a duplicate is NetBox's own 409 rather than a silent
// adoption. The ipam.RouteTarget derivation.
//
// The combination of algorithms is not validated here. Nothing in this repository branches on
// a cipher or a DH group: NetBox's own `clean()` is the authority on which cipher, HMAC and
// group make sense together, exactly as it is for an interface type (api/v1alpha1/
// dcim_interface.go), and a CRD rule that guessed would reject configurations a real device
// accepts.
type NetBoxIKEProposalSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the proposal's identity, and it is unique across the whole NetBox install
	// (docs/netbox-schema.md -> vpn.IKEProposal, `name CharField REQ UNIQUE len=100`).
	//
	// Globally unique over namespaced CRDs, exactly like a Site's `slug`: two namespaces
	// cannot both own `ike-aes256`, and the loser gets a Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// AuthenticationMethod is how the peers authenticate in phase 1
	// (docs/netbox-schema.md -> vpn.IKEProposal, `authentication_method CharField REQ`).
	//
	// Required by the column, so required here: NetBox rejects a proposal without one, and a
	// CRD that accepted the object would turn a `kubectl apply` error into a condition
	// nobody reads. The enum carries no empty member, so `""` is refused by the enum itself
	// and needs no separate rule.
	AuthenticationMethod AuthenticationMethod `json:"authenticationMethod"`

	// EncryptionAlgorithm is the phase 1 cipher (docs/netbox-schema.md -> vpn.IKEProposal,
	// `encryption_algorithm CharField REQ`).
	//
	// One CEL rule on top of the shared enum, because EncryptionAlgorithm carries `""` as a
	// member -- `vpn.IPSecProposal`'s column is nullable and this one is not. One type, two
	// nullabilities, and the difference is expressed where it belongs: on the field. A
	// `MinLength` marker cannot say it, because controller-gen refuses a length marker on a
	// field whose schema comes from a named type ("must apply minlength to a textual value").
	// +kubebuilder:validation:XValidation:rule="self != ''",message="encryptionAlgorithm is required on an IKE proposal and may not be empty"
	EncryptionAlgorithm EncryptionAlgorithm `json:"encryptionAlgorithm"`

	// AuthenticationAlgorithm is the phase 1 HMAC (docs/netbox-schema.md -> vpn.IKEProposal,
	// `authentication_algorithm CharField choices=AuthenticationAlgorithmChoices`).
	//
	// Optional, because the column is `blank=True, null=True`: an AEAD cipher such as
	// `aes-256-gcm` authenticates as it encrypts and needs no separate HMAC.
	//
	// Unset leaves NetBox's own value alone; `""` clears it. Those are two different
	// instructions and the operator tells them apart from metadata.managedFields
	// (docs/concepts/field-ownership.md); the wording differs from the other clearable fields
	// here only because this one carries an enum, exactly as NetBoxRack.formFactor's does.
	//
	// Cleared as `null` rather than as an empty string, because NetBox's serializer returns
	// `null` for an unset choice and a payload of `""` would differ from the value read back
	// on every pass (#170) -- see registry.Field.EmptyIsNull.
	// +optional
	AuthenticationAlgorithm AuthenticationAlgorithm `json:"authenticationAlgorithm,omitempty"`

	// Group is the Diffie-Hellman group number the proposal offers
	// (docs/netbox-schema.md -> vpn.IKEProposal, `group PositiveSmallIntegerField REQ
	// choices=DHGroupChoices`).
	//
	// Named `group` and not `dhGroup`, because that is the column: the field map is a
	// spec-to-column table and a spec name that reads better than the column it writes makes
	// the table the only place the connection is recorded. Nothing about it relates to
	// `NetBoxTunnelGroup`.
	//
	// Required by the column. A DHGroup is an integer, so there is no zero value that means
	// "unset" -- `group: 0` is rejected by the enum rather than treated as omitted.
	Group DHGroup `json:"group"`

	// SALifetime is how long a phase 1 security association lives, in seconds
	// (docs/netbox-schema.md -> vpn.IKEProposal, `sa_lifetime PositiveIntegerField`).
	//
	// A pointer, so omitting it leaves NetBox's value alone rather than clearing it, and so
	// that `0` -- which NetBox accepts as a `PositiveIntegerField` -- is distinguishable from
	// unset. The upper bound is Django's `PositiveIntegerField` ceiling, 2147483647; NetBox
	// declares no validator of its own on the column.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=2147483647
	// +optional
	SALifetime *int64 `json:"saLifetime,omitempty"`

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

// NetBoxIKEProposal is one vpn.IKEProposal in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbikeprop
// +kubebuilder:printcolumn:name="Encryption",type=string,JSONPath=`.spec.encryptionAlgorithm`
// +kubebuilder:printcolumn:name="Group",type=integer,JSONPath=`.spec.group`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIKEProposal struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIKEProposalSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxIKEProposal) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxIKEProposal) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxIKEProposalList is a list of NetBoxIKEProposal.
// +kubebuilder:object:root=true
type NetBoxIKEProposalList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIKEProposal `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIKEProposal{}, &NetBoxIKEProposalList{})
}
