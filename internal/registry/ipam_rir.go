package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamRIRDescriptor()) }

// ipamRIRDescriptor is ipam.RIR as data.
//
// An OrganizationalModel with one column of its own, and the root of this milestone's
// allocation registry: ipam.ASN, ipam.ASNRange and ipam.Aggregate all declare
// `rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT` (docs/netbox-schema.md), so nothing in
// that group can be created before an RIR exists.
func ipamRIRDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRIR"),
		Endpoint:   "ipam/rirs",
		ObjectType: "ipam.rir",
		Scope:      apiextensionsv1.NamespaceScoped,

		// An OrganizationalModel mixes in both TagsMixin and CustomFieldsMixin
		// (docs/netbox-schema.md -> netbox.OrganizationalModel), so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain. Deleting an RIR is refused by NetBox while
		// anything points at it, and recreating one gives every aggregate and ASN underneath
		// a different id -- so the CR going away must not take the row with it.
		RetainOnDelete: true,

		// `isPrivate` -> `is_private` is the entry that earns an explicit table: NetBox
		// ignores a field name it does not know rather than rejecting it, so `isPrivate`
		// sent verbatim would write nothing and report success.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "isPrivate", API: "is_private"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, from the column-level `REQ UNIQUE` the base class declares:
		// `slug (OrganizationalModel) SlugField REQ UNIQUE len=100`
		// (docs/netbox-schema.md -> ipam.RIR). ipam.RIR carries no meta.constraints, and it
		// does not need any -- a unique column identifies at most one row on its own, with no
		// conditional constraint to express as a second candidate and no parent to pin to
		// null.
		//
		// `name` carries the same UNIQUE and deliberately is not a second candidate: a kind
		// gets one identity, and a second candidate on an equally-unique column would only
		// ever be reached when the first matched nothing -- which for a unique column means
		// the object does not exist and should be created.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. No `_`-prefixed cache and no
		// CounterCacheField on this model; the serializer's `aggregate_count` and
		// `asn_count` are RelatedObjectCountFields -- returned, never accepted -- and are not
		// listed for the same reason ipam.VRF's `prefix_count` is not: this list guards the
		// field map, and no spec field maps onto them.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// No ContainmentRef. ipam.RIR declares no foreign key at all besides
		// `owner (OwnerMixin) -> users.Owner on_delete=PROTECT`, which the operator does not
		// map, so there is no FK the server cascades and therefore no containment parent
		// (docs/decisions/0003-ownership-and-references.md rule 4). That is a consequence
		// rather than a gap.
	}
}
