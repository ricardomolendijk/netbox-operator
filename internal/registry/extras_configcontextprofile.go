package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

func init() { MustRegister(extrasConfigContextProfileDescriptor()) }

// extrasConfigContextProfileDescriptor is extras.ConfigContextProfile as data.
//
// Taggable and not CustomFieldable, and the reason is exactly the API-versus-AST gap
// docs/regenerating.md warns about. The digest has this model as a `PrimaryModel`, which mixes
// in both `TagsMixin` and `CustomFieldsMixin` -- so from the AST it should carry a whole
// provenance stamp. The REST serializer disagrees: its write path is `name, description,
// schema, tags, owner, comments, data_*, created, last_updated` with **no `custom_fields`**
// (hack/testdata/ir-4.6.8.json.gz). A `custom_fields` key sent to
// `extras/config-context-profiles` is a key NetBox ignores rather than rejects, so declaring
// CustomFieldable here would write a stamp that silently goes nowhere and report success.
// The flag follows the API.
func extrasConfigContextProfileDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxConfigContextProfile"),
		Endpoint:   "extras/config-context-profiles",
		ObjectType: "extras.configcontextprofile",
		Scope:      apiextensionsv1.NamespaceScoped,

		Taggable:        true,
		CustomFieldable: false,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "schema", API: "schema", Class: ClassJSON},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// `name CharField REQ UNIQUE len=100` (docs/netbox-schema.md ->
		// extras.ConfigContextProfile), so one candidate is the whole identity and it cannot
		// be ambiguous -- unlike the template kinds in this app, whose `name` carries no
		// UNIQUE at all.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		ReadOnly: []string{"created", "last_updated", "url", "display", "data_synced"},
	}
}
