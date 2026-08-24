package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// SubdeviceRole is one value of NetBox's SubdeviceRoleChoices.
//
// A parent device houses child devices in device bays; a device type that is neither leaves
// the column blank, which is why the empty string is a member here (docs/netbox-schema.md ->
// dcim.DeviceType, `subdevice_role CharField len=50 choices=SubdeviceRoleChoices`, and the
// column is `blank=True, null=True`). The values are `netbox/dcim/choices.py`,
// `SubdeviceRoleChoices`, in the 4.6.8 tree the digest was taken from. Unlike most NetBox
// choice sets this one declares no `key`, so it cannot be extended by a deployment's
// FIELD_CHOICES and the closed enum below cannot reject a legitimately configured value.
//
// +kubebuilder:validation:Enum="";parent;child
type SubdeviceRole string

const (
	// SubdeviceRoleParent houses child devices in its device bays.
	SubdeviceRoleParent SubdeviceRole = "parent"

	// SubdeviceRoleChild is installed in a parent device's device bay.
	SubdeviceRoleChild SubdeviceRole = "child"
)

// DeviceAirflow is one value of NetBox's DeviceAirflowChoices.
//
// The empty string is a member for the same reason as on SubdeviceRole: the column is
// `blank=True, null=True`, so "unspecified" is a real state (docs/netbox-schema.md ->
// dcim.DeviceType, `airflow CharField len=50 choices=DeviceAirflowChoices`). Values from
// `netbox/dcim/choices.py`, `DeviceAirflowChoices`, in the 4.6.8 tree.
//
// This choice set *does* declare `key = 'Device.airflow'`, so a deployment can add values
// through FIELD_CHOICES and a closed enum would reject one at admission. Enumerated anyway,
// following SiteStatus and PrefixStatus, whose choice sets are extensible in exactly the same
// way: a typo caught by `kubectl apply` is worth more than an extension nobody has made, and
// widening the enum is a one-line change to this list.
//
// No Go constants for the ten values, unlike SubdeviceRole's two: nothing in Go or in the
// tests names an airflow direction, and a constant per value would be ten doc comments
// restating the string they hold. The enum marker is what enforces the set.
//
// +kubebuilder:validation:Enum="";front-to-rear;rear-to-front;left-to-right;right-to-left;side-to-rear;rear-to-side;bottom-to-top;top-to-bottom;passive;mixed
type DeviceAirflow string

// NetBoxDeviceTypeSpec describes one dcim.DeviceType.
//
// One make and model of device -- `dcim.Device.device_type` is a required foreign key
// (docs/netbox-schema.md -> dcim.Device), so no NetBoxDevice exists without one.
//
// The first kind whose identity is scoped by a **required** reference to another kind. Both
// of NetBox's constraints start at `manufacturer`, so an unresolved `manufacturerRef` leaves
// no applicable candidate at all and the object writes nothing -- the NetBoxLocation shape
// rather than the NetBoxPrefix one, where the object is created without the field.
type NetBoxDeviceTypeSpec struct {
	NetBoxObjectSpec `json:",inline"`

	// ManufacturerRef is who makes this device type. Required, because NetBox's column is
	// (`manufacturer ForeignKey REQ -> dcim.Manufacturer on_delete=PROTECT`).
	//
	// It is half of both natural keys -- `meta.constraints` is `(manufacturer, model)` and
	// `(manufacturer, slug)` -- so until it resolves the object reports
	// RefsResolved=False naming this field and makes no NetBox write at all, rather than
	// creating a manufacturer-less device type NetBox would refuse anyway.
	//
	// PROTECT, so NetBox refuses to delete a manufacturer while any device type points at
	// it; that surfaces on the *manufacturer* as Deleting=False, Reason=Protected.
	ManufacturerRef ManufacturerRef `json:"manufacturerRef"`

	// Model is the model name, as NetBox displays it
	// (docs/netbox-schema.md -> dcim.DeviceType, `model CharField REQ len=100`).
	//
	// Unique per manufacturer, not globally: `..._unique_manufacturer_model` on
	// `(manufacturer, model)`. It is a candidate key and deliberately not the lookup key --
	// a kind gets one identity and `slug` is the stable one -- so a model rename that
	// collides comes back as NetBox's own 409.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	Model string `json:"model"`

	// Slug is the device type's URL-safe identifier, and half of this kind's natural key.
	//
	// Unique per manufacturer (docs/netbox-schema.md -> dcim.DeviceType, `meta.constraints`:
	// `..._unique_manufacturer_slug` on `(manufacturer, slug)`), which is what makes
	// `ubiquiti/ucg-ultra` and `mikrotik/ucg-ultra` two legitimate objects and two CRs in
	// two namespaces claiming `ubiquiti/ucg-ultra` one object and a Conflict.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=100
	// +kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$`
	Slug string `json:"slug"`

	// DefaultPlatformRef is the platform a device of this type gets when it names none of
	// its own (docs/netbox-schema.md -> dcim.DeviceType, `default_platform ForeignKey ->
	// dcim.Platform on_delete=SET_NULL`).
	//
	// Not part of the identity, so it is an ordinary reference: SET_NULL means deleting the
	// platform in NetBox clears this column rather than refusing, and the next reconcile
	// finds the drift and PATCHes it back.
	// +optional
	DefaultPlatformRef *PlatformRef `json:"defaultPlatformRef,omitempty"`

	// PartNumber is the vendor's discrete part number
	// (docs/netbox-schema.md -> dcim.DeviceType, `part_number CharField len=50`).
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=50
	// +optional
	PartNumber string `json:"partNumber,omitempty"`

	// UHeight is how many rack units a device of this type occupies, as a string.
	//
	// A string and not a float64: NetBox stores it as `u_height DecimalField decimal(4,1)
	// def=1.0` and the API returns it padded, `"1.00"` for a spec that said `"1"`. An
	// OpenAPI `number` round-trips through IEEE-754 on the way in and out of the API server,
	// so a half-height `0.5` is not reliably what was written -- and the engine compares two
	// numeric strings numerically (internal/netbox/drift.go, scalarEqual), so `"0.5"` and
	// `"0.50"` produce no PATCH.
	//
	// The pattern is the numeric-format rule and is read straight off `decimal(4,1)`: four
	// digits, one of them after the point, so 0 to 999.9 in steps of a tenth. No CEL rule on
	// top -- the column is not nullable and has no range validator, so a CEL bound would
	// duplicate the pattern rather than add a constraint.
	//
	// Defaulted to NetBox's own default, so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct.
	// +kubebuilder:default="1.0"
	// +kubebuilder:validation:Pattern=`^[0-9]{1,3}(\.[0-9])?$`
	// +optional
	UHeight string `json:"uHeight,omitempty"`

	// ExcludeFromUtilization leaves devices of this type out of rack utilisation arithmetic
	// (docs/netbox-schema.md -> dcim.DeviceType, `exclude_from_utilization BooleanField
	// def=False`).
	//
	// A pointer, for the reason IsFullDepth gives.
	// +optional
	ExcludeFromUtilization *bool `json:"excludeFromUtilization,omitempty"`

	// IsFullDepth says a device of this type consumes both the front and the rear rack face
	// (docs/netbox-schema.md -> dcim.DeviceType, `is_full_depth BooleanField def=True`).
	//
	// A pointer, and the reason is the column's default. A plain `bool` cannot tell "not
	// managed" from "managed as false", so adopting a hand-made half-depth type would
	// silently make it full-depth. Nil leaves NetBox's value alone; `false` writes false.
	// +optional
	IsFullDepth *bool `json:"isFullDepth,omitempty"`

	// SubdeviceRole is whether this type is a parent, a child, or neither.
	//
	// Unset leaves NetBox's own value alone; `""` clears it, which is how NetBox spells
	// "neither" (the column is `blank=True`). Those are two different instructions and the
	// operator tells them apart from metadata.managedFields
	// (docs/concepts/field-ownership.md) -- the wording differs from the other clearable
	// fields here only because this one carries an enum.
	// +optional
	SubdeviceRole SubdeviceRole `json:"subdeviceRole,omitempty"`

	// Airflow is the direction air moves through a device of this type.
	//
	// Unset leaves NetBox's own value alone; `""` clears it. Cleared as `null` rather than
	// as an empty string, because NetBox's serializer returns `null` for an unset choice and
	// a payload of `""` would differ from the value read back on every pass -- see
	// registry.Field.EmptyIsNull.
	// +optional
	Airflow DeviceAirflow `json:"airflow,omitempty"`

	// Description is free text shown next to the device type.
	//
	// Inherited from PrimaryModel (docs/netbox-schema.md -> dcim.DeviceType, `description
	// (PrimaryModel) CharField len=200`); an inherited column is as writable as a declared
	// one.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +kubebuilder:validation:MaxLength=200
	// +optional
	Description string `json:"description,omitempty"`

	// Comments is the device type's long-form notes field.
	//
	// Also inherited from PrimaryModel, and a TextField rather than a CharField: it has no
	// max_length, so there is no MaxLength marker to derive.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
	// NetBox. The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md).
	// +optional
	Comments string `json:"comments,omitempty"`
}

// NetBoxDeviceType is one dcim.DeviceType in NetBox.
//
// Namespaced like every kind in v1alpha1 (docs/decisions/0002-crd-scoping.md), and the kind
// where that costs the most: `deviceTypeRef` and `manufacturerRef` from a team namespace into
// a shared catalogue namespace both need a NetBoxRefGrant, and without one every NetBoxDevice
// in every team namespace sits at RefsResolved=False, Reason=RefDenied. See
// docs/reference/netboxdevicetype.md, which opens with the grant.
//
// Two things this kind does not carry, both deliberate:
//
//   - `front_image` and `rear_image` are ImageFields uploaded as multipart form data
//     (docs/netbox-schema.md -> dcim.DeviceType). The REST API returns a URL and does not
//     take one, so a spec field for either would be a field the operator sends and NetBox
//     ignores -- and a CR spec is not a file transport.
//   - `weight` and `weight_unit` come from WeightMixin and are NBO-027's declared
//     out-of-scope columns. The digest does list them, so they are verified rather than
//     guessed; they are absent because the ticket says so, and NBO-060 is the audit that
//     picks them up.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=nbdtype
// +kubebuilder:printcolumn:name="Manufacturer",type=string,JSONPath=`.spec.manufacturerRef.name`
// +kubebuilder:printcolumn:name="Model",type=string,JSONPath=`.spec.model`
// +kubebuilder:printcolumn:name="U",type=string,JSONPath=`.spec.uHeight`
// +kubebuilder:printcolumn:name="ID",type=integer,JSONPath=`.status.id`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type NetBoxDeviceType struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   NetBoxDeviceTypeSpec `json:"spec,omitempty"`
	Status NetBoxObjectStatus   `json:"status,omitempty"`
}

// NetBoxSpec returns the engine-owned part of the spec.
func (t *NetBoxDeviceType) NetBoxSpec() *NetBoxObjectSpec { return &t.Spec.NetBoxObjectSpec }

// NetBoxStatus returns the engine-owned part of the status, for the engine to write.
func (t *NetBoxDeviceType) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }

// NetBoxDeviceTypeList is a list of NetBoxDeviceType.
// +kubebuilder:object:root=true
type NetBoxDeviceTypeList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []NetBoxDeviceType `json:"items"`
}

func init() {
	SchemeBuilder.Register(&NetBoxDeviceType{}, &NetBoxDeviceTypeList{})
}
