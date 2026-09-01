package v1alpha1

// The shared half of NBO-051: NetBox declares `dcim.RackBase` as an abstract base and
// derives both `dcim.RackType` and `dcim.Rack` from it (docs/netbox-schema.md ->
// dcim.RackBase, bases: WeightMixin, PrimaryModel), so the twelve dimension columns and the
// four choice sets they use are one fact each rather than two. RackDimensions is embedded
// inline in both specs, which keeps the CR field paths flat -- `spec.uHeight`, not
// `spec.dimensions.uHeight` -- so a Descriptor's field map addresses them by their own JSON
// names and neither engine nor generator learns that the base class exists.
//
// `form_factor` is deliberately *not* here: NetBox declares it on each subclass separately
// and with different nullability -- `REQ` with no default on dcim.RackType, `blank=True,
// null=True` on dcim.Rack (docs/netbox-schema.md, and the IR's `required-on-create` conflict
// entry for dcim.RackType.form_factor) -- so one shared field would have to be wrong for one
// of the two kinds.

// RackFormFactor is one value of NetBox's RackFormFactorChoices.
//
// Read from `netbox/dcim/choices.py:54`, `RackFormFactorChoices`, in the same NetBox 4.6.8
// tree docs/netbox-schema.md was taken from. The ChoiceSet declares no `key`, so a
// deployment cannot extend it through FIELD_CHOICES and the closed enum below cannot reject
// a legitimately configured value.
//
// The empty string is a member because dcim.Rack's column is `blank=True, null=True`
// (docs/netbox-schema.md -> dcim.Rack, `form_factor CharField len=50`), so "unspecified" is
// a real state there. NetBoxRackType narrows it back by requiring the field, since its own
// column is NOT NULL with no default.
//
// +kubebuilder:validation:Enum="";"2-post-frame";"4-post-frame";"4-post-cabinet";wall-frame;wall-frame-vertical;wall-cabinet;wall-cabinet-vertical
type RackFormFactor string

const (
	// RackFormFactorTwoPostFrame is an open two-post relay rack.
	RackFormFactorTwoPostFrame RackFormFactor = "2-post-frame"

	// RackFormFactorFourPostFrame is an open four-post frame.
	RackFormFactorFourPostFrame RackFormFactor = "4-post-frame"

	// RackFormFactorFourPostCabinet is an enclosed four-post cabinet.
	RackFormFactorFourPostCabinet RackFormFactor = "4-post-cabinet"
)

// RackWidth is one value of NetBox's RackWidthChoices, in inches.
//
// An integer rather than a string: the column is
// `width PositiveSmallIntegerField def=UNRESOLVED:RackWidthChoices.WIDTH_19IN
// choices=RackWidthChoices` (docs/netbox-schema.md -> dcim.RackBase), so NetBox stores and
// returns a number. The four members are `netbox/dcim/choices.py:75` at 4.6.8: 10, 19, 21
// and 23 inches. The ChoiceSet declares no `key` and so is not extensible.
//
// No Go constants: nothing in Go or in the tests names a rack width, and four constants
// would be four doc comments restating the integer they hold. The enum marker is what
// enforces the set.
//
// +kubebuilder:validation:Enum=10;19;21;23
type RackWidth int32

// RackDimensionUnit is one value of NetBox's RackDimensionUnitChoices: the unit the three
// `outer*` measurements and `mountingDepth` are given in.
//
// Two members, `netbox/dcim/choices.py:108` at 4.6.8, plus the empty string because the
// column is `blank=True, null=True` (docs/netbox-schema.md -> dcim.RackBase, `outer_unit
// CharField len=50 choices=RackDimensionUnitChoices`). The ChoiceSet declares no `key` and
// so is not extensible.
//
// +kubebuilder:validation:Enum="";mm;in
type RackDimensionUnit string

const (
	// RackDimensionUnitMillimeters measures the outer dimensions in millimetres.
	RackDimensionUnitMillimeters RackDimensionUnit = "mm"

	// RackDimensionUnitInches measures the outer dimensions in inches.
	RackDimensionUnitInches RackDimensionUnit = "in"
)

// WeightUnit is one value of NetBox's WeightUnitChoices.
//
// Declared in `netbox/netbox/choices.py:184` rather than in `dcim`, because WeightMixin is a
// NetBox-wide mixin (docs/netbox-schema.md -> dcim.RackBase, `weight_unit (WeightMixin)`).
// The empty string is a member because the column is `blank=True, null=True`. The ChoiceSet
// declares no `key` and so is not extensible.
//
// +kubebuilder:validation:Enum="";kg;g;lb;oz
type WeightUnit string

const (
	// WeightUnitKilograms measures the weight in kilograms.
	WeightUnitKilograms WeightUnit = "kg"

	// WeightUnitGrams measures the weight in grams.
	WeightUnitGrams WeightUnit = "g"

	// WeightUnitPounds measures the weight in pounds.
	WeightUnitPounds WeightUnit = "lb"

	// WeightUnitOunces measures the weight in ounces.
	WeightUnitOunces WeightUnit = "oz"
)

// RackDimensions are the physical measurements NetBox's `dcim.RackBase` declares, shared by
// NetBoxRackType and NetBoxRack.
//
// Embedded inline in both specs, so every field below is addressed as `spec.<field>` and the
// two kinds cannot drift apart on a validation bound. `_abs_max_weight` and `_abs_weight` are
// absent and always will be: they are `_`-prefixed denormalised caches NetBox maintains from
// `maxWeight`/`weight` and `weightUnit` (docs/netbox-schema.md, preamble on `_`-prefixed
// columns), and the IR records both as absent from the serializer's write path entirely.
type RackDimensions struct {
	// Width is the rail-to-rail width in inches.
	//
	// Defaulted to NetBox's own default so the operator manages the field from the first
	// reconcile: a defaulted field that never reaches a payload is a field the operator can
	// never correct (docs/netbox-schema.md -> dcim.RackBase, `width
	// PositiveSmallIntegerField def=UNRESOLVED:RackWidthChoices.WIDTH_19IN`, which is 19).
	// +kubebuilder:default=19
	// +optional
	Width RackWidth `json:"width,omitempty"`

	// UHeight is the rack's height in rack units.
	//
	// Defaulted to NetBox's own default, for the reason Width gives. The digest records it as
	// `u_height PositiveSmallIntegerField def=UNRESOLVED:RACK_U_HEIGHT_DEFAULT`
	// (docs/netbox-schema.md -> dcim.RackBase) -- a symbol the AST walk does not evaluate --
	// and `RACK_U_HEIGHT_DEFAULT` is 42 in `netbox/dcim/constants.py` in the same 4.6.8 tree
	// the digest was taken from.
	//
	// The floor is 1 because `startingUnit` is 1-based and a rack of no units has no
	// elevation to mount anything in. There is deliberately no ceiling marker: NetBox applies
	// a tighter model-level validator than the column's own `PositiveSmallIntegerField` range
	// and the digest does not carry its bound, so restating one here would be a guess -- an
	// over-tall rack comes back as NetBox's own `400`, reported as
	// `Ready=False, Reason=Invalid`.
	// +kubebuilder:default=42
	// +kubebuilder:validation:Minimum=1
	// +optional
	UHeight int32 `json:"uHeight,omitempty"`

	// StartingUnit is the number given to the lowest rack unit.
	//
	// Defaulted to NetBox's own default for the reason Width gives: the digest records
	// `starting_unit PositiveSmallIntegerField def=UNRESOLVED:RACK_STARTING_UNIT_DEFAULT`
	// (docs/netbox-schema.md -> dcim.RackBase), and `RACK_STARTING_UNIT_DEFAULT` is 1 in
	// `netbox/dcim/constants.py` at 4.6.8.
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +optional
	StartingUnit int32 `json:"startingUnit,omitempty"`

	// DescUnits numbers the rack units from the top down rather than from the bottom up
	// (docs/netbox-schema.md -> dcim.RackBase, `desc_units BooleanField def=False`).
	//
	// A pointer, and the reason is the column's default. A plain `bool` cannot tell "not
	// managed" from "managed as false", so adopting a hand-made descending rack would
	// silently renumber every unit in it. Nil leaves NetBox's value alone; `false` writes
	// false.
	// +optional
	DescUnits *bool `json:"descUnits,omitempty"`

	// OuterWidth is the external width of the rack, in OuterUnit.
	//
	// A pointer with **two** states rather than three, and that is a statement about the
	// column rather than an omission here: `outer_width PositiveSmallIntegerField` is
	// `blank=True, null=True` and every value it can hold is a real measurement, so there is
	// no empty *value* to write. Nil leaves NetBox's own value alone; a number claims and
	// sets it. Clearing the column back to null is NBO-060's audit item, not a state this
	// field can express.
	// +kubebuilder:validation:Minimum=1
	// +optional
	OuterWidth *int32 `json:"outerWidth,omitempty"`

	// OuterHeight is the external height of the rack, in OuterUnit. Two states, as
	// OuterWidth explains.
	// +kubebuilder:validation:Minimum=1
	// +optional
	OuterHeight *int32 `json:"outerHeight,omitempty"`

	// OuterDepth is the external depth of the rack, in OuterUnit. Two states, as OuterWidth
	// explains.
	// +kubebuilder:validation:Minimum=1
	// +optional
	OuterDepth *int32 `json:"outerDepth,omitempty"`

	// OuterUnit is the unit the three `outer*` measurements are given in.
	//
	// Unset leaves NetBox's own value alone; `""` clears it. Cleared as `null` rather than as
	// an empty string, because NetBox's serializer returns `null` for an unset choice and a
	// payload of `""` would differ from the value read back on every pass -- see
	// registry.Field.EmptyIsNull.
	// +optional
	OuterUnit RackDimensionUnit `json:"outerUnit,omitempty"`

	// MountingDepth is the usable depth between the mounting rails, in millimetres
	// (docs/netbox-schema.md -> dcim.RackBase, `mounting_depth PositiveSmallIntegerField`).
	//
	// Millimetres regardless of OuterUnit: NetBox documents this column as mm on the model
	// itself, and it is the one measurement here that does not follow the unit choice. Two
	// states, as OuterWidth explains.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MountingDepth *int32 `json:"mountingDepth,omitempty"`

	// MaxWeight is the maximum load the rack is rated for, in WeightUnit
	// (docs/netbox-schema.md -> dcim.RackBase, `max_weight PositiveIntegerField`).
	//
	// Two states, as OuterWidth explains.
	// +kubebuilder:validation:Minimum=1
	// +optional
	MaxWeight *int32 `json:"maxWeight,omitempty"`

	// Weight is the rack's own weight, in WeightUnit, as a string.
	//
	// A string and not a float64, the same decision `NetBoxDeviceType.uHeight` documents:
	// NetBox stores it as `weight DecimalField decimal(8,2)` (docs/netbox-schema.md ->
	// dcim.RackBase, from WeightMixin) and the API returns it padded, `"18.50"` for a spec
	// that said `"18.5"`. An OpenAPI `number` round-trips through IEEE-754 on the way in and
	// out of the API server, so a fractional weight is not reliably what was written -- and
	// the engine compares two numeric strings numerically (internal/netbox/drift.go,
	// scalarEqual), so `"18.5"` and `"18.50"` produce no PATCH.
	//
	// The pattern is read straight off `decimal(8,2)`: eight digits, two of them after the
	// point, so 0 to 999999.99 in hundredths.
	//
	// Omit it to leave NetBox's own value alone; set it to `""` to clear the value in NetBox.
	// The two are different intents and the operator can tell them apart
	// (docs/concepts/field-ownership.md). The empty string is sent as `null`, because DRF
	// parses `""` as a number and rejects it on a nullable DecimalField -- see
	// registry.Field.EmptyIsNull.
	// +kubebuilder:validation:Pattern=`^([0-9]{1,6}(\.[0-9]{1,2})?)?$`
	// +optional
	Weight string `json:"weight,omitempty"`

	// WeightUnit is the unit Weight and MaxWeight are given in.
	//
	// Unset leaves NetBox's own value alone; `""` clears it, and is sent as `null` for the
	// reason OuterUnit gives.
	// +optional
	WeightUnit WeightUnit `json:"weightUnit,omitempty"`
}
