package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(ipamServiceDescriptor()) }

// serviceParentFK is the `(parent_object_type, parent_object_id)` pair, with the three members
// NetBox accepts and the cascade stated per member.
//
// All three cascade, and by one mechanism: dcim.Device, virtualization.VirtualMachine and
// ipam.FHRPGroup each declare a `services` GenericRelation (docs/netbox-schema.md), so
// deleting any of the three deletes the services parented to it. Stated per member because
// that is where the fact lives (#214) rather than because they disagree here.
//
// No Cached columns: this pair maintains no denormalised columns.
func serviceParentFK() GenericFKSpec {
	cascades := true

	return GenericFKSpec{
		TypeField:    "parent_object_type",
		IDField:      "parent_object_id",
		Spec:         "parent",
		AllowedTypes: []string{"dcim.device", "virtualization.virtualmachine", "ipam.fhrpgroup"},
		Members: []GenericFKMember{
			{
				Spec:            "deviceRef",
				Target:          netboxv1alpha1.DeviceRef{}.TargetGVK(),
				CascadeOnDelete: &cascades,
			},
			{
				Spec:            "virtualMachineRef",
				Target:          netboxv1alpha1.VirtualMachineRef{}.TargetGVK(),
				CascadeOnDelete: &cascades,
			},
			{
				Spec:            "fhrpGroupRef",
				Target:          netboxv1alpha1.FHRPGroupRef{}.TargetGVK(),
				CascadeOnDelete: &cascades,
			},
		},
	}
}

// ipamServiceDescriptor is ipam.Service as data.
//
// The only Kind so far that carries a polymorphic pair **and** a many-to-many **and** an
// ordered array, all three on one object, and it is still entirely data: a generic FK, a
// ClassRefMany field and a ClassArray field, with the one-line controller every other Kind
// has.
func ipamServiceDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxService"),
		Endpoint:   "ipam/services",
		ObjectType: "ipam.service",
		Scope:      apiextensionsv1.NamespaceScoped,

		// ipam.Service is a PrimaryModel (docs/netbox-schema.md -> ipam.Service,
		// bases: ContactsMixin, ServiceBase, PrimaryModel), which mixes in both TagsMixin and
		// CustomFieldsMixin, so it carries the whole provenance stamp.
		Taggable:        true,
		CustomFieldable: true,

		// `parent` is absent from this table on purpose: one spec field writing two columns is
		// a GenericFKSpec, not a Field. So is `_ports_lowest` -- NetBox recomputes it from
		// `ports` on every save (netbox/ipam/models/services.py:41-47), and it appears in
		// ReadOnly below.
		//
		// `ports` is ClassArray and `ipAddresses` is ClassRefMany, and the difference is the
		// point rather than a detail. Both arrive as JSON lists; a Postgres ArrayField's order
		// is data and a many-to-many's is not, so comparing the array order-independently
		// would miss a reordering the user asked for and comparing the M2M order-sensitively
		// would PATCH the same list forever (internal/netbox/drift.go).
		Fields: []Field{
			{Spec: "name", API: "name"},
			{Spec: "protocol", API: "protocol"},
			{Spec: "ports", API: "ports", Class: ClassArray},
			{Spec: "description", API: "description"},
			{Spec: "comments", API: "comments"},
			{
				Spec: "ipAddresses", API: "ipaddresses", Class: ClassRefMany,
				Target: netboxv1alpha1.IPAddressRef{}.TargetGVK(),
			},
		},

		// One candidate, and the honest provenance is that **ipam.Service carries no
		// meta.constraints at all**: its table-level lines are
		// `meta.ordering: ('protocol', '_ports_lowest', 'id')` and two non-unique indexes
		// (docs/netbox-schema.md -> ipam.Service). `(parent, name, protocol)` is therefore a
		// convention, and two services agreeing on all three are a legal server state
		// reported as a Conflict naming the candidate ids rather than resolved by taking the
		// first.
		//
		// The parent halves are pinned by *column* name, which is what makes this candidate
		// safe at all: `?name=ssh&protocol=tcp` alone matches the SSH service on every device
		// in the NetBox, so the first reconcile would adopt somebody else's row and the
		// follow-up PATCH would reparent it. reconciler.applyGenericFK writes the resolved
		// pair into the decoded spec under these two names, the same mechanism
		// ipam.VLANGroup's identity uses (#180). Both columns are `REQ`, so there is no null
		// variant and none is possible.
		//
		// Server-side these filters exist:
		// `ServiceFilterSet.Meta.fields = ('id', 'name', 'protocol', 'description',
		// 'parent_object_type', 'parent_object_id')`, with
		// `parent_object_type = MultiValueContentTypeFilter()`
		// (netbox/ipam/filtersets.py:1239-1289).
		//
		// **`ports` is deliberately not a filter.** A query parameter carries one value, and
		// NetBox's only port filter is `port = NumericArrayFilter(field_name='ports',
		// lookup_expr='contains')` (netbox/ipam/filtersets.py:1282-1285) -- a single-value
		// containment test that cannot express "these ports and no others". Leaving it out is
		// what makes reordering or editing `ports` incapable of producing a *second* object:
		// the lookup finds the same row and the difference becomes a PATCH.
		NaturalKeys: []NaturalKey{{
			Fields: []KeyField{
				{Filter: "parent_object_type", Spec: "parent_object_type"},
				{Filter: "parent_object_id", Spec: "parent_object_id"},
				{Filter: "name", Spec: "name"},
				{Filter: "protocol", Spec: "protocol"},
			},
		}},

		UpdateStrategy: UpdatePatch,

		// The `(parent_object_type, parent_object_id)` pair, with the three targets NetBox
		// accepts.
		GenericFKs: []GenericFKSpec{serviceParentFK()},

		// The four columns every ChangeLoggedModel carries, plus the ports cache. NetBox
		// maintains `_ports_lowest` from `ports` on save, so writing it does not fail -- it
		// silently no-ops, which is a PATCH loop rather than an error.
		ReadOnly: []string{"created", "last_updated", "url", "display", "_ports_lowest"},

		// The containment parent, and under docs/decisions/0003-ownership-and-references.md
		// rule 4 it is not a preference: it is whichever FK the *server* cascades. All three
		// parent targets declare a `services` GenericRelation (docs/netbox-schema.md), so
		// deleting the device, the VM or the FHRP group a service runs on takes the service
		// with it, and the owner reference is what makes the CR go too.
		//
		// `parent_object_type` being `on_delete=PROTECT` is about the *ContentType row*, not
		// about the parent object: content types are not deleted in normal operation, and the
		// cascade that matters comes from the GenericRelation on the far side.
		//
		// `ipAddresses` is ruled out by cardinality rather than by taste: a to-many containment
		// ref is ErrContainmentToMany, because garbage collection waits for every owner and a
		// list of parents is that mistake with no upper bound.
		ContainmentRef: "parent",
	}
}
