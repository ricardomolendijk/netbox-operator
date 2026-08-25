package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxFHRPGroupAssignmentSpec describes one ipam.FHRPGroupAssignment: the join row that
// says an interface participates in a first-hop-redundancy group, at a priority.
//
// `docs/netbox-schema.md -> ipam.FHRPGroupAssignment` records `bases: ChangeLoggedModel`,
// which is the whole reason this Kind looks unlike its neighbours. A ChangeLoggedModel mixes
// in neither TagsMixin nor CustomFieldsMixin, so there is **no `tags` column and no
// `custom_fields` column** -- the descriptor's `Taggable` and `CustomFieldable` are both
// false and this object carries no provenance stamp. It also declares no `description` and no
// `comments`, which is why it is the one object Kind with no clearable field at all.
//
// Its identity is a real database constraint, unlike three of the four other lookup-only
// Kinds in this milestone: `meta.constraints` declares
// `UniqueConstraint(fields=('interface_type', 'interface_id', 'group'))`.
type NetBoxFHRPGroupAssignmentSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Interface is the interface being enrolled -- a device interface or a virtual-machine
	// interface (docs/netbox-schema.md -> ipam.FHRPGroupAssignment, `interface_type` /
	// `interface_id`).
	//
	// A polymorphic pair, so it is a union struct rather than one ref plus a `kind`
	// discriminator: the field name pins the target Kind, CEL rejects an illegal target at
	// admission, and `kubectl explain` lists what the field accepts
	// (docs/concepts/generic-refs.md).
	//
	// Both halves of the pair are part of the identity, and the engine can key on them
	// because `applyGenericFK` writes the resolved pair back into the decoded spec under the
	// two column names -- the same mechanism `NetBoxVLANGroup` uses for `(scope_type,
	// scope_id, slug)`.
	Interface FHRPInterface `json:"interface"`

	// GroupRef is the group the interface joins (docs/netbox-schema.md ->
	// ipam.FHRPGroupAssignment, `group ForeignKey REQ -> ipam.FHRPGroup
	// on_delete=CASCADE`).
	//
	// **This Kind's containment parent**, and `CASCADE` is why: deleting the group deletes
	// this row server-side, so the CR has to go with it or the engine's create-if-absent step
	// resurrects what NetBox deliberately deleted
	// (docs/decisions/0003-ownership-and-references.md rule 4).
	//
	// `dcim.Interface` and `virtualization.VMInterface` cascade too -- both declare an
	// `fhrp_group_assignments` GenericRelation (docs/netbox-schema.md) -- and a Kind gets
	// exactly one containment parent, because Kubernetes garbage collection waits for *every*
	// owner and a second one would silently turn "delete the group or the interface" into
	// "delete both". `group` wins as the declared `on_delete=CASCADE` foreign key on this
	// model itself.
	GroupRef FHRPGroupRef `json:"groupRef"`

	// Priority is this interface's priority within the group -- which router is master
	// (docs/netbox-schema.md -> ipam.FHRPGroupAssignment, `priority
	// PositiveSmallIntegerField REQ`).
	//
	// Required by the column, so there is no "leave NetBox's value alone" state for it.
	// NetBox orders assignments by `-priority`, highest first.
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=65535
	Priority int32 `json:"priority"`
}

// NetBoxFHRPGroupAssignment is one ipam.FHRPGroupAssignment in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// The GROUP column reads the spec rather than the status, because visible intent next to an
// empty ID is what a still-resolving reference looks like.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbfhrpa
// +kubebuilder:printcolumn:name="Group",type=string,JSONPath=`.spec.groupRef.name`
// +kubebuilder:printcolumn:name="Priority",type=integer,JSONPath=`.spec.priority`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxFHRPGroupAssignment struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxFHRPGroupAssignmentSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus            `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (a *NetBoxFHRPGroupAssignment) NetBoxSpec() *NetBoxObjectSpec {
	return &a.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (a *NetBoxFHRPGroupAssignment) NetBoxStatus() *NetBoxObjectStatus { return &a.Status }

// NetBoxFHRPGroupAssignmentList is a list of NetBoxFHRPGroupAssignment.
// +kubebuilder:object:root=true
type NetBoxFHRPGroupAssignmentList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxFHRPGroupAssignment `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxFHRPGroupAssignment{}, &NetBoxFHRPGroupAssignmentList{})
}
