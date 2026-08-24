package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(tenancyTenantGroupDescriptor()) }

// tenancyTenantGroupDescriptor is tenancy.TenantGroup as data.
//
// The second NestedGroupModel, and the one that shows the tree shape is not the thing that
// decides a natural key. dcim.Region and tenancy.TenantGroup have the same base, the same
// self-referential `parent`, and completely different identities:
//
//   - dcim.Region declares `meta.constraints` on `(parent, name)` plus `(name)` conditioned
//     on `parent IS NULL`, so `parent` is part of its identity and a top-level region is a
//     different natural key.
//   - tenancy.TenantGroup declares **no `meta.constraints` at all** and puts column-level
//     `UNIQUE` on both `name` and `slug` (docs/netbox-schema.md -> tenancy.TenantGroup).
//     Its uniqueness is global, so `slug` identifies at most one group whatever its parent
//     is.
//
// One candidate and no null pin, therefore. Adding a `parent_id__isnull=true` filter here
// -- the shape plan.md §8.1 asserts every MPTT kind needs -- would be wrong twice over: it
// would make a nested group's slug unfindable, and it would express a constraint the
// database does not have.
func tenancyTenantGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxTenantGroup"),
		Endpoint:   "tenancy/tenant-groups",
		ObjectType: "tenancy.tenantgroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// tenancy.TenantGroup is a NestedGroupModel (docs/netbox-schema.md ->
		// tenancy.TenantGroup, bases), which mixes in both TagsMixin and
		// CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			// A foreign key is written as `parent` and filtered as `parent_id`. Only the
			// write name is needed here, because no natural key on this kind filters on it.
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantGroupRef{}.TargetGVK(),
			},
		},

		// `slug` alone, from the column-level UNIQUE rather than from a table constraint.
		// `name` is column-unique too and deliberately is not a candidate: a kind gets one
		// identity, and `slug` is the one the spec calls the group's identifier.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		// Deferrable precisely because `parent` is outside the natural key. A group whose
		// parent has not been created yet is still identifiable by its slug, so the engine
		// creates it top-level and PATCHes `parent` on when the reference resolves --
		// which is what makes a parent and child applied in one batch converge without a
		// resync.
		//
		// IfUnresolved and not Always: a resolved parent belongs in the create payload.
		// Stripping it would leave the object top-level for one pass, which is a visible
		// wrong state in NetBox for no gain (internal/reconciler/deferred.go, strip).
		Deferred: []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does
		// not fail, it silently no-ops, so the next reconcile finds the same difference
		// and PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
