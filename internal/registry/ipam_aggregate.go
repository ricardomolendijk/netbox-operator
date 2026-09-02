package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamAggregateDescriptor()) }

// ipamAggregateDescriptor is ipam.Aggregate as data.
//
// One of the four Kinds in this milestone with **no meta.constraints at all**
// (docs/netbox-schema.md -> ipam.Aggregate lists only `meta.ordering: ('prefix', 'pk')` and a
// non-unique `('prefix', 'id')` index), so its natural key is a lookup convention rather than
// a database guarantee and more than one match is a legitimate server state reported as a
// Conflict.
func ipamAggregateDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxAggregate"),
		Endpoint:   "ipam/aggregates",
		ObjectType: "ipam.aggregate",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.Aggregate is a PrimaryModel (docs/netbox-schema.md -> ipam.Aggregate,
		// bases: ContactsMixin, GetAvailablePrefixesMixin, PrimaryModel), which mixes in both
		// TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `dateAdded` -> `date_added` is the entry that earns an explicit table, and it is
		// also the one field on this kind that needs EmptyIsNull: the column is a nullable
		// DateField, and NetBox rejects `""` for a DateField outright, so an emptied value has
		// to go over the wire as null to clear rather than to fail (#170, the same handling
		// dcim.Site's coordinates get).
		Fields: []Field{
			{Spec: "prefix", API: "prefix"},
			{Spec: "dateAdded", API: "date_added", EmptyIsNull: true},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "rirRef", API: "rir", Class: ClassRefOne,
				Target: netboxv1alpha1.RIRRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
		},

		// One candidate, `(prefix, rir_id)`, and there is no second one because there cannot
		// be: `rir` is `REQ`, so there is no state in which it is absent and no null variant
		// to pin. An aggregate whose RIR has not been created yet therefore matches nothing
		// and the engine waits -- which is the correct outcome, because the alternative,
		// `?prefix=10.0.0.0/8` alone, would adopt the same block filed under a different
		// registry and the follow-up PATCH would move it
		// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
		//
		// Two CRs with the same `(prefix, rir)` are the acceptance criterion this shape
		// exists for: the first creates the row, the second finds it, and whichever does not
		// own the provenance stamp reports Conflict rather than writing a second aggregate.
		NaturalKeys: []NaturalKey{{
			Fields: []KeyField{
				{Filter: "prefix", Spec: "prefix"},
				{Filter: "rir_id", Spec: "rirRef"},
			},
		}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. This model has no `_`-prefixed
		// caches: unlike ipam.Prefix it has no hierarchy to memoise, because an aggregate is
		// by definition top-level.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// No ContainmentRef. Both of this model's mapped foreign keys are
		// `on_delete=PROTECT` (docs/netbox-schema.md -> ipam.Aggregate: `rir ... REQ ->
		// ipam.RIR on_delete=PROTECT`, `tenant ... -> tenancy.Tenant on_delete=PROTECT`), so
		// neither cascades and neither can be a containment parent
		// (docs/decisions/0003-ownership-and-references.md rule 4). ipam.RIR declares no
		// `aggregates` GenericRelation either, so there is no second mechanism to check.
	}
}
