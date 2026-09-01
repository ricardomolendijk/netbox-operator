package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasConfigTemplateDescriptor()) }

// extrasConfigTemplateDescriptor is extras.ConfigTemplate as data.
//
// The first descriptor to declare Taggable without CustomFieldable, which is the case the two
// flags exist separately for: `TagsMixin` and no `CustomFieldsMixin`
// (docs/netbox-schema.md -> extras.ConfigTemplate, bases). So the engine stamps this kind's
// objects with the provenance tag and writes no custom fields onto them, and neither half of
// that is a branch anywhere -- it is these two booleans.
func extrasConfigTemplateDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxConfigTemplate"),
		Endpoint:   "extras/config-templates",
		ObjectType: "extras.configtemplate",
		Scope:      apiextensionsv1.NamespaceScoped,

		// TagsMixin is in the bases, so `tags` is a writable column and the stamp's tag is
		// written. CustomFieldsMixin is not, so `custom_fields` is not a column here at all --
		// which also keeps this kind out of the `object_types` list the provenance bootstrap
		// derives, where a kind that does not carry the container has no business being.
		Taggable:        true,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			{Spec: "debug", API: "debug"},
			{Spec: "templateCode", API: "template_code"},
			{Spec: "mimeType", API: "mime_type"},
			{Spec: "fileName", API: "file_name"},
			{Spec: "fileExtension", API: "file_extension"},
			{Spec: "asAttachment", API: "as_attachment"},
			{Spec: "environmentParams", API: "environment_params", Class: ClassJSON},
		},

		// `name` is not unique on this model either -- `name CharField REQ len=100`, no
		// `unique=True`, no meta.constraints (docs/netbox-schema.md -> extras.ConfigTemplate).
		// Two templates sharing a name are an ambiguous lookup and a Conflict, for the reason
		// extras.ExportTemplate's are.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		ReadOnly: []string{"created", "last_updated", "url", "display", "data_synced"},
	}
}
