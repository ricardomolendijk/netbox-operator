package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RackReservationStatus is one value of NetBox's RackReservationStatusChoices.
//
// Three values, read from `netbox/dcim/choices.py:146` in the same NetBox 4.6.8 tree
// docs/netbox-schema.md was taken from -- the digest records the choice *class*, not its
// members (docs/netbox-schema.md -> dcim.RackReservation, `status CharField len=50
// def=UNRESOLVED:RackReservationStatusChoices.STATUS_ACTIVE`).
//
// Its own Go type and not RackStatus, whose five members are entirely different. This
// ChoiceSet declares `key = 'RackReservation.status'`, so a deployment can extend it through
// FIELD_CHOICES; enumerated anyway, for the reason RackStatus gives.
//
// The column is not nullable and carries a default, so there is no empty member.
//
// +kubebuilder:validation:Enum=pending;active;stale
type RackReservationStatus string

const (
	// RackReservationStatusPending is a reservation not yet in force.
	RackReservationStatusPending RackReservationStatus = "pending"

	// RackReservationStatusActive is a reservation in force, and NetBox's own default.
	RackReservationStatusActive RackReservationStatus = "active"

	// RackReservationStatusStale is a reservation nobody has confirmed lately.
	RackReservationStatusStale RackReservationStatus = "stale"
)

// NetBoxRackReservationSpec describes one dcim.RackReservation.
//
// A claim on a set of rack units, so that nobody mounts a device in them. It is the odd kind
// in NBO-051 twice over.
//
// **It has no `meta.constraints` and no column-level `UNIQUE`** (docs/netbox-schema.md ->
// dcim.RackReservation), and its `meta.ordering` is `['created', 'pk']`, which identifies
// nothing -- so unlike ipam.Prefix, whose ordering at least names the columns that matter,
// there is no schema fact to derive an identity from at all. The lookup key is therefore the
// pure convention `(rack, description)`, the tenancy.Contact shape: `rack` and `description`
// are the two required columns a filter can carry, and a second reservation matching both is
// reported as `Conflict` rather than adopted (docs/concepts/lookups.md). NetBox will not stop
// you creating one.
//
// **Its `user` column is a required foreign key with no Kind to point at.** `user ForeignKey
// REQ -> settings.AUTH_USER_MODEL on_delete=PROTECT`, and the whole `users` app is deferred,
// so `UserID` below is a raw NetBox primary key rather than a reference. Why that is a plain
// value field and not `userRef.id` is on the field itself.
type NetBoxRackReservationSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// RackRef is the rack whose units are reserved. Required, because NetBox's column is
	// (`rack ForeignKey REQ -> dcim.Rack on_delete=CASCADE`).
	//
	// It is half of the lookup key, so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all.
	//
	// It is also this kind's containment reference, declared as such on the Descriptor
	// (internal/registry/dcim_rackreservation.go, ContainmentRef), and the *only* cascading
	// foreign key anywhere in NBO-051: `CASCADE` means NetBox deletes a rack's reservations
	// with the rack, so the rack is the containment parent under
	// docs/decisions/0003-ownership-and-references.md rule 4 and `kubectl delete netboxrack`
	// garbage-collects the reservation CRs in the same namespace.
	RackRef RackRef `json:"rackRef"`

	// Units are the rack units this reservation covers.
	//
	// Required and non-empty, because the column is (`units ArrayField REQ`) and a
	// reservation of no units reserves nothing. A Postgres `ArrayField` of positive small
	// integers, numbered in the rack's own scheme -- so `startingUnit` and `descUnits` on the
	// rack decide what `1` means.
	//
	// **Order is data here, not incidental.** NetBox stores the array as given and returns it
	// in stored order, so the field is registry.ClassArray and reordering it is a real change
	// the operator PATCHes -- the same rule ipam.VLANGroup's `vidRanges` follows, and the
	// opposite of a many-to-many, where NetBox does not preserve order and comparing
	// order-sensitively would PATCH forever
	// (registry.FieldClass, internal/netbox/drift.go). Write them sorted and the diff stays
	// empty; NBO-051's spec expected a set here and the engine's array semantics are what
	// ship.
	//
	// Bounded at 100 items, narrower than the 256 a reference list gets: the elements are
	// integers rather than ObjectRefs so they carry no CEL rules to cost, and a reservation
	// cannot cover more units than a rack has -- `uHeight`'s own ceiling.
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	// +kubebuilder:validation:items:Minimum=1
	Units []int32 `json:"units"`

	// UserID is the NetBox user the reservation is booked to, as a literal primary key.
	//
	// A raw id in a plain value field, which is the one place in this API that happens, and
	// it is a consequence rather than a preference. NetBox's column is `user ForeignKey REQ ->
	// settings.AUTH_USER_MODEL on_delete=PROTECT` (docs/netbox-schema.md ->
	// dcim.RackReservation) and the `users` app is deferred whole, so there is no
	// NetBoxUser Kind. A `userRef ObjectRef` would not help: internal/resolver dispatches
	// every one of its four modes -- `id` and `lookup` included -- through
	// `Descriptors.Get(Field.Target)` to learn the endpoint to query, so a reference whose
	// target Kind has no Descriptor cannot resolve at all and would report
	// `RefsResolved=False, Reason=RefKindUnavailable` forever
	// (internal/resolver/resolver.go, Resolve). Resolving `spec.username` through
	// `users/users?username=` needs the same missing fact. See
	// docs/reference/netboxrackreservation.md, which is where that gap is written up.
	//
	// Required, so a reservation the operator cannot attribute is refused by `kubectl apply`
	// rather than reported later: NetBox would refuse the create too, and it must never be
	// guessed from the token's own user -- that would silently book every reservation to the
	// operator's service account.
	//
	// Find the id with `GET /api/users/users/?username=<name>` and pin it in the manifest;
	// NetBox user ids are stable.
	// +kubebuilder:validation:Minimum=1
	UserID int64 `json:"userID"`

	// Description is what the units are reserved for.
	//
	// **Required**, unusually: dcim.RackReservation shadows PrimaryModel's own `description`
	// to make it so (docs/netbox-schema.md -> dcim.RackReservation, "shadows inherited:
	// description (PrimaryModel)", `description CharField REQ len=200`). It is therefore not
	// clearable and carries no tri-state note, and it is the other half of this kind's lookup
	// key -- so two reservations on one rack should not share one.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=200
	Description string `json:"description"`

	// Status is the reservation's lifecycle state.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default=active
	// +optional
	Status RackReservationStatus `json:"status,omitempty"`

	// TenantRef is who the reservation is on behalf of
	// (docs/netbox-schema.md -> dcim.RackReservation, `tenant ForeignKey -> tenancy.Tenant
	// on_delete=PROTECT`).
	//
	// `PROTECT`, so a reservation holding this reference blocks deletion of that tenant in
	// NetBox, reported on the *tenant* as `Deleting=False, Reason=Protected`. Not a
	// containment reference: a reservation outliving its tenant is a normal state.
	// +optional
	TenantRef *TenantRef `json:"tenantRef,omitempty"`

	// Comments is the reservation's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive. It is the only clearable field
	// on this kind, because `description` is required.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRackReservation is one dcim.RackReservation in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and the only
// kind in NBO-051 with a containment parent: `rackRef`, because `rack` is the only cascading
// foreign key in the whole rack hierarchy.
//
// Absent deliberately:
//
//   - `unit_count` is a counter NetBox derives from `units` and refuses on write
//     (hack/testdata/ir-4.6.8.json.gz -> dcim.RackReservation.write_path). Declared read-only
//     on the Descriptor.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind -- the same
//     deferral that makes `UserID` a raw id.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbrackres
// +kubebuilder:printcolumn:name="Rack",type=string,JSONPath=`.spec.rackRef.name`
// +kubebuilder:printcolumn:name="Units",type=string,JSONPath=`.spec.units`
// +kubebuilder:printcolumn:name="Status",type=string,JSONPath=`.spec.status`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRackReservation struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRackReservationSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus        `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (r *NetBoxRackReservation) NetBoxSpec() *NetBoxObjectSpec {
	return &r.Spec.NetBoxObjectSpec
}

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (r *NetBoxRackReservation) NetBoxStatus() *NetBoxObjectStatus { return &r.Status }

// NetBoxRackReservationList is a list of NetBoxRackReservation.
// +kubebuilder:object:root=true
type NetBoxRackReservationList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRackReservation `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRackReservation{}, &NetBoxRackReservationList{})
}
