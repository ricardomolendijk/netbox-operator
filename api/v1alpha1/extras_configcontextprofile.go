package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxConfigContextProfileSpec describes one extras.ConfigContextProfile: a JSON Schema
// that config contexts are validated against.
//
// The smallest kind in `extras` and the second whose subject is NetBox's *schema* rather than
// its data.
//
// **Taggable but not custom-fieldable**, and it is the one kind in the catalogue where that
// combination does not come from the mixins. `extras.ConfigContextProfile` is a
// `PrimaryModel`, which mixes in both `TagsMixin` and `CustomFieldsMixin`
// (docs/netbox-schema.md -> extras.ConfigContextProfile, bases) -- but the REST serializer's
// write path carries `tags` and no `custom_fields` at all. That is the API-versus-AST gap
// `docs/regenerating.md` says to expect on the sixteen models that shadow an inherited
// column, and this is one of them. So the profile carries half a provenance stamp: the tag,
// and no custom fields, because a `custom_fields` key on this endpoint is one NetBox ignores
// rather than rejects.
//
// The `SyncedDataMixin` columns (`data_source`, `data_file`, `data_path`,
// `auto_sync_enabled`) are absent for the reason they are absent from every template kind:
// NetBox overwrites `schema` from a `core.DataSource` itself, so declaring both would be two
// writers for one column.
type NetBoxConfigContextProfileSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the profile's name, and this kind's natural key.
	//
	// `name CharField REQ UNIQUE len=100` (docs/netbox-schema.md ->
	// extras.ConfigContextProfile), so unlike the template kinds in this app the database
	// enforces the identity and two profiles of one name cannot exist to be ambiguous
	// about.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// Schema is the JSON Schema document a config context's `data` is validated against.
	//
	// NetBox validates the document itself when it is set and rejects one that is not a
	// legal JSON Schema, so the operator is a pipe rather than a second validator
	// (`schema JSONField` with `blank=True, null=True`).
	//
	// Omit it to leave NetBox's own value alone; set it to `{}` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
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

	// Comments is the long-form note NetBox renders as Markdown.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=8192
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxConfigContextProfile is one extras.ConfigContextProfile in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Deleting one destroys nothing: `ConfigContext.profile` is `on_delete=PROTECT`
// (docs/netbox-schema.md), so NetBox refuses while a config context still names it and that
// arrives as `Deleting=False, Reason=Protected`, clearing itself when the last context stops
// pointing at it. No data-loss guard -- a profile only validates, it stores nothing.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbccp
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxConfigContextProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxConfigContextProfileSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus             `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (p *NetBoxConfigContextProfile) NetBoxSpec() *NetBoxObjectSpec {
	return &p.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (p *NetBoxConfigContextProfile) NetBoxStatus() *NetBoxObjectStatus { return &p.Status }

// NetBoxConfigContextProfileList is a list of NetBoxConfigContextProfile.
// +kubebuilder:object:root=true
type NetBoxConfigContextProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxConfigContextProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxConfigContextProfile{}, &NetBoxConfigContextProfileList{})
}
