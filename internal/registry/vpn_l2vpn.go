package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnL2VPNDescriptor()) }

// vpnL2VPNDescriptor is vpn.L2VPN as data.
//
// The second kind with `ipam.RouteTarget` on both ends of a pair of many-to-many relations,
// after ipam.VRF, and it is the same two ClassRefMany entries with nothing else added:
// M2MFields() derives the order-independent comparison from them, internal/resolver resolves
// each element, and internal/reconciler writes the sorted id list. No engine change, which is
// the claim NBO-022 made and this kind holds it to a second time.
//
// **No terminations.** `vpn.L2VPNTermination` is a separate model whose identity is
// `(assigned_object_type, assigned_object_id)` over a generic foreign key, and it is not part
// of this change. `terminations` is not a column on this model: it is the reverse accessor of
// `L2VPNTermination.l2vpn`.
func vpnL2VPNDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxL2VPN"),
		Endpoint:   "vpn/l2vpns",
		ObjectType: "vpn.l2vpn",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.L2VPN is a PrimaryModel (docs/netbox-schema.md -> vpn.L2VPN, bases), which mixes
		// in both TagsMixin and CustomFieldsMixin. ContactsMixin contributes a GenericRelation
		// only.
		Taggable:        true,
		CustomFieldable: true,

		// `type` and `status` need no field class: NetBox returns a choice as
		// {"value","label"} and takes the bare value, and neither column is nullable.
		// `identifier` is a nullable integer, so its spec field is a pointer and an omitted
		// pointer is an omitted key.
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "type", API: "type"},
			{Spec: "status", API: "status"},
			{Spec: "identifier", API: "identifier"},
			{
				Spec: "importTargets", API: "import_targets", Class: ClassRefMany,
				Target: netboxv1alpha1.RouteTargetRef{}.TargetGVK(),
			},
			{
				Spec: "exportTargets", API: "export_targets", Class: ClassRefMany,
				Target: netboxv1alpha1.RouteTargetRef{}.TargetGVK(),
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate. The model declares no `meta.constraints`
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.L2VPN.natural_keys, `[]`) and *two* columns
		// carry UNIQUE -- `name` and `slug` (docs/netbox-schema.md -> vpn.L2VPN) -- so either
		// would identify one row. `slug` is the candidate for the reason it is on every
		// OrganizationalModel: a kind gets one identity and the slug is the stable one, so a
		// rename updates the object NetBox already holds.
		//
		// There is deliberately no `name` fallback. Candidates are tried in order and the
		// engine falls through when one matches nothing, so a `name` second candidate would be
		// reached exactly when the slug has changed -- and it would adopt the object whose
		// slug disagrees and PATCH this slug onto it, renaming somebody else's L2VPN. The
		// ipam.VRF reasoning, with the same conclusion and no pin to soften it.
		//
		// `slug` is in L2VPNFilterSet's `meta.fields` (NetBox 4.6.8,
		// `netbox/vpn/filtersets.py:332`).
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: `tenant` is PROTECT and the two route-target relations are
		// ManyToManyFields, which cascade nothing.

		// The four columns every ChangeLoggedModel carries. This model's serializer returns no
		// counter in its write path (hack/testdata/ir-4.6.8.json.gz -> vpn.L2VPN.write_path).
		ReadOnly: []string{"created", "last_updated", "url", "display"},
	}
}
