package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(virtualizationClusterDescriptor()) }

// virtualizationClusterDescriptor is virtualization.Cluster as data.
//
// The second CachedScopeMixin kind after ipam.Prefix, and the one netbox-populator gets
// wrong: `../reconcile.go:270` writes `"site": siteID` to `virtualization/clusters`. NetBox's
// ClusterSerializer has no `site` member at all (v4.6.8,
// virtualization/api/serializers_/clusters.py), and DRF drops a key it does not know rather
// than rejecting it -- so that write returns 201, creates an unscoped cluster, and never
// drifts. There is no `site` in Fields and no `siteRef` on the CRD, because since NetBox 4.2
// there is no such column (docs/concepts/generic-refs.md#the-failure-this-prevents).
//
// Nothing below is per-kind engine code, which is the claim NBO-028 tests: three foreign keys,
// a choice column, the shared scope union and a two-candidate natural key, all data.
func virtualizationClusterDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxCluster"),
		Endpoint:   "virtualization/clusters",
		ObjectType: "virtualization.cluster",
		Scope:      apiextensionsv1.NamespaceScoped,

		// virtualization.Cluster is a PrimaryModel (docs/netbox-schema.md ->
		// virtualization.Cluster, bases), which mixes in both TagsMixin and
		// CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `scope` is absent from this table on purpose: one spec field writing two columns is
		// a GenericFKSpec, not a Field. So is every `_`-prefixed cache, which appears in
		// ReadOnly below.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "status", API: "status"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "typeRef", API: "type", Class: ClassRefOne,
				Target: netboxv1alpha1.ClusterTypeRef{}.TargetGVK(),
			},
			{
				Spec: "groupRef", API: "group", Class: ClassRefOne,
				Target: netboxv1alpha1.ClusterGroupRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
		},

		// `meta.constraints` is `(('group','name'), ('_site','name'))` -- two separate
		// constraints, not one composite (docs/netbox-schema.md -> virtualization.Cluster).
		// Only the first is expressible as a lookup, and the honest reason is worth writing
		// down rather than hiding:
		//
		//  1. `(group, name)` -> `group_id=<id>&name=<name>`. Applicable only once `groupRef`
		//     resolves, so a cluster whose group has not been created yet waits instead of
		//     falling through and adopting a groupless cluster of the same name.
		//  2. `(_site, name)` would be `site_id=<id>&name=<name>`, and `site_id` filters the
		//     cached `_site` -- correct *as a lookup*, since NetBox maintains it, and wrong as
		//     a write, which is the whole distinction this kind exists to draw. It is not
		//     declared here because the value cannot be reached: the site id lives inside the
		//     `scope` union, and internal/reconciler writes a resolved generic FK to the
		//     payload only, never back into the spec a natural key filters on
		//     (internal/reconciler/refs.go, applyGenericFK). A candidate naming `scope` would
		//     be Applicable and then fail params() as unfilterable, which is a stopped
		//     reconcile rather than a lookup. ipam.VLANGroup -- unique on
		//     `(scope_type, scope_id, slug)` -- is the kind that has to make the engine able
		//     to express this; until then a scoped, groupless cluster is identified by
		//     candidate 2 below and an ambiguous name is a Conflict rather than a guess.
		//
		// So the fallback is `name` with `group_id` pinned to null, not `name` alone. Pinned
		// rather than omitted for the reason every null pin in this registry exists: an
		// omitted `group_id` means "this name in any group", so every groupless cluster would
		// adopt an unrelated grouped one and the follow-up PATCH would move somebody else's
		// cluster out of its group
		// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
		//
		// The pin does not make `name` unique. Two groupless clusters called `proxmox` in two
		// different sites are a legal NetBox state -- the `(_site, name)` constraint keeps
		// them apart and this lookup cannot -- so both CRs report Conflict naming the ids and
		// neither writes. That is the intended outcome (plan.md §6.2 step 5b): adopting the
		// wrong cluster would move every VM and device that hangs off it.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "group_id", Spec: "groupRef"},
					{Filter: "name", Spec: "name"},
				},
			},
			{
				Fields: []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{
					// A foreign key: NetBox registers only negation on an FK filter, so the pin
					// is the sentinel value rather than a suffix (NBO-206).
					{Filter: "group_id", Spec: "groupRef", Column: NullColumnRef},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The scope union, in one line. ScopeFK carries the pair's column names, the four
		// `app_label.model` strings NetBox accepts -- which are exactly NetBox's own
		// `LOCATION_SCOPE_TYPES`, the queryset its `scope_type` field is limited to -- the
		// four CR spec fields that select them, and the cache list. So this kind cannot get
		// the `dcim.sitegroup` spelling wrong or forget a cache column.
		GenericFKs: []GenericFKSpec{ScopeFK("scope", ScopeCascadesFromEvery())},

		// The four columns every ChangeLoggedModel carries, plus the four scope caches, plus
		// `site`.
		//
		// `site` is the entry that is not a column at all, and it is deliberate. The other
		// nine are things NetBox returns and refuses to accept; `site` is a key NetBox
		// *silently discards*, which is worse, and listing it makes `siteRef -> site` a boot
		// failure (ErrFieldReadOnly) rather than a field somebody adds in a hurry and a
		// cluster that reports itself scoped while sitting in no site.
		//
		// Not listed: `device_count`, `virtualmachine_count`, `allocated_vcpus`,
		// `allocated_memory`, `allocated_disk` and `scope`. All six are read-only serializer
		// fields rather than columns -- the counts are RelatedObjectCountField annotations, so
		// docs/netbox-schema.md shows no CounterCacheField on this model -- and this list
		// guards the field map, which no spec field points at them from. They still have to
		// survive the drift comparison, which is asserted in
		// internal/registry/virtualization_cluster_test.go.
		ReadOnly: append(ScopeCacheColumns(),
			"created", "last_updated", "url", "display", "site"),

		// docs/decisions/0003-ownership-and-references.md rule 4: `scope` is the containment
		// parent, and every one of its four members cascades -- by two different mechanisms,
		// which is the whole reason the cascade is stated per member (#214). `clusters` is a
		// GenericRelation on dcim.Region and dcim.SiteGroup, and dcim.CachedScopeMixin's
		// `_site` and `_location` are `on_delete=CASCADE`, so deleting the NetBoxSite or
		// NetBoxLocation a cluster is scoped to takes the cluster with it through the cached
		// column that has no GenericRelation (docs/netbox-schema.md).
		//
		// This Kind had no containment parent when it landed (#210), on the reading that the
		// missing `clusters GenericRelation` on dcim.Site meant no cascade from a site. It
		// meant the opposite: the GenericRelations exist on dcim.Region and dcim.SiteGroup
		// *because* `_region` and `_site_group` are SET_NULL, and are not needed on the two
		// targets whose cached column is CASCADE. Without the owner reference, deleting a site
		// left the NetBoxCluster CR behind and the engine recreated in NetBox the cluster
		// NetBox had just deleted.
		//
		// Exactly one, because Kubernetes garbage collection waits for *every* owner: adding
		// `typeRef` or `groupRef` would turn "delete the site and the cluster goes" into
		// "delete all three", and NetBox's PROTECT on both would refuse the delete anyway.
		ContainmentRef: "scope",
	}
}
