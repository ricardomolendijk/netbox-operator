package registry

// rackBaseFields are the twelve `dcim.RackBase` dimension columns, as they are written.
//
// A function returning a literal slice, the dcimDeviceFields shape: nothing here is dynamic
// and nothing here is a closure on a Descriptor -- it is one copy of a table two Kinds share
// because NetBox shares it. `dcim.RackType` and `dcim.Rack` both derive from `dcim.RackBase`
// (docs/netbox-schema.md -> dcim.RackType and dcim.Rack, bases), and v1alpha1.RackDimensions
// is the matching inline spec struct, so the spec names below are the same on both Kinds and
// a bound corrected on one cannot fail to be corrected on the other.
//
// The three `EmptyIsNull` entries are the nullable non-text columns. `outer_unit` and
// `weight_unit` are `blank=True, null=True` choice columns whose serializer returns `null`
// rather than `""` for an unset value, and `weight` is a nullable `DecimalField` that DRF
// refuses `""` for -- the dcim.Site latitude case (#170). Without the flag an emptied field
// differs from the value read back on every pass, which is a PATCH loop rather than an error.
//
// `_abs_max_weight` and `_abs_weight` are not here and never can be: they are `_`-prefixed
// caches NetBox maintains from these columns (docs/netbox-schema.md, preamble), and the IR
// records both as absent from the serializer's write path entirely.
func rackBaseFields() []Field {
	return []Field{
		{Spec: "width", API: "width"},
		{Spec: "uHeight", API: "u_height"},
		{Spec: "startingUnit", API: "starting_unit"},
		{Spec: "descUnits", API: "desc_units"},
		{Spec: "outerWidth", API: "outer_width"},
		{Spec: "outerHeight", API: "outer_height"},
		{Spec: "outerDepth", API: "outer_depth"},
		{Spec: "outerUnit", API: "outer_unit", EmptyIsNull: true},
		{Spec: "mountingDepth", API: "mounting_depth"},
		{Spec: "maxWeight", API: "max_weight"},
		{Spec: "weight", API: "weight", EmptyIsNull: true},
		{Spec: "weightUnit", API: "weight_unit", EmptyIsNull: true},
	}
}

// rackBaseReadOnly are the two denormalised weight caches every RackBase model carries.
//
// Listed rather than left implicit even though no field map reaches for one: the list is what
// makes a future entry that does a boot failure (ErrFieldReadOnly) instead of a payload NetBox
// silently drops and the engine re-sends forever.
var rackBaseReadOnly = []string{"_abs_max_weight", "_abs_weight"}
