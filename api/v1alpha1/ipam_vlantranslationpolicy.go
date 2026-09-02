package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxVLANTranslationPolicySpec describes one ipam.VLANTranslationPolicy: a named table of
// VLAN ID rewrites, applied to an interface as a whole rather than to one VLAN.
//
// Two columns of its own and a reverse relation. `docs/netbox-schema.md ->
// ipam.VLANTranslationPolicy` records `name CharField REQ UNIQUE len=100`, with `description`
// (`len=200`) and `comments` (TextField) inherited from PrimaryModel, and `rules` on the far
// side of `ipam.VLANTranslationRule.policy`.
//
// **It carries no tags and no custom fields, and it is the serializer rather than the model
// that decides that.** ipam.VLANTranslationPolicy is a PrimaryModel, so the *model* mixes in
// TagsMixin and CustomFieldsMixin like every other one; but
// `VLANTranslationPolicySerializer.Meta.fields` is written out longhand as
// `('id', 'url', 'display', 'name', 'description', 'display', 'rules', 'owner', 'comments')`
// (NetBox 4.6.8, `netbox/ipam/api/serializers_/vlans.py:123`), and neither `tags` nor
// `custom_fields` is in it. So there is no provenance stamp on this kind or on its rules --
// adoption is by natural key alone, and `docs/concepts/provenance.md`'s "the operator can tell
// its own objects apart" does not hold here. The audit checks the flags against that
// serializer rather than against the model (internal/registry/coverage_test.go,
// mixesInTagsMixin).
//
// **It ships to unblock two columns that have been waiting for it.** `dcim.Interface` and
// `virtualization.VMInterface` both declare
// `vlan_translation_policy ForeignKey -> ipam.VLANTranslationPolicy on_delete=PROTECT`
// (docs/netbox-schema.md -> dcim.Interface, virtualization.VMInterface), and both Kinds
// shipped without the field because there was nothing for it to point at. PROTECT, so a policy
// in use by an interface cannot be deleted and the refusal arrives as
// Deleting=False, Reason=Protected.
type NetBoxVLANTranslationPolicySpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the policy's name, and its natural key (docs/netbox-schema.md ->
	// ipam.VLANTranslationPolicy, `name CharField REQ UNIQUE len=100`).
	//
	// Globally unique in NetBox over namespaced CRDs: two namespaces cannot both own
	// `dc1-to-dc2`, and the loser gets a Conflict rather than a second object.
	//
	// There is no `slug` on this model to prefer instead, unlike every OrganizationalModel in
	// the catalogue, so the name is what a reference from another namespace spells.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Description is free text shown next to the policy. Inherited from PrimaryModel
	// (docs/netbox-schema.md -> ipam.VLANTranslationPolicy, `description (PrimaryModel)
	// CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the policy's long-form notes field. Also inherited, and a TextField rather
	// than a CharField: it has no max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`

	// Rules are the translations this policy declares, materialised as
	// NetBoxVLANTranslationRule children (docs/concepts/inline-children.md,
	// docs/decisions/0003-ownership-and-references.md rule 5).
	//
	// Sugar, and optional like all of it: every rule is equally writable as its own CR with a
	// `policyRef`, a rule already materialised survives this field being emptied at a version
	// boundary, and a hand-written rule pointing at this policy is never materialised, never
	// pruned and never appears in `status.children`.
	//
	// A **map list keyed by `localVID`**, and that key is not a second copy of NetBox's
	// `(policy, local_vid)` constraint -- it is the identity of an entry *within this list*,
	// which is what ChildName and the owned-by path are both built from. Two entries with the
	// same `localVID` would derive one child CR name, so a duplicate is not a state the
	// operator can represent at all, and rejecting it at admission names the field instead of
	// surfacing as a materialiser Conflict on a name collision.
	//
	// `remoteVID` is deliberately **not** keyed, bounded or cross-checked, which is the case
	// this ticket is actually about: NetBox enforces `(policy, remote_vid)` as a second
	// constraint, so a rule that swaps two VIDs inside one policy is refused by the database
	// and reported as Conflict naming the constraint. Duplicating that check here is how the
	// two would drift apart.
	//
	// Bounded at 128. A translation table is per-interface configuration rather than an
	// inventory, and 128 rewrites is past any real one while keeping the CRD's cost to the API
	// server bounded (docs/concepts/references.md, "A list needs a bound").
	//
	// Omit it and this policy materialises no rules. `[]` is the same statement and prunes the
	// ones a previous revision declared.
	// +kubebuilder:validation:MaxItems=128
	// +listType=map
	// +listMapKey=localVID
	// +optional
	Rules []InlineVLANTranslationRule `json:"rules,omitempty"`
}

// NetBoxVLANTranslationPolicy is one ipam.VLANTranslationPolicy in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). A shared
// catalogue namespace plus a NetBoxRefGrant is how a team namespace points at one.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbvtp
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Children",type=string,JSONPath=`.status.conditions[?(@.type=="ChildrenReady")].status`,priority=1
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxVLANTranslationPolicy struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxVLANTranslationPolicySpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus              `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxVLANTranslationPolicy) NetBoxSpec() *NetBoxObjectSpec {
	return &p.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxVLANTranslationPolicy) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxVLANTranslationPolicyList is a list of NetBoxVLANTranslationPolicy.
// +kubebuilder:object:root=true
type NetBoxVLANTranslationPolicyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxVLANTranslationPolicy `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxVLANTranslationPolicy{}, &NetBoxVLANTranslationPolicyList{})
}
