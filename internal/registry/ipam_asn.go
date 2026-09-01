package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamASNDescriptor()) }

// ipamASNDescriptor is ipam.ASN as data.
//
// The one Kind in the catalogue whose identity is a *number* rather than a name or a slug:
// ipam.ASN declares no `name` and no `slug` at all, and `asn ASNField REQ UNIQUE`
// (docs/netbox-schema.md -> ipam.ASN) is both the whole of the object's meaning and the whole
// of its lookup.
func ipamASNDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxASN"),
		Endpoint:   "ipam/asns",
		ObjectType: "ipam.asn",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.ASN is a PrimaryModel (docs/netbox-schema.md -> ipam.ASN,
		// bases: ContactsMixin, PrimaryModel), which mixes in both TagsMixin and
		// CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain. An ASN is an allocation from a registry --
		// deleting the row destroys the record of who holds it, and a fresh row with the same
		// number is a different object with a different id.
		RetainOnDelete: true,

		Fields: []Field{
			{Spec: "asn", API: "asn"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "rirRef", API: "rir", Class: ClassRefOne,
				Target: netboxv1alpha1.RIRRef{}.TargetGVK(),
			},
			// ipam.Role, not dcim.DeviceRole and not the `role` choice column ipam.IPAddress
			// carries. Three different things with one name, and RoleRef is the alias that
			// pins which one (internal/registry/ipam_role.go).
			{
				Spec: "roleRef", API: "role", Class: ClassRefOne,
				Target: netboxv1alpha1.RoleRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
		},

		// One candidate, and it is a real database guarantee rather than a convention:
		// `asn ASNField REQ UNIQUE` (docs/netbox-schema.md -> ipam.ASN). The table's only
		// other line is `meta.ordering: ['asn']`.
		//
		// `rir_id` is deliberately *not* a second filter, even though it is required. A
		// unique column already matches at most one row, so adding the RIR could only ever
		// narrow a match that cannot be ambiguous -- while it would make the lookup wait on a
		// reference the identity does not depend on, and would fail to find an ASN a human
		// filed under a different registry. That is a case for a PATCH, not for a second
		// object.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "asn", Spec: "asn"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. No `_`-prefixed cache and no
		// CounterCacheField on this model; `site_count` and `provider_count` on the
		// serializer are RelatedObjectCountFields, which no spec field maps onto.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// No ContainmentRef, and the three foreign keys are why rather than an omission.
		// `rir` and `tenant` are `on_delete=PROTECT`, so NetBox *refuses* to delete either
		// while this ASN exists and there is no server-side deletion for an owner reference
		// to mirror; `role` is `on_delete=SET_NULL`, which is the same mistake in the other
		// direction -- the row survives with the column cleared, and a cluster-side cascade
		// would have deleted the CR that described it
		// (docs/decisions/0003-ownership-and-references.md rule 4, ErrContainmentNotCascade).
	}
}
