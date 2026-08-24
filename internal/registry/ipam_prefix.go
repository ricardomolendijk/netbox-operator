package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamPrefixDescriptor()) }

// ipamPrefixDescriptor is ipam.Prefix as data.
//
// prefixScopeFK is the scope union as ipam.Prefix carries it: cascading, because all four
// scope targets -- dcim.Region, dcim.SiteGroup, dcim.Site and dcim.Location -- declare a
// `prefixes` GenericRelation, so deleting any of them deletes the prefixes scoped to it
// (docs/netbox-schema.md). ScopeFK leaves the flag to the caller because that is not true of
// every scoped kind: `clusters` and `wireless_lans` are declared on only two of the four.
func prefixScopeFK() GenericFKSpec {
	scope := ScopeFK("scope")
	scope.CascadeOnDelete = true

	return scope
}

// The first scoped kind, and the reason registry.ScopeFK exists: one line declares the
// `(scope_type, scope_id)` pair, the four object types NetBox permits in it, the four CR
// spec fields that select them, and the four read-only caches that must never be written.
// There is no `site` entry in the field map and no `siteRef` on the CRD, because since
// NetBox 4.2 there is no such column -- writing it returns 201 and sets nothing
// (docs/concepts/generic-refs.md#the-failure-this-prevents).
func ipamPrefixDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxPrefix"),
		Endpoint:   "ipam/prefixes",
		ObjectType: "ipam.prefix",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.Prefix is a PrimaryModel (docs/netbox-schema.md -> ipam.Prefix, bases),
		// which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `scope` is absent from this table on purpose: one spec field writing two columns
		// is a GenericFKSpec, not a Field. So are `_depth` and `_children` -- NetBox
		// maintains both from the prefix value itself, and they appear in ReadOnly below.
		//
		// `markUtilized` -> `mark_utilized` and `isPool` -> `is_pool` are the entries that
		// earn an explicit table: NetBox ignores a field name it does not know rather than
		// rejecting it, so `markUtilized` sent verbatim would write nothing and report
		// success.
		Fields: []Field{
			{Spec: "prefix", API: "prefix"},
			{Spec: "status", API: "status"},
			{Spec: "isPool", API: "is_pool"},
			{Spec: "markUtilized", API: "mark_utilized"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "vrfRef", API: "vrf", Class: ClassRefOne,
				Target: netboxv1alpha1.VRFRef{}.TargetGVK(),
			},
			{
				Spec: "vlanRef", API: "vlan", Class: ClassRefOne,
				Target: netboxv1alpha1.VLANRef{}.TargetGVK(),
			},
			{
				Spec: "roleRef", API: "role", Class: ClassRefOne,
				Target: netboxv1alpha1.RoleRef{}.TargetGVK(),
			},
		},

		// Two candidates, and the honest provenance is that **ipam.Prefix carries no
		// meta.constraints at all**. Its only table-level lines are
		// `meta.ordering: (F('vrf').asc(nulls_first=True), 'prefix', 'pk')` and
		// `meta.indexes: (models.Index(fields=('scope_type', 'scope_id')),
		// GistIndex(fields=['prefix'], name='ipam_prefix_gist_idx',
		// opclasses=['inet_ops']))` -- an ordering and two non-unique indexes
		// (docs/netbox-schema.md -> ipam.Prefix). `(vrf, prefix)` is the ordering tuple, and
		// it is a *convention* rather than a database guarantee: duplicates are legal when
		// the enclosing VRF has `enforce_unique=false` (docs/netbox-schema.md -> ipam.VRF)
		// or global uniqueness enforcement is off. More than one match is therefore a
		// legitimate server state, which is exactly why it is reported as a Conflict and
		// nothing is written -- and why, once status.id is set, the natural key is not
		// consulted again.
		//
		// The order is not a fallback. A prefix in a VRF and the same CIDR in the global
		// table are two different objects, and Applicable keeps them apart: candidate 2
		// asserts `vrfRef` was never declared, so a prefix whose VRF has not been created
		// yet matches neither candidate and the engine waits. Falling through would adopt
		// the global prefix of the same CIDR and then PATCH a VRF onto somebody else's row.
		//
		// The `vrf_id` pin is load-bearing rather than tidy. `../inventory.yaml` puts the
		// same 10.0.x.0/24 ranges in per-house VRFs precisely so they can coexist, so a
		// lookup that merely omitted `vrf_id` would make every house adopt another house's
		// prefixes (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "prefix", Spec: "prefix"},
					{Filter: "vrf_id", Spec: "vrfRef"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "prefix", Spec: "prefix"}},
				NullFields: []NullField{{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef}},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The scope union, in one line. ScopeFK carries the pair's column names, the four
		// `app_label.model` strings NetBox accepts, the four CR spec fields that select
		// them and the cache list, so this kind cannot get the `dcim.sitegroup` spelling
		// wrong or forget a cache column.
		GenericFKs: []GenericFKSpec{prefixScopeFK()},

		// The four columns every ChangeLoggedModel carries, plus the scope caches, plus the
		// two hierarchy counters.
		//
		// `_depth` and `_children` are the same shape of column as dcim.Region's and a
		// different mechanism: a prefix has no `parent` foreign key at all, so NetBox
		// derives the tree from the prefix value using the `inet` GiST index and caches the
		// answer here. Writing either does not fail, it silently no-ops, so the next
		// reconcile finds the same difference and PATCHes again forever -- which is also
		// exactly what `_site` would do, and why Validate refuses a Cached column that is
		// not listed here.
		ReadOnly: append(ScopeCacheColumns(),
			"created", "last_updated", "url", "display", "_depth", "_children"),

		// The containment parent, and under docs/decisions/0003-ownership-and-references.md
		// rule 4 it is not a preference: the containment parent is whichever FK the *server*
		// cascades. Every one of the four scope targets declares a `prefixes` GenericRelation
		// (docs/netbox-schema.md), so deleting the NetBoxSite a prefix is scoped to takes the
		// prefix with it, and the owner reference is what makes the CR go too.
		//
		// `vrfRef` is ruled out by the same rule rather than by taste: `ipam.Prefix.vrf` is
		// `on_delete=PROTECT`, so NetBox *refuses* to delete a VRF that still has prefixes and
		// there is no server-side deletion for an owner reference to mirror. Exactly one in any
		// case, because Kubernetes garbage collection waits for *every* owner: a second owner
		// would turn "delete the site or the VRF and the prefix goes" into "delete both",
		// silently.
		ContainmentRef: "scope",
	}
}
