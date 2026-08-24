package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimRegionDescriptor()) }

// dcimRegionDescriptor is dcim.Region as data.
//
// The first kind whose identity depends on a reference, which is why M2 is tested against
// it: `parent` is a self-referential foreign key, and whether it is set decides *which*
// natural key applies rather than merely changing one filter's value.
func dcimRegionDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRegion"),
		Endpoint:   "dcim/regions",
		ObjectType: "dcim.region",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Region is a NestedGroupModel (docs/netbox-schema.md -> dcim.Region, bases),
		// which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			// A foreign key is written as `parent` and filtered as `parent_id`; the field
			// map carries the write name, the natural keys below carry the filter name.
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.RegionRef{}.TargetGVK(),
			},
		},

		// Two candidates, in this order, straight out of
		// docs/netbox-schema.md -> dcim.Region.meta.constraints: unique on
		// (parent, name), plus a separate unique on (name) conditioned on
		// parent IS NULL.
		//
		// The order is not a fallback. A region with a parent is identified by the first
		// candidate and a top-level one by the second, and NaturalKey.Applicable is what
		// keeps them apart: the second asserts `parentRef` was never declared, so a child
		// whose parent has not been created yet matches neither and the engine waits.
		// Falling through would find an unrelated top-level region of the same name, adopt
		// it, and the follow-up PATCH would reparent somebody else's data (NBO-015).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "parent_id", Spec: "parentRef"},
					{Filter: "name", Spec: "name"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "name", Spec: "name"}},
				NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef"}},
			},
		},

		// `parentRef` is the containment parent, so a sub-region gets a non-controller owner
		// reference to its parent region and `kubectl delete` on the parent takes its
		// children with it (ADR-0003 rule 4).
		//
		// Not a stylistic choice. `dcim.Region.parent` is a TreeForeignKey with
		// `on_delete=CASCADE` (docs/netbox-schema.md -> dcim.Region), so deleting a region
		// in NetBox deletes its descendants server-side. Without the owner reference the
		// child CR outlives the row it described, finds nothing at status.id on the next
		// reconcile, and the engine's create-if-absent step *recreates* the region NetBox
		// deliberately deleted -- the same resurrection ADR-0003 describes for
		// `assignedObject`, whose general rule is that a server-side cascade implies an
		// owner reference.
		//
		// It is also the one FK this kind has, which satisfies "each kind nominates exactly
		// one containment ref, and it is the required FK" without a choice to make.
		ContainmentRef: "parentRef",

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes
		// (docs/netbox-schema.md, preamble on `_`-prefixed columns); writing either does
		// not fail, it silently no-ops, so the next reconcile finds the same difference
		// and PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
