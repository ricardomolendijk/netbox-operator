package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamServiceTemplateDescriptor()) }

// ipamServiceTemplateDescriptor is ipam.ServiceTemplate as data.
//
// ipam.Service minus the parent and the addresses, and the pair is worth reading side by
// side: the same `protocol` and `ports` columns from the same abstract base
// (`ipam.ServiceBase`), and an identity of the opposite kind. `name CharField REQ UNIQUE
// len=100` here is a database guarantee, where ipam.Service has no meta.constraints at all
// and a convention (docs/netbox-schema.md).
//
// NetBox keeps no link from a service back to the template it was stamped from -- the values
// are copied at creation time -- so this Kind has no reverse relation and editing a template
// changes nothing that already exists.
func ipamServiceTemplateDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxServiceTemplate"),
		Endpoint:   "ipam/service-templates",
		ObjectType: "ipam.servicetemplate",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.ServiceTemplate is a PrimaryModel (docs/netbox-schema.md ->
		// ipam.ServiceTemplate, bases: ServiceBase, PrimaryModel), which mixes in both
		// TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain, and the whole app takes the same default
		// rather than a per-kind judgement about how expensive each row is to lose.
		RetainOnDelete: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "protocol", API: "protocol"},
			{Spec: "ports", API: "ports", Class: ClassArray},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, from the column-level `name CharField REQ UNIQUE len=100`
		// (docs/netbox-schema.md -> ipam.ServiceTemplate). The table's only other line is
		// `meta.ordering: ('name',)`.
		//
		// LookupExact rather than LookupIExact, and the distinction is load-bearing: the
		// constraint is a plain `unique=True` on the column, not a UniqueConstraint over
		// `Lower('name')` the way dcim.Device's and virtualization.VirtualMachine's are, so
		// `SSH` and `ssh` are two legal rows and adopting one for the other would PATCH
		// somebody else's template.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus the ports cache NetBox
		// recomputes from `ports` on save (netbox/ipam/models/services.py:41-47).
		ReadOnly: []string{"created", "last_updated", "url", "display", "_ports_lowest"},

		// No ContainmentRef. ipam.ServiceTemplate declares no foreign key besides
		// `owner (OwnerMixin)`, which the operator does not map, so there is no FK the server
		// cascades (docs/decisions/0003-ownership-and-references.md rule 4).
	}
}
