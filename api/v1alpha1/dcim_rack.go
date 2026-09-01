package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RackStatus is one value of NetBox's RackStatusChoices.
//
// Five values, read from `netbox/dcim/choices.py:90`, `RackStatusChoices`, in the same NetBox
// 4.6.8 tree docs/netbox-schema.md was taken from -- the digest records the choice *class*
// and not its members, because the AST walk cannot evaluate one
// (docs/netbox-schema.md -> dcim.Rack, `status CharField len=50
// def=UNRESOLVED:RackStatusChoices.STATUS_ACTIVE choices=RackStatusChoices`).
//
// Not the same members as any status enum shipped so far, and deliberately its own Go type:
// `reserved` and `available` are rack-specific and `staging`, `decommissioning` and `retired`
// -- SiteStatus's and LocationStatus's -- are absent here. This ChoiceSet declares
// `key = 'Rack.status'`, so a deployment can add values through FIELD_CHOICES and a closed
// enum would reject one at admission; enumerated anyway, following SiteStatus and
// PrefixStatus, because a typo caught by `kubectl apply` is worth more than an extension
// nobody has made and widening the enum is a one-line change.
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=reserved;available;planned;active;deprecated
type RackStatus string

const (
	// RackStatusReserved is a rack set aside and not yet in service.
	RackStatusReserved RackStatus = "reserved"

	// RackStatusAvailable is a rack with capacity to allocate.
	RackStatusAvailable RackStatus = "available"

	// RackStatusPlanned is a rack that does not physically exist yet.
	RackStatusPlanned RackStatus = "planned"

	// RackStatusActive is a rack in service, and NetBox's own default.
	RackStatusActive RackStatus = "active"

	// RackStatusDeprecated is a rack being retired.
	RackStatusDeprecated RackStatus = "deprecated"
)

// RackAirflow is one value of NetBox's RackAirflowChoices.
//
// Two values, `netbox/dcim/choices.py:130` at 4.6.8, plus the empty string because the column
// is `blank=True, null=True` (docs/netbox-schema.md -> dcim.Rack, `airflow CharField len=50
// choices=RackAirflowChoices`), so "unspecified" is a real state.
//
// A different Go type from DeviceAirflow, whose ten members include `passive`, `mixed` and six
// side-to-side directions: they are two separate ChoiceSets in NetBox, both extensible through
// FIELD_CHOICES, so sharing one enum would make a value added to one silently legal on the
// other.
//
// +kubebuilder:validation:Enum="";front-to-rear;rear-to-front
type RackAirflow string

const (
	// RackAirflowFrontToRear draws cold air in at the front.
	RackAirflowFrontToRear RackAirflow = "front-to-rear"

	// RackAirflowRearToFront draws cold air in at the rear.
	RackAirflowRearToFront RackAirflow = "rear-to-front"
)

// NetBoxRackSpec describes one dcim.Rack.
//
// The rack itself, and the kind in NBO-051 whose identity NetBox does not enforce where you
// would expect it to. Both of its `meta.constraints` are keyed on `location`:
//
//	models.UniqueConstraint(fields=('location', 'name'),        name='..._unique_location_name')
//	models.UniqueConstraint(fields=('location', 'facility_id'), name='..._unique_location_facility_id')
//
// (docs/netbox-schema.md -> dcim.Rack.meta.constraints.) `location` is optional and
// `on_delete=SET_NULL`, so a rack with no location has **no enforced key at all** and two
// identically named location-less racks in one site are legal NetBox state. The natural key
// therefore falls back to `(site, name)` with `location_id` pinned null -- a lookup
// convention rather than a constraint -- and an ambiguous match is reported as `Conflict`
// rather than adopted. See internal/registry/dcim_rack.go and
// docs/reference/netboxrack.md#natural-keys.
//
// **This kind is not scope-mixed.** NetBox 4.2 replaced `site` with a `(scope_type, scope_id)`
// pair on ipam.Prefix, ipam.VLANGroup and virtualization.Cluster, and it is the trap
// docs/reference/netboxcluster.md says it broke silently -- but `dcim.Rack` was not part of
// that change. It still declares `site ForeignKey REQ -> dcim.Site` and `location ForeignKey
// -> dcim.Location` as real, writable columns (docs/netbox-schema.md -> dcim.Rack; the
// serializer's write path carries `site` and `location` and neither `scope_type` nor
// `scope_id`, hack/testdata/ir-4.6.8.json.gz -> dcim.Rack.write_path). So `siteRef` here is
// an ordinary reference that really is written, not the cached column that returns 201 and
// sets nothing, and there is no ScopeRef union on this Kind.
type NetBoxRackSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// RackDimensions are the `dcim.RackBase` measurements of this rack. Inline, so each one
	// is `spec.<field>`.
	//
	// Setting `rackTypeRef` instead is the other way to get them: NetBox copies the type's
	// dimensions onto the rack server-side on create, so a rack built from a catalogue entry
	// needs none of these fields restated. They are all defaulted or optional, and the three
	// defaulted ones (`width`, `uHeight`, `startingUnit`) are NetBox's own defaults, so a
	// rack that names a type and leaves them alone is written with the values NetBox would
	// have used anyway.
	RackDimensions `json:",inline"`

	// Name is the rack's name.
	//
	// Unique per *location* rather than per site or globally
	// (docs/netbox-schema.md -> dcim.Rack.meta.constraints), and not unique at all for a rack
	// with no location. Two racks called `R1` in two locations are a legitimate NetBox state.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Name string `json:"name"`

	// SiteRef is the site this rack stands in. Required, because NetBox's column is
	// (`site ForeignKey REQ -> dcim.Site on_delete=PROTECT`) and there is no such thing as a
	// rack outside a site.
	//
	// Every natural-key candidate reads it or reads `location`, which is inside it, so until
	// it resolves the object reports RefsResolved=False naming this field and makes no NetBox
	// write at all -- rather than creating a rack in the wrong site.
	//
	// It is **not** this kind's containment reference, and cannot be: `PROTECT` means NetBox
	// refuses to delete a site while a rack points at it, so there is no server-side deletion
	// for an owner reference to mirror (docs/decisions/0003-ownership-and-references.md rule
	// 4). Deleting the site CR is refused on the *site*, as
	// `Deleting=False, Reason=Protected`; delete the racks first.
	SiteRef SiteRef `json:"siteRef"`

	// LocationRef is the room or row within the site this rack stands in
	// (docs/netbox-schema.md -> dcim.Rack, `location ForeignKey -> dcim.Location
	// on_delete=SET_NULL`).
	//
	// Optional, and the single most load-bearing optional field in this spec: it is what both
	// of NetBox's unique constraints are keyed on. Setting it gets the rack a database-backed
	// identity, `(location, name)`; leaving it unset gets it the `(site, name)` convention
	// with `location_id` pinned null, where an ambiguous match is a `Conflict` the database
	// will not prevent (docs/reference/netboxrack.md#natural-keys).
	//
	// A pointer to the typed alias, so it has two states rather than three: absent means
	// unmanaged, and a value claims the column. `SET_NULL` means NetBox *can* hold a rack
	// with no location, and clearing the column from a manifest needs registry.EmptyIsNull
	// and a v1alpha1.OptionalRef -- a third state no shipped kind uses yet (#185). Until then
	// the way to move a rack out of a location is to clear it in NetBox and stop declaring
	// the field.
	// +optional
	LocationRef *LocationRef `json:"locationRef,omitempty"`

	// GroupRef is the rack group this rack belongs to
	// (docs/netbox-schema.md -> dcim.Rack, `group ForeignKey -> dcim.RackGroup
	// on_delete=PROTECT`).
	//
	// A flat label rather than a position in a hierarchy -- see NetBoxRackGroup, which is an
	// `OrganizationalModel` and has no `parent` at all. It is in no natural-key candidate:
	// NetBox constrains nothing on it.
	//
	// Two states, as LocationRef explains.
	// +optional
	GroupRef *RackGroupRef `json:"groupRef,omitempty"`

	// RackTypeRef is the catalogue entry this rack is built from
	// (docs/netbox-schema.md -> dcim.Rack, `rack_type ForeignKey -> dcim.RackType
	// on_delete=PROTECT`).
	//
	// Setting it makes NetBox copy the type's `RackBase` dimensions onto this rack on create,
	// server-side -- the operator does not re-send them and does not have to. `PROTECT`, so
	// deleting a rack type in use is refused on the *type*.
	//
	// Two states, as LocationRef explains.
	// +optional
	RackTypeRef *RackTypeRef `json:"rackTypeRef,omitempty"`

	// RoleRef is the rack's functional role
	// (docs/netbox-schema.md -> dcim.Rack, `role ForeignKey -> dcim.RackRole
	// on_delete=PROTECT`).
	//
	// `dcim.RackRole` and not `dcim.DeviceRole` or `ipam.Role`: three separate NetBox models
	// spell "role", and this column names the first.
	//
	// Two states, as LocationRef explains.
	// +optional
	RoleRef *RackRoleRef `json:"roleRef,omitempty"`

	// TenantRef is who the rack belongs to
	// (docs/netbox-schema.md -> dcim.Rack, `tenant ForeignKey -> tenancy.Tenant
	// on_delete=PROTECT`).
	//
	// Not a containment parent and never a cascade -- see docs/reference/netboxtenant.md on
	// why `tenantRef` does not cascade, and docs/concepts/references.md on why a namespace
	// does not imply a tenant.
	//
	// Two states, as LocationRef explains.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Status is the rack's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status RackStatus `json:"status,omitempty"`

	// FormFactor is the rack's physical construction.
	//
	// Optional here and **required on NetBoxRackType**, because the two columns differ: this
	// one is `blank=True, null=True` and dcim.RackType's is `REQ` with no default
	// (docs/netbox-schema.md). Unset leaves NetBox's own value alone; `""` clears it, which is
	// how NetBox spells "unspecified". Those are two different instructions and the operator
	// tells them apart from metadata.managedFields
	// (docs/concepts/field-ownership.md); the wording differs from the other clearable fields
	// here only because this one carries an enum.
	//
	// Cleared as `null` rather than as an empty string, because NetBox's serializer returns
	// `null` for an unset choice and a payload of `""` would differ from the value read back
	// on every pass -- see registry.Field.EmptyIsNull.
	// +optional
	FormFactor RackFormFactor `json:"formFactor,omitempty"`

	// Airflow is the direction air moves through the rack.
	//
	// Unset leaves NetBox's own value alone; `""` clears it, and is sent as `null` for the
	// reason FormFactor gives.
	// +optional
	Airflow RackAirflow `json:"airflow,omitempty"`

	// FacilityID is the rack's designation in the facility's own numbering
	// (docs/netbox-schema.md -> dcim.Rack, `facility_id CharField len=50`).
	//
	// NetBox's second unique constraint is `(location, facility_id)`, so setting it gives the
	// rack a database-backed second identity and is what lets the operator adopt a rack the
	// facility renamed. `facility_id` and not a reference: it is a free-text label the data
	// centre assigns, not an id of anything in NetBox.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	FacilityID string `json:"facilityID,omitempty"`

	// Serial is the manufacturer's serial number
	// (docs/netbox-schema.md -> dcim.Rack, `serial CharField len=50`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	Serial string `json:"serial,omitempty"`

	// AssetTag is the organisation's own inventory tag
	// (docs/netbox-schema.md -> dcim.Rack, `asset_tag CharField UNIQUE len=50`).
	//
	// Globally unique across every rack in the NetBox, and deliberately **not** a
	// natural-key candidate: the asset tag identifies a chassis and the CR describes a rack
	// position, so adopting by asset tag would let moving a chassis silently rewrite the site
	// and location of somebody else's rack. A duplicate comes back as NetBox's own 409,
	// reported as `Ready=False, Reason=Invalid`.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	AssetTag string `json:"assetTag,omitempty"`

	// Description is free text shown next to the rack.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the rack's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRack is one dcim.Rack in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md).
//
// Absent deliberately:
//
//   - **No containment parent at all**, the dcim.Device shape rather than the dcim.Location
//     one: every foreign key on dcim.Rack is `PROTECT` except `location`, which is
//     `SET_NULL`, so nothing on the server side disappears when a parent goes and there is
//     nothing for an owner reference to mirror
//     (docs/decisions/0003-ownership-and-references.md rule 4).
//   - `device_count` and `powerfeed_count` are counters the serializer returns and the API
//     refuses (hack/testdata/ir-4.6.8.json.gz -> dcim.Rack.write_path; docs/netbox-schema.md,
//     preamble on every CounterCacheField). Declared read-only on the Descriptor.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind.
//   - `_abs_max_weight` and `_abs_weight` are `_`-prefixed caches, absent from the write path.
//   - `vlan_groups`, `contacts` and `images` are GenericRelations -- the far end of somebody
//     else's foreign key. A rack is a legal `ContactAssignment` target through the union on
//     NetBoxContactAssignment, and a legal `VLANGroup` scope through the one on
//     NetBoxVLANGroup; both are written from the *other* object.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrack
// +kubebuilder:printcolumn:name="Site",type=string,JSONPath=`.spec.siteRef.name`
// +kubebuilder:printcolumn:name="Location",type=string,JSONPath=`.spec.locationRef.name`
// +kubebuilder:printcolumn:name="U",type=integer,JSONPath=`.spec.uHeight`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRack struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRackSpec     `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRack) NetBoxSpec() *NetBoxObjectSpec { return &r.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRack) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRackList is a list of NetBoxRack.
// +kubebuilder:object:root=true
type NetBoxRackList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRack `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRack{}, &NetBoxRackList{})
}
