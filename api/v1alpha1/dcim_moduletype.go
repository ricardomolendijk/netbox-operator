package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// ModuleAirflow is one value of NetBox's ModuleAirflowChoices.
//
// Six values, read from `netbox/dcim/choices.py:264`, `ModuleAirflowChoices`, in the same
// NetBox 4.6.8 tree docs/netbox-schema.md was taken from -- the digest records the choice
// *class* and not its members, because the AST walk cannot evaluate one
// (docs/netbox-schema.md -> dcim.ModuleType, `airflow CharField len=50
// choices=ModuleAirflowChoices`).
//
// A different Go type from RackAirflow and from DeviceAirflow, and not by preference: the
// three are separate ChoiceSets in NetBox and this one declares `key = 'Module.airflow'`
// (hack/testdata/api-schema-4.6.8.json.gz -> choices.ModuleAirflowChoices), so a deployment
// can extend it through FIELD_CHOICES independently of the other two. Sharing one enum would
// make a value added to one silently legal on the others.
//
// Enumerated anyway, following RackStatus and SiteStatus: a typo caught by `kubectl apply` is
// worth more than an extension nobody has made, and widening the enum is a one-line change.
//
// The empty string is a member because the column is `blank=True, null=True`
// (hack/testdata/ir-4.6.8.json.gz -> dcim.ModuleType.airflow), so "unspecified" is a real
// state -- and it is the state NetBox returns as `null`, which is why the descriptor marks
// the field EmptyIsNull.
//
// +kubebuilder:validation:Enum="";front-to-rear;rear-to-front;left-to-right;right-to-left;side-to-rear;passive
type ModuleAirflow string

const (
	// ModuleAirflowFrontToRear draws cold air in at the front.
	ModuleAirflowFrontToRear ModuleAirflow = "front-to-rear"

	// ModuleAirflowRearToFront draws cold air in at the rear.
	ModuleAirflowRearToFront ModuleAirflow = "rear-to-front"

	// ModuleAirflowLeftToRight draws cold air in at the left.
	ModuleAirflowLeftToRight ModuleAirflow = "left-to-right"

	// ModuleAirflowRightToLeft draws cold air in at the right.
	ModuleAirflowRightToLeft ModuleAirflow = "right-to-left"

	// ModuleAirflowSideToRear draws cold air in at the side and exhausts at the rear.
	ModuleAirflowSideToRear ModuleAirflow = "side-to-rear"

	// ModuleAirflowPassive is a module with no fan of its own.
	ModuleAirflowPassive ModuleAirflow = "passive"
)

// NetBoxModuleTypeSpec describes one dcim.ModuleType.
//
// A make and model of module in the hardware catalogue -- a line card, a power supply, an
// optic -- so that `NetBoxModule.moduleTypeRef` has something to point at.
//
// The dcim.DeviceType identity shape with one constraint rather than two: `meta.constraints`
// is exactly `UniqueConstraint(fields=('manufacturer', 'model'))` (docs/netbox-schema.md ->
// dcim.ModuleType.meta.constraints), and there is no `slug` column on this model at all. So
// an unresolved `manufacturerRef` leaves no applicable candidate and the object writes
// nothing rather than being created without the field.
type NetBoxModuleTypeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ManufacturerRef is who makes this module type. Required, because NetBox's column is
	// (`manufacturer ForeignKey REQ -> dcim.Manufacturer on_delete=PROTECT`).
	//
	// It is half of the only natural key, so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all.
	//
	// PROTECT, so NetBox refuses to delete a manufacturer while any module type points at
	// it; that surfaces on the *manufacturer* as Deleting=False, Reason=Protected.
	ManufacturerRef ManufacturerRef `json:"manufacturerRef"`

	// Model is the model name, as NetBox displays it
	// (docs/netbox-schema.md -> dcim.ModuleType, `model CharField REQ len=100`).
	//
	// Unique per manufacturer: `..._unique_manufacturer_model`, and the other half of the
	// natural key. Unlike dcim.RackType and dcim.DeviceType there is no `slug` to fall back
	// to, so a model rename is a *new* object to the lookup rather than a PATCH -- the
	// second candidate those kinds have does not exist here because the column it would use
	// does not.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Model string `json:"model"`

	// ProfileRef is the dcim.ModuleTypeProfile whose JSON Schema validates Attributes
	// (docs/netbox-schema.md -> dcim.ModuleType,
	// `profile ForeignKey -> dcim.ModuleTypeProfile on_delete=PROTECT`).
	//
	// Optional, and not part of the natural key even though `meta.ordering` and
	// `meta.indexes` both lead with it: the unique constraint names `(manufacturer, model)`
	// only, so two module types of one manufacturer and model cannot exist under different
	// profiles and adding `profile_id` to the lookup would narrow it below what the database
	// enforces.
	//
	// PROTECT, so a profile cannot be deleted while a module type claims it, and there is no
	// containment parent here for the same reason.
	// +optional
	ProfileRef *ModuleTypeProfileRef `json:"profileRef,omitempty"`

	// PartNumber is the manufacturer's ordering part number
	// (docs/netbox-schema.md -> dcim.ModuleType, `part_number CharField len=50`).
	//
	// Not unique in NetBox and so not a lookup candidate.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	PartNumber string `json:"partNumber,omitempty"`

	// Airflow is the direction air moves through a module of this type.
	//
	// Unset leaves NetBox's own value alone; `""` clears it, and is sent as `null` rather
	// than as the empty string, because NetBox returns an unset choice as `null` and a `""`
	// would differ from the value read back on every pass (#170, registry.Field.EmptyIsNull).
	// +optional
	Airflow ModuleAirflow `json:"airflow,omitempty"`

	// Attributes is the profile-specific attribute document
	// (docs/netbox-schema.md -> dcim.ModuleType, `attribute_data JSONField`).
	//
	// Two names for one column, and the API's name is the one that matters: the model field
	// is `attribute_data`, and `ModuleTypeSerializer` exposes it as `attributes` through an
	// `AttributesField` (hack/testdata/api-schema-4.6.8.json.gz -> ModuleTypeSerializer,
	// `declared.attributes`; the IR records `attribute_data` as **not** in the write path for
	// exactly this reason). The descriptor writes `attributes`; writing `attribute_data`
	// would be a field NetBox does not know, and NetBox drops an unknown field rather than
	// rejecting it, so it would report success and set nothing.
	//
	// Validated server-side against `profileRef`'s `schema`, and nowhere else. The operator
	// sends the document opaquely and surfaces NetBox's field-level 400 verbatim in the Ready
	// message; a client-side JSON-Schema check would be a second copy of the profile, drifting
	// from the one NetBox actually enforces.
	//
	// A pointer with `omitempty`, like every JSONDocument in the API
	// (docs/concepts/field-ownership.md).
	// +optional
	Attributes *JSONDocument `json:"attributes,omitempty"`

	// Weight is the module type's weight, in WeightUnit, as a string.
	//
	// A string and not a float64, the decision `NetBoxRackType.weight` documents at length:
	// NetBox stores it as `weight DecimalField decimal(8,2)` (docs/netbox-schema.md ->
	// dcim.ModuleType, from WeightMixin) and the API returns it padded, `"1.50"` for a spec
	// that said `"1.5"`. The engine compares two numeric strings numerically
	// (internal/netbox/drift.go, scalarEqual), so the two produce no PATCH.
	//
	// The pattern is read straight off `decimal(8,2)`.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear it. The empty string
	// is sent as `null`, because DRF parses `""` as a number and rejects it on a nullable
	// DecimalField -- see registry.Field.EmptyIsNull.
	// +kubebuilder:validation:Pattern=`^([0-9]{1,6}(\.[0-9]{1,2})?)?$`
	// +optional
	Weight string `json:"weight,omitempty"`

	// WeightUnit is the unit Weight is given in.
	//
	// The same Go type dcim.RackBase uses, and that sharing is correct here where sharing an
	// airflow enum was not: `WeightUnitChoices` is one ChoiceSet declared once in
	// `netbox/choices.py:184`, it declares no `key`, and both models mix in the same
	// `WeightMixin` (docs/netbox-schema.md -> dcim.ModuleType, bases). One ChoiceSet is one
	// Go type.
	//
	// Unset leaves NetBox's own value alone; `""` clears it, and is sent as `null` for the
	// reason Weight gives.
	// +optional
	WeightUnit WeightUnit `json:"weightUnit,omitempty"`

	// Description is free text shown next to the module type.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the module type's long-form notes field.
	//
	// A TextField, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox.
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxModuleType is one dcim.ModuleType in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and a
// catalogue kind, so the shared-namespace shape dcim.DeviceType documents applies here too:
// `manufacturerRef`, `profileRef` and `moduleTypeRef` crossing from a team namespace into a
// shared catalogue namespace each need a NetBoxRefGrant there
// (docs/reference/netboxrefgrant.md).
//
// Absent deliberately:
//
//   - The **nine** CounterCacheFields -- `module_count` and one `*_template_count` per
//     component template kind. NetBox maintains each from the rows pointing at this type
//     (docs/netbox-schema.md, preamble on every CounterCacheField); they are in the
//     serializer's write path and the API refuses them, so writing one silently no-ops and
//     the next reconcile finds the same difference forever. Declared read-only on the
//     Descriptor.
//   - The ten `*Template` inline lists NBO-053 asks for. None of those Kinds exists yet, so
//     there is nothing to inline; deferred with them (#54).
//   - `owner` is `ForeignKey -> users.Owner` and the `users` app has no Kind.
//   - `_abs_weight` is a WeightMixin cache, and the IR records it as absent from the write
//     path entirely.
//   - `images` is an ImageAttachmentsMixin GenericRelation: the reverse of somebody else's
//     foreign key, not a column.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbmoduletype
// +kubebuilder:printcolumn:name="Manufacturer",type=string,JSONPath=`.spec.manufacturerRef.name`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="Profile",type=string,JSONPath=`.spec.profileRef.name`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxModuleType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxModuleTypeSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus   `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxModuleType) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxModuleType) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxModuleTypeList is a list of NetBoxModuleType.
// +kubebuilder:object:root=true
type NetBoxModuleTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxModuleType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxModuleType{}, &NetBoxModuleTypeList{})
}
