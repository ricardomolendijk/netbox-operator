package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// NetBoxRackTypeSpec describes one dcim.RackType.
//
// A make and model of rack in the hardware catalogue, so that `NetBoxRack.rackTypeRef` has
// something to point at and NetBox copies the dimensions onto each rack server-side.
//
// Identically shaped to dcim.DeviceType where it matters: `manufacturer ForeignKey REQ ->
// dcim.Manufacturer on_delete=PROTECT` and both `meta.constraints` start at it --
// `(manufacturer, model)` and `(manufacturer, slug)`
// (docs/netbox-schema.md -> dcim.RackType.meta.constraints). So an unresolved
// `manufacturerRef` leaves **no** applicable natural-key candidate and the object writes
// nothing at all, rather than being created without the field.
//
// One thing dcim.DeviceType does not have: `slug` here is *also* globally `UNIQUE` at the
// column level (`slug SlugField REQ UNIQUE len=100`), so `(manufacturer, slug)` is stricter
// than the database needs and two manufacturers cannot in fact share a slug. The natural key
// still carries `manufacturer_id`, because the pair is what the constraint names and a
// candidate that drops a filter matches more objects rather than fewer.
type NetBoxRackTypeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// RackDimensions are the `dcim.RackBase` measurements a rack of this type gets. Inline,
	// so each one is `spec.<field>`.
	RackDimensions `json:",inline"`

	// ManufacturerRef is who makes this rack type. Required, because NetBox's column is
	// (`manufacturer ForeignKey REQ -> dcim.Manufacturer on_delete=PROTECT`).
	//
	// It is half of both natural keys, so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all.
	//
	// PROTECT, so NetBox refuses to delete a manufacturer while any rack type points at it;
	// that surfaces on the *manufacturer* as Deleting=False, Reason=Protected.
	ManufacturerRef ManufacturerRef `json:"manufacturerRef"`

	// Model is the model name, as NetBox displays it
	// (docs/netbox-schema.md -> dcim.RackType, `model CharField REQ len=100`).
	//
	// Unique per manufacturer: `..._unique_manufacturer_model`. A candidate key and
	// deliberately the *second* one, for the reason Slug gives.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Model string `json:"model"`

	// Slug is the rack type's URL-safe identifier, and the leading half of this kind's
	// natural key.
	//
	// It leads because it is the stable identifier: a marketing rename edits `model`, and
	// looking up by `slug` first keeps that a PATCH rather than a second object.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// FormFactor is the physical construction of a rack of this type.
	//
	// **Required here and optional on NetBoxRack**, and that asymmetry is the column's rather
	// than a choice: the digest reads `form_factor CharField REQ len=50` on dcim.RackType and
	// `blank=True, null=True` on dcim.Rack (docs/netbox-schema.md). The IR records the
	// disagreement between the two halves it reads and resolves it in the serializer's favour
	// -- `{"fact": "required-on-create", "field": "form_factor", "kind": "dcim.RackType",
	// "models.json": "NOT NULL, no default", "rest": "serializer declares required=False"}`
	// (hack/testdata/ir-4.6.8.json.gz -> conflicts) -- so DRF accepts a create without it and
	// Postgres then refuses the INSERT.
	//
	// Requiring it at admission is what turns that into a `kubectl apply` error instead of a
	// `500` from NetBox, and the CEL rule is the second half of that: RackFormFactor carries
	// the empty string as a member for dcim.Rack's sake, where the column really is nullable,
	// so requiring the *field* is not enough on its own. One shared enum and a per-field rule
	// rather than a second Go type, because there is one ChoiceSet here and two columns using
	// it -- a duplicate enum would be a set that could drift.
	// +kubebuilder:validation:XValidation:rule="self != ''",message="formFactor is required on a rack type: dcim.RackType.form_factor is NOT NULL with no default"
	FormFactor RackFormFactor `json:"formFactor"`

	// Description is free text shown next to the rack type.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the rack type's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxRackType is one dcim.RackType in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and a
// catalogue kind, so the shared-namespace shape dcim.DeviceType documents applies here too:
// `manufacturerRef` and `rackTypeRef` pointing from a team namespace into a shared catalogue
// namespace each need a NetBoxRefGrant there
// (docs/reference/netboxrefgrant.md).
//
// Absent deliberately:
//
//   - `rack_count` is a CounterCacheField NetBox maintains from the racks pointing at this
//     type (docs/netbox-schema.md, preamble on every CounterCacheField). It is in the
//     serializer's write path and the API refuses it, so writing it silently no-ops and the
//     next reconcile finds the same difference forever. Declared read-only on the Descriptor.
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind.
//   - `_abs_max_weight` and `_abs_weight` are `_`-prefixed caches, and the IR records both as
//     absent from the write path entirely.
//   - `images` is an ImageAttachmentsMixin GenericRelation: the reverse of somebody else's
//     foreign key, not a column.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbracktype
// +kubebuilder:printcolumn:name="Manufacturer",type=string,JSONPath=`.spec.manufacturerRef.name`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="U",type=integer,JSONPath=`.spec.uHeight`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxRackType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxRackTypeSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxRackType) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxRackType) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxRackTypeList is a list of NetBoxRackType.
// +kubebuilder:object:root=true
type NetBoxRackTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxRackType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxRackType{}, &NetBoxRackTypeList{})
}
