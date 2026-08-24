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
// in it, and the four CR spec fields that write it.
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
// CascadeOnDelete is deliberately left false here and set by the caller, for the same reason
// Cached is cleared by the caller: it is not a property of the union, it is a property of the
// *referring* kind. The scope cascade is a GenericRelation on each target model, and which
// ones exist differs per relation -- `prefixes` and `vlan_groups` are declared on all four of
// dcim.Region, dcim.SiteGroup, dcim.Site and dcim.Location, while `clusters` and
// `wireless_lans` are declared only on the first two (docs/netbox-schema.md). So ipam.Prefix
// may set it and virtualization.Cluster may not, and defaulting it either way would be wrong
// for half the callers. One flag per pair also cannot express a union whose members disagree;
// such a kind gets no containment parent at all.
func ScopeFK(spec string) GenericFKSpec {
	return GenericFKSpec{
		TypeField: ScopeTypeField,
		IDField:   ScopeIDField,
		Spec:      spec,
		// Not derived from Members' targets, deliberately. This is the referring kind's
		// statement of what NetBox will accept in its own `scope_type`, and
		// Registry.Validate checks the two against each other -- which only means
		// something while they are stated independently.
		AllowedTypes: []string{"dcim.region", "dcim.sitegroup", "dcim.site", "dcim.location"},
		Members: []GenericFKMember{
			{Spec: "regionRef", Target: netboxv1alpha1.RegionRef{}.TargetGVK()},
			{Spec: "siteGroupRef", Target: netboxv1alpha1.SiteGroupRef{}.TargetGVK()},
			{Spec: "siteRef", Target: netboxv1alpha1.SiteRef{}.TargetGVK()},
			{Spec: "locationRef", Target: netboxv1alpha1.LocationRef{}.TargetGVK()},
		},
		Cached: ScopeCacheColumns(),
	}
}
