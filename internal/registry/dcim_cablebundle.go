package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimCableBundleDescriptor()) }

// dcimCableBundleDescriptor is dcim.CableBundle as data.
//
// The plainest PrimaryModel in the catalogue: one column of its own,
// `name CharField REQ UNIQUE len=100` (docs/netbox-schema.md -> dcim.CableBundle), plus the
// base class's `description` and `comments`.
//
// It is the first kind whose natural key is `name` on the strength of a **column-level
// UNIQUE**. tenancy.ContactGroup keys on `name` because its base class gives it no `slug`, and
// tenancy.Contact keys on `name` with nothing but an index behind it -- so two contacts of one
// name is legal server state and a Conflict (docs/reference/netboxcontact.md). Here the
// database refuses the second row outright, which makes the key as strong as any
// OrganizationalModel's `slug`.
//
// The model exists, which is worth stating because NBO-049's issue text asks for it without
// evidence: `dcim.CableBundle` is in docs/netbox-schema.md, its endpoint `dcim/cable-bundles`
// is in the endpoint map, and `CableBundleSerializer` is at
// netbox/dcim/api/serializers_/cables.py:28.
func dcimCableBundleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCableBundle"),
		Endpoint:   "dcim/cable-bundles",
		ObjectType: "dcim.cablebundle",
		Scope:      apiextensionsv1.NamespaceScoped,

		// A PrimaryModel mixes in TagsMixin and CustomFieldsMixin
		// (docs/netbox-schema.md -> dcim.CableBundle, bases), so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate. `name` is registered on CableBundleFilterSet as a
		// MultiValueCharFilter from `Meta.fields` (netbox/dcim/filtersets.py:2620) -- worth
		// checking rather than assuming, because django-filter drops a parameter it does not
		// recognise and answers with the *unfiltered* set, so a guessed filter name is a
		// lookup that matches everything (#206).
		//
		// No second candidate and no null pin: the column is `REQ UNIQUE`, so there is no
		// state in which it is absent and nothing weaker to fall back to.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. `cable_count` is a
		// RelatedObjectCountField on the serializer -- returned, never accepted -- and is not
		// listed for the reason virtualization.ClusterType's is not: this list guards the
		// field map, and no spec field maps onto it.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
