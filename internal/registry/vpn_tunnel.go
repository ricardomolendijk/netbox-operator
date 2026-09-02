package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnTunnelDescriptor()) }

// vpnTunnelDescriptor is vpn.Tunnel as data.
//
// The one kind in this change whose identity is conditional, and the MPTT shape on a model
// that is not a tree. See vpnTunnelKeys for the derivation.
//
// **No terminations.** `vpn.TunnelTermination` is a separate model with its own endpoint and
// its own identity over a generic foreign key, and it is not part of this change -- neither as
// a Kind nor as an inline child list here. `terminations` is not a column on this model at all
// (docs/netbox-schema.md -> vpn.Tunnel): it is the reverse accessor of
// `TunnelTermination.tunnel`, which is where the write happens.
func vpnTunnelDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxTunnel"),
		Endpoint:   "vpn/tunnels",
		ObjectType: "vpn.tunnel",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.Tunnel is a PrimaryModel (docs/netbox-schema.md -> vpn.Tunnel, bases), which
		// mixes in both TagsMixin and CustomFieldsMixin. ContactsMixin contributes a
		// GenericRelation only.
		Taggable:        true,
		CustomFieldable: true,

		Fields: vpnTunnelFields(),

		NaturalKeys: vpnTunnelKeys(),

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef. Every foreign key on the model is PROTECT -- `group`,
		// `ipsec_profile`, `tenant` (docs/netbox-schema.md -> vpn.Tunnel) -- so nothing on the
		// server side disappears when a parent does and there is nothing for an owner
		// reference to mirror (docs/decisions/0003-ownership-and-references.md rule 4).
		// validateContainment would refuse it (ErrContainmentNotCascade), and rightly: NetBox
		// refuses to delete a tunnel group that still has tunnels, so a cluster-side cascade
		// would delete the CR and leave the row.
		//
		// RetainOnDelete is left false: a tunnel is configuration a manifest recreates, not
		// allocated state (#176, docs/concepts/deletion.md).

		// The four columns every ChangeLoggedModel carries, plus the counter the serializer
		// returns and the API refuses: `terminations_count` is in the write path
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.Tunnel.write_path) and read-only there.
		ReadOnly: []string{"created", "last_updated", "url", "display", "terminations_count"},
	}
}

// vpnTunnelFields is this kind's spec-to-column map.
//
// `groupRef` -> `group` and `ipsecProfileRef` -> `ipsec_profile` are pairs a
// camelCase-to-snake_case convention would get wrong or only get right by luck, and NetBox
// ignores a field name it does not know rather than rejecting it -- so a wrong spelling would
// write nothing and report success.
//
// `status` and `encapsulation` need no field class: NetBox returns a choice as
// {"value","label"} and takes the bare value, which internal/netbox/drift.go's unwrapNested
// already reduces by the absence of an "id" key. Neither column is nullable, so neither needs
// EmptyIsNull; `tunnel_id` is a nullable integer, whose spec field is a pointer and whose
// omission is an omitted key rather than an empty string.
func vpnTunnelFields() []Field {
	return []Field{
		{Spec: "name", API: "name"},
		{Spec: "status", API: "status"},
		{
			Spec: "groupRef", API: "group", Class: ClassRefOne,
			Target: netboxv1alpha1.TunnelGroupRef{}.TargetGVK(),
		},
		{Spec: "encapsulation", API: "encapsulation"},
		{
			Spec: "ipsecProfileRef", API: "ipsec_profile", Class: ClassRefOne,
			Target: netboxv1alpha1.IPSecProfileRef{}.TargetGVK(),
		},
		{
			Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
			Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
		},
		{Spec: "tunnelId", API: "tunnel_id"},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
	}
}

// vpnTunnelKeys are the lookup candidates, in priority order.
//
// Two, and only ever one of them applicable to a given object, because they disagree about
// whether `groupRef` is declared. Both come straight from meta.constraints
// (docs/netbox-schema.md -> vpn.Tunnel; hack/testdata/ir-4.6.8.json.gz ->
// vpn.Tunnel.natural_keys):
//
//		UniqueConstraint(fields=('group', 'name'), name='..._group_name')
//		UniqueConstraint(fields=('name',),         name='..._name', condition=Q(group__isnull=True))
//
//	 1. `(group_id, name)` -- the database constraint, used by a tunnel that names a group.
//	 2. `name` with `group_id` pinned null -- the second constraint, used by a tunnel that names
//	    none. The pin is what makes it safe: without it the lookup would match a tunnel of that
//	    name inside somebody else's group and the follow-up PATCH would move it out, and
//	    NaturalKey.Applicable offers this candidate only while `groupRef` is *undeclared*, so a
//	    tunnel whose group has not been created yet waits instead of falling through (NBO-015).
//
// `?group_id=null` is the wire spelling of NullColumnRef. The committed IR marks this
// candidate `unusable` with the reason *"group_id registers no `empty` suffix
// (ModelMultipleChoiceFilter -> FILTER_NEGATION_LOOKUP_MAP)"*, and that reason is about the
// `__empty` suffix, which is not the spelling used here: NetBox's ModelMultipleChoiceFilter
// accepts the literal `null` sentinel, which is how `dcim.Location.parent_id` and
// `dcim.Rack.location_id` are already pinned (#216, internal/registry/dcim_rack.go,
// internal/registry/dcim_location.go). `group_id` is the same filter class
// (hack/testdata/ir-4.6.8.json.gz -> vpn.Tunnel.filters.group_id, `ModelMultipleChoiceFilter`,
// NetBox 4.6.8 `netbox/vpn/filtersets.py:40`), so the same spelling reaches it. The audit
// reaches the same conclusion independently and writes it down: docs/coverage.md's
// "natural-key candidates the IR calls unusable" table records this constraint as
// `usable via #216`, one of the seventeen. Emitting `__empty` instead would be the #206
// defect, which is why NullColumn exists and why the choice is made in one place.
//
// Both candidates are constraint-backed rather than conventions, so a second match is not
// possible: `name` also carries a column-level UNIQUE at 4.6.8 (docs/netbox-schema.md ->
// vpn.Tunnel, `name CharField REQ UNIQUE len=100`; hack/testdata/ir-4.6.8.json.gz ->
// vpn.Tunnel.fields, `"sql": {"unique": true}`), which makes the pair strictly narrower than
// the column. A tunnel renamed into a name another tunnel holds is NetBox's own 409, reported
// as a Conflict.
//
// `tunnel_id` is deliberately not a candidate: no constraint names it, so two tunnels may
// carry the same number and adopting by it would rewrite the wrong tunnel's group and
// encapsulation.
//
// Both filters are registered on TunnelFilterSet (NetBox 4.6.8, `netbox/vpn/filtersets.py:40`):
// `name` is in `meta.fields` and `group_id` is declared.
func vpnTunnelKeys() []NaturalKey {
	return []NaturalKey{
		{
			Fields: []KeyField{
				{Filter: "group_id", Spec: "groupRef"},
				{Filter: "name", Spec: "name"},
			},
		},
		{
			Fields: []KeyField{{Filter: "name", Spec: "name"}},
			NullFields: []NullField{
				{Filter: "group_id", Spec: "groupRef", Column: NullColumnRef},
			},
		},
	}
}
