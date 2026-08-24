package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxClusterTypeSpec describes one virtualization.ClusterType.
//
// A pure OrganizationalModel: `docs/netbox-schema.md -> virtualization.ClusterType` records
// `(no own columns -- every field is inherited from OrganizationalModel)`, so `name`
// (`REQ UNIQUE len=100`), `slug` (`REQ UNIQUE len=100`) and `description` (`len=200`) are the
// whole of it. Nothing here is derived from the REST documentation: a column that is neither
// in the digest nor in the base class the digest documents does not get a field until NBO-041
// confirms it from the OpenAPI schema.
//
// `comments` is the one inherited column left out. OrganizationalModel declares it and
// NetBox's ClusterTypeSerializer does accept it, but NBO-028's field table names three fields
// and the group kinds that shipped before this one -- NetBoxSiteGroup, NetBoxRegion -- leave
// it out too. Adding it later is additive; a field nobody asked for is not removable.
type NetBoxClusterTypeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the cluster type's label -- `VMware vSphere`, `Proxmox VE`, `Hyper-V`.
	//
	// Column-unique (`name CharField REQ UNIQUE len=100`), so it identifies a type on its
	// own. `slug` is the natural key anyway, for the reason every catalogue kind in this
	// operator prefers it: a slug is the spelling a reference from another namespace uses.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the cluster type's URL-safe identifier, and its natural key.
	//
	// Globally unique in NetBox over namespaced CRDs, exactly like a Site's: two namespaces
	// cannot both own `proxmox`, and the loser gets a Conflict rather than a second object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Description is free text shown next to the cluster type.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxClusterType is one virtualization.ClusterType in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). It reads like
// a catalogue object that wants to be cluster-scoped, and ADR-0002's answer is the one it
// gives for NetBoxLocation: a shared catalogue namespace plus a NetBoxRefGrant (NBO-014) is
// how a team namespace points at it.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbctype
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxClusterType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxClusterTypeSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxClusterType) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxClusterType) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxClusterTypeList is a list of NetBoxClusterType.
// +kubebuilder:object:root=true
type NetBoxClusterTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxClusterType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxClusterType{}, &NetBoxClusterTypeList{})
}
