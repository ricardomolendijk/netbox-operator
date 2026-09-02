package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnIKEProposalDescriptor()) }

// vpnIKEProposalDescriptor is vpn.IKEProposal as data.
//
// The first of the `vpn` app's five crypto-catalogue kinds, and the plainest: four scalars,
// two enums, no reference of any kind and no secret. Every column in
// `hack/testdata/ir-4.6.8.json.gz -> vpn.IKEProposal.write_path` is either mapped below, in
// ReadOnly, or `owner` -- a `ForeignKey -> users.Owner`, and the `users` app has no Kind, so
// internal/resolver could not dispatch a reference to it (the RackReservation.userID finding,
// #250).
func vpnIKEProposalDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIKEProposal"),
		Endpoint:   "vpn/ike-proposals",
		ObjectType: "vpn.ikeproposal",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.IKEProposal is a PrimaryModel (docs/netbox-schema.md -> vpn.IKEProposal, bases),
		// which mixes in both TagsMixin and CustomFieldsMixin, so it carries the whole
		// provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `authenticationAlgorithm` needs EmptyIsNull, because the column is `blank=True,
		// null=True` and NetBox's serializer returns `null` rather than `""` for an unset
		// choice -- an emptied field sent as `""` would differ from the value read back on
		// every pass (#170). The other three enum-or-scalar columns are REQ or non-nullable
		// and need nothing.
		//
		// `group` and `saLifetime` need no field class either: both are integers, compared as
		// values, and NetBox returns a choice as {"value","label"} which
		// internal/netbox/drift.go's unwrapNested already reduces by the absence of an "id"
		// key.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "authenticationMethod", API: "authentication_method"},
			{Spec: "encryptionAlgorithm", API: "encryption_algorithm"},
			{Spec: "authenticationAlgorithm", API: "authentication_algorithm", EmptyIsNull: true},
			{Spec: "group", API: "group"},
			{Spec: "saLifetime", API: "sa_lifetime"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, no pin. The model declares no `meta.constraints` at all
		// (docs/netbox-schema.md -> vpn.IKEProposal; hack/testdata/ir-4.6.8.json.gz ->
		// vpn.IKEProposal.natural_keys, `[]`), so the identity comes from the one column that
		// carries a UNIQUE: `name CharField REQ UNIQUE len=100`. The ipam.RouteTarget
		// derivation.
		//
		// The filter is registered: `name` is in IKEProposalFilterSet's `meta.fields`
		// (NetBox 4.6.8, `netbox/vpn/filtersets.py:143`).
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: the model has no foreign key bar `owner`, and the policies that
		// point *at* a proposal do so through a ManyToManyField, which cascades nothing.
		//
		// RetainOnDelete is left false: a crypto proposal is configuration a manifest
		// recreates, not allocated state (#176, docs/concepts/deletion.md).

		// The four columns every ChangeLoggedModel carries and the operator must never write
		// (docs/netbox-schema.md, preamble). This model's serializer returns no counter.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
