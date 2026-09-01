package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so that adding a kind is a new file and never an edit to shared
// logic (CONTRIBUTING.md, "Extensibility").
func init() { MustRegister(ipamIPRangeDescriptor()) }

// ipamIPRangeDescriptor is ipam.IPRange as data.
//
// An ordinary PrimaryModel with two interesting properties. It has **no scope union** --
// `bases: ContactsMixin, PrimaryModel`, no CachedScopeMixin (docs/netbox-schema.md ->
// ipam.IPRange) -- so unlike ipam.Prefix there is no `(scope_type, scope_id)` pair and no
// containment ref to derive from one. And its `size` column is derived, which is why it appears
// in ReadOnly and in no field map: `size` is `editable=False` and set in `IPRange.save()` as
// `end - start + 1` (netbox/ipam/models/ip.py, NetBox 4.6.8), so a `size` in a payload is
// dropped without complaint and a `size` in a diff would be a PATCH that changes nothing,
// forever. The schema digest records it as `REQ` because the column is not nullable, which is
// the trap docs/concepts/generic-refs.md#the-req-trap-in-the-schema-digest describes.
//
// This kind is scheduled in NBO-055 and delivered here, because NBO-064's NetBoxIPRangeClaim
// allocates ipam.IPRange objects and a claim whose allocated Kind cannot be reconciled
// declaratively is a claim whose result nobody can correct.
func ipamIPRangeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIPRange"),
		Endpoint:   "ipam/ip-ranges",
		ObjectType: "ipam.iprange",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.IPRange is a PrimaryModel (docs/netbox-schema.md -> ipam.IPRange, bases),
		// which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp -- and the allocation identity a NetBoxIPRangeClaim writes.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain. Deleting a range frees every address in it
		// for reallocation at once, and if a NetBoxIPRangeClaim allocated it (ADR-0004) that
		// hands somebody else a block this cluster believes it owns.
		RetainOnDelete: true,

		// `markPopulated` -> `mark_populated` and `markUtilized` -> `mark_utilized` are the
		// entries that earn an explicit table: NetBox ignores a field name it does not know
		// rather than rejecting it, so either sent verbatim would write nothing and report
		// success.
		Fields: []Field{
			{Spec: "startAddress", API: "start_address"},
			{Spec: "endAddress", API: "end_address"},
			{Spec: "status", API: "status"},
			{Spec: "markPopulated", API: "mark_populated"},
			{Spec: "markUtilized", API: "mark_utilized"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "vrfRef", API: "vrf", Class: ClassRefOne,
				Target: netboxv1alpha1.VRFRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
			{
				Spec: "roleRef", API: "role", Class: ClassRefOne,
				Target: netboxv1alpha1.RoleRef{}.TargetGVK(),
			},
		},

		// `ipam.IPRange` declares no meta.constraints at all: its only table-level lines are
		// `meta.ordering: (F('vrf').asc(nulls_first=True), 'start_address', 'pk')` and two
		// non-unique host indexes (docs/netbox-schema.md -> ipam.IPRange). So the natural key
		// is the tuple NetBox's own `clean()` uses to reject a duplicate --
		// `(start_address, end_address, vrf)` -- which is a *convention* rather than a database
		// guarantee, exactly as ipam.Prefix's is. More than one match is a legitimate server
		// state and is reported as a Conflict rather than guessed at.
		//
		// The filters are exact despite looking like containment queries. NetBox's
		// `IPRangeFilterSet.start_address` is a MultiValueCharFilter routed to `__net_in`, and
		// `NetIn.as_sql` compiles a value *carrying a mask* to `start_address IN ('...')`
		// (netbox/ipam/lookups.py, NetBox 4.6.8) -- an equality test including the mask. A
		// value without a mask would instead match on `HOST()` alone, which is why
		// NetBoxIPRange.startAddress requires the mask rather than accepting a bare address:
		// the same string is the natural key and the payload.
		//
		// Two candidates for the reason ipam.Prefix has two: a range in a VRF and the same
		// addresses in the global table are different objects, and candidate 2 asserts
		// `vrfRef` was never declared so that a range whose VRF has not been created yet
		// matches neither and the engine waits
		// (docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "start_address", Spec: "startAddress"},
					{Filter: "end_address", Spec: "endAddress"},
					{Filter: "vrf_id", Spec: "vrfRef"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "start_address", Spec: "startAddress"},
					{Filter: "end_address", Spec: "endAddress"},
				},
				NullFields: []NullField{{Filter: "vrf_id", Spec: "vrfRef", Column: NullColumnRef}},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries, plus `size` and `family`, both of
		// which NetBox derives from the two endpoints and neither of which the operator may
		// send. There is no `_`-prefixed cache on this model and no scope pair.
		//
		// No ContainmentRef: every foreign key ipam.IPRange has is `PROTECT` or `SET_NULL`
		// (docs/netbox-schema.md -> ipam.IPRange), so no parent's deletion cascades to a range
		// and there is no server-side cascade for an owner reference to mirror
		// (docs/decisions/0003-ownership-and-references.md rule 4).
		ReadOnly: []string{"created", "last_updated", "url", "display", "size", "family"},
	}
}
