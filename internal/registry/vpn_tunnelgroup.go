package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(vpnTunnelGroupDescriptor()) }

// vpnTunnelGroupDescriptor is vpn.TunnelGroup as data.
//
// The kind with no columns of its own:
//
//	## vpn.TunnelGroup   (vpn/models/tunnels.py)
//	   bases: ContactsMixin, OrganizationalModel
//	   (no own columns — every field is inherited from ContactsMixin, OrganizationalModel)
//	   meta.ordering: ('name',)
//
// (docs/netbox-schema.md -> vpn.TunnelGroup.) #59's ticket calls this a "schema gap -- an
// endpoint but no model entry"; at 4.6.8 the entry exists and says `OrganizationalModel`,
// which is not a gap but an answer: the field list is the base class's, and the identity is
// its column-level unique `slug`. The dcim.RackGroup derivation.
//
// `ContactsMixin` contributes a `GenericRelation` only -- a reverse accessor, never on the
// write path (hack/testdata/ir-4.6.8.json.gz -> vpn.TunnelGroup.fields, `contacts`) -- so it
// changes nothing here.
func vpnTunnelGroupDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxTunnelGroup"),
		Endpoint:   "vpn/tunnel-groups",
		ObjectType: "vpn.tunnelgroup",
		Scope:      apiextensionsv1.NamespaceScoped,

		// vpn.TunnelGroup is an OrganizationalModel (docs/netbox-schema.md -> vpn.TunnelGroup,
		// bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "slug", API: "slug"},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
		},

		// One candidate. `name` is UNIQUE too and is deliberately not a second one: a kind
		// gets one identity, and reaching a `name` fallback would mean adopting a group whose
		// slug disagrees and PATCHing this slug onto it. The dcim.RackRole derivation.
		//
		// `slug` is in TunnelGroupFilterSet's `meta.fields` (NetBox 4.6.8,
		// `netbox/vpn/filtersets.py:32`).
		NaturalKeys: []NaturalKey{{Fields: []KeyField{{Filter: "slug", Spec: "slug"}}}},

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef: the model has no foreign key bar `owner`. `Tunnel.group` points
		// at it with `on_delete=PROTECT`, so deleting a group in use is refused rather than
		// cascading.

		// The four columns every ChangeLoggedModel carries, plus the CounterCacheField the
		// serializer returns and the API refuses -- `tunnel_count` is in the write path
		// (hack/testdata/ir-4.6.8.json.gz -> vpn.TunnelGroup.write_path) and read-only there,
		// which is exactly the failure this list exists for: NetBox maintains it from the
		// child rows and ignores an attempt to set it, so writing it does not fail -- it
		// silently no-ops, and the operator PATCHes forever.
		ReadOnly: []string{"created", "last_updated", "url", "display", "tunnel_count"},
	}
}
