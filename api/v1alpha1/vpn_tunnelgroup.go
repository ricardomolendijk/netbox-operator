package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxTunnelGroupSpec describes one vpn.TunnelGroup: a flat label a set of tunnels is
// filed under.
//
// The kind with **no columns of its own**. docs/netbox-schema.md records it in full as:
//
//	## vpn.TunnelGroup   (vpn/models/tunnels.py)
//	   bases: ContactsMixin, OrganizationalModel
//	   (no own columns — every field is inherited from ContactsMixin, OrganizationalModel)
//	     name (OrganizationalModel)  CharField  REQ UNIQUE len=100
//	     slug (OrganizationalModel)  SlugField  REQ UNIQUE len=100
//	   meta.ordering: ('name',)
//
// so the field list here is `OrganizationalModel`'s and nothing else -- the dcim.RackGroup
// shape exactly. `ContactsMixin` contributes a `GenericRelation` only, which is a reverse
// accessor and never on the write path.
//
// Three consequences, all things this kind therefore does *not* have: no `parentRef`, no
// `parent IS NULL` natural-key variant and no cycle check, because there is no `parent`
// column and no MPTT base. Its identity is `slug` alone, from the base class's column-level
// `UNIQUE`.
//
// A tunnel group is a flat label rather than a tree: `Branch offices`, `Partners`, `Lab`.
type NetBoxTunnelGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the group's name.
	//
	// Globally unique (`name CharField REQ UNIQUE len=100`), and not this kind's natural key:
	// a kind gets one identity and `slug` is the stable one, so renaming a group in the spec
	// updates the object NetBox already holds rather than orphaning it.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the group's URL-safe identifier, and this kind's natural key.
	//
	// Globally unique over namespaced CRs (docs/netbox-schema.md -> vpn.TunnelGroup, `slug
	// SlugField REQ UNIQUE len=100`) -- which it is *because* the base class is
	// `OrganizationalModel` rather than `NestedGroupModel`, whose `slug` carries no `UNIQUE`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Description is free text shown next to the group.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the group's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive. Mapped, unlike on the six
	// organisational kinds that shipped in M8 and left it out (hack/coverage-exclusions.yaml):
	// the kinds that shipped after dcim.RackGroup map it, and adding a field is additive while
	// removing one is not.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxTunnelGroup is one vpn.TunnelGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). It is what
// `NetBoxTunnel.groupRef` points at, and half of that kind's natural key.
//
// `owner` is absent for the reason it is absent everywhere: `ForeignKey -> users.Owner`, and
// the `users` app has no Kind.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbtunnelgroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxTunnelGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxTunnelGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxTunnelGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxTunnelGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxTunnelGroupList is a list of NetBoxTunnelGroup.
// +kubebuilder:object:root=true
type NetBoxTunnelGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxTunnelGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxTunnelGroup{}, &NetBoxTunnelGroupList{})
}
