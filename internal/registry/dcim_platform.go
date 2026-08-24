package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimPlatformDescriptor()) }

// dcimPlatformDescriptor is dcim.Platform as data.
//
// The NestedGroupModel whose uniqueness is not scoped by its own tree. Its `meta.constraints`
// are keyed on `manufacturer`, not on `parent` (docs/netbox-schema.md ->
// dcim.Platform.meta.constraints):
//
//	UniqueConstraint(fields=('manufacturer', 'name'), name='..._manufacturer_name')
//	UniqueConstraint(fields=('name',), name='..._name', condition=Q(manufacturer__isnull=True))
//	UniqueConstraint(fields=('manufacturer', 'slug'), name='..._manufacturer_slug')
//	UniqueConstraint(fields=('slug',), name='..._slug', condition=Q(manufacturer__isnull=True))
//
// So this kind gets the null pin -- from `manufacturer__isnull=True` -- and *not* on the field
// every other nested-group kind pins. dcim.DeviceRole, in this same ticket, is the mirror
// image: it pins `parent_id` and has no manufacturer. Reading the base class instead of the
// constraints would get both of them wrong, and the failure is silent: a lookup keyed on the
// wrong column adopts the wrong platform.
func dcimPlatformDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxPlatform"),
		Endpoint:   "dcim/platforms",
		ObjectType: "dcim.platform",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Platform is a NestedGroupModel (docs/netbox-schema.md -> dcim.Platform,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `config_template` is absent deliberately: extras.ConfigTemplate has no Kind yet
		// (NBO-059), and a field the CRD accepts and the payload drops reports success while
		// writing nothing.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			// A foreign key is written as `manufacturer` and filtered as `manufacturer_id`;
			// the field map carries the write name, the natural keys below carry the filter
			// name.
			{
				Spec: "manufacturerRef", API: "manufacturer", Class: ClassRefOne,
				Target: netboxv1alpha1.ManufacturerRef{}.TargetGVK(),
			},
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.PlatformRef{}.TargetGVK(),
			},
		},

		// Two candidates, in constraint order, keyed on `manufacturer` and never on `parent`.
		// Not a fallback chain: a vendor-specific platform is identified by the first and a
		// vendor-neutral one by the second, and NaturalKey.Applicable keeps them apart -- the
		// second asserts `manufacturerRef` was never declared, so a platform whose
		// manufacturer has not been created yet matches neither and the engine waits.
		//
		// The second pins `manufacturer_id__isnull=true` rather than omitting the filter.
		// Omitting it asks "this slug under any manufacturer", so a vendor-neutral platform
		// would adopt some vendor's platform of the same slug and then PATCH the manufacturer
		// off it (docs/concepts/lookups.md).
		//
		// `slug` and not `name`: NetBox constrains both pairwise, and a kind gets one
		// identity.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "manufacturer_id", Spec: "manufacturerRef"},
					{Filter: "slug", Spec: "slug"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "slug", Spec: "slug"}},
				NullFields: []NullField{{Filter: "manufacturer_id", Spec: "manufacturerRef"}},
			},
		},

		// Deferrable precisely because `parent` is outside the natural key -- the same reason
		// tenancy.TenantGroup's is, and the reason dcim.DeviceRole's is not. A platform whose
		// parent has not been created yet is still identifiable by its slug, so the engine
		// creates it top-level and PATCHes `parent` on when the reference resolves, which is
		// what makes a parent and child applied in one batch converge without a resync.
		//
		// IfUnresolved and not Always: a resolved parent belongs in the create payload.
		// Stripping it would leave the object top-level for one pass, which is a visible
		// wrong state in NetBox for no gain (internal/reconciler/deferred.go, strip).
		//
		// `manufacturer` is not deferred and cannot be: candidate 1 matches on it
		// (registry.ErrDeferredNaturalKey).
		Deferred: []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}},

		UpdateStrategy: UpdatePatch,

		// `parentRef` and not `manufacturerRef`: `dcim.Platform.parent` is a TreeForeignKey
		// with `on_delete=CASCADE`, so NetBox deletes a platform's descendants with it and the
		// child CR has to go the same way -- otherwise the next reconcile recreates the row
		// NetBox deliberately deleted. `manufacturer` is `on_delete=PROTECT`, which cascades
		// nothing, and exactly one field may carry containment
		// (docs/decisions/0003-ownership-and-references.md rule 4).
		ContainmentRef: "parentRef",

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does not
		// fail, it silently no-ops, so the next reconcile finds the same difference and
		// PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
