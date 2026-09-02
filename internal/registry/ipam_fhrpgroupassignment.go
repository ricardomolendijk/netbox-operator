package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamFHRPGroupAssignmentDescriptor()) }

// fhrpInterfaceFK is the `(interface_type, interface_id)` pair, with the two members NetBox
// accepts and the cascade stated per member.
//
// Both members cascade, and by the same mechanism: dcim.Interface and
// virtualization.VMInterface each declare an `fhrp_group_assignments` GenericRelation
// (docs/netbox-schema.md), so deleting either interface deletes the assignment rows hanging
// off it. Stated per member rather than per pair because that is where the fact lives (#214)
// -- a union whose members state it unevenly is a boot failure, and one where none states it
// simply has no cascade.
//
// No Cached columns: this pair maintains no denormalised columns at all, unlike
// dcim.CachedScopeMixin's `_site` and friends.
func fhrpInterfaceFK() GenericFKSpec {
	cascades := true

	return GenericFKSpec{
		TypeField:    "interface_type",
		IDField:      "interface_id",
		Spec:         "interface",
		AllowedTypes: []string{"dcim.interface", "virtualization.vminterface"},
		Members: []GenericFKMember{
			{
				Spec:            "interfaceRef",
				Target:          netboxv1alpha1.InterfaceRef{}.TargetGVK(),
				CascadeOnDelete: &cascades,
			},
			{
				Spec:            "vmInterfaceRef",
				Target:          netboxv1alpha1.VMInterfaceRef{}.TargetGVK(),
				CascadeOnDelete: &cascades,
			},
		},
	}
}

// ipamFHRPGroupAssignmentDescriptor is ipam.FHRPGroupAssignment as data.
//
// The first Kind whose model is a bare **ChangeLoggedModel** (docs/netbox-schema.md ->
// ipam.FHRPGroupAssignment, bases), and everything unusual about this descriptor follows from
// that one line: a ChangeLoggedModel mixes in neither TagsMixin nor CustomFieldsMixin, so
// Taggable and CustomFieldable are both false, the object carries **no provenance stamp**, and
// its object type is absent from internal/controller/provenance_test.go's stampedObjectTypes
// on purpose. It also declares no `description` and no `comments`, which makes it the one
// object Kind with no clearable field -- see noClearableFields in
// internal/controller/manifests_test.go.
//
// Writing `tags` or `custom_fields` here would not fail: NetBox ignores a column it does not
// know, the value would vanish, and the next read would find it absent and PATCH it again
// forever. Which is why Taggable is a declaration rather than a default.
func ipamFHRPGroupAssignmentDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxFHRPGroupAssignment"),
		Endpoint:   "ipam/fhrp-group-assignments",
		ObjectType: "ipam.fhrpgroupassignment",
		Scope:      apiextensionsv1.NamespaceScoped,

		// Both false, from `bases: ChangeLoggedModel`. See the type comment.
		Taggable:        false,
		CustomFieldable: false,

		// `interface` is absent from this table on purpose: one spec field writing two
		// columns is a GenericFKSpec, not a Field.
		Fields: []Field{
			{Spec: "priority", API: "priority"},
			// The containment parent. `group ForeignKey REQ -> ipam.FHRPGroup
			// on_delete=CASCADE` (docs/netbox-schema.md), read straight off the digest.
			{
				Spec: "groupRef", API: "group", Class: ClassRefOne,
				Target:          netboxv1alpha1.FHRPGroupRef{}.TargetGVK(),
				CascadeOnDelete: true,
			},
		},

		// One candidate, and unlike three of the four other lookup-only Kinds in this
		// milestone it is a **real database constraint**:
		// `UniqueConstraint(fields=('interface_type', 'interface_id', 'group'),
		// name='%(app_label)s_%(class)s_unique_interface_group')` (docs/netbox-schema.md ->
		// ipam.FHRPGroupAssignment, meta.constraints). So this lookup matches at most one row
		// and an ambiguous match is impossible rather than merely reported.
		//
		// The pair is named by its two *column* names rather than by the union's spec field,
		// because a lookup on a polymorphic pair needs two filters and the union has no single
		// value to offer one. reconciler.applyGenericFK writes the resolved pair back into the
		// decoded spec under exactly these names, which is the mechanism ipam.VLANGroup's
		// `(scope_type, scope_id, slug)` identity is built on (#180).
		//
		// All three filters exist on the server side:
		// `FHRPGroupAssignmentFilterSet.Meta.fields = ('id', 'group_id', 'interface_type',
		// 'interface_id', 'priority')` plus `interface_type = MultiValueContentTypeFilter()`
		// (netbox/ipam/filtersets.py:891-921).
		//
		// No null variant, and none is possible: all three columns are `REQ`, so there is no
		// state in which one is absent.
		NaturalKeys: []NaturalKey{{
			Fields: []KeyField{
				{Filter: "interface_type", Spec: "interface_type"},
				{Filter: "interface_id", Spec: "interface_id"},
				{Filter: "group_id", Spec: "groupRef"},
			},
		}},

		UpdateStrategy: UpdatePatch,

		// The `(interface_type, interface_id)` pair, with the two targets NetBox accepts.
		GenericFKs: []GenericFKSpec{fhrpInterfaceFK()},

		// Only the two columns a ChangeLoggingMixin carries, plus the two the REST API adds.
		// This model has no `_`-prefixed cache and no CounterCacheField.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// The containment parent, and **two candidates cascade**, which is the decision worth
		// reading. `group` is `on_delete=CASCADE` on this model
		// (docs/netbox-schema.md -> ipam.FHRPGroupAssignment), and both interface targets
		// cascade too through their `fhrp_group_assignments` GenericRelation -- so either
		// would satisfy ADR-0003 rule 4 and validateContainment.
		//
		// Exactly one is permitted, because Kubernetes garbage collection waits for *every*
		// owner: a second owner would turn "delete the group or the interface and the
		// assignment goes" into "delete both", silently. `groupRef` is the one, on two
		// grounds -- it is the declared foreign key on this model with `on_delete=CASCADE`
		// written on it, the most direct evidence there is; and it is the only member whose
		// Kind ships (NetBoxInterface does not exist yet, so `interfaceRef` can only resolve
		// in slug, lookup or id mode, and a reference to an object with no CR cannot produce
		// an owner reference anyway).
		//
		// The consequence is stated rather than hidden: deleting a NetBoxVMInterface whose
		// interface NetBox cascades leaves this CR behind, and the engine recreates the row on
		// the next pass. That is the cost of one containment parent, and it is the same cost
		// every polymorphic Kind in this API pays.
		ContainmentRef: "groupRef",
	}
}
