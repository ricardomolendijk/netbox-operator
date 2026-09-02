package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxModuleTypeProfileSpec describes one dcim.ModuleTypeProfile.
//
// The schema half of NetBox's module-attribute feature: a profile names a class of module --
// "SFP transceiver", "line card" -- and carries the JSON Schema that every
// `NetBoxModuleType.attributes` document claiming that profile is validated against, by
// NetBox and only by NetBox.
//
// The model is a plain `PrimaryModel` with two columns of its own
// (docs/netbox-schema.md -> dcim.ModuleTypeProfile), and its identity is the simplest in the
// module block: `name CharField REQ UNIQUE len=100`, a column-level unique with no
// `meta.constraints` line anywhere on the model. That is the dcim.Manufacturer derivation --
// see internal/registry/dcim_moduletypeprofile.go for why the committed IR reports no
// natural-key candidate for it and the key is hand-declared.
type NetBoxModuleTypeProfileSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the profile's name, and this kind's whole natural key
	// (docs/netbox-schema.md -> dcim.ModuleTypeProfile, `name CharField REQ UNIQUE len=100`).
	//
	// There is no `slug` on this model -- unusually for a NetBox catalogue kind -- so `name`
	// is both the display string and the lookup key, and NetBox's own `UNIQUE` is what makes
	// that safe.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Schema is the JSON Schema document NetBox validates a module type's `attributes`
	// against (docs/netbox-schema.md -> dcim.ModuleTypeProfile, `schema JSONField`).
	//
	// Opaque to the operator, deliberately. NetBox applies the schema server-side when a
	// dcim.ModuleType is written, and a client-side copy of that check would be a second
	// validator that drifts from the first: the operator sends the document and surfaces
	// NetBox's own field error verbatim.
	//
	// A pointer with `omitempty`, like every JSONDocument in the API, so the three states of
	// docs/concepts/field-ownership.md stay distinguishable: absent means "do not manage this
	// column", `{}` means an empty document, and `null` means the column's own null.
	// +optional
	Schema *JSONDocument `json:"schema,omitempty"`

	// Description is free text shown next to the profile.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the profile's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxModuleTypeProfile is one dcim.ModuleTypeProfile in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and a
// catalogue kind: a `profileRef` pointing from a team namespace into a shared catalogue
// namespace needs a NetBoxRefGrant there (docs/reference/netboxrefgrant.md).
//
// Absent deliberately:
//
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind
//     (hack/coverage-exclusions.yaml, `users/*`).
//   - `bookmarks`, `journal_entries` and `subscriptions` are GenericRelations: the reverse of
//     somebody else's foreign key, not columns.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbmoduletypeprofile
// +kubebuilder:printcolumn:name="Name",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxModuleTypeProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxModuleTypeProfileSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus          `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxModuleTypeProfile) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxModuleTypeProfile) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxModuleTypeProfileList is a list of NetBoxModuleTypeProfile.
// +kubebuilder:object:root=true
type NetBoxModuleTypeProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxModuleTypeProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxModuleTypeProfile{}, &NetBoxModuleTypeProfileList{})
}
