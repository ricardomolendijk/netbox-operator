package main

import (
	"fmt"
	"slices"
	"strings"
)

// refListBound is the MaxItems every to-many reference field carries. ObjectRef has five CEL
// rules and the API server costs each at the list's maximum length, so an unbounded list is
// costed as unbounded and the whole CRD is refused at install -- while controller-gen,
// kustomize and `make verify` all stay green (CONTRIBUTING.md, "A list of references needs a
// MaxItems"; api/v1alpha1/reflistbounds_test.go).
const refListBound = 256

// slugPattern is NetBox's own SlugField validator.
const slugPattern = `^[-a-zA-Z0-9_]+$`

// triStateNote is the sentence internal/controller/manifests_test.go looks for, verbatim. It
// is emitted only on a field whose schema can actually hold the empty value: the same test
// fails a field that documents an empty state its own `default`, `enum`, `minLength` or
// pattern forbids.
func triStateNote() []string {
	return []string{
		"Omit it to leave NetBox's own value alone; set it to `\"\"` to clear the value in",
		"NetBox. The two are different intents and the operator can tell them apart",
		"(docs/concepts/field-ownership.md).",
	}
}

// docWidth is where a doc comment wraps. Not cosmetic: `kubectl explain` prints these, and a
// line that runs past a terminal is what a reader stops reading.
const docWidth = 92

// wrap breaks one sentence into comment lines at docWidth, never mid-word.
func wrap(text string) []string {
	var out []string

	line := ""

	for _, word := range strings.Fields(text) {
		if line != "" && len(line)+1+len(word) > docWidth {
			out = append(out, line)
			line = ""
		}

		if line != "" {
			line += " "
		}

		line += word
	}

	if line != "" {
		out = append(out, line)
	}

	return out
}

// stringTypes are the Django field classes the REST API returns as a JSON string. Decimals
// are absent on purpose: they are strings too, and they get a numeric-format rule the others
// must not have.
var stringTypes = []string{
	"CharField", "TextField", "SlugField", "ColorField", "URLField", "EmailField",
	"IPAddressField", "IPNetworkField", "MACAddressField", "WWNField", "TimeZoneField",
	"DateField", "DateTimeField", "UUIDField", "FilePathField", "NaturalOrderingField",
}

// intTypes are the Django field classes that arrive as a JSON number.
var intTypes = []string{
	"PositiveSmallIntegerField", "PositiveIntegerField", "PositiveBigIntegerField",
	"SmallIntegerField", "BigIntegerField", "ASNField",
}

// field is one emitted spec field.
type field struct {
	// Doc is the provenance comment: the NetBox column, its Django class and, for a
	// reference, the target and on_delete. A comment that restates the code is banned
	// (CONTRIBUTING.md), so there is no sentence here that the field name already says.
	Doc []string

	// Markers are the kubebuilder markers, in emission order.
	Markers []string

	Name string
	Type string
	Tag  string
}

// buildField maps one IR column onto one spec field, or reports that it is not emitted.
//
// same fan-out behind a table that is harder to read than the switch.
//
//nolint:cyclop // One branch per NetBox field class, and collapsing them would only move the
func (b *builder) buildField(kind irKind, f irField) (field, bool, error) {
	optional := !f.Required

	out := field{Name: b.goFieldName(f), Doc: b.fieldDoc(kind, f)}

	goType, markers, err := b.fieldType(kind, f, optional)
	if err != nil {
		return field{}, false, err
	}

	out.Type = goType
	out.Markers = append(out.Markers, markers...)

	out.Tag = fmt.Sprintf("`json:%q`", b.jsonFieldName(f)+omitEmpty(optional))

	// A default only on a value type. On a pointer it would defeat the pointer: the field is
	// never absent once the API server fills it in, so "not managed" and "managed as the
	// default" become the same state and adopting an object a human had set would silently
	// reset it.
	if def := b.defaultMarker(f); def != "" && !strings.HasPrefix(goType, "*") {
		out.Markers = append(out.Markers, def)
	}

	if optional {
		out.Markers = append(out.Markers, "+optional")
	}

	if goType == "string" && b.clearable(f, optional, out.Markers) {
		out.Doc = append(out.Doc, "")
		out.Doc = append(out.Doc, triStateNote()...)
	}

	return out, true, nil
}

// goFieldName is the exported Go field name, built from the column and the same suffix the
// JSON name gets, so `vrf` is `VRFRef` next to `json:"vrfRef"` rather than `VrfRef`.
func (b *builder) goFieldName(f irField) string {
	name := b.names.goName(f.Name)

	// A `Ref` suffix on a to-one reference and none on a to-many, which is the convention the
	// shipped kinds set: `vrfRef` names one object, `importTargets` names a set, and the
	// plural already says a list of references is what it is (api/v1alpha1/ipam_vrf.go).
	if f.Class == "Ref" {
		return name + "Ref"
	}

	return name
}

// jsonFieldName is the spec field's JSON name. A reference gets a `Ref` suffix so a field
// naming an object is visibly not a string: `vrf` is written `vrfRef`.
func (b *builder) jsonFieldName(f irField) string {
	name := b.names.jsonName(f.Name)

	if f.Class == "Ref" {
		return name + "Ref"
	}

	return name
}

// fieldType is the Go type and the validation markers that go with it.
func (b *builder) fieldType(kind irKind, f irField, optional bool) (string, []string, error) {
	switch f.Class {
	case "Ref":
		return b.refType(f, optional)
	case "M2M":
		target, err := b.refTarget(f)
		if err != nil {
			return "", nil, err
		}

		return "[]" + target, []string{fmt.Sprintf("+kubebuilder:validation:MaxItems=%d", refListBound)}, nil
	case "ObjectTypeList":
		return "[]string", objectTypeListMarkers(), nil
	case "Enum":
		return b.enumType(kind, f)
	case "Decimal":
		return "string", decimalMarkers(f), nil
	case "Array":
		return b.arrayType(kind, f)
	case "Scalar":
		return scalarType(kind, f, optional)
	}

	return "", nil, fmt.Errorf("%w: %s.%s is class %q", errUnmappedField, kind.App, f.Name, f.Class)
}

// refType is a single reference: a pointer when it is optional, because an optional reference
// has three states and a struct value cannot express "absent"; a value when it is required,
// because there is no absent state to express and a required pointer says two contradictory
// things about the same field. The shipped kinds set the convention both ways
// (api/v1alpha1/virtualization_vminterface.go, `VirtualMachineRef VirtualMachineRef`).
func (b *builder) refType(f irField, optional bool) (string, []string, error) {
	target, err := b.refTarget(f)
	if err != nil {
		return "", nil, err
	}

	if !optional {
		return target, nil, nil
	}

	return "*" + target, nil, nil
}

// refTarget is the typed ObjectRef alias for an FK's target, and records that the alias has
// to be emitted.
func (b *builder) refTarget(f irField) (string, error) {
	if f.Ref == nil || f.Ref.Target == "" {
		return "", fmt.Errorf("%w: %s has no ref target", errUnmappedField, f.Name)
	}

	name := b.names.refTypeName(f.Ref.Target)
	b.refs[name] = f.Ref.Target

	return name, nil
}

// enumType is the shared choices type, and records the ChoiceSet for the enum file.
func (b *builder) enumType(kind irKind, f irField) (string, []string, error) {
	set, ok := b.ir.Enums[f.Enum]
	if !ok {
		return "", nil, fmt.Errorf("%w: %s.%s cites %s", errUnknownEnum, kind.App, f.Name, f.Enum)
	}

	b.enums[f.Enum] = set

	return b.names.enumTypeName(f.Enum), nil, nil
}

// scalarType maps a plain column. A nullable or defaulted number is a pointer: a plain
// int32 cannot tell "not managed" from "managed as zero", and adopting an object a human had
// set would silently clear it. The same argument makes every boolean a pointer, since
// BooleanField always carries a default.
func scalarType(kind irKind, f irField, optional bool) (string, []string, error) {
	switch {
	case slices.Contains(stringTypes, f.Type):
		return "string", stringMarkers(f), nil
	case f.Type == "BooleanField":
		return "*bool", nil, nil
	case slices.Contains(intTypes, f.Type):
		if optional {
			return "*int32", nil, nil
		}

		return "int32", nil, nil
	}

	return "", nil, fmt.Errorf("%w: %s.%s is %s", errUnmappedField, kind.App, f.Name, f.Type)
}

// stringMarkers are the length and pattern bounds NetBox's own column declares. MinLength=1
// on a required column, because NetBox rejects an empty required string and a rejection at
// admission names the field.
func stringMarkers(f irField) []string {
	var out []string

	if f.Required {
		out = append(out, "+kubebuilder:validation:MinLength=1")
	}

	if f.SQL.MaxLength > 0 {
		out = append(out, fmt.Sprintf("+kubebuilder:validation:MaxLength=%d", f.SQL.MaxLength))
	}

	if f.Type == "SlugField" {
		out = append(out, "+kubebuilder:validation:Pattern=`"+slugPattern+"`")
	}

	if f.Type == "ColorField" {
		out = append(out, "+kubebuilder:validation:Pattern=`^[0-9a-f]{6}$`")
	}

	return out
}

// decimalMarkers type a decimal as a string with a numeric-format rule. The API returns
// decimals as strings -- `dcim.DeviceType.u_height`, `dcim.Device.position`,
// `virtualization.VirtualMachine.vcpus` (plan.md 8.1) -- so a float64 here would round-trip
// `1.5` as `1.5` and `1.0` as `1`, and the drift comparison would fire forever.
func decimalMarkers(f irField) []string {
	whole, places := max(f.SQL.MaxDigits-f.SQL.DecimalPlaces, 1), max(f.SQL.DecimalPlaces, 1)

	// `[0-9]` and `[.]` rather than `\d` and `\.`: a backslash inside a double-quoted marker
	// value is read as a Go char escape by controller-gen's own parser, and `\d` is not one, so
	// the whole marker is rejected with `invalid char escape` and no CRD is written at all. A
	// Pattern marker can use backticks and escape freely; an XValidation rule cannot.
	pattern := fmt.Sprintf(`^[0-9]{1,%d}([.][0-9]{1,%d})?$`, whole, places)

	return []string{
		"+kubebuilder:validation:Pattern=`" + pattern + "`",
		fmt.Sprintf("+kubebuilder:validation:XValidation:rule=\"self.matches('%s')\","+
			"message=\"must be a decimal with at most %d digits before and %d after the point, "+
			"written as a string: NetBox returns this column as a string\"", pattern, whole, places),
	}
}

// objectTypeListMarkers bound a list of `app_label.model` strings. Not references: the
// values name content types rather than NetBox objects, so there is no CR to point at
// (registry.ClassObjectTypeList).
func objectTypeListMarkers() []string {
	return []string{
		fmt.Sprintf("+kubebuilder:validation:MaxItems=%d", refListBound),
		"+kubebuilder:validation:items:MaxLength=100",
		"+kubebuilder:validation:items:Pattern=`^[a-z_]+\\.[a-z0-9_]+$`",
	}
}

// arrayType is a Postgres ArrayField, whose element type is a constructor argument the AST
// walk does not record. There is no guess to make, so the override is required and its
// absence is a named failure rather than a `[]string` that silently accepts anything.
func (b *builder) arrayType(kind irKind, f irField) (string, []string, error) {
	goType, ok := b.over.of(kind.App + "." + kind.Model).GoTypes[f.Name]
	if !ok {
		return "", nil, fmt.Errorf("%w: %s.%s is an ArrayField; declare goTypes.%s in overrides.yaml",
			errUnmappedField, kind.App, f.Name, f.Name)
	}

	markers := []string{fmt.Sprintf("+kubebuilder:validation:MaxItems=%d", refListBound)}

	return goType, markers, nil
}

// defaultMarker mirrors NetBox's own default so the operator manages the field from the first
// reconcile: a defaulted field that never reaches a payload is one the operator can never
// correct.
//
// A choice default arrives as the unevaluated attribute reference the AST walk saw --
// `PrefixStatusChoices.STATUS_ACTIVE` -- so it is resolved by matching the attribute's tail
// against the set's own values, and dropped when it does not match. Guessing here would put a
// value NetBox never accepts into every CRD's schema.
func (b *builder) defaultMarker(f irField) string {
	switch value := f.SQL.Default.(type) {
	case bool, float64:
		return fmt.Sprintf("+kubebuilder:default=%v", value)
	case string:
		return b.choiceDefault(f, value)
	}

	return ""
}

// choiceDefault resolves `<Set>.<ATTR>` to the member value ATTR names.
func (b *builder) choiceDefault(f irField, reference string) string {
	set, ok := b.ir.Enums[f.Enum]
	if !ok {
		return ""
	}

	_, attr, found := strings.Cut(reference, ".")
	if !found {
		return ""
	}

	tail := strings.ToLower(attr[strings.LastIndex(attr, "_")+1:])

	for _, choice := range set.Values {
		if choice.String() == tail {
			return "+kubebuilder:default=" + choice.String()
		}
	}

	return ""
}

// clearable reports whether the tri-state note applies: the field is optional, and nothing in
// its own schema forbids the empty value. The condition mirrors
// internal/controller/manifests_test.go's forbidsEmptyValue exactly, because that test fails
// both directions -- a field that documents a state its schema rejects, and an object kind
// that documents none at all.
func (b *builder) clearable(f irField, optional bool, markers []string) bool {
	if !optional || f.Class == "Enum" {
		return false
	}

	for _, marker := range markers {
		for _, forbids := range []string{":default=", "MinLength=", "MinItems=", "Pattern="} {
			if strings.Contains(marker, forbids) {
				return false
			}
		}
	}

	return true
}

// omitEmpty is the tag suffix. Keeping omitempty is not cosmetic: without it a typed Go
// client marshals every unset string as `""` and claims it, so adopting a pre-existing
// NetBox object would wipe every value the user had not restated (CONTRIBUTING.md).
func omitEmpty(optional bool) string {
	if optional {
		return ",omitempty"
	}

	return ""
}

// fieldDoc is the provenance comment: the NetBox column and its Django class, plus the FK
// target and on_delete where there is one, and the base that declared an inherited column.
func (b *builder) fieldDoc(kind irKind, f irField) []string {
	model := kind.App + "." + kind.Model
	where := model + "." + f.Name

	if f.DeclaredBy != "" {
		where += " (inherited from " + f.DeclaredBy + ")"
	}

	name := b.goFieldName(f)

	if f.Ref != nil && f.Ref.Target != "" && f.SQL.OnDelete != "" {
		return wrap(fmt.Sprintf("%s is %s -- %s to %s, on_delete=%s.",
			name, where, f.Type, f.Ref.Target, strings.TrimPrefix(f.SQL.OnDelete, "models.")))
	}

	if f.Ref != nil && f.Ref.Target != "" {
		return wrap(fmt.Sprintf("%s is %s -- %s to %s.", name, where, f.Type, f.Ref.Target))
	}

	if f.Class == "Enum" {
		set := b.ir.Enums[f.Enum]

		return wrap(fmt.Sprintf("%s is %s -- %s, choices %s (%s).",
			name, where, f.Type, f.Enum, set.Source))
	}

	return wrap(fmt.Sprintf("%s is %s -- %s.", name, where, f.Type))
}
