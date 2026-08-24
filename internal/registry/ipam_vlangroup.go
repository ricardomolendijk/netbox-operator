package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamVLANGroupDescriptor()) }

// ipamVLANGroupDescriptor is ipam.VLANGroup as data.
//
// The kind whose **identity includes a polymorphic pair**, which is the case NBO-018 left
// open and #180 describes: `meta.constraints` on this model is
// `UniqueConstraint(fields=('scope_type', 'scope_id', 'name'), name='…unique_scope_name')`
// and `UniqueConstraint(fields=('scope_type', 'scope_id', 'slug'), name='…unique_scope_slug')`
// (docs/netbox-schema.md -> ipam.VLANGroup), so the natural key is two scope columns plus a
// slug and there is no single value the union's own spec field could offer a filter.
// applyGenericFK therefore writes the resolved pair into the decoded spec under the *column*
// names, and the candidate below matches on those -- see docs/concepts/generic-refs.md,
// "Natural keys".
//
// It is also the scoped kind with **no cached columns**. ipam.Prefix gets `scope_type` /
// `scope_id` from dcim.CachedScopeMixin, which brings `_region`, `_site_group`, `_site` and
// `_location` with it; ipam.VLANGroup declares the two columns on the model itself and has
// none of the four, so Cached is cleared below rather than the union being restated.
func ipamVLANGroupDescriptor() Descriptor {
	scope := ScopeFK("scope")
	// The scope genuinely cascades, so it is a legal containment parent (NBO-193): every one
	// of the four scope targets declares a `vlan_groups` GenericRelation -- dcim.Region,
	// dcim.SiteGroup, dcim.Site and dcim.Location (docs/netbox-schema.md) -- so deleting any
	// of them takes its VLAN groups with it. ScopeFK cannot default this: `clusters` and
	// `wireless_lans` exist on only two of the four, so a union's cascade is a fact about the
	// referring model rather than about the union.
	scope.CascadeOnDelete = true

	// The one difference from every other scoped kind, and the reason it is a mutation of the
	// shared union rather than a copy of it: the members, the four permitted `app_label.model`
	// strings and the spelling all stay in internal/registry/scope.go, and only the fact that
	// *this* model has no denormalised caches is stated here. Validate requires every Cached
	// column to appear in ReadOnly, so leaving the list in place would mean declaring four
	// columns this table does not have.
	scope.Cached = nil

	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVLANGroup"),
		Endpoint:   "ipam/vlan-groups",
		ObjectType: "ipam.vlangroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.VLANGroup is an OrganizationalModel (docs/netbox-schema.md -> ipam.VLANGroup,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		// `scope` is absent from this table on purpose: one spec field writing two columns is
		// a GenericFKSpec, not a Field.
		//
		// `vidRanges` is ClassArray rather than ClassRefMany even though both arrive as JSON
		// lists. NetBox stores `vid_ranges` as a Postgres ArrayField and returns it in stored
		// order, so the order is data: compared order-independently, `[[1,100],[200,300]]`
		// and `[[200,300],[1,100]]` would look equal while NetBox holds two different values.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "vidRanges", API: "vid_ranges", Class: ClassArray},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
		},

		// Two candidates, and unusually for an OrganizationalModel **neither of them is
		// `slug` on its own**. `slug` carries no UNIQUE on this model (docs/netbox-schema.md
		// -> ipam.VLANGroup, `slug SlugField REQ len=100` with no UNIQUE), so two VLAN groups
		// may share one as long as their scopes differ -- which extras.Tag, dcim.Site and
		// tenancy.TenantGroup all forbid. A `slug`-only key here would make every scoped
		// group adopt an unrelated group of the same slug in a different scope and then PATCH
		// somebody else's row into this scope.
		//
		// Candidate 1 matches on the pair by *column name*, which is what
		// reconciler.applyGenericFK writes into the decoded spec once the union resolves. It
		// is `scope_type` and `scope_id` and not a per-target filter (`?site=3`) because the
		// pair is one reference: VLANGroupFilterSet accepts both --
		// `scope_type = MultiValueContentTypeFilter()` and `scope_id` in `Meta.fields`
		// (NetBox 4.6.8, netbox/ipam/filtersets.py:948 and :980) -- while the per-target
		// filters are eight separate names that would put the union's dispatch table in the
		// natural key as well.
		//
		// Candidate 2 is the same constraint with both halves null, and the pin is the whole
		// point of it. Postgres treats NULLs as distinct, so with both scope columns null
		// *neither* unique constraint fires and two globally-scoped groups can legitimately
		// share a slug -- more than one match is therefore a real server state and is
		// reported as a Conflict rather than resolved by taking the first. Omitting the pin
		// instead would be worse than non-unique: `?slug=mgmt` alone matches every scoped
		// group with that slug too, so a global group would adopt a site's group
		// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
		//
		// One pin, not two, even though candidate 1 matches both columns. `scope_id` is
		// `PositiveBigIntegerField` (docs/netbox-schema.md -> ipam.VLANGroup) and NetBox
		// registers a null filter for it -- `?scope_id__empty=true`. It registers none for
		// `scope_type`, whose filter is MultiValueContentTypeFilter, and asking anyway is
		// not merely useless but actively wrong: the sentinel makes the filter
		// `scope_type__in=[]`, which matches nothing at all, so the engine would conclude
		// the group does not exist and create a second one (see knownNullColumns for the
		// NetBox lines). Pinning the id half alone loses nothing, because NetBox refuses one
		// half of the pair without the other -- `Cannot set scope_type without scope_id` and
		// its converse, netbox/ipam/models/vlans.py:105-109 -- so `scope_id IS NULL` is
		// exactly the set of groups with no scope.
		//
		// The order is not a fallback. Applicable keeps the two apart in both directions: a
		// group whose scope is declared but has not resolved yet matches neither candidate --
		// candidate 1 needs the columns resolved, candidate 2 needs `scope` never declared --
		// so the engine waits instead of adopting the global group of the same slug and then
		// PATCHing a scope onto it.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: ScopeTypeField, Spec: ScopeTypeField},
					{Filter: ScopeIDField, Spec: ScopeIDField},
					{Filter: "slug", Spec: "slug"},
				},
			},
			{
				Fields: []KeyField{{Filter: "slug", Spec: "slug"}},
				NullFields: []NullField{
					{Filter: ScopeIDField, Spec: "scope", Column: NullColumnNumeric},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The scope union, in one line -- the same line ipam.Prefix uses, with Cached cleared
		// above. ScopeFK carries the pair's column names, the four object types NetBox permits
		// in it and the four CR spec fields that select them, so this kind cannot get the
		// `dcim.sitegroup` spelling wrong.
		GenericFKs: []GenericFKSpec{scope},

		// The four columns every ChangeLoggedModel carries, plus one counter.
		//
		// `total_vlan_ids` is `PositiveBigIntegerField def=UNRESOLVED:VLAN_VID_MAX -
		// VLAN_VID_MIN + 1` (docs/netbox-schema.md -> ipam.VLANGroup): NetBox maintains it
		// from `vid_ranges`, so writing it does not fail, it silently no-ops, and the next
		// reconcile finds the same difference and PATCHes again forever.
		//
		// No `_`-prefixed columns and no ScopeCacheColumns(), unlike ipam.Prefix -- the
		// difference the cleared Cached above records.
		ReadOnly: []string{"created", "last_updated", "url", "display", "total_vlan_ids"},

		// docs/decisions/0003-ownership-and-references.md rule 4 names `scopeRef` as a
		// containment parent, so deleting the NetBoxSite a group is scoped to takes the group
		// with it -- when that is legal, meaning same namespace. A catalogue-shaped group in a
		// shared namespace gets no owner reference and reports CascadeUnavailable naming
		// `scope`. Exactly one, because Kubernetes garbage collection waits for *every* owner:
		// adding `tenantRef` as a second would turn "delete the site and the group goes" into
		// "delete both", silently.
		ContainmentRef: "scope",
	}
}
