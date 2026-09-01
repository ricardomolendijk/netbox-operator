package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxConfigTemplateSpec describes one extras.ConfigTemplate: a Jinja2 template NetBox
// renders into a device or virtual-machine configuration.
//
// **Taggable but not custom-fieldable**, which is the one combination none of the kinds
// before it had. The bases are `RenderTemplateMixin, SyncedDataMixin, CustomLinksMixin,
// ExportTemplatesMixin, OwnerMixin, TagsMixin, ChangeLoggedModel`
// (docs/netbox-schema.md -> extras.ConfigTemplate) -- TagsMixin and no CustomFieldsMixin. So
// a NetBoxConfigTemplate carries half a provenance stamp: the tag, and no custom fields.
// That is a state docs/operations/provenance.md already covers, and the two flags being
// independent is why they are two flags.
//
// The `SyncedDataMixin` columns are absent for the reason they are absent from
// NetBoxExportTemplate: NetBox overwrites `template_code` from a `core.DataSource` itself, so
// declaring both would be two writers for one column.
type NetBoxConfigTemplateSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the template's name, and this kind's natural key.
	//
	// **Not unique in NetBox**: `name CharField REQ len=100` with no `unique=True` and no
	// `meta.constraints` (docs/netbox-schema.md -> extras.ConfigTemplate). Identity is a
	// convention rather than something the database enforces, so two templates sharing a name
	// make the lookup ambiguous and the object reports `Ready=False, Reason=Conflict` naming
	// both ids rather than picking one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// TemplateCode is the Jinja2 template body, rendered with the device or virtual machine
	// as context.
	//
	// Required by NetBox: `template_code TextField REQ` on RenderTemplateMixin with no
	// `blank=True`. Bounded at 128 KiB here for the reason
	// NetBoxExportTemplate.TemplateCode is -- a CR lives in etcd, whose object limit is about
	// 1.5 MiB, and a spec field with no bound is a CR that can be created and never updated.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=131072
	TemplateCode string `json:"templateCode"`

	// EnvironmentParams are extra keyword arguments for the Jinja2 `Environment`.
	//
	// `autoescape` is silently ignored on this kind: NetBox forces it off, because a config
	// template renders plain text and an escaping template would be a latent XSS sink if the
	// output were ever shown as HTML (`netbox/extras/models/configs.py:321-329`). Everything
	// else NetBox validates against Jinja's own constructor.
	//
	// Omit it to leave NetBox's own value alone; set it to `{}` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:pruning:PreserveUnknownFields
	// +kubebuilder:validation:Type=object
	// +optional
	EnvironmentParams *JSONDocument `json:"environmentParams,omitempty"`

	// Description is free text shown next to the template.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// MIMEType is the content type of the rendered output. Empty means NetBox's own default
	// of `text/plain; charset=utf-8` (`netbox/extras/constants.py:29`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	MIMEType string `json:"mimeType,omitempty"`

	// FileName is the base name given to the downloaded file.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	FileName string `json:"fileName,omitempty"`

	// FileExtension is appended to FileName.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=15
	// +optional
	FileExtension string `json:"fileExtension,omitempty"`

	// AsAttachment sends the rendered configuration as a download rather than rendering it in
	// the browser.
	// +kubebuilder:default=true
	// +optional
	AsAttachment *bool `json:"asAttachment,omitempty"`

	// Debug returns the full Python traceback when the template fails to render, instead of
	// a one-line message.
	//
	// NetBox's own help text says "not recommended for production use"
	// (`netbox/extras/models/configs.py:295-301`), and it means it: the traceback is returned
	// to whoever asked for the render.
	// +kubebuilder:default=false
	// +optional
	Debug *bool `json:"debug,omitempty"`
}

// NetBoxConfigTemplate is one extras.ConfigTemplate in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Nothing points at one yet. `dcim.Device.config_template` and
// `virtualization.VirtualMachine.config_template` are `ForeignKey -> extras.ConfigTemplate
// on_delete=PROTECT` (docs/netbox-schema.md), and neither is a field on its Kind's spec yet
// -- NetBoxVirtualMachine's descriptor says so in as many words. So this Kind ships with the
// `ConfigTemplateRef` alias and no user of it: the alias is where the target Kind is written
// down, and a reference added later is then a field on a spec rather than a second change to
// objectref.go.
//
// Deleting one destroys nothing and is refused by NetBox while a device or VM still uses it
// (`on_delete=PROTECT`), which arrives as `Deleting=False, Reason=Protected` and clears
// itself when the last user goes. No data-loss guard.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbct
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxConfigTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxConfigTemplateSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus       `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxConfigTemplate) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxConfigTemplate) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxConfigTemplateList is a list of NetBoxConfigTemplate.
// +kubebuilder:object:root=true
type NetBoxConfigTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxConfigTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxConfigTemplate{}, &NetBoxConfigTemplateList{})
}
