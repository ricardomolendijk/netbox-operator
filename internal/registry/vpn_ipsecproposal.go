package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnIPSecProposalDescriptor()) }

// vpnIPSecProposalDescriptor is vpn.IPSecProposal as data.
//
// The phase 2 counterpart of vpn.IKEProposal, and the one place the two models differ is
// nullability: `encryption_algorithm` is `REQ` there and `blank=True, null=True` here
// (docs/netbox-schema.md -> vpn.IKEProposal, vpn.IPSecProposal), so both algorithm columns
// below carry EmptyIsNull and neither does on the IKE side.
func vpnIPSecProposalDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIPSecProposal"),
		Endpoint:   "vpn/ipsec-proposals",
		ObjectType: "vpn.ipsecproposal",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.IPSecProposal is a PrimaryModel (docs/netbox-schema.md -> vpn.IPSecProposal,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		// Both enum columns are nullable, so both are cleared with `null` rather than `""`
		// (#170). The two `sa_lifetime_*` columns are independent nullable integers: the
		// operator writes what the spec declares and infers neither from the other.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "encryptionAlgorithm", API: "encryption_algorithm", EmptyIsNull: true},
			{Spec: "authenticationAlgorithm", API: "authentication_algorithm", EmptyIsNull: true},
			{Spec: "saLifetimeSeconds", API: "sa_lifetime_seconds"},
			{Spec: "saLifetimeData", API: "sa_lifetime_data"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, no pin: no `meta.constraints`
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.IPSecProposal.natural_keys, `[]`) and
		// `name CharField REQ UNIQUE len=100`. `name` is in IPSecProposalFilterSet's
		// `meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:221`).
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: no foreign key on the model bar `owner`.

		// The four columns every ChangeLoggedModel carries.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
