package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxTenantGroupSpec describes one tenancy.TenantGroup.
//
// A NestedGroupModel, like dcim.Region -- and the comparison is the interesting part.
// dcim.Region's uniqueness is per-parent (`meta.constraints` on `(parent, name)` plus a
// separate `name WHERE parent IS NULL`), so whether `parentRef` is set decides which
// natural key applies. tenancy.TenantGroup has **no `meta.constraints` at all** and puts
// column-level `UNIQUE` on both `name` and `slug` (docs/netbox-schema.md ->
// tenancy.TenantGroup), so its uniqueness is global and `parent` is not part of its
// identity in any variant. One natural key, `slug`, and no null pin -- see
// internal/registry/tenancy_tenantgroup.go.
type NetBoxTenantGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the group's label in the NetBox UI.
	//
	// Column-unique across NetBox (docs/netbox-schema.md -> tenancy.TenantGroup,
	// `name CharField REQ UNIQUE len=100`), unlike a NetBoxRegion's, which is unique only
	// per parent.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the group's URL-safe identifier, and this kind's natural key.
	//
	// NetBox enforces uniqueness on it globally (docs/netbox-schema.md ->
	// tenancy.TenantGroup, `slug SlugField REQ UNIQUE len=100`) while this CRD is
	// namespaced (docs/decisions/0002-crd-scoping.md), so two NetBoxTenantGroups in
	// different namespaces claiming one slug is one group and a Conflict -- not two groups.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ParentRef nests this group under another one.
	//
	// Self-referential: `parent (NestedGroupModel) TreeForeignKey -> tenancy.TenantGroup`
	// (docs/netbox-schema.md -> tenancy.TenantGroup). Unlike a NetBoxRegion's `parentRef`
	// this one is *not* part of the natural key, because tenancy.TenantGroup declares no
	// `meta.constraints` and is column-unique on `slug` alone. A group whose parent does
	// not exist yet is therefore still identifiable, so the engine creates it top-level and
	// PATCHes `parent` on once the reference resolves -- which is why the field is declared
	// deferred (Descriptor.Deferred, DeferIfUnresolved) rather than blocking the create.
	//
	// In between, the object reports Ready=False with RefsResolved naming this field and
	// `parentRef` in status.deferredPending. Cycle detection is NBO-016.
	// +optional
	ParentRef *TenantGroupRef `json:"parentRef,omitempty"`

	// Description is free text shown next to the group
	// (docs/netbox-schema.md -> tenancy.TenantGroup, `description (NestedGroupModel)
	// CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxTenantGroup is one tenancy.TenantGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). It is
// catalogue-shaped, so the convention is one shared namespace holding the groups and a
// NetBoxRefGrant letting team namespaces point at them.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbtenantgroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxTenantGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxTenantGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus    `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxTenantGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxTenantGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxTenantGroupList is a list of NetBoxTenantGroup.
// +kubebuilder:object:root=true
type NetBoxTenantGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxTenantGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxTenantGroup{}, &NetBoxTenantGroupList{})
}
