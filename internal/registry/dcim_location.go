package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimLocationDescriptor()) }

// dcimLocationDescriptor is dcim.Location as data.
//
// The nested-group kind whose identity is a pair of references rather than one. Both
// candidates below start at `site`, because every constraint NetBox declares on the model
// does (docs/netbox-schema.md -> dcim.Location.meta.constraints): a location's name is
// unique within a site, never globally. So a location whose `siteRef` has not resolved has no
// applicable candidate at all and the engine waits, which is the same protection dcim.Region
// gets from its `parent` and for the same reason -- a lookup with `site_id` merely left out
// would match a location of that name in somebody else's site and adopt it.
//
// It is the other half of what NBO-018's scope union was missing, and the only Kind so far
// that is both a scope target and the holder of a required containment reference.
func dcimLocationDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxLocation"),
		Endpoint:   "dcim/locations",
		ObjectType: "dcim.location",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Location is a NestedGroupModel (docs/netbox-schema.md -> dcim.Location,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `status` needs no field class: NetBox returns a choice as {"value","label"} and
		// takes the bare value, which internal/netbox/drift.go's unwrapNested already
		// reduces by the absence of an "id" key. dcim.Site proved that; it is restated
		// nowhere and declared nowhere, which is the whole claim.
		//
		// `tenant` is absent deliberately. dcim.Location has the foreign key, but
		// NetBoxTenant does not exist yet (NBO-021), and a field the CRD accepts and the
		// payload drops reports success while writing nothing.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "status", API: "status"},
			{Spec: "facility", API: "facility"},
			{Spec: "description", API: "description"},
			{
				Spec: "siteRef", API: "site", Class: ClassRefOne,
				Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
			},
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.LocationRef{}.TargetGVK(),
			},
		},

		// Two candidates, in this order, from
		// docs/netbox-schema.md -> dcim.Location.meta.constraints: unique on
		// (site, parent, name), plus a separate unique on (site, name) conditioned on
		// parent IS NULL.
		//
		// Not a fallback chain: a nested location is identified by the first and a
		// site-top-level one by the second, and the null pin on the second is what keeps a
		// child whose parent does not exist yet from adopting an unrelated top-level
		// location in the same site (NBO-015).
		//
		// NetBox also constrains the slug pair-wise, `(site, parent, slug)` and
		// `(site, slug) WHERE parent IS NULL`. Those are not candidates for the same reason
		// dcim.Region's are not: a kind gets one identity, and `name` is the one the
		// constraints lead with.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "site_id", Spec: "siteRef"},
					{Filter: "parent_id", Spec: "parentRef"},
					{Filter: "name", Spec: "name"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "site_id", Spec: "siteRef"},
					{Filter: "name", Spec: "name"},
				},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef"}},
			},
		},

		UpdateStrategy: UpdatePatch,

		// `site` and not `parentRef`: NetBox deletes a site's locations with it
		// (`site ForeignKey REQ on_delete=CASCADE`), so the site is the containment parent
		// and deleting the NetBoxSite should cascade to the CRs the same way. Exactly one
		// field may carry it, because Kubernetes garbage collection waits for every owner
		// reference (docs/decisions/0003-ownership-and-references.md rule 4) -- `parent` is
		// also CASCADE in NetBox and still gets none.
		//
		// Declared here and not yet acted on: nothing in this build writes an owner
		// reference, so no cascade happens today. Stating it is still the right move -- the
		// mechanism reads this field, and a kind that had to be revisited to declare its
		// containment parent later is exactly the per-kind edit a Descriptor exists to
		// avoid.
		ContainmentRef: "siteRef",

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does
		// not fail, it silently no-ops, so the next reconcile finds the same difference and
		// PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
