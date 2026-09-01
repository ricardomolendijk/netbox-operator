package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxExportTemplateSpec describes one extras.ExportTemplate: a Jinja2 template NetBox
// offers as an export format on a list view.
//
// Neither taggable nor custom-fieldable: the bases are `SyncedDataMixin, CloningMixin,
// ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel, RenderTemplateMixin`
// (docs/netbox-schema.md -> extras.ExportTemplate).
//
// The `SyncedDataMixin` columns -- `dataSource`, `dataFile`, `dataPath`, `autoSyncEnabled`,
// `dataSynced` -- are deliberately absent from every template kind here. They are NetBox's
// own git-sync mechanism: NetBox pulls the template body out of a `core.DataSource` and
// overwrites `template_code` itself, so a CR that declared both would be fighting NetBox for
// the same column. Declare the body here or sync it there, not both.
type NetBoxExportTemplateSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// Name is the template's name, and this kind's natural key.
	//
	// **Not unique in NetBox.** `extras.ExportTemplate` declares `name CharField REQ len=100`
	// with no `unique=True` and no `meta.constraints` at all (docs/netbox-schema.md ->
	// extras.ExportTemplate) -- so identity here is a convention rather than something the
	// database enforces, as it is for ipam.Prefix. If NetBox holds two templates called
	// `csv`, the lookup is ambiguous and the object reports `Ready=False, Reason=Conflict`
	// naming both ids rather than picking one.
	//
	// `table` is a reserved name NetBox refuses, case-insensitively
	// (`netbox/extras/models/models.py:498-503`). Not enforced here: NetBox's own message
	// says it better than a CEL rule would, and it is one of several `clean()` rules on this
	// model -- a schema that enforced one would invite the reader to believe it enforced all
	// of them.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// ObjectTypes are the NetBox models this template exports, as Django ContentType
	// strings.
	//
	// Required by NetBox: no `required=False` on the serializer
	// (`netbox/extras/api/serializers_/exporttemplates.py:14-17`). Not references -- the
	// values are `app_label.model` strings, so the descriptor declares this an ObjectTypeList
	// and the resolver never sees it. NetBox scopes the queryset to
	// `ObjectType.objects.with_feature('export_templates')`, so a type that cannot be
	// exported is rejected there rather than here.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=256
	// +kubebuilder:validation:items:MaxLength=100
	// +kubebuilder:validation:items:Pattern=`^[a-z_]+\.[a-z0-9_]+$`
	ObjectTypes []string `json:"objectTypes"`

	// TemplateCode is the Jinja2 template body, rendered with the exported queryset as
	// context.
	//
	// Required, and required by NetBox: `template_code TextField REQ` on RenderTemplateMixin
	// with no `blank=True` (docs/netbox-schema.md -> extras.RenderTemplateMixin).
	//
	// Bounded at 128 KiB here where NetBox's column is unbounded. A CR is stored in etcd,
	// which has a hard object limit of about 1.5 MiB, and an unbounded text field in a spec
	// is a CR that can be created and then never updated. A template larger than this is one
	// NetBox's own git sync should be pulling in.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=131072
	TemplateCode string `json:"templateCode"`

	// EnvironmentParams are extra keyword arguments for the Jinja2 `Environment`, as a JSON
	// object: `{"trim_blocks": true}`.
	//
	// NetBox validates the keys against Jinja's own constructor
	// (`netbox/extras/models/mixins.py:143-160`), so an unknown one arrives as
	// `Reason=Invalid` naming it.
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

	// AsAttachment sends the rendered output as a download rather than rendering it in the
	// browser.
	//
	// A pointer with an explicit default rather than a plain bool: `omitempty` on a plain
	// bool drops a deliberate `false` out of the payload, so the operator could never turn it
	// off again.
	// +kubebuilder:default=true
	// +optional
	AsAttachment *bool `json:"asAttachment,omitempty"`
}

// NetBoxExportTemplate is one extras.ExportTemplate in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md). Presentation
// rather than data: nothing in NetBox depends on one, so deleting it destroys nothing and
// there is no data-loss guard.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbet
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxExportTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxExportTemplateSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus       `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxExportTemplate) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxExportTemplate) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxExportTemplateList is a list of NetBoxExportTemplate.
// +kubebuilder:object:root=true
type NetBoxExportTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxExportTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxExportTemplate{}, &NetBoxExportTemplateList{})
}
