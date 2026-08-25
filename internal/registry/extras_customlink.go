package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasCustomLinkDescriptor()) }

// extrasCustomLinkDescriptor is extras.CustomLink as data.
//
// The same shape as extras.Tag -- one unique scalar key, no foreign keys, one content-type
// list -- and it is here to be a NetBox *UI* object rather than a network one, which is the
// whole of what this app is for.
func extrasCustomLinkDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCustomLink"),
		Endpoint:   "extras/custom-links",
		ObjectType: "extras.customlink",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Bases are `CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel`
		// (docs/netbox-schema.md -> extras.CustomLink): neither mixin, stated rather than
		// omitted, because NetBox ignores a column it does not know rather than rejecting it
		// -- so a wrongly-declared `tags` here would vanish on write and be PATCHed forever.
		Taggable:        false,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			// `linkText` -> `link_text` and `linkUrl` -> `link_url` are the entries that earn
			// the explicit table: no camelCase convention gets `linkUrl` to `link_url` and
			// `buttonClass` to `button_class` without an acronym list, and NetBox ignores a
			// field name it does not know rather than rejecting it -- so a wrong one writes
			// nothing and reports success.
			{Spec: "linkText", API: "link_text"},
			{Spec: "linkUrl", API: "link_url"},
			{Spec: "groupName", API: "group_name"},
			{Spec: "buttonClass", API: "button_class"},
			{Spec: "enabled", API: "enabled"},
			{Spec: "newWindow", API: "new_window"},
			{Spec: "weight", API: "weight"},
			// A ManyToManyField onto contenttypes.ContentType, so `app_label.model` strings
			// rather than object ids, compared as an order-independent string set and resolved
			// against nothing.
			{Spec: "objectTypes", API: "object_types", Class: ClassObjectTypeList},
		},

		// `name` is column-unique (docs/netbox-schema.md -> extras.CustomLink, `name CharField
		// REQ UNIQUE len=100`), so one candidate identifies at most one link.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
