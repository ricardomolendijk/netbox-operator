package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamASNRangeDescriptor()) }

// ipamASNRangeDescriptor is ipam.ASNRange as data.
//
// The one entry in this app whose digest line reads `shadows inherited: name
// (OrganizationalModel), slug (OrganizationalModel)` -- the model redeclares both columns
// rather than inheriting them (docs/netbox-schema.md -> ipam.ASNRange). It changes nothing
// about the descriptor, and it is worth naming anyway: both redeclarations carry
// `REQ UNIQUE len=100`, exactly as the base class does, so the shadowing is a NetBox
// implementation detail and not a difference in identity.
func ipamASNRangeDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxASNRange"),
		Endpoint:   "ipam/asn-ranges",
		ObjectType: "ipam.asnrange",
		Scope:      apiextensionsv1.NamespaceScoped,

		// An OrganizationalModel mixes in both TagsMixin and CustomFieldsMixin, so it
		// carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain.
		RetainOnDelete: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "start", API: "start"},
			{Spec: "end", API: "end"},
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

		// One candidate, from `slug SlugField REQ UNIQUE len=100` (docs/netbox-schema.md ->
		// ipam.ASNRange). `name` carries the same UNIQUE and is deliberately not a second
		// candidate, for the reason every catalogue kind here gives: a kind gets one
		// identity, and on a unique column a second candidate is only reached when the object
		// does not exist.
		//
		// `(start, end)` is not a candidate either. Nothing in NetBox makes the pair unique --
		// the table has no meta.constraints at all, only `meta.ordering: ('name',)` -- so two
		// ranges may legitimately cover the same span under different names, and keying on it
		// would adopt one for the other.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. The serializer's `asn_count` is a
		// RelatedObjectCountField, which no spec field maps onto.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// No ContainmentRef. `rir` and `tenant` are both `on_delete=PROTECT`
		// (docs/netbox-schema.md -> ipam.ASNRange), so neither cascades: NetBox refuses to
		// delete either while this range exists. An owner reference on a PROTECT-ed FK would
		// promise a cluster-side cascade the server declines -- garbage collection removes the
		// CR, the finalizer's DELETE is refused, and the row outlives the object
		// (ErrContainmentNotCascade).
		//
		// The `tenant` half is the most confusing failure mode in the design and is worth
		// stating where the fact lives: a NetBoxASNRange holding a tenant **blocks that
		// tenant's deletion**, and the tenant reports `Deleting=False, Reason=Protected`
		// naming this range (docs/concepts/deletion.md).
	}
}
