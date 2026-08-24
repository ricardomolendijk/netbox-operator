package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamVLANDescriptor()) }

// ipamVLANDescriptor is ipam.VLAN as data.
//
// **The one kind in this milestone that writes `site`, and it is correct here.**
// docs/netbox-schema.md -> ipam.VLAN lists `site ForeignKey -> dcim.Site
// on_delete=PROTECT`: a real column, on the model, writable. ipam.Prefix lists no `site`
// column at all -- only dcim.CachedScopeMixin among its bases -- which is why
// internal/registry/ipam_prefix.go has a GenericFKs entry and no `site` field, and why this
// file has a `site` field and no GenericFKs entry. Getting the two the wrong way round is
// silent in both directions: NetBox drops a field it does not know rather than rejecting it.
//
// `l2vpn_terminations` is absent on purpose. It is a Django GenericRelation -- a reverse
// accessor, a read-only view of somebody else's foreign key -- and its `REQ` in the digest is
// the extractor artefact NBO-019 describes (docs/concepts/generic-refs.md). There is nothing
// to write, so it is not in Fields and not in ReadOnly either.
func ipamVLANDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxVLAN"),
		Endpoint:   "ipam/vlans",
		ObjectType: "ipam.vlan",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.VLAN is a PrimaryModel (docs/netbox-schema.md -> ipam.VLAN, bases), which mixes
		// in both TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `qinqSVLANRef` -> `qinq_svlan` and `qinqRole` -> `qinq_role` are the entries that
		// earn an explicit table: NetBox ignores a field name it does not know rather than
		// rejecting it, so `qinqRole` sent verbatim would write nothing and report success.
		Fields: []Field{
			{Spec: "vid", API: "vid"},
			{Spec: "name", API: "name"},
			{Spec: "status", API: "status"},
			{Spec: "qinqRole", API: "qinq_role"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "siteRef", API: "site", Class: ClassRefOne,
				Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
			},
			{
				Spec: "groupRef", API: "group", Class: ClassRefOne,
				Target: netboxv1alpha1.VLANGroupRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
			{
				Spec: "roleRef", API: "role", Class: ClassRefOne,
				Target: netboxv1alpha1.RoleRef{}.TargetGVK(),
			},
			{
				Spec: "qinqSVLANRef", API: "qinq_svlan", Class: ClassRefOne,
				Target: netboxv1alpha1.VLANRef{}.TargetGVK(),
			},
		},

		// Three candidates, and the honest provenance is that **only the first is a database
		// constraint**. `meta.constraints` on ipam.VLAN is `(group, vid)`, `(group, name)`,
		// `(qinq_svlan, vid)` and `(qinq_svlan, name)` (docs/netbox-schema.md -> ipam.VLAN);
		// there is **no `(site, vid)` constraint**. plan.md §8 lists the natural key as
		// "`(group, vid)` or `(site, vid)`" as if both came from Meta.constraints. Only the
		// first does.
		//
		// That matters concretely rather than pedantically: every VLAN in ../inventory.yaml
		// has a site and no group, so every VLAN the operator creates for the real inventory
		// falls into candidate 2 -- the branch nothing in the database enforces. With `group`
		// null the `unique_group_vid` constraint does not fire either, because Postgres treats
		// NULLs as distinct, so two VLANs with `vid: 20` on one site are a legal server state.
		// More than one match is reported as a Conflict naming the candidate ids and nothing
		// is written. ../reconcile.go:230 uses the same `{vid, site_id}` filter and takes the
		// first match; that is the bug this kind exists not to inherit.
		//
		// The `group_id` pin on candidates 2 and 3 is load-bearing, not tidy. Omitted, a VLAN
		// whose group has not been created yet would match candidate 2 by site and vid, adopt
		// an ungrouped VLAN, and the follow-up PATCH would move somebody else's VLAN into this
		// group. Pinned, such a VLAN matches nothing -- candidate 1 needs the group resolved,
		// 2 and 3 need it never declared -- and the engine waits, which is the correct outcome
		// (NBO-015, docs/concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).
		//
		// Candidate 3 exists so that a VLAN with neither a site nor a group has an identity at
		// all. `?vid=20` with both pins is a wide net, and it is the whole of what such an
		// object is: the alternative is no applicable candidate, which makes the engine wait
		// forever for an identity that cannot be built -- the worst of the three outcomes
		// (ErrToManyNaturalKey says the same thing about a to-many filter).
		//
		// `(qinq_svlan, vid)` is a real constraint and still cannot be a candidate here:
		// `qinq_svlan` is in Deferred below, so it is unresolved by construction when the
		// lookup runs.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "group_id", Spec: "groupRef"},
					{Filter: "vid", Spec: "vid"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "site_id", Spec: "siteRef"},
					{Filter: "vid", Spec: "vid"},
				},
				NullFields: []NullField{{Filter: "group_id", Spec: "groupRef", Column: NullColumnRef}},
			},
			{
				Fields: []KeyField{{Filter: "vid", Spec: "vid"}},
				NullFields: []NullField{
					{Filter: "group_id", Spec: "groupRef", Column: NullColumnRef},
					{Filter: "site_id", Spec: "siteRef", Column: NullColumnRef},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// A self-referencing foreign key, which plan.md §5.4 names explicitly. Two VLANs that
		// point at each other as service and customer VLAN cannot both be created with the
		// reference set, so it is left out of the create payload and applied by a follow-up
		// PATCH. DeferAlways rather than DeferIfUnresolved: the cycle is the normal shape of
		// Q-in-Q rather than an apply-order accident, so there is no ordering under which
		// including it at create time is safe.
		Deferred: []DeferredField{{APIField: "qinq_svlan", Mode: DeferAlways}},

		// The four columns every ChangeLoggedModel carries, and nothing else. Unlike
		// ipam.Prefix, ipam.VLAN has no `_`-prefixed cached columns and no CounterCacheField:
		// it holds a real `site` foreign key rather than a scope pair, so there is no `_site`
		// cache to exclude, and its place in no hierarchy is computed.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// docs/decisions/0003-ownership-and-references.md rule 4 names `siteRef` as a
		// containment parent, so deleting the NetBoxSite a VLAN belongs to takes the VLAN with
		// No ContainmentRef. `ipam.VLAN.site` and `.group` are both `on_delete=PROTECT`
		// (docs/netbox-schema.md -> ipam.VLAN), so neither cascades: NetBox refuses to delete a
		// site that still has VLANs. An owner reference on a PROTECT-ed FK would promise a
		// cluster-side cascade the server declines -- garbage collection removes the CR, the
		// finalizer's DELETE is refused, and the row outlives the object. NBO-193 makes that a
		// boot failure (ErrContainmentNotCascade) rather than a convention, which is what caught
		// the `siteRef` this descriptor originally declared.
	}
}
