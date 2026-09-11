package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxVLANTranslationRuleSpec describes one ipam.VLANTranslationRule: a single VLAN ID
// rewrite inside a policy.
//
// Four columns, all of them declared on the model itself (docs/netbox-schema.md ->
// ipam.VLANTranslationRule): `policy ForeignKey REQ -> ipam.VLANTranslationPolicy
// on_delete=CASCADE`, `local_vid PositiveSmallIntegerField REQ`, `remote_vid
// PositiveSmallIntegerField REQ` and `description CharField len=200`. Nothing inherited, and
// no `comments`: it is a NetBoxModel rather than a PrimaryModel.
//
// **Two identities, both real and both enforced.** `meta.constraints` carries
// `UniqueConstraint(fields=('policy', 'local_vid'))` *and*
// `UniqueConstraint(fields=('policy', 'remote_vid'))`, so within one policy neither side of
// the rewrite may repeat. A pair of rules that swap two VIDs -- `100 -> 200` and `200 -> 100`
// -- satisfies both constraints and is legal; a pair that maps two locals onto one remote is
// refused by the database. The operator does not pre-validate either: NetBox's 409 arrives as
// `Ready=False, Reason=Conflict` naming the constraint, which is one statement of the rule
// rather than two that can drift (see internal/registry/ipam_vlantranslationrule.go for how
// both constraints become natural-key candidates).
//
// **No tags and no custom fields**, for the reason NetBoxVLANTranslationPolicy's doc comment
// gives: `VLANTranslationRuleSerializer.Meta.fields` is
// `('id', 'url', 'display', 'policy', 'local_vid', 'remote_vid', 'description')` (NetBox
// 4.6.8, `netbox/ipam/api/serializers_/vlans.py:116`) and neither column is in it, however
// much the NetBoxModel base suggests otherwise. So there is no provenance stamp on a rule and
// adoption is by natural key alone.
type NetBoxVLANTranslationRuleSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// PolicyRef is the policy this rule belongs to (docs/netbox-schema.md ->
	// ipam.VLANTranslationRule, `policy ForeignKey REQ -> ipam.VLANTranslationPolicy
	// on_delete=CASCADE`).
	//
	// Required, because NetBox's column is, and half of both identities as well as the
	// containment parent: `local_vid: 100` says nothing on its own, so an unresolved reference
	// means the operator cannot tell whether this rule exists and it waits rather than
	// adopting a rule out of somebody else's policy (docs/concepts/lookups.md).
	//
	// The `on_delete=CASCADE` is what makes it the containment parent and the single
	// non-controller owner reference (docs/decisions/0003-ownership-and-references.md rule 4).
	// It has to be: NetBox deletes a policy's rules with the policy, so a rule CR that
	// outlived its row would be recreated by the engine's create-if-absent step and the
	// deletion would silently undo itself (#203).
	PolicyRef VLANTranslationPolicyRef `json:"policyRef"`

	// LocalVID is the VLAN ID on this side of the translation (docs/netbox-schema.md ->
	// ipam.VLANTranslationRule, `local_vid PositiveSmallIntegerField REQ`).
	//
	// 1-4094, on the same terms as `NetBoxVLAN.spec.vid`: 0 and 4095 are reserved by 802.1Q
	// and rejected at admission rather than arriving as a 400 from NetBox three steps later.
	//
	// Required, and therefore not a pointer: the three states an optional field has do not
	// apply to a column NetBox will not accept as null.
	//
	// Part of the first identity, `(policy, local_vid)`. Changing it is a PATCH rather than a
	// new object, and if another rule in the policy already holds the new value NetBox refuses
	// with a 409 naming `ipam_vlantranslationrule_unique_policy_local_vid`.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	LocalVID int32 `json:"localVID"`

	// RemoteVID is the VLAN ID this rule translates to (docs/netbox-schema.md ->
	// ipam.VLANTranslationRule, `remote_vid PositiveSmallIntegerField REQ`).
	//
	// 1-4094, required, and part of the *second* identity, `(policy, remote_vid)` -- which is
	// a constraint of equal standing rather than a fallback. Editing it PATCHes this one
	// column and nothing else.
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=4094
	RemoteVID int32 `json:"remoteVID"`

	// Description is free text shown next to the rule (docs/netbox-schema.md ->
	// ipam.VLANTranslationRule, `description CharField len=200`).
	//
	// Declared on the model rather than inherited, unlike almost every other `description` in
	// this API, and optional either way.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxVLANTranslationRule is one ipam.VLANTranslationRule in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Usable
// standalone, and also the Kind a NetBoxVLANTranslationPolicy's `spec.rules` materialises.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvtr
// +kubebuilder:printcolumn:name="Policy",type=string,JSONPath=`.spec.policyRef.name`
// +kubebuilder:printcolumn:name="Local",type=integer,JSONPath=`.spec.localVID`
// +kubebuilder:printcolumn:name="Remote",type=integer,JSONPath=`.spec.remoteVID`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVLANTranslationRule struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVLANTranslationRuleSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus            `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxVLANTranslationRule) NetBoxSpec() *NetBoxObjectSpec {
	return &r.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxVLANTranslationRule) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxVLANTranslationRuleList is a list of NetBoxVLANTranslationRule.
// +kubebuilder:object:root=true
type NetBoxVLANTranslationRuleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVLANTranslationRule `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVLANTranslationRule{}, &NetBoxVLANTranslationRuleList{})
}
