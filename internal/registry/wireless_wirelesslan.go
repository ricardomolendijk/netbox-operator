package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(wirelessWirelessLANDescriptor()) }

// wirelessWirelessLANDescriptor is wireless.WirelessLAN as data.
//
// The fourth kind on CachedScopeMixin, after ipam.Prefix, ipam.VLANGroup and
// virtualization.Cluster (netbox/wireless/models.py:80). Being fourth is the point: the union,
// the four allowed object types, the cache list and the `_`-prefixed ReadOnly columns are all
// ScopeFK's, so this kind restates none of them and cannot get the content-type spelling or
// the cache list wrong (docs/concepts/generic-refs.md#the-scope-pair).
//
// **The cascade table.** Every scope member cascades, by the two different mechanisms
// ScopeFK's comment insists on reading separately, and this Kind is one of the two that needs
// both halves:
//
//   - dcim.Region and dcim.SiteGroup carry a `wireless_lans` GenericRelation pointed at
//     `scope_type` / `scope_id` (netbox/dcim/models/sites.py:51-56 and :122-127), so deleting
//     either deletes the SSIDs scoped to it.
//   - dcim.Site and dcim.Location carry **no** such GenericRelation -- and do not need one,
//     because CachedScopeMixin declares `_site` and `_location` with `on_delete=CASCADE`
//     (netbox/dcim/models/mixins.py:63-74). The cached column is the cascade for those two.
//
// The other two caches are `on_delete=SET_NULL` on the same mixin
// (netbox/dcim/models/mixins.py:75-89) with the comment saying why: they cache an *ancestor* of
// the actual scope, so deleting that ancestor must not delete this object -- and the
// GenericRelation handles the case where the Region or SiteGroup *is* the scope. Reading only
// the SET_NULL half and concluding "region and site group do not cascade" is how
// virtualization.Cluster came to have no containment parent at all (#214). All four cascade, so
// ScopeCascadesFromEvery() is the table and `scope` is the containment ref.
//
// **This kind's identity is not enforced by NetBox.** wireless.WirelessLAN declares no
// `meta.constraints` -- only `(ssid, id)` for the default ordering and `(scope_type, scope_id)`
// (netbox/wireless/models.py:118-125). Two identical SSIDs in one scope are legal, so
// `(ssid, scope, tenant)` is a lookup convention and a lookup matching more than one row is an
// *AmbiguousError the engine reports as Conflict, writing nothing
// (internal/netbox/client.go). Same shape as ipam.VLANGroup, and the same one helper: there
// isn't one, because "several matches is ambiguous" is decided once in the client and no kind
// gets to decide it again.
func wirelessWirelessLANDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxWirelessLAN"),
		Endpoint:   "wireless/wireless-lans",
		ObjectType: "wireless.wirelesslan",
		Scope:      apiextensionsv1.NamespaceScoped,

		// wireless.WirelessLAN is a PrimaryModel (netbox/wireless/models.py:80), which mixes in
		// both TagsMixin and CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// No `site` entry, and that absence is the whole of decision 2 in ADR-0003. NetBox
		// drops a column it does not know rather than rejecting it, so a `site` key here would
		// return 201, create the SSID unscoped, and compare clean on every subsequent read --
		// Ready=True forever against a scope that was never set. `scope` below is the only way
		// this Kind can express where an SSID lives.
		//
		// No `auth_psk` entry either. A pre-shared key may not be inline in a spec (NBO-050,
		// plan.md §15) and sourcing one from a Secret needs a new FieldClass plus a Secret read
		// in the payload path -- shared machinery rather than descriptor data. With no spec
		// field mapped onto it the column cannot reach a payload at all, which is the safe
		// half: NetBox keeps whatever PSK it holds. `auth_psk` is already in
		// internal/netbox/do.go's secretFields, so the log-redaction half needs nothing.
		Fields: []Field{
			{Spec: "ssid", API: "ssid"},
			{Spec: "status", API: "status"},
			{Spec: "authType", API: "auth_type"},
			{Spec: "authCipher", API: "auth_cipher"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "groupRef", API: "group", Class: ClassRefOne,
				Target: netboxv1alpha1.WirelessLANGroupRef{}.TargetGVK(),
				// `group ForeignKey -> wireless.WirelessLANGroup on_delete=SET_NULL`
				// (netbox/wireless/models.py:88-94). Declared false by omission rather than
				// left unsaid: SET_NULL leaves the SSID behind with a null group, so this
				// reference cannot be the containment parent and validateContainment would
				// refuse it if it were named.
			},
			{
				Spec: "vlanRef", API: "vlan", Class: ClassRefOne,
				Target: netboxv1alpha1.VLANRef{}.TargetGVK(),
				// `vlan ForeignKey -> ipam.VLAN on_delete=PROTECT`
				// (netbox/wireless/models.py:101-107). PROTECT cascades nothing.
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
				// `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`
				// (netbox/wireless/models.py:108-114).
			},
		},

		// One line for the whole scope union, cascade table included. The table is passed
		// rather than defaulted so a new scoped kind cannot acquire a cascade by omission.
		GenericFKs: []GenericFKSpec{ScopeFK("scope", ScopeCascadesFromEvery())},

		// Four candidates, because `scope` and `tenant` are **independent** optional terms and
		// each has three states -- resolved, cleared/absent, or declared-and-not-yet-resolved.
		// That is a fuller matrix than ipam.VLAN's three candidates, where `group` and `site`
		// are alternatives rather than independent.
		//
		// The order is not a fallback. NaturalKey.Applicable matches only on a *resolved* field
		// and pins only a *never-declared* one, so exactly one candidate applies to any given
		// spec, and a term that is declared but unresolved makes every candidate inapplicable
		// -- the engine then waits rather than adopting the SSID of the same name in some other
		// scope and PATCHing this scope onto it.
		//
		// Filters: `ssid` and `scope_id` are in WirelessLANFilterSet's Meta.fields
		// (netbox/wireless/filtersets.py:86-88), `scope_type` is the
		// MultiValueContentTypeFilter that ScopedFilterSet contributes
		// (netbox/dcim/base_filtersets.py:18), and `tenant_id` comes from TenancyFilterSet.
		//
		// The pins follow the column class, which is the one thing about a null pin that cannot
		// be guessed (#206). `scope_id` is a PositiveBigIntegerField, so NullColumnNumeric and
		// `?scope_id__empty=true`; `tenant_id` is a foreign key, so NullColumnRef and the
		// `?tenant_id=null` sentinel. `scope_type` is pinned by nobody: it is an FK to
		// contenttypes.ContentType behind MultiValueContentTypeFilter, which registers neither
		// spelling, and the sentinel would make the request match nothing. Pinning the paired
		// `_id` asks the same question.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: ScopeTypeField, Spec: ScopeTypeField},
					{Filter: ScopeIDField, Spec: ScopeIDField},
					{Filter: "tenant_id", Spec: "tenantRef"},
					{Filter: "ssid", Spec: "ssid"},
				},
			},
			{
				Fields: []KeyField{
					{Filter: ScopeTypeField, Spec: ScopeTypeField},
					{Filter: ScopeIDField, Spec: ScopeIDField},
					{Filter: "ssid", Spec: "ssid"},
				},
				NullFields: []NullField{
					{Filter: "tenant_id", Spec: "tenantRef", Column: NullColumnRef},
				},
			},
			{
				Fields: []KeyField{
					{Filter: "tenant_id", Spec: "tenantRef"},
					{Filter: "ssid", Spec: "ssid"},
				},
				NullFields: []NullField{
					{Filter: ScopeIDField, Spec: "scope", Column: NullColumnNumeric},
				},
			},
			{
				Fields: []KeyField{{Filter: "ssid", Spec: "ssid"}},
				NullFields: []NullField{
					{Filter: ScopeIDField, Spec: "scope", Column: NullColumnNumeric},
					{Filter: "tenant_id", Spec: "tenantRef", Column: NullColumnRef},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// `scope` is the containment parent: all four members cascade, so an SSID gets a
		// non-controller owner reference to whichever of Region, SiteGroup, Site or Location it
		// actually resolved through, decided per pass from that member (#214). `groupRef`,
		// `vlanRef` and `tenantRef` are SET_NULL, PROTECT and PROTECT respectively, so none of
		// them is even eligible -- there is one cascading reference and one slot, and no
		// tiebreak to make.
		ContainmentRef: "scope",

		// The four columns every ChangeLoggedModel carries, plus the four scope caches. Every
		// column named in a GenericFKSpec's Cached list must also be in ReadOnly and Validate
		// enforces it at boot, so this cannot be got wrong one kind at a time. Writing `_site`
		// does not fail -- it is dropped exactly like `site`, the next read finds it unchanged,
		// and the operator PATCHes it again on every resync, forever.
		ReadOnly: append(ScopeCacheColumns(), "created", "last_updated", "url", "display"),
	}
}
