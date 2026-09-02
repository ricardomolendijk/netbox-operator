package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// IKEVersion is one value of NetBox's IKEVersionChoices: which version of the Internet Key
// Exchange protocol the policy speaks.
//
// An integer rather than a string, because the column is
// `version PositiveSmallIntegerField def=UNRESOLVED:IKEVersionChoices.VERSION_2
// choices=IKEVersionChoices` (docs/netbox-schema.md -> vpn.IKEPolicy): NetBox stores and
// returns `1` or `2`, and the operator compares a number. The RackWidth derivation.
//
// Two members, `netbox/vpn/choices.py:73` at 4.6.8. The class declares no `key`, so the set
// is closed (hack/testdata/ir-4.6.8.json.gz -> enums.IKEVersionChoices).
//
// +kubebuilder:validation:Enum=1;2
type IKEVersion int32

const (
	// IKEVersion1 is IKEv1.
	IKEVersion1 IKEVersion = 1

	// IKEVersion2 is IKEv2, and NetBox's own default.
	IKEVersion2 IKEVersion = 2
)

// IKEMode is one value of NetBox's IKEModeChoices: how phase 1 negotiates.
//
// Two members, `netbox/vpn/choices.py:83` at 4.6.8, plus the empty string because the column
// is `blank=True, null=True` (docs/netbox-schema.md -> vpn.IKEPolicy, `mode CharField
// choices=IKEModeChoices`). The class declares no `key`, so the set is closed
// (hack/testdata/ir-4.6.8.json.gz -> enums.IKEModeChoices).
//
// Nullable rather than defaulted, and NetBox means it: IKEv2 has no mode at all, so a policy
// that names one is an IKEv1 policy. Defaulting it here would assert a negotiation style
// nobody described.
//
// +kubebuilder:validation:Enum="";aggressive;main
type IKEMode string

const (
	// IKEModeAggressive is IKEv1 aggressive mode: fewer round trips, identities in the clear.
	IKEModeAggressive IKEMode = "aggressive"

	// IKEModeMain is IKEv1 main mode.
	IKEModeMain IKEMode = "main"
)

// NetBoxIKEPolicySpec describes one vpn.IKEPolicy: the phase 1 policy a peer offers, built
// from one or more NetBoxIKEProposals.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary spec
// fields that a user writes alongside the rest.
//
// # preshared_key is deliberately absent from this spec
//
// `vpn.IKEPolicy.preshared_key` is a `TextField` on the model and a writable field on the
// serializer (docs/netbox-schema.md -> vpn.IKEPolicy; hack/testdata/ir-4.6.8.json.gz ->
// vpn.IKEPolicy.write_path). It holds a pre-shared key, and **a secret may never be inline in
// a spec**: a CRD field would put the key in an object every reader of the namespace can
// `get`, and in every `kubectl get -o yaml`, every GitOps repository and every etcd backup.
//
// The only permitted shape is `spec.presharedKeySecretRef` -> a key of a Secret, and reading
// a Secret into a NetBox payload field is a capability the engine does not have: there is no
// `FieldClass` for it and internal/reconciler/payload.go has nowhere to fetch one from. That
// mechanism -- the field class, the Secret informer scoped to the object's own namespace, and
// the RBAC decision that comes with it -- is #241, and it is engine surgery rather than
// something a kind can carry on its own.
//
// So the column is left unmapped and recorded in hack/coverage-exclusions.yaml, exactly as
// `ipam.FHRPGroup.auth_key` (api/v1alpha1/ipam_fhrpgroup.go) and
// `wireless.WirelessLAN.auth_psk` (api/v1alpha1/wireless_auth.go) already are. Set the key in
// NetBox by hand until #241 lands; the operator never writes the column and therefore never
// clears it, and adding `presharedKeySecretRef` later is a purely additive CRD change.
// `preshared_key` is in internal/netbox/do.go's redaction set regardless, because NetBox
// *returns* it on every read of this endpoint.
//
// # Identity
//
// `name CharField REQ UNIQUE len=100` -- a column-level UNIQUE, and this model declares no
// `meta.constraints` at all. One candidate, no pin.
type NetBoxIKEPolicySpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the policy's identity, and it is unique across the whole NetBox install
	// (docs/netbox-schema.md -> vpn.IKEPolicy, `name CharField REQ UNIQUE len=100`).
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Version is which IKE version this policy speaks (docs/netbox-schema.md ->
	// vpn.IKEPolicy, `version PositiveSmallIntegerField def=IKEVersionChoices.VERSION_2`).
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct. NetBox returns it as `{"value":2,"label":"IKEv2"}` and accepts the bare
	// value, and the differ compares the value (docs/concepts/drift.md).
	// +kubebuilder:default=2
	// +optional
	Version IKEVersion `json:"version,omitempty"`

	// Mode is the phase 1 negotiation mode, and applies to IKEv1 only
	// (docs/netbox-schema.md -> vpn.IKEPolicy, `mode CharField choices=IKEModeChoices`).
	//
	// Undefaulted, because the column is nullable with no Django default and an IKEv2 policy
	// has no mode. Setting it on an IKEv2 policy is NetBox's `clean()` to refuse, not this
	// schema's: the operator models no crypto rules of its own.
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
	Mode IKEMode `json:"mode,omitempty"`

	// Proposals is the set of IKE proposals this policy offers, in preference order as far as
	// the peer is concerned and as an unordered set as far as NetBox is concerned.
	//
	// A to-many reference: every element is resolved to a NetBox id and the field is written
	// as the whole list, because NetBox replaces a many-to-many wholesale on PATCH -- there is
	// no add or remove verb. So the listed set *is* the set, and reordering it produces no
	// write (docs/concepts/drift.md).
	//
	// **Optional in the CRD and required by NetBox, and the gap is deliberate.** The column is
	// `proposals ManyToManyField -> vpn.IKEProposal` with no `blank=True`
	// (docs/netbox-schema.md -> vpn.IKEPolicy), so NetBox's serializer refuses to *create* a
	// policy without one. It is still optional here, because a spec omission means "do not
	// manage this relation" (docs/concepts/field-ownership.md) and a policy the operator
	// adopts should keep the proposals somebody else set. A create with the field omitted is
	// NetBox's own 400, surfaced as a condition; a required CRD field would instead make
	// adoption impossible.
	//
	// `minItems: 1` therefore bounds the *declared* list rather than requiring one: `[]` would
	// ask NetBox to clear a relation it refuses to leave empty, so the empty list is rejected
	// at `kubectl apply` instead of becoming a 400 on every reconcile. That is the one place
	// this field differs from NetBoxVRF's `importTargets`, where `[]` is a legitimate clear.
	//
	// **All or nothing.** If any element cannot be resolved the whole field is left out of the
	// payload and the object reports RefsResolved=False naming the element that failed, so a
	// policy applied before its proposals waits rather than being created without them.
	//
	// MaxItems is the project standard 256 (docs/concepts/references.md, "A list needs a
	// bound"): ObjectRef carries five CEL rules and the API server costs a rule on a list item
	// at the list's maximum length, so an unbounded list of refs is rejected outright.
	// +optional
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	Proposals []IKEProposalRef `json:"proposals,omitempty"`

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
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxIKEPolicy is one vpn.IKEPolicy in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// The pre-shared key this policy may carry in NetBox is not part of this CR and is not
// written by the operator -- see NetBoxIKEPolicySpec.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbikepol
// +kubebuilder:printcolumn:name="Version",type=integer,JSONPath=`.spec.version`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxIKEPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxIKEPolicySpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxIKEPolicy) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxIKEPolicy) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxIKEPolicyList is a list of NetBoxIKEPolicy.
// +kubebuilder:object:root=true
type NetBoxIKEPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxIKEPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxIKEPolicy{}, &NetBoxIKEPolicyList{})
}
