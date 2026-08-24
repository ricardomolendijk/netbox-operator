package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(tenancyTenantDescriptor()) }

// tenancyTenantDescriptor is tenancy.Tenant as data.
//
// The first kind whose identity is scoped by a reference to a *different* kind rather than
// to itself, and the first real use of a null pin outside the MPTT case. Both candidates
// come straight out of docs/netbox-schema.md -> tenancy.Tenant.meta.constraints, which
// declares four constraints in two pairs -- one pair on `name`, one on `slug`:
//
//	UniqueConstraint(fields=('group', 'name'), name='..._unique_group_name')
//	UniqueConstraint(fields=('name',),  name='..._unique_name',  condition=Q(group__isnull=True))
//	UniqueConstraint(fields=('group', 'slug'), name='..._unique_group_slug')
//	UniqueConstraint(fields=('slug',),  name='..._unique_slug',  condition=Q(group__isnull=True))
//
// The `slug` pair is the identity and the `name` pair is deliberately not: a kind gets one
// identity, and `slug` is the stable one. The consequence is worth naming, because it is
// the good outcome rather than a gap -- `name` staying a database constraint the operator
// does not look up on means a rename that collides comes back as a 409 the engine reports
// as Invalid, instead of being adopted silently under the other candidate.
func tenancyTenantDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxTenant"),
		Endpoint:   "tenancy/tenants",
		ObjectType: "tenancy.tenant",
		Scope:      apiextensionsv1.NamespaceScoped,

		// tenancy.Tenant is a PrimaryModel (docs/netbox-schema.md -> tenancy.Tenant,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin, so it carries the
		// whole provenance stamp. ContactsMixin, the other base, contributes only reverse
		// relations and no columns.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			// Written as `group`, filtered as `group_id`: the field map carries the write
			// name and the natural keys below carry the filter name.
			{
				Spec: "groupRef", API: "group", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantGroupRef{}.TargetGVK(),
			},
		},

		// Two candidates, in constraint order. Not a fallback chain: a grouped tenant is
		// identified by the first and a groupless one by the second, and
		// NaturalKey.Applicable is what keeps them apart -- the second asserts `groupRef`
		// was never declared, so a tenant whose group has not been created yet matches
		// neither and the engine waits.
		//
		// The second pins `group_id__isnull=true` rather than omitting `group_id`. Omitting
		// it asks "this slug in any group", so every groupless tenant would match every
		// tenant of that slug anywhere -- the engine would adopt somebody else's and then
		// PATCH the group off it (docs/concepts/lookups.md).
		//
		// `groupRef` is not deferred, and cannot be: it is matched on by candidate 1, so
		// stripping it from a create would mean the lookup asked a different question from
		// the create it decided on (registry.ErrDeferredNaturalKey).
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "group_id", Spec: "groupRef"},
					{Filter: "slug", Spec: "slug"},
				},
			},
			{
				Fields:     []KeyField{{Filter: "slug", Spec: "slug"}},
				NullFields: []NullField{{Filter: "group_id", Spec: "groupRef"}},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries and the operator must never
		// write (docs/netbox-schema.md, preamble). tenancy.Tenant declares no `_`-prefixed
		// cache and no CounterCacheField of its own; the counts its serializer returns
		// (`prefix_count`, `vlan_count`, ...) are equally unwritable and are not listed
		// because this list guards the field map -- a column no spec field maps onto cannot
		// reach a payload, so listing it would document rather than prevent.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
