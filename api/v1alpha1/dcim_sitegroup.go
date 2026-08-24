package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxSiteGroupSpec describes one dcim.SiteGroup.
//
// The sibling of NetBoxRegion rather than of NetBoxSite: dcim.SiteGroup is a
// NestedGroupModel with the same inherited columns and the same pair of unique constraints
// (docs/netbox-schema.md -> dcim.SiteGroup, bases and meta.constraints), so everything
// NBO-011 established about a self-referential identity holds here unchanged. Where a Region
// groups sites geographically, a SiteGroup groups them functionally -- branch, campus,
// colo -- and NetBox keeps the two hierarchies independent.
//
// Like dcim.Region, every column is inherited: `OrganizationalModel` gives `name`, `slug`
// and `description`, and `NestedGroupModel` adds `parent`. The schema entry itself lists
// only the model's GenericRelations, which is what its own preamble warns about.
type NetBoxSiteGroupSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the site group's name.
	//
	// Not globally unique: `dcim.SiteGroup.meta.constraints` makes it unique per parent,
	// with a separate constraint for top-level groups. That is why this kind has two
	// natural-key candidates rather than one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the site group's URL-safe identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ParentRef nests this site group under another one.
	//
	// Self-referential (`parent -> self`). Leaving it unset makes a top-level group, which
	// is a different natural key rather than the same key with a field omitted -- see
	// docs/concepts/lookups.md on why a null filter is pinned.
	//
	// Resolved to a NetBox id before the payload is built. Until it resolves, the object
	// reports RefsResolved=False naming this field, and `parent` is left out of the payload
	// rather than sent as null. A change to the target re-enqueues this object directly, so
	// applying parent and child in either order converges without waiting for a resync.
	// +optional
	ParentRef *SiteGroupRef `json:"parentRef,omitempty"`

	// Description is free text shown next to the site group.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxSiteGroup is one dcim.SiteGroup in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbsitegroup
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxSiteGroup struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxSiteGroupSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus  `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (g *NetBoxSiteGroup) NetBoxSpec() *NetBoxObjectSpec { return &g.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (g *NetBoxSiteGroup) NetBoxStatus() *NetBoxObjectStatus { return &g.Status }

// NetBoxSiteGroupList is a list of NetBoxSiteGroup.
// +kubebuilder:object:root=true
type NetBoxSiteGroupList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxSiteGroup `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxSiteGroup{}, &NetBoxSiteGroupList{})
}
