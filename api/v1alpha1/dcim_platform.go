package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxPlatformSpec describes one dcim.Platform.
//
// The operating system or firmware a device runs -- `dcim.Device.platform` and
// `dcim.DeviceType.default_platform` both point here (docs/netbox-schema.md).
//
// A `NestedGroupModel` whose uniqueness is **not** scoped by its own tree, which makes it the
// odd one out among the four nested-group kinds shipped so far. `meta.constraints` is keyed
// on `manufacturer`, not on `parent`, so `parent` is a plain attribute here while
// `manufacturer` is identity -- the exact opposite of dcim.DeviceRole. Getting it backwards
// means adopting the wrong platform.
type NetBoxPlatformSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the platform's name, as NetBox displays it.
	//
	// Inherited from NestedGroupModel (docs/netbox-schema.md -> dcim.Platform, `name
	// (NestedGroupModel) CharField REQ len=100`) and not column-unique: `meta.constraints`
	// scopes it per manufacturer, with a separate constraint for manufacturer-less
	// platforms.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the platform's URL-safe identifier, and this kind's natural key.
	//
	// Unique *per manufacturer* (docs/netbox-schema.md -> dcim.Platform, `meta.constraints`:
	// `..._manufacturer_slug` on `(manufacturer, slug)`, plus `..._slug` on `(slug)` with
	// `condition=Q(manufacturer__isnull=True)`). Two platforms called `ios` under different
	// manufacturers are two legitimate objects, and both reconcile Ready.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// ManufacturerRef limits this platform to one vendor's devices
	// (docs/netbox-schema.md -> dcim.Platform, `manufacturer ForeignKey ->
	// dcim.Manufacturer on_delete=PROTECT`).
	//
	// Part of this kind's identity, and the surprising half of it: leaving it unset makes a
	// *vendor-neutral* platform, which is a different natural key rather than the same key
	// with a filter omitted, so the lookup pins `manufacturer_id__isnull=true` instead of
	// dropping the filter (docs/concepts/lookups.md). Declaring it and having it not resolve
	// yet makes neither candidate applicable, and the engine waits rather than adopting an
	// unrelated vendor-neutral platform of the same slug.
	//
	// PROTECT, so NetBox refuses to delete a manufacturer while a platform points at it.
	// +optional
	ManufacturerRef *ManufacturerRef `json:"manufacturerRef,omitempty"`

	// ParentRef nests this platform under another one
	// (docs/netbox-schema.md -> dcim.Platform, `parent (NestedGroupModel) TreeForeignKey ->
	// dcim.Platform on_delete=CASCADE`).
	//
	// Outside the identity, unlike on every other nested-group kind: no constraint on
	// dcim.Platform mentions `parent`, so a platform is findable by its slug whatever its
	// place in the tree. That is what makes this reference safe to *defer* -- the engine
	// creates the platform top-level and PATCHes `parent` on when the reference resolves,
	// so a parent and a child applied in one batch converge without a resync.
	// +optional
	ParentRef *PlatformRef `json:"parentRef,omitempty"`

	// Description is free text shown next to the platform.
	//
	// Inherited from NestedGroupModel (docs/netbox-schema.md -> dcim.Platform, `description
	// (NestedGroupModel) CharField len=200`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`
}

// NetBoxPlatform is one dcim.Platform in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). A
// `platformRef` or `defaultPlatformRef` from a team namespace into the catalogue namespace is
// a cross-namespace reference and needs a NetBoxRefGrant -- see
// docs/reference/netboxplatform.md.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbplatform
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Manufacturer",type=string,JSONPath=`.spec.manufacturerRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxPlatform struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxPlatformSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxPlatform) NetBoxSpec() *NetBoxObjectSpec { return &p.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxPlatform) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxPlatformList is a list of NetBoxPlatform.
// +kubebuilder:object:root=true
type NetBoxPlatformList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxPlatform `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxPlatform{}, &NetBoxPlatformList{})
}
