package main

import (
	"fmt"
	"slices"
	"strings"
)

// nullColumnNames maps the overrides' short spelling onto the registry constant. A name absent
// from this map fails the run: registry.NullColumn has no zero value on purpose.
var nullColumnNames = map[string]string{
	"ref": nullColumnRef, "char": nullColumnChar, "numeric": nullColumnNumeric,
}

// nullColumn is the registry.NullColumn value a pin declares. Spelled here as the strings the
// template emits, because the class decides the wire spelling and there is no default that is
// safe for all of them: registry.NullColumn has no zero value, so an undeclared class fails
// Validate() at boot rather than guessing (internal/registry/naturalkey.go).
const (
	nullColumnRef     = "NullColumnRef"
	nullColumnChar    = "NullColumnChar"
	nullColumnNumeric = "NullColumnNumeric"
)

// refFieldTypes are the Django classes whose filter NetBox registers as
// ModelMultipleChoiceFilter, which takes FILTER_NEGATION_LOOKUP_MAP and so registers `n` and
// nothing else. `?x_id__empty=true` and `?x_id__isnull=true` are both dropped without a word;
// the pin is the sentinel `?x_id=null` (#216).
var refFieldTypes = []string{"ForeignKey", "OneToOneField", "TreeForeignKey"}

// lookupNames map the IR's lookup suffix onto the registry constant. Only the two a natural key
// may use: substring, prefix and negation lookups cannot identify at most one object, so a
// suffix absent from this map fails the run rather than becoming a filter that matches many.
var lookupNames = map[string]string{"": "", "ie": "LookupIExact"}

// keyField is one matched filter of a candidate.
type keyField struct {
	Filter string
	Spec   string
	Lookup string
}

// nullField is one filter pinned to null.
type nullField struct {
	Filter string
	Spec   string
	Column string
}

// naturalKey is one lookup candidate.
type naturalKey struct {
	// Doc cites the constraint the candidate came from, which is the only reviewable fact
	// about it: a key that does not match a UNIQUE is a key that adopts the wrong object.
	Doc        []string
	Fields     []keyField
	NullFields []nullField
}

// buildNaturalKeys turns a kind's constraints into candidates, in IR order.
//
// Two sources, because `meta.constraints` is not all of them: a column with `unique=True` on
// the field itself carries no Meta entry, so the IR lists no candidate for dcim.Site.slug or
// ipam.VRF.rd and a descriptor built from `natural_keys` alone fails Validate with
// ErrNoNaturalKey. The constraint-derived candidates come first, since a composite key is
// more specific than a single column.
func (b *builder) buildNaturalKeys(kind irKind, specOf map[string]string) ([]naturalKey, error) {
	if declared := b.over.of(kind.App + "." + kind.Model).NaturalKeys; len(declared) > 0 {
		return declaredKeys(kind, declared)
	}

	out := make([]naturalKey, 0, len(kind.NaturalKeys))

	for _, candidate := range kind.NaturalKeys {
		key, ok, err := b.buildCandidate(kind, candidate, specOf)
		if err != nil {
			return nil, err
		}

		if ok {
			out = append(out, key)
		}
	}

	out = append(out, b.uniqueColumnKeys(kind, specOf)...)

	// An empty list is not a smaller descriptor, it is one that fails Validate with
	// ErrNoNaturalKey at boot. Failing here instead names the kind and says where to fix it.
	if len(out) == 0 {
		return nil, fmt.Errorf("%w: %s.%s has no UNIQUE the operator can query; "+
			"declare naturalKeys for it in overrides.yaml, with the citation for why it identifies "+
			"at most one object", errNoNaturalKey, kind.App, kind.Model)
	}

	return out, nil
}

// declaredKeys are the candidates overrides.yaml states outright.
func declaredKeys(kind irKind, declared []naturalKeyOverride) ([]naturalKey, error) {
	out := make([]naturalKey, 0, len(declared))

	for _, candidate := range declared {
		if candidate.Doc == "" {
			return nil, fmt.Errorf("%w: a naturalKeys entry on %s.%s carries no doc",
				errNoNaturalKey, kind.App, kind.Model)
		}

		key := naturalKey{Doc: wrap(candidate.Doc)}

		for _, f := range candidate.Fields {
			lookup, known := lookupNames[f.Lookup]
			if !known {
				return nil, fmt.Errorf("%w: %s.%s declares lookup %q; use \"\" or \"ie\"",
					errUnknownLookup, kind.App, kind.Model, f.Lookup)
			}

			key.Fields = append(key.Fields, keyField{Filter: f.Filter, Spec: f.Spec, Lookup: lookup})
		}

		for _, f := range candidate.NullFields {
			class, known := nullColumnNames[f.Column]
			if !known {
				return nil, fmt.Errorf("%w: %s.%s pins %s as %q; use ref, char or numeric",
					errUnpinnableColumn, kind.App, kind.Model, f.Filter, f.Column)
			}

			key.NullFields = append(key.NullFields, nullField{Filter: f.Filter, Spec: f.Spec, Column: class})
		}

		out = append(out, key)
	}

	return out, nil
}

// uniqueColumnKeys are the single-column candidates a `unique=True` field declares.
func (b *builder) uniqueColumnKeys(kind irKind, specOf map[string]string) []naturalKey {
	out := make([]naturalKey, 0, len(kind.Fields))

	for _, f := range kind.Fields {
		spec, emitted := specOf[f.Name]
		if !f.SQL.Unique || !emitted || !b.registers(kind, f.Name) {
			continue
		}

		out = append(out, naturalKey{
			Doc: wrap(fmt.Sprintf("`%s` carries UNIQUE on the column itself, which is not a "+
				"meta.constraints entry and so is absent from the IR's natural_keys.", f.Name)),
			Fields: []keyField{{Filter: f.Name, Spec: spec}},
		})
	}

	return out
}

// buildCandidate maps one constraint. It returns false for a candidate the operator cannot
// express -- a column whose spec field is not emitted, so Applicable could never read it.
func (b *builder) buildCandidate(kind irKind, c irNaturalKey, specOf map[string]string) (naturalKey, bool, error) {
	out := naturalKey{Doc: wrap("From " + c.Source)}

	for _, f := range c.Fields {
		spec, ok := specOf[f.Column]
		if !ok || f.Filter == "" {
			return naturalKey{}, false, nil
		}

		lookup, known := lookupNames[f.Lookup]
		if !known {
			return naturalKey{}, false, fmt.Errorf("%w: %s.%s matches %s with lookup %q",
				errUnknownLookup, kind.App, kind.Model, f.Column, f.Lookup)
		}

		out.Fields = append(out.Fields, keyField{Filter: f.Filter, Spec: spec, Lookup: lookup})
	}

	for _, f := range c.NullFields {
		pin, ok, err := b.nullPin(kind, f, specOf)
		if err != nil {
			return naturalKey{}, false, err
		}

		if !ok {
			return naturalKey{}, false, nil
		}

		if slices.ContainsFunc(out.NullFields, func(p nullField) bool { return p.Filter == pin.Filter }) {
			// Two halves of one pair redirect onto one filter. One pin, not two.
			continue
		}

		out.NullFields = append(out.NullFields, pin)
	}

	if len(out.Fields) == 0 {
		return naturalKey{}, false, nil
	}

	return out, true, nil
}

// nullPin decides the class of a null-pinned column, which is what decides its wire spelling.
//
// The IR's own verdict is deliberately not trusted here: it marks every FK pin `unusable`
// because neither `__isnull` nor `__empty` is a registered parameter, which was true until
// #216 established the sentinel `?x_id=null`. The IR is the source for *what the filterset
// registers*; this is the client's statement of what it can spell, and the two are different
// questions.
//
// A content-type column gets no pin and never can: `scope_type`'s filter is
// MultiValueContentTypeFilter, which registers neither spelling, and the sentinel is worse
// than dropped -- it makes the filter `scope_type__in=[]`, which matches nothing, so the
// engine would create a duplicate instead of adopting. The paired `_id` half asks the same
// question, because NetBox refuses one half without the other.
func (b *builder) nullPin(kind irKind, f irNullField, specOf map[string]string) (nullField, bool, error) {
	column := b.column(kind, f.Column)
	if column == nil {
		return nullField{}, false, nil
	}

	spec, ok := specOf[f.Column]
	if !ok {
		return nullField{}, false, nil
	}

	// The content-type half of a polymorphic pair is redirected to the id half rather than
	// pinned, because it has no spelling and cannot get one -- and the sentinel is worse than
	// dropped: `'null'.lower().split('.')` raises, the filter becomes `scope_type__in=[]` and
	// the request matches *nothing*, so the engine creates a duplicate instead of adopting.
	// The id half asks the same question, since NetBox refuses one half without the other.
	if column.Class == "GenericFKType" {
		return b.pin(kind, strings.TrimSuffix(f.Column, "_type")+"_id", spec, nullColumnNumeric)
	}

	switch {
	case slices.Contains(refFieldTypes, column.Type):
		return b.pin(kind, f.Column+"_id", spec, nullColumnRef)
	case slices.Contains(stringTypes, column.Type):
		return b.pin(kind, f.Column, spec, nullColumnChar)
	case slices.Contains(intTypes, column.Type), column.Class == "Decimal":
		return b.pin(kind, f.Column, spec, nullColumnNumeric)
	}

	return nullField{}, false, fmt.Errorf(
		"%w: %s.%s.%s is %s, and there is no null-pin spelling for it; "+
			"registry.NullColumn has no zero value, so this cannot be defaulted",
		errUnpinnableColumn, kind.App, kind.Model, f.Column, column.Type)
}

// pin builds the pin once the class is known, after checking the parameter is registered at
// all. django-filter drops an unregistered parameter silently and returns the *unfiltered*
// result set, so the engine would adopt the wrong object (#206).
func (b *builder) pin(kind irKind, filter, spec, class string) (nullField, bool, error) {
	if !b.registers(kind, filter) {
		return nullField{}, false, nil
	}

	return nullField{Filter: filter, Spec: spec, Column: class}, true, nil
}

// registers reports whether the kind's filterset accepts the parameter.
func (b *builder) registers(kind irKind, param string) bool {
	if _, ok := kind.Filters[param]; ok {
		return true
	}

	base, suffix, found := strings.Cut(param, "__")
	if !found {
		return false
	}

	filter, ok := kind.Filters[base]
	if !ok {
		return false
	}

	_, accepted := filter.Lookups[suffix]

	return accepted
}

// column finds a kind's IR column by name.
func (b *builder) column(kind irKind, name string) *irField {
	for i := range kind.Fields {
		if kind.Fields[i].Name == name {
			return &kind.Fields[i]
		}
	}

	return nil
}
