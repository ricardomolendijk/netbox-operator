package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRegionSpec describes one dcim.Region.
//
// Every column here is inherited rather than declared on the model, so
// docs/netbox-schema.md lists none of them under `dcim.Region` -- that body predates the
// NBO-070 extractor fix and its own preamble says not to derive a CRD's fields from an
// entry without reading the model's bases. The bases are the source:
// `OrganizationalModel` gives `name`, `slug` and `description`, and `NestedGroupModel`
// adds `parent` (docs/netbox-schema.md, preamble on inherited columns).
type NetBoxRegionSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the region's name.
	//
	// Not globally unique, unlike a Site's: `dcim.Region.meta.constraints` makes it unique
	// per parent, plus a separate constraint for top-level regions. That is why this kind
	// has two natural-key candidates rather than one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the region's URL-safe identifier.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ParentRef nests this region under another one.
	//
	// Self-referential (`parent -> self`), and the reason this is the kind M2 is tested
	// against: it is the smallest case where identity depends on a reference. Leaving it
	// unset makes a top-level region, which is a different natural key rather than the
	// same key with a field omitted -- see docs/concepts/lookups.md on why a null filter
	// is pinned.
	//
	// Resolved to a NetBox id before the payload is built. Until it resolves, the object
	// reports RefsResolved=False naming this field, and `parent` is left out of the payload
	// rather than sent as null. A change to the target re-enqueues this object directly, so
	// applying parent and child in either order converges without waiting for a resync.
	// +optional
	ParentRef *RegionRef `json:"parentRef,omitempty"`

	// Description is free text shown next to the region.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxRegion is one dcim.Region in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbregion
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Parent",type=string,JSONPath=`.spec.parentRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRegion struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRegionSpec   `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRegion) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRegion) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRegionList is a list of NetBoxRegion.
// +kubebuilder:object:root=true
type NetBoxRegionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRegion `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRegion{}, &NetBoxRegionList{})
}
