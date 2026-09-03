package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IPSecMode is one value of NetBox's IPSecModeChoices: which IPSec protocol the profile uses.
//
// Two members, `netbox/vpn/choices.py:107` at 4.6.8. No empty member, because the column is
// `mode CharField REQ choices=IPSecModeChoices` (docs/netbox-schema.md -> vpn.IPSecProfile)
// -- there is no clearing intent to express. The class declares no `key`, so the set is
// closed (hack/testdata/ir-4.6.8.json.gz -> enums.IPSecModeChoices).
//
// +kubebuilder:validation:Enum=esp;ah
type IPSecMode string

const (
	// IPSecModeESP is Encapsulating Security Payload: encryption and integrity.
	IPSecModeESP IPSecMode = "esp"

	// IPSecModeAH is Authentication Header: integrity only.
	IPSecModeAH IPSecMode = "ah"
)

// NetBoxIPSecProfileSpec describes one vpn.IPSecProfile: an IKE policy and an IPSec policy
// bound together into the thing a tunnel points at.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// **Two required references, and both point at kinds this change ships.** `ike_policy` and
// `ipsec_policy` are `ForeignKey REQ ... on_delete=PROTECT` (docs/netbox-schema.md ->
// vpn.IPSecProfile), so a profile cannot exist without both. Applied in any order they
// converge: a profile whose policies do not exist yet reports `RefsResolved=False` and waits,
// and nothing is written until both resolve (docs/concepts/references.md).
//
// **No ContainmentRef.** Both foreign keys are `PROTECT`, so nothing on the server side
// disappears when a policy does -- NetBox refuses the delete instead -- and there is nothing
// for an owner reference to mirror (docs/decisions/0003-ownership-and-references.md rule 4).
//
// **Identity is `name`**, `CharField REQ UNIQUE len=100`, with no `meta.constraints` on the
// model. One candidate, no pin. Deliberately *not* `(ike_policy, ipsec_policy)`: nothing makes
// that pair unique, so two profiles may legitimately combine the same policies with different
// modes.
type NetBoxIPSecProfileSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the profile's identity, and it is unique across the whole NetBox install
	// (docs/netbox-schema.md -> vpn.IPSecProfile, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Mode is which IPSec protocol the profile uses: `esp` or `ah`
	// (docs/netbox-schema.md -> vpn.IPSecProfile, `mode CharField REQ`).
	//
	// Required by the column, and undefaulted: NetBox declares no Django default, so choosing
	// one here would put a protocol into every profile the operator adopted. The enum carries
	// no empty member, so `""` is refused by the enum itself.
	Mode IPSecMode `json:"mode"`

	// IKEPolicyRef is the phase 1 policy this profile uses (docs/netbox-schema.md ->
	// vpn.IPSecProfile, `ike_policy ForeignKey REQ -> vpn.IKEPolicy on_delete=PROTECT`).
	//
	// Required by the column, so a bare value rather than a pointer.
	IKEPolicyRef IKEPolicyRef `json:"ikePolicyRef"`

	// IPSecPolicyRef is the phase 2 policy this profile uses (docs/netbox-schema.md ->
	// vpn.IPSecProfile, `ipsec_policy ForeignKey REQ -> vpn.IPSecPolicy on_delete=PROTECT`).
	IPSecPolicyRef IPSecPolicyRef `json:"ipsecPolicyRef"`

	// Description is free text shown next to the profile. Inherited from PrimaryModel.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the profile's long-form notes field. Also inherited, and a TextField, so
	// there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxIPSecProfile is one vpn.IPSecProfile in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). It is what
// `NetBoxTunnel.ipsecProfileRef` points at.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbipsecprof
// +kubebuilder:printcolumn:name="Mode",type=string,JSONPath=`.spec.mode`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIPSecProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIPSecProfileSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus     `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxIPSecProfile) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxIPSecProfile) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxIPSecProfileList is a list of NetBoxIPSecProfile.
// +kubebuilder:object:root=true
type NetBoxIPSecProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIPSecProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIPSecProfile{}, &NetBoxIPSecProfileList{})
}
