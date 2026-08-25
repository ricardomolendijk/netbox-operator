package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamRoleDescriptor()) }

// ipamRoleDescriptor is ipam.Role as data.
//
// **This is not dcim.DeviceRole.** Two separate Django models, two separate endpoints --
// `ipam/roles` here, `dcim/device-roles` there (docs/netbox-schema.md, endpoint map) -- and
// the operator has a separate typed alias for each: RoleRef points here, DeviceRoleRef
// points at the other. Nor is it `ipam.IPAddress.role`, which is a *choice column* of the
// same name and is why internal/registry/ipam_ipaddress.go maps `role` as a plain value
// while ipam_prefix.go and ipam_vlan.go map `roleRef` as ClassRefOne.
//
// The Kind RoleRef has been declared against since NBO-024, so `roleRef` on a NetBoxPrefix,
// NetBoxVLAN or NetBoxASN resolves in `name` mode for the first time here.
func ipamRoleDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRole"),
		Endpoint:   "ipam/roles",
		ObjectType: "ipam.role",
		Scope:      apiextensionsv1.NamespaceScoped,

		// An OrganizationalModel mixes in both TagsMixin and CustomFieldsMixin
		// (docs/netbox-schema.md -> netbox.OrganizationalModel), so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// Decision #176: IPAM defaults to Retain. A role is pointed at by prefixes, VLANs, IP
		// ranges and ASNs through `on_delete=SET_NULL` columns, so deleting the row silently
		// clears the role on every one of them -- an edit the operator never asked for and
		// cannot see.
		RetainOnDelete: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "weight", API: "weight"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, from `slug (OrganizationalModel) SlugField REQ UNIQUE len=100`
		// (docs/netbox-schema.md -> ipam.Role). No meta.constraints on this model, and none
		// needed: a unique column identifies at most one row.
		//
		// `weight` is deliberately not part of the identity even though it appears in
		// `meta.ordering: ('weight', 'name')` and in the model's only index. An ordering is
		// not a constraint, and a role whose weight a human retuned in the UI must still be
		// found by the same lookup afterwards.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries. No `_`-prefixed cache and no
		// CounterCacheField on this model.
		ReadOnly: []string{"created", "last_updated", "url", "display"},

		// No ContainmentRef, for the same reason as ipam.RIR: this model declares no mapped
		// foreign key, so there is nothing the server cascades to mirror
		// (docs/decisions/0003-ownership-and-references.md rule 4).
	}
}
