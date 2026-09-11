package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnIPSecProfileDescriptor()) }

// vpnIPSecProfileDescriptor is vpn.IPSecProfile as data.
//
// The join of the two policy kinds, and the first kind in this app with references the engine
// has to resolve before it can write anything: `ike_policy` and `ipsec_policy` are both
// `ForeignKey REQ ... on_delete=PROTECT` (docs/netbox-schema.md -> vpn.IPSecProfile). Applied
// in any order the graph converges -- a profile whose policies do not exist yet reports
// `RefsResolved=False` and waits, because a required reference that is left out of the payload
// would be a create NetBox refuses with a 400 (docs/concepts/references.md).
//
// **No ContainmentRef, and PROTECT is why.** An owner reference mirrors a server-side cascade
// (docs/decisions/0003-ownership-and-references.md rule 4); NetBox refuses to delete a policy
// that a profile still points at, so nothing on the server disappears and validateContainment
// would refuse the declaration (ErrContainmentNotCascade).
func vpnIPSecProfileDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIPSecProfile"),
		Endpoint:   "vpn/ipsec-profiles",
		ObjectType: "vpn.ipsecprofile",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.IPSecProfile is a PrimaryModel (docs/netbox-schema.md -> vpn.IPSecProfile,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		// Written as `ike_policy` and `ipsec_policy`, filtered as `ike_policy_id` and
		// `ipsec_policy_id` -- neither is a natural-key filter here, so only the write
		// spelling appears. `mode` is REQ and non-nullable, so no EmptyIsNull.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "mode", API: "mode"},
			{
				Spec: "ikePolicyRef", API: "ike_policy", Class: ClassRefOne,
				Target: netboxv1alpha1.IKEPolicyRef{}.TargetGVK(),
			},
			{
				Spec: "ipsecPolicyRef", API: "ipsec_policy", Class: ClassRefOne,
				Target: netboxv1alpha1.IPSecPolicyRef{}.TargetGVK(),
			},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, no pin: no `meta.constraints`
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.IPSecProfile.natural_keys, `[]`) and
		// `name CharField REQ UNIQUE len=100`. `name` is in IPSecProfileFilterSet's
		// `meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:287`).
		//
		// Deliberately *not* `(ike_policy, ipsec_policy)`: nothing makes that pair unique, so
		// two profiles may legitimately combine the same policies under different modes, and
		// a key that matched both would adopt whichever the API returned first.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// The four columns every ChangeLoggedModel carries.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
