package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(wirelessWirelessLANGroupDescriptor()) }

// wirelessWirelessLANGroupDescriptor is wireless.WirelessLANGroup as data.
//
// The third NestedGroupModel, and the one worth reading the constraint lines for rather than
// assuming from the tree shape. plan.md §8.1 asserts an MPTT kind needs a `(parent, name)`
// candidate plus a `parent IS NULL` variant; that is true of two of the three and false here,
// and getting it backwards makes a nested group's slug unfindable.
//
// The three arrangements, each straight off its model:
//
//   - dcim.Region: four `meta.constraints` -- `(parent, name)`, `(name)` with
//     `condition=Q(parent__isnull=True)`, `(parent, slug)`, `(slug)` with the same condition
//     (netbox/dcim/models/sites.py:62-82). `parent` is part of the identity, so two candidates
//     and a `?parent_id=null` pin. dcim.SiteGroup and dcim.Location are the same shape.
//   - tenancy.TenantGroup: **no `meta.constraints` at all**, column-level `UNIQUE` on `name`
//     and `slug`. Global uniqueness, one candidate, no pin (#203).
//   - wireless.WirelessLANGroup: **both mechanisms, and it lands where TenantGroup does.**
//     `name = CharField(max_length=100, unique=True)` (netbox/wireless/models.py:53-58) and
//     `slug = SlugField(max_length=100, unique=True)` (netbox/wireless/models.py:59-63) carry
//     column-level `UNIQUE`, and the single table constraint is
//     `UniqueConstraint(fields=('parent', 'name'),
//     name='%(app_label)s_%(class)s_unique_parent_name')`
//     (netbox/wireless/models.py:70-75) -- **with no `condition=` clause.**
//
// So there is no conditional constraint to model and nothing for a null pin to express.
// `(parent, name)` is strictly weaker than the column-level `UNIQUE` on `name` that already
// makes a name globally unique, so it adds nothing to the identity, and `slug` alone
// identifies at most one group whatever its parent is. One candidate, no pin.
func wirelessWirelessLANGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxWirelessLANGroup"),
		Endpoint:   "wireless/wireless-lan-groups",
		ObjectType: "wireless.wirelesslangroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// wireless.WirelessLANGroup is a NestedGroupModel (netbox/wireless/models.py:49),
		// which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// A foreign key is written as `parent` and filtered as `parent_id`. Only the write
			// name is needed here, because no natural key on this kind filters on it.
			{
				Spec: "parentRef", API: "parent", Class: ClassRefOne,
				Target: netboxv1alpha1.WirelessLANGroupRef{}.TargetGVK(),
				// `parent TreeForeignKey -> self on_delete=CASCADE` on NestedGroupModel
				// (netbox/netbox/models/__init__.py:171-178), which is what makes this field
				// the ContainmentRef below.
				CascadeOnDelete: true,
			},
		},

		// `slug` alone, from the column-level UNIQUE rather than from a table constraint.
		// `slug` is in WirelessLANGroupFilterSet's Meta.fields
		// (netbox/wireless/filtersets.py:47-49). `name` is column-unique too and deliberately
		// is not a candidate: a kind gets one identity, and `slug` is the one the spec calls
		// the group's identifier.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		// Deferrable precisely because `parent` is outside the natural key. A group whose
		// parent has not been created yet is still identifiable by its slug, so the engine
		// creates it top-level and PATCHes `parent` on when the reference resolves -- which is
		// what makes a parent and child applied in one batch converge without a resync.
		//
		// IfUnresolved and not Always: a resolved parent belongs in the create payload.
		// Stripping it would leave the object top-level for one pass, which is a visible wrong
		// state in NetBox for no gain (internal/reconciler/deferred.go, strip).
		Deferred: []DeferredField{{APIField: "parent", Mode: DeferIfUnresolved}},

		// `parent` is the only foreign key on this Kind and NetBox cascades through it, so by
		// ADR-0003 rule 4 it is the containment parent.
		//
		// It matters here for the same reason it matters on tenancy.TenantGroup and not on the
		// dcim nested groups (#203): this Kind's single candidate is `slug` alone, so it stays
		// applicable when the parent is gone, finds nothing, and create-if-absent re-creates a
		// row NetBox cascade-deleted. The dcim groups are saved by their candidates all reading
		// `parent_id` or pinning it null, which this one does neither of. The owner reference is
		// what removes the CR before that pass can happen.
		ContainmentRef: "parentRef",

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus MPTT's two denormalised
		// caches. `_depth` and `_children` are maintained by NetBox as the tree changes;
		// writing either does not fail, it silently no-ops, so the next reconcile finds the
		// same difference and PATCHes again forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_depth", "_children"},
	}
}
