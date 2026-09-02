package registry

import (
	"slices"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// One init() per kind, so adding a kind is a new file and never an edit to shared logic.
func init() { MustRegister(dcimRackDescriptor()) }

// dcimRackDescriptor is dcim.Rack as data.
//
// The kind whose natural key NetBox does not enforce where it matters, and the third of them
// after ipam.IPAddress and tenancy.Contact. Both declared constraints are keyed on `location`
// (docs/netbox-schema.md -> dcim.Rack.meta.constraints):
//
//	UniqueConstraint(fields=('location', 'name'),        name='..._unique_location_name')
//	UniqueConstraint(fields=('location', 'facility_id'), name='..._unique_location_facility_id')
//
// `location` is optional (`ForeignKey -> dcim.Location on_delete=SET_NULL`), so a rack with no
// location satisfies neither -- Postgres treats NULLs as distinct -- and two identically named
// location-less racks in one site are legal server state. Candidate 2 below is the convention
// that covers them, and it is a convention rather than a constraint: a second match is
// answered with Conflict rather than adopted (docs/concepts/lookups.md).
//
// **No scope union.** NetBox 4.2's CachedScopeMixin change touched ipam.Prefix, ipam.VLANGroup
// and virtualization.Cluster; `dcim.Rack` kept `site` and `location` as real writable columns,
// and the serializer's write path carries both and neither `scope_type` nor `scope_id`
// (docs/netbox-schema.md -> dcim.Rack; hack/testdata/ir-4.6.8.json.gz ->
// dcim.Rack.write_path). So `site` here is a foreign key the operator really does write, not
// the cached column that answers 201 and sets nothing.
//
// **No containment parent.** Every foreign key on the model is PROTECT except `location`,
// which is SET_NULL, so nothing on the server side disappears when a parent does and there is
// nothing for an owner reference to mirror
// (docs/decisions/0003-ownership-and-references.md rule 4). NBO-051's ticket asks for an owner
// reference from `siteRef`; validateContainment would refuse it (ErrContainmentNotCascade), and
// rightly -- NetBox refuses to delete a site that still has racks, so a cluster-side cascade
// would delete the CR and leave the row. Reported on the PR.
func dcimRackDescriptor() Descriptor {
	return Descriptor{
		GVK:        netboxv1alpha1.GroupVersion.WithKind("NetBoxRack"),
		Endpoint:   "dcim/racks",
		ObjectType: "dcim.rack",
		Scope:      apiextensionsv1.NamespaceScoped,

		// dcim.Rack derives from RackBase, which is a PrimaryModel (docs/netbox-schema.md ->
		// dcim.Rack and dcim.RackBase, bases), so it mixes in both TagsMixin and
		// CustomFieldsMixin and carries the whole provenance stamp. ContactsMixin and
		// ImageAttachmentsMixin contribute GenericRelations only.
		Taggable:        true,
		CustomFieldable: true,

		Fields: dcimRackFields(),

		NaturalKeys: dcimRackKeys(),

		UpdateStrategy: UpdatePatch,

		// No ContainmentRef -- see the doc comment.

		// The four columns every ChangeLoggedModel carries, the two counters the serializer
		// returns and the API refuses, and RackBase's two weight caches.
		//
		// `device_count` and `powerfeed_count` are in the write path
		// (hack/testdata/ir-4.6.8.json.gz -> dcim.Rack.write_path) and read-only there, which
		// is the failure this list exists for: NetBox maintains them from the child rows and
		// ignores an attempt to set one, so writing either does not fail -- it silently no-ops,
		// the next reconcile finds the same difference, and the operator PATCHes forever.
		ReadOnly: append([]string{
			"created", "last_updated", "url", "display", "device_count", "powerfeed_count",
		}, rackBaseReadOnly...),
	}
}

// dcimRackFields is this kind's spec-to-column map.
//
// Extracted from the descriptor for length, not because anything about it is dynamic -- the
// dcimDeviceFields shape. It is still a literal.
//
// Three entries earn the explicit table on their own. `facilityID` -> `facility_id` and
// `assetTag` -> `asset_tag` are pairs a camelCase-to-snake_case convention gets wrong
// (`facility_i_d`, and `asset_tag` only by luck), and `rackTypeRef` -> `rack_type` is one it
// gets right; NetBox ignores a field name it does not know rather than rejecting it, so a wrong
// spelling would write nothing and report success.
//
// `roleRef` -> `role` earns it twice over: the spec name says nothing about which of NetBox's
// three role models it is, and the answer is `dcim.RackRole` -- not the `dcim.DeviceRole` a
// NetBoxDevice's identically named field points at, and not `ipam.Role`.
//
// `status` needs no field class: NetBox returns a choice as {"value","label"} and takes the
// bare value, which internal/netbox/drift.go's unwrapNested already reduces by the absence of
// an "id" key. `formFactor` and `airflow` need EmptyIsNull, because both columns are
// `blank=True, null=True` and NetBox's serializer returns `null` rather than `""` for an unset
// choice -- an emptied field sent as `""` would differ from the value read back on every pass
// (#170).
func dcimRackFields() []Field {
	return slices.Concat([]Field{
		{Spec: "name", API: "name"},
		{Spec: "facilityID", API: "facility_id"},
		{Spec: "status", API: "status"},
		{Spec: "formFactor", API: "form_factor", EmptyIsNull: true},
		{Spec: "airflow", API: "airflow", EmptyIsNull: true},
		{Spec: "serial", API: "serial"},
		{Spec: "assetTag", API: "asset_tag"},
		{Spec: "description", API: "description"},
		{Spec: "comments", API: "comments"},
		// Written as `site`, filtered as `site_id`. PROTECT, so no cascade to declare -- which
		// is what leaves this Kind without a containment parent.
		{
			Spec: "siteRef", API: "site", Class: ClassRefOne,
			Target: netboxv1alpha1.SiteRef{}.TargetGVK(),
		},
		// SET_NULL, so no cascade either: NetBox clears the column rather than deleting the
		// rack, and a CR deleted for a cleared column would be the mistake
		// ErrContainmentNotCascade exists to catch.
		{
			Spec: "locationRef", API: "location", Class: ClassRefOne,
			Target: netboxv1alpha1.LocationRef{}.TargetGVK(),
		},
		{
			Spec: "groupRef", API: "group", Class: ClassRefOne,
			Target: netboxv1alpha1.RackGroupRef{}.TargetGVK(),
		},
		{
			Spec: "rackTypeRef", API: "rack_type", Class: ClassRefOne,
			Target: netboxv1alpha1.RackTypeRef{}.TargetGVK(),
		},
		{
			Spec: "roleRef", API: "role", Class: ClassRefOne,
			Target: netboxv1alpha1.RackRoleRef{}.TargetGVK(),
		},
		{
			Spec: "tenantRef", API: "tenant", Class: ClassRefOne,
			Target: netboxv1alpha1.TenantRef{}.TargetGVK(),
		},
	}, rackBaseFields())
}

// dcimRackKeys are the lookup candidates, in priority order.
//
// Three, and only ever two of them applicable to one object, because the first two disagree
// about whether `locationRef` is declared:
//
//  1. `(location_id, name)` -- the database constraint, used by a rack that names a location.
//  2. `(site_id, name)` with `location_id` pinned null -- the convention for a rack that names
//     none. The pin is what makes it safe: without it the lookup would match a rack of that
//     name in a room somebody else declared and adopt it, and NaturalKey.Applicable only
//     offers this candidate while `locationRef` is *undeclared*, so a rack whose location has
//     not been created yet waits instead of falling through (NBO-015). `?location_id=null` is
//     the wire spelling of NullColumnRef; `location_id` is a TreeNodeMultipleChoiceFilter,
//     which subclasses the ModelMultipleChoiceFilter `dcim.Location.parent_id` already pins
//     this way (#216, NetBox 4.6.8 `netbox/dcim/filtersets.py`).
//  3. `(location_id, facility_id)` -- NetBox's second constraint, reached only when the first
//     matched nothing and both halves are set. It is a fallback chain in the dcim.DeviceType
//     sense and safe for the same reason: the pair is unique in the database, so the rack it
//     finds *is* the facility slot the spec describes and creating a second one would be a 409.
//     Adopting it and PATCHing the name beats failing every reconcile, which is what a facility
//     rename would otherwise cause.
//
// `name` leads rather than `facility_id` because it is the identity NetBox itself orders and
// indexes on: `meta.ordering: ('site', 'location', 'name', 'pk')` and
// `meta.indexes: (models.Index(fields=('site', 'location', 'name', 'id')),)`.
//
// There is deliberately no `(site_id, facility_id)` variant to match candidate 2. No constraint
// backs it and neither does `meta.ordering`, so it would be a second guess layered on the first
// -- and a location-less rack that has only a facility id gets no candidate at all rather than
// an invented one.
//
// `asset_tag` is `UNIQUE` across the whole install and is deliberately not a candidate either:
// it identifies a chassis and this CR describes a rack position, so adopting by asset tag would
// let moving a chassis rewrite the site and location of somebody else's rack. A duplicate comes
// back as NetBox's own 409.
//
// Every filter is registered on `RackFilterSet` (NetBox 4.6.8,
// `netbox/dcim/filtersets.py`): `site_id` and `location_id` are declared, and `name` and
// `facility_id` are in `meta_fields`.
func dcimRackKeys() []NaturalKey {
	return []NaturalKey{
		{
			Fields: []KeyField{
				{Filter: "location_id", Spec: "locationRef"},
				{Filter: "name", Spec: "name"},
			},
		},
		{
			Fields: []KeyField{
				{Filter: "site_id", Spec: "siteRef"},
				{Filter: "name", Spec: "name"},
			},
			NullFields: []NullField{
				{Filter: "location_id", Spec: "locationRef", Column: NullColumnRef},
			},
		},
		{
			Fields: []KeyField{
				{Filter: "location_id", Spec: "locationRef"},
				{Filter: "facility_id", Spec: "facilityID"},
			},
		},
	}
}
