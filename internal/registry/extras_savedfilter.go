package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasSavedFilterDescriptor()) }

// extrasSavedFilterDescriptor is extras.SavedFilter as data.
//
// The first kind with two independently-unique columns, which is why it has two natural-key
// candidates for a reason other than a conditional constraint -- see NaturalKeys below.
func extrasSavedFilterDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxSavedFilter"),
		Endpoint:   "extras/saved-filters",
		ObjectType: "extras.savedfilter",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Bases are `CloningMixin, ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel`
		// (docs/netbox-schema.md -> extras.SavedFilter): neither mixin.
		Taggable:        false,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "enabled", API: "enabled"},
			{Spec: "shared", API: "shared"},
			{Spec: "weight", API: "weight"},
			{Spec: "objectTypes", API: "object_types", Class: ClassObjectTypeList},
			// A JSONField, so a whole document: `{"status": ["active"]}`. ClassJSON rather than
			// ClassValue because the scalar comparison unwraps a JSON object carrying an `id`
			// key -- and `{"id": ["3"]}` is an ordinary NetBox filter, so a saved filter on an
			// id would be compared as that id against the whole document and PATCHed forever.
			{Spec: "parameters", API: "parameters", Class: ClassJSON},
		},

		// Two candidates, and the second is not a fallback for an unset field -- both columns
		// are required, so both candidates are always applicable. It is a fallback for a
		// *changed* one: `slug` first, so the usual case is one filtered lookup; `name` second,
		// so that editing `slug` in Git finds nothing under the new value, falls through, adopts
		// the existing filter and PATCHes the slug. That is a rename. Without the second
		// candidate the engine would try to create a new filter and NetBox would refuse it on
		// the unique `name`, leaving the object stuck at Reason=Invalid forever
		// (docs/netbox-schema.md -> extras.SavedFilter: `name` and `slug` are both REQ UNIQUE).
		NaturalKeys: []NaturalKey{
			{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}},
			{Fields: []KeyField{{Filter: "name", Spec: "name"}}},
		},

		UpdateStrategy: UpdatePatch,

		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
