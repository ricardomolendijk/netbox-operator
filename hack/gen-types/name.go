package main

import (
	"strings"
	"unicode"
)

// namer turns NetBox's snake_case column names into Go identifiers and JSON names, with one
// acronym table so `vrf` is `VRFRef` in both.
type namer struct{ acronyms []string }

// goName is the exported Go field name for a snake_case column: `mark_utilized` ->
// `MarkUtilized`, `qinq_svlan` -> `QinQSVLAN`.
func (n namer) goName(column string) string {
	var out strings.Builder

	for _, part := range strings.Split(column, "_") {
		out.WriteString(n.word(part))
	}

	return out.String()
}

// jsonName is the camelCase JSON name: `mark_utilized` -> `markUtilized`, `qinq_svlan` ->
// `qinqSVLAN`. The leading word is lower-cased whole, so an acronym-initial name reads
// `vrfRef` rather than `vRFRef`.
func (n namer) jsonName(column string) string {
	parts := strings.Split(column, "_")

	out := strings.ToLower(parts[0])
	for _, part := range parts[1:] {
		out += n.word(part)
	}

	return out
}

// word capitalises one part, keeping a known initialism in the casing the acronym table
// spells it: `vrf` -> `VRF`, `qinq` -> `QinQ`. Whole-part matching only. Prefix matching was
// tried and reverted -- with `ID` in the table it turns `idle` into `IDle`, and a table that
// has to be audited against every English word that starts with an initialism is a worse
// bargain than spelling the glued cases out.
func (n namer) word(part string) string {
	if part == "" {
		return ""
	}

	for _, acronym := range n.acronyms {
		if strings.EqualFold(part, acronym) {
			return acronym
		}
	}

	return string(unicode.ToUpper(rune(part[0]))) + part[1:]
}

// enumTypeName is the Go type for a ChoiceSet: `PrefixStatusChoices` -> `PrefixStatus`.
//
// The class name is the key, not the column, because two kinds sharing a ChoiceSet must
// share one Go type -- and because a column name would collide the moment a second kind has
// a `status`.
func (n namer) enumTypeName(choiceSet string) string {
	return strings.TrimSuffix(choiceSet, "Choices")
}

// enumConstName is the constant for one member: (`PrefixStatus`, `active`) ->
// `PrefixStatusActive`.
//
// A decimal point becomes a `p` rather than a word break, because dropping it collides:
// `dcim.InterfaceTypeChoices` has both `25gbase-t` and `2.5gbase-t`, and with the point
// discarded both spell `25gbaseT`. Every other separator can be dropped, since the runs either
// side of it differ.
func (n namer) enumConstName(typeName, value string) string {
	parts := make([]string, 0, len(value))

	for _, run := range strings.FieldsFunc(strings.ReplaceAll(value, ".", "p"), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		parts = append(parts, n.word(run))
	}

	name := typeName + strings.Join(parts, "")
	if !unicode.IsLetter(rune(name[0])) {
		return typeName + "Value" + strings.Join(parts, "")
	}

	return name
}

// kubeKind is the CRD kind for a NetBox model: `Prefix` -> `NetBoxPrefix`.
func kubeKind(model string) string { return "NetBox" + model }

// refTypeName is the typed ObjectRef alias for a target kind: `ipam.VLAN` -> `VLANRef`.
func (n namer) refTypeName(target string) string {
	_, model, _ := strings.Cut(target, ".")

	return n.goName(model) + "Ref"
}
