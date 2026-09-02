package registry

import (
	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimRackReservationDescriptor()) }

// dcimRackReservationDescriptor is dcim.RackReservation as data.
//
// The weakest identity in NBO-051, and the only kind here with a containment parent.
//
// **No `meta.constraints` and no column-level UNIQUE** (docs/netbox-schema.md ->
// dcim.RackReservation). `meta.ordering` is `['created', 'pk']` and `meta.indexes` is
// `(models.Index(fields=('created', 'id')),)`, so neither offers a column that identifies
// anything -- this is a step weaker than ipam.Prefix, whose ordering at least names `prefix`
// and `vrf`. The key is therefore a pure convention over the two required columns a filter can
// carry, `(rack_id, description)`, and a second match is answered with Conflict rather than
// adopted (docs/concepts/lookups.md). The tenancy.Contact shape, one further down: there the
// column at least has an index behind it.
//
// **`units` is not in the key, and cannot be.** NBO-051's ticket proposes `(rack,
// sorted(units))`, but a natural-key filter carries one scalar value and `units` is a JSON
// list: reconciler.filterValue renders strings, bools and numbers only, so a list never
// reaches SpecState.Resolved and a candidate matching on it would never be applicable -- the
// object would wait forever for an identity it cannot build. NetBox's own `unit` filter is
// `NumericArrayFilter(lookup_expr='contains')`, which asks whether the array *contains* one
// unit rather than whether it equals a set, so it cannot express the key either
// (NetBox 4.6.8, `netbox/dcim/filtersets.py:519`). Reported on the PR.
//
// **`user` is a required FK with no Kind to point at.** `user ForeignKey REQ ->
// settings.AUTH_USER_MODEL on_delete=PROTECT`, and the `users` app is deferred whole, so the
// spec field is a literal id (`userID`) mapped straight onto the column rather than an
// ObjectRef. internal/resolver dispatches every reference mode through
// `Descriptors.Get(Field.Target)` to learn the endpoint, so a reference whose target Kind has
// no Descriptor cannot resolve in *any* mode -- `id` and `lookup` included -- and the ticket's
// `spec.userRef.id` escape hatch is not expressible today. Reported on the PR.
func dcimRackReservationDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRackReservation"),
		Endpoint:   "dcim/rack-reservations",
		ObjectType: "dcim.rackreservation",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.RackReservation is a PrimaryModel (docs/netbox-schema.md ->
		// dcim.RackReservation, bases), which mixes in both TagsMixin and CustomFieldsMixin.
		Taggable:        true,
		CustomFieldable: true,

		// `units` is ClassArray rather than ClassRefMany even though both arrive as JSON lists.
		// NetBox stores it as a Postgres ArrayField and returns it in stored order, so the
		// order is data: compared order-independently, `[1,2,3]` and `[3,2,1]` would look equal
		// while NetBox holds two different values. ipam.VLANGroup's `vid_ranges` made the same
		// call.
		//
		// `userID` is a plain value: a literal NetBox primary key written onto the `user`
		// column, for the reason the doc comment gives. Filtered nowhere -- it is not in the
		// key, so a reservation handed to a different user is a PATCH rather than a duplicate.
		Fields: []Field{
			{Spec: "units", API: "units", Class: ClassArray},
			{Spec: "userID", API: "user"},
			{Spec: "description", API: "description"},
			{Spec: "status", API: "status"},
			{Spec: "comments", API: "comments"},
			// Written as `rack`, filtered as `rack_id`. CASCADE, which is what makes it this
			// kind's containment parent and the only cascading foreign key in NBO-051
			// (docs/netbox-schema.md -> dcim.RackReservation, `rack ForeignKey REQ ->
			// dcim.Rack on_delete=CASCADE`).
			{
				Spec: "rackRef", API: "rack", Class: ClassRefOne,
				Target:          netboxv1alpha1.RackRef{}.TargetGVK(),
				CascadeOnDelete: true,
			},
			{
				Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
				Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
			},
		},

		// One candidate, and both halves of it are required columns, so it is applicable as
		// soon as `rackRef` resolves and there is nothing to fall back to. `description` is
		// `REQ` here because the model shadows PrimaryModel's own to make it so
		// (docs/netbox-schema.md -> dcim.RackReservation, "shadows inherited: description"),
		// which is the only reason it can be in a key at all.
		//
		// `rack_id` is declared on RackReservationFilterSet and `description` is in its
		// `meta_fields` (`('id', 'created', 'description')`, NetBox 4.6.8
		// `netbox/dcim/filtersets.py:519`).
		//
		// No DuplicateSpec. Two reservations on one rack with one description is not a case
		// where the operator picks its own by the provenance stamp -- that is
		// ipam.IPAddress's `allowDuplicate`, where NetBox's data model requires the duplicate.
		// Here it means somebody declared the same reservation twice, and the honest answer is
		// Conflict.
		NaturalKeys: []NaturalKey{
			{
				Fields: []KeyField{
					{Filter: "rack_id", Spec: "rackRef"},
					{Filter: "description", Spec: "description"},
				},
			},
		},

		UpdateStrategy: UpdatePatch,

		// The rack, because `rack` is the FK the *server* cascades: NetBox deletes a rack's
		// reservations with the rack, so the CR has to go with the row or the engine's
		// create-if-absent step resurrects what NetBox deliberately deleted
		// (docs/decisions/0003-ownership-and-references.md rule 4). `tenant` is PROTECT and is
		// not a candidate for the slot.
		ContainmentRef: "rackRef",

		// The four columns every ChangeLoggedModel carries, plus the counter NetBox derives
		// from `units` and refuses on write (hack/testdata/ir-4.6.8.json.gz ->
		// dcim.RackReservation.write_path).
		ReadOnly: []string{"created", "last_updated", "url", "display", "unit_count"},
	}
}
