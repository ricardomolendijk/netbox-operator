package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasExportTemplateDescriptor()) }

// extrasExportTemplateDescriptor is extras.ExportTemplate as data.
//
// The SyncedDataMixin columns are absent from Fields and from ReadOnly alike, and the two
// omissions mean different things. They are not read-only -- NetBox accepts a write to
// `data_source` -- they are simply not this operator's to write: NetBox pulls
// `template_code` out of the data source itself, so a CR declaring both would be the second
// writer of that column. `data_synced` *is* read-only and is listed.
func extrasExportTemplateDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxExportTemplate"),
		Endpoint:   "extras/export-templates",
		ObjectType: "extras.exporttemplate",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Bases are `SyncedDataMixin, CloningMixin, ExportTemplatesMixin, OwnerMixin,
		// ChangeLoggedModel, RenderTemplateMixin` (docs/netbox-schema.md ->
		// extras.ExportTemplate): neither TagsMixin nor CustomFieldsMixin. Note that
		// `ExportTemplatesMixin` is what makes a model *exportable*, not what makes it an
		// export template -- an easy pair of names to read the wrong way round.
		Taggable:        false,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			// The RenderTemplateMixin columns. `templateCode` -> `template_code`,
			// `mimeType` -> `mime_type`, `fileName` -> `file_name`,
			// `fileExtension` -> `file_extension` and `asAttachment` -> `as_attachment` are
			// five entries a camelCase convention would get wrong in five different ways.
			{Spec: "templateCode", API: "template_code"},
			{Spec: "mimeType", API: "mime_type"},
			{Spec: "fileName", API: "file_name"},
			{Spec: "fileExtension", API: "file_extension"},
			{Spec: "asAttachment", API: "as_attachment"},
			{Spec: "environmentParams", API: "environment_params", Class: ClassJSON},
			{Spec: "objectTypes", API: "object_types", Class: ClassObjectTypeList},
		},

		// One candidate on a column NetBox does *not* declare unique: `name CharField REQ
		// len=100` with no `unique=True` and no meta.constraints (docs/netbox-schema.md ->
		// extras.ExportTemplate). So identity here is a convention, as it is for ipam.Prefix,
		// and two templates sharing a name make the lookup ambiguous -- which the client
		// reports as an *AmbiguousError naming every match, and the engine as
		// Ready=False, Reason=Conflict. That is the right outcome: guessing which of two
		// templates a CR meant would overwrite somebody's export format.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// `data_synced` is NetBox's record of when it last pulled the template from its data
		// source. Listed so that a later addition cannot map a spec field onto it: writing it
		// would not fail, it would silently no-op and PATCH forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "data_synced"},
	}
}
