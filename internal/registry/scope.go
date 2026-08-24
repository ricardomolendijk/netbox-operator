package registry

import (
	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// The two column names of NetBox's polymorphic scope, spelled once.
const (
	// ScopeTypeField holds the target's `app_label.model` string.
	ScopeTypeField = "scope_type"

	// ScopeIDField holds the target's primary key.
	ScopeIDField = "scope_id"
)

// ScopeCacheColumns are the denormalised columns NetBox maintains from `(scope_type,
// scope_id)` on a CachedScopeMixin model, and which the operator must never write
// (docs/netbox-schema.md -> dcim.CachedScopeMixin).
//
// A function rather than a package-level slice so a caller cannot append to the one copy
// every scoped kind shares.
func ScopeCacheColumns() []string {
	return []string{"_region", "_site_group", "_site", "_location"}
}

// ScopeFK is the scope union as descriptor data: the pair, the object types NetBox permits
// in it, the four CR spec fields that write it, and which of those four cascade.
//
// Every scoped kind calls this rather than restating the union, so adding
// virtualization.Cluster or wireless.WirelessLAN is one line and cannot get the
// content-type spelling wrong. The spelling is the Django `model` attribute, lowercased and
// unpunctuated: `dcim.sitegroup`, never `dcim.SiteGroup`.
//
// A kind that has the pair and no caches -- ipam.VLANGroup declares `scope_type` /
// `scope_id` on the model itself -- clears Cached on the returned value; a kind that has
// them must also carry ScopeCacheColumns() in ReadOnly, which Validate enforces.
//
// `cascades` is supplied by the caller and cannot be defaulted, for the same reason Cached is
// cleared by the caller: the cascade is not a property of the union, it is a property of the
// *referring* kind per target. It is keyed on the member's spec field name and every member
// must appear -- a map covering three of the four is ErrMemberCascadePartial at boot rather
// than a fourth member that silently does not cascade, which is the failure mode that matters
// (#214): a member wrongly reading "no cascade" leaves the CR behind when NetBox deletes the
// row, and the engine's create-if-absent step then recreates it.
//
// Both mechanisms NetBox uses have to be read to fill this in, and they are in different
// places. A `GenericRelation` on the target model cascades -- `prefixes`, `vlan_groups`,
// `clusters` and `wireless_lans` on dcim.Region and dcim.SiteGroup -- and so does a
// denormalised `on_delete=CASCADE` column on the *referring* model: dcim.CachedScopeMixin
// declares `_site` and `_location` as CASCADE and `_region` and `_site_group` as SET_NULL,
// which is exactly why the two GenericRelations that look missing on dcim.Site and
// dcim.Location are not needed there (docs/netbox-schema.md). Reading only the first half is
// how virtualization.Cluster came to have no containment parent at all.
func ScopeFK(spec string, cascades map[string]bool) GenericFKSpec {
	members := []GenericFKMember{
		{Spec: "regionRef", Target: netboxv1alpha1.RegionRef{}.TargetGVK()},
		{Spec: "siteGroupRef", Target: netboxv1alpha1.SiteGroupRef{}.TargetGVK()},
		{Spec: "siteRef", Target: netboxv1alpha1.SiteRef{}.TargetGVK()},
		{Spec: "locationRef", Target: netboxv1alpha1.LocationRef{}.TargetGVK()},
	}

	for i, member := range members {
		// Left nil when the caller did not name the member, rather than defaulted to false:
		// "unstated" is what validateGenericFKMembers holds to all-or-none, and a false here
		// would make a forgotten member indistinguishable from a member that genuinely does
		// not cascade.
		if cascade, stated := cascades[member.Spec]; stated {
			members[i].CascadeOnDelete = &cascade
		}
	}

	return GenericFKSpec{
		TypeField: ScopeTypeField,
		IDField:   ScopeIDField,
		Spec:      spec,
		// Not derived from Members' targets, deliberately. This is the referring kind's
		// statement of what NetBox will accept in its own `scope_type`, and
		// Registry.Validate checks the two against each other -- which only means
		// something while they are stated independently.
		AllowedTypes: []string{"dcim.region", "dcim.sitegroup", "dcim.site", "dcim.location"},
		Members:      members,
		Cached:       ScopeCacheColumns(),
	}
}

// ScopeCascadesFromEvery is the cascade table for a scoped kind whose four scope members all
// cascade, which every scoped kind NetBox 4.6.8 ships happens to be -- by two different
// mechanisms, and per kind:
//
//   - ipam.Prefix: `prefixes GenericRelation` on all four, and `_site` / `_location` CASCADE
//     from dcim.CachedScopeMixin on top.
//   - ipam.VLANGroup: `vlan_groups GenericRelation` on all four, and no cached columns at
//     all -- the GenericRelation is the whole of its cascade.
//   - virtualization.Cluster, wireless.WirelessLAN: `clusters` / `wireless_lans
//     GenericRelation` on dcim.Region and dcim.SiteGroup, and `_site` / `_location` CASCADE
//     for the other two.
//
// Written out here rather than inlined per kind because the *value* is shared while the
// *reason* is not: each caller cites its own two mechanisms, and a kind that does not cascade
// from every member must not reach for this at all. It is not a default -- ScopeFK still
// takes the table as an argument, so a new scoped kind cannot acquire a cascade by omission.
func ScopeCascadesFromEvery() map[string]bool {
	return map[string]bool{
		"regionRef": true, "siteGroupRef": true, "siteRef": true, "locationRef": true,
	}
}
