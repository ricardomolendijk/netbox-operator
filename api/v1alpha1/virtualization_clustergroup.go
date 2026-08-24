package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxClusterGroupSpec describes one virtualization.ClusterGroup.
//
// The same three fields as NetBoxClusterType and for the same reason: `bases: ContactsMixin,
// OrganizationalModel` (docs/netbox-schema.md -> virtualization.ClusterGroup), and the model's
// only own entries are `vlan_groups GenericRelation` and `contacts (ContactsMixin)
// GenericRelation`. A GenericRelation is a reverse relation rather than a column -- not
// writable, mostly not even serialized -- so neither becomes a field here.
//
// It is a separate Kind from NetBoxClusterType rather than one "cluster catalogue" Kind with a
// discriminator, because they are two NetBox models at two endpoints with independent ids: a
// cluster carries both a `type` and a `group`, and a shared Kind would make that reference
// ambiguous in exactly the place `PROTECT` makes it unrecoverable.
type NetBoxClusterGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the cluster group's label -- `Production`, `Amsterdam DC`.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the cluster group's URL-safe identifier, and its natural key.
	//
	// Column-unique in NetBox and therefore globally unique over namespaced CRDs: two
	// namespaces cannot both own `production`, and the loser gets a Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Description is free text shown next to the cluster group.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxClusterGroup is one virtualization.ClusterGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbcgroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxClusterGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxClusterGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus     `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxClusterGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxClusterGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxClusterGroupList is a list of NetBoxClusterGroup.
// +kubebuilder:object:root=true
type NetBoxClusterGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxClusterGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxClusterGroup{}, &NetBoxClusterGroupList{})
}
