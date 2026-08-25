package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamFHRPGroupDescriptor()) }

// ipamFHRPGroupDescriptor is ipam.FHRPGroup as data.
//
// The Kind IPAssignment.fhrpGroupRef and ServiceParent.fhrpGroupRef have been declared
// against since NBO-025, so `ipam.fhrpgroup` becomes resolvable in `name` mode here.
//
// **`auth_key` is not in the field map**, and its absence is a decision rather than an
// oversight. plan.md §15 permits the value only as `spec.authKeySecretRef`, and reading a
// Secret into a NetBox payload field is a capability the engine does not have: there is no
// FieldClass for it and internal/reconciler/payload.go has nowhere to fetch one from. Adding
// one is a shared-logic change, which adding a Kind is not allowed to be
// (docs/concepts/descriptor.md), so the column is simply never written -- and therefore never
// cleared either. `auth_key` is in internal/netbox/do.go's redaction set regardless, because
// NetBox *returns* it on every read of this endpoint and a debug-level response log would
// otherwise print a pre-shared key.
//
// Two GenericRelations, `ip_addresses` and `services`, are absent for the reason
// ipam.VLAN's `l2vpn_terminations` is: a reverse accessor is a read-only view of somebody
// else's foreign key, so there is nothing to write and nothing to exclude
// (docs/concepts/generic-refs.md).
func ipamFHRPGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxFHRPGroup"),
		Endpoint:   "ipam/fhrp-groups",
		ObjectType: "ipam.fhrpgroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.FHRPGroup is a PrimaryModel (docs/netbox-schema.md -> ipam.FHRPGroup, bases),
		// which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain. Deleting a group cascades in NetBox --
		// `ipam.FHRPGroupAssignment.group` is `on_delete=CASCADE` -- so it takes every
		// assignment with it, including ones no CR describes.
		RetainOnDelete: true,

		// `groupId` -> `group_id` and `authType` -> `auth_type` are the entries that earn an
		// explicit table: NetBox ignores a field name it does not know rather than rejecting
		// it, so `groupId` sent verbatim would write nothing and report success.
		Fields: []Field{
			{Spec: "groupId", API: "group_id"},
			{Spec: "protocol", API: "protocol"},
			{Spec: "name", API: "name"},
			{Spec: "authType", API: "auth_type"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, and the honest provenance is that **ipam.FHRPGroup carries no
		// meta.constraints at all**: its only table-level lines are
		// `meta.ordering: ['protocol', 'group_id', 'pk']` and
		// `meta.indexes: (models.Index(fields=('protocol', 'group_id', 'id')),)` -- an
		// ordering and one non-unique index (docs/netbox-schema.md -> ipam.FHRPGroup).
		// `(protocol, group_id)` is the ordering tuple and a *convention*: two VRRP groups
		// with VRID 10 on two unrelated segments are a perfectly legal server state, so more
		// than one match is reported as a Conflict naming the candidate ids and nothing is
		// written.
		//
		// `name` is not in the key even though it is the friendliest identifier, because it is
		// nullable: `name CharField len=100` with no REQ, so a group with no name would have
		// no identity at all.
		NaturalKeys: []NaturalKey{{
			Fields: []KeyField{
				{Filter: "protocol", Spec: "protocol"},
				{Filter: "group_id", Spec: "groupId"},
			},
		}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. No `_`-prefixed cache and no
		// CounterCacheField on this model.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// No ContainmentRef. ipam.FHRPGroup declares no foreign key of its own besides
		// `owner (OwnerMixin)`, which the operator does not map -- the cascade in this family
		// runs the other way, from the group down to its assignments
		// (internal/registry/ipam_fhrpgroupassignment.go).
	}
}
