package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnIKEPolicyDescriptor()) }

// vpnIKEPolicyDescriptor is vpn.IKEPolicy as data.
//
// **The kind with the secret, and the field map is where its absence is enforced.**
// `preshared_key` is a writable `TextField` on this model
// (hack/testdata/ir-4.6.8.json.gz -> vpn.IKEPolicy.write_path), and there is deliberately no
// Field entry for it: a secret may never be inline in a spec, the only permitted shape is a
// SecretRef, and the engine has no FieldClass that reads a Secret into a payload -- that is
// #241, and it is engine surgery. An unmapped column cannot reach a payload and cannot be
// compared, so the operator neither writes nor clears the key NetBox holds. The reasoning
// lives in full on api/v1alpha1/vpn_ikepolicy.go; the gap is recorded in the `notes` section
// of `hack/coverage-exclusions.yaml` -- as a gap, not an excuse, exactly as
// `ipam.FHRPGroup.auth_key` and `wireless.WirelessLAN.auth_psk` are -- and `preshared_key` is
// already in internal/netbox/do.go's `secretFields`, so the value NetBox *returns* is masked
// in every request and response line at every level.
//
// The other thing this kind proves is that a to-many reference costs nothing but data a
// second time: `proposals` is one ClassRefMany entry, and M2MFields() derives the
// order-independent comparison from it (the ipam.VRF shape).
func vpnIKEPolicyDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxIKEPolicy"),
		Endpoint:   "vpn/ike-policies",
		ObjectType: "vpn.ikepolicy",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.IKEPolicy is a PrimaryModel (docs/netbox-schema.md -> vpn.IKEPolicy, bases),
		// which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		// `mode` needs EmptyIsNull: the column is `blank=True, null=True` and NetBox returns
		// `null` rather than `""` for an unset choice (#170). `version` is an integer with a
		// Django default and needs nothing.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "version", API: "version"},
			{Spec: "mode", API: "mode", EmptyIsNull: true},
			{
				Spec: "proposals", API: "proposals", Class: ClassRefMany,
				Target: netboxv1alpha1.IKEProposalRef{}.TargetGVK(),
			},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate, no pin: no `meta.constraints` on the model
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.IKEPolicy.natural_keys, `[]`) and
		// `name CharField REQ UNIQUE len=100`. `name` is in IKEPolicyFilterSet's `meta.fields`
		// (NetBox 4.6.8, `netbox/vpn/filtersets.py:187`).
		//
		// `proposals` is deliberately not part of it, and could not be: a natural key filters
		// on scalars, an M2M matches a superset rather than an identity, and the ids are not
		// known until every element resolves.
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "name", Spec: "name"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: `proposals` is a ManyToManyField, which cascades nothing, and
		// `ipsec_profile` points the other way with PROTECT.

		// The four columns every ChangeLoggedModel carries. `preshared_key` is *not* listed
		// here: ReadOnly guards the field map against writing a column NetBox refuses, and
		// this column is writable -- the operator declines to write it, which is a different
		// statement and belongs in the field map's absence rather than in this list.
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
