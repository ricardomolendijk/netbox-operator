package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnIPSecPolicyDescriptor()) }

// vpnIPSecPolicyDescriptor is vpn.IPSecPolicy as data.
//
// The phase 2 counterpart of vpn.IKEPolicy, and the one without a secret: the whole `vpn` app
// carries exactly one secret-valued column and it is `vpn.IKEPolicy.preshared_key`
// (docs/netbox-schema.md, the six crypto and tunnel models). So this kind ships complete
// while its IKE twin does not.
func vpnIPSecPolicyDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIPSecPolicy"),
		Endpoint:   "vpn/ipsec-policies",
		ObjectType: "vpn.ipsecpolicy",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.IPSecPolicy is a PrimaryModel (docs/netbox-schema.md -> vpn.IPSecPolicy,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		// `pfsGroup` needs no EmptyIsNull: it is an integer, so the spec field is a pointer
		// and an omitted pointer is an omitted key rather than an empty string that has to be
		// translated. EmptyIsNull exists for the columns whose empty *value* is `""` on the
		// wire and `null` in NetBox (#170), which is a string problem.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{
				Spec: "proposals", API: "proposals", Class: ClassRefMany,
				Target: netboxv1alpha1.IPSecProposalRef{}.TargetGVK(),
			},
			{Spec: "pfsGroup", API: "pfs_group"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, no pin: no `meta.constraints`
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.IPSecPolicy.natural_keys, `[]`) and
		// `name CharField REQ UNIQUE len=100`. `name` is in IPSecPolicyFilterSet's
		// `meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:257`).
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: `proposals` is a ManyToManyField, which cascades nothing.

		// The four columns every ChangeLoggedModel carries.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
