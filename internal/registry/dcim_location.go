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
				// `site ForeignKey REQ -> dcim.Site on_delete=CASCADE`
				// (docs/netbox-schema.md -> dcim.Location).
				CascadeOnDelete: true,
			},
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.LocationRef{}.TargetGVK(),
				// `parent TreeForeignKey -> dcim.Location on_delete=CASCADE`, also a cascade.
				// Declared truthfully even though it is not the containment parent: the flag
				// is a fact about the column, and the tiebreak below is what reads it.
				CascadeOnDelete: true,
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

		// The only Kind so far with **two** cascading parents and one slot, so the cascade rule
		// of ADR-0003 rule 4 does not settle it on its own: `site` and `parent` are both
		// `on_delete=CASCADE`. The tiebreak, and why it lands on `siteRef` (#193, #198):
		//
		//  1. `site` is REQ and `parent` is not. A containment ref the spec may leave unset
		//     gives *no* owner reference to every top-level location -- the common shape --
		//     while `siteRef` is set on every location there can be, so it is the choice that
		//     protects every object of this Kind rather than a subset. It is also what
		//     ADR-0003 already asks for: the containment ref is normally the required FK.
		//  2. Deleting the site cascades to every location in it, nested ones included, so its
		//     rows are a strict superset of what deleting any one parent location takes. Owning
		//     by the site therefore covers the larger deletion, and `kubectl delete netboxsite`
		//     garbage-collects the whole location tree.
		//  3. The parent-deletion path is not left unguarded, which is what makes this a
		//     tiebreak rather than a trade. Every candidate below reads `parent_id` or pins it
		//     null, so a child whose `parentRef` no longer resolves has *no applicable
		//     candidate* at all: locate() returns errNoCandidate and the engine waits. The
		//     create-if-absent resurrection the rule exists to prevent is already unreachable
		//     on that path -- unlike dcim.SiteGroup and dcim.Region, whose parent is in their
		//     natural keys for the same reason, or tenancy.TenantGroup, whose is not.
		//
		// Switching to `parentRef` would trade all three away: top-level locations would carry
		// no owner reference, `kubectl delete netboxsite` would stop removing location CRs, and
		// the required FK -- the one that is always there -- would be the one with no cascade.
		ContainmentRef: "siteRef",

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does
		// not fail, it silently no-ops, so the next reconcile finds the same difference and
		// PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
