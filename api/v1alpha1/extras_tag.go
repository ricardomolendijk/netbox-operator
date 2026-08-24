package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxTagSpec describes one extras.Tag.
//
// NetBoxObjectSpec is inline, so endpointRef, onConflict and deletionPolicy are ordinary
// spec fields that a user writes alongside the rest -- and the engine excludes exactly
// those from the NetBox payload by reflecting over that struct, so an addition to the
// envelope cannot leak into NetBox as an unknown column.
type NetBoxTagSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the tag's label in the NetBox UI. Unique across NetBox.
	//
	// Declared on django-taggit's TagBase rather than on extras.Tag, so
	// docs/netbox-schema.md does not list it under `extras.Tag` -- see that file's
	// preamble on inherited columns, which are as required and as writable as declared
	// ones.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Slug is the tag's URL-safe identifier, and this kind's natural key.
	//
	// NetBox enforces uniqueness on it globally while this CRD is namespaced
	// (docs/decisions/0002-crd-scoping.md), so two NetBoxTags in different namespaces
	// claiming one slug is one tag and a Conflict -- not two tags. Also inherited from
	// TagBase.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// Color is the tag's colour as six hexadecimal digits, without a leading `#`.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator
	// can never correct (docs/netbox-schema.md -> extras.Tag,
	// `color ColorField def='ColorChoices.COLOR_GREY'`, which is `9e9e9e`).
	// +kubebuilder:default="9e9e9e"
	// +kubebuilder:validation:Pattern=`^[0-9a-f]{6}$`
	// +optional
	Color string `json:"color,omitempty"`

	// Description is free text shown next to the tag.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Weight orders tags in NetBox's UI, lowest first
	// (docs/netbox-schema.md -> extras.Tag, `meta.ordering: ('weight', 'name')`).
	//
	// A pointer with an explicit default rather than a plain integer, for the same reason
	// as Color: NetBox defaults it to 1000, and `omitempty` on a plain int32 would drop a
	// deliberate `weight: 0` -- a value the column permits -- out of the payload.
	// +kubebuilder:default=1000
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=32767
	// +optional
	Weight *int32 `json:"weight,omitempty"`

	// ObjectTypes restricts the NetBox models this tag may be applied to. Empty means
	// every model that can carry tags.
	//
	// The values are Django ContentType strings (`dcim.device`), not references to other
	// CRs: object_types is a ManyToManyField onto contenttypes.ContentType
	// (docs/netbox-schema.md -> extras.Tag), so there is no NetBox object behind an entry
	// and nothing for the resolver to resolve. The descriptor therefore declares it an
	// ObjectTypeList rather than an M2M. The item pattern is the REST spelling: lowercased
	// and unpunctuated, so `dcim.device` and never `dcim.Device`.
	//
	// Omit it to leave NetBox's own value alone; set it to `[]` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[a-z_]+\.[a-z0-9_]+$`
	// +optional
	ObjectTypes []string `json:"objectTypes,omitempty"`
}

// NetBoxTag is one extras.Tag in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Tags are
// catalogue-shaped, so the convention is that they live in a shared namespace and are
// referenced from team namespaces -- which makes a slug collision across namespaces a
// routine case rather than a curiosity.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbtag
// +kubebuilder:printcolumn:name="Slug",type=string,JSONPath=`.spec.slug`
// +kubebuilder:printcolumn:name="Color",type=string,JSONPath=`.spec.color`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxTag struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxTagSpec      `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec. One of the two methods that are
// the whole of the per-kind code the engine needs.
func (t *NetBoxTag) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxTag) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxTagList is a list of NetBoxTag.
// +kubebuilder:object:root=true
type NetBoxTagList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxTag `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxTag{}, &NetBoxTagList{})
}
