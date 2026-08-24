package netbox

import (
	"slices"
	"strings"
)

// Lookup is a NetBox filter-expression modifier, appended to a query parameter after a
// double underscore as in `?name__ie=dns`.
//
// The constants are spelled the same as registry.Lookup, where a kind declares which
// modifier its natural key needs. They are repeated rather than shared because neither
// package imports the other -- registry holds per-kind facts, this one builds HTTP
// requests -- and because the strings are NetBox's wire format, not a choice either
// package gets to make.
type Lookup string

const (
	// LookupExact matches the value exactly: `?name=dns`. It is the zero value, so a
	// caller that asks for no modifier gets the conservative behaviour.
	LookupExact Lookup = ""

	// LookupIExact matches case-insensitively: `?name__ie=dns`. dcim.Device is unique on
	// `Lower('name')`, as is virtualization.VirtualMachine (docs/netbox-schema.md ->
	// dcim.Device.meta.constraints, virtualization.VirtualMachine.meta.constraints), so an
	// exact lookup for `dns` does not find the device stored as `DNS`. The engine then
	// creates a second device, which NetBox either rejects or accepts under a different
	// case. This modifier is the difference between adopting the existing object and
	// duplicating it.
	LookupIExact Lookup = "ie"
)

// NullColumn says which of NetBox's filter classes backs a column that a natural key
// pins to null. It has to be declared, because NetBox spells "this column holds nothing"
// differently per class and only one spelling is registered for any given column -- and
// django-filter answers an unregistered parameter as if it were absent, so getting it
// wrong widens the lookup silently (#206).
//
// The constants are spelled the same as registry.NullColumn, for the same reason the
// Lookup constants are: neither package imports the other, and the wire format is
// NetBox's to decide.
type NullColumn string

const (
	// NullColumnRef is a foreign key, filtered by a model-choice filter -- every `_id`
	// column the operator pins. It takes the null sentinel, `?parent_id=null`.
	//
	// Not a `__` suffix, because a foreign key has none to use: NetBox's own reference says
	// foreign keys "support just the negation expression: `n`" (netbox/docs/reference/
	// filtering.md -> "Foreign Keys & Other Fields"), which BaseFilterSet enforces by
	// handing ModelChoiceFilter and ModelMultipleChoiceFilter the negation-only lookup map
	// (netbox/netbox/filtersets.py:164-170 -> FILTER_NEGATION_LOOKUP_MAP). The sentinel is
	// FILTERS_NULL_CHOICE_VALUE (netbox/netbox/settings.py:771); django-filter's
	// MultipleChoiceFilter.filter turns it into a NULL predicate
	// (`if v == self.null_value: v = None`, django_filters/filters.py:262-264), and
	// NetBox's TreeNodeMultipleChoiceFilter overrides get_filter_predicate to render that
	// None as `__isnull=True` rather than `__in=None` (netbox/utilities/filters.py:135-138)
	// -- which is what a tenant group or a parent location is filtered by.
	NullColumnRef NullColumn = "ref"

	// NullColumnChar is a char column, filtered by MultiValueCharFilter. It takes the same
	// sentinel: `?rd=null`.
	//
	// A char column does register the suffix -- FILTER_CHAR_BASED_LOOKUP_MAP has
	// `empty='empty'` (netbox/utilities/constants.py:15) -- but it asks a wider question.
	// The `empty` ORM lookup is a string-length test, `CAST(LENGTH(col) AS BOOLEAN) IS NOT
	// TRUE` (netbox/extras/lookups.py:69-73), which is true for NULL *and* for the empty
	// string. `ipam.VRF.rd` is `CharField UNIQUE blank=True null=True` with no
	// normalisation on save (netbox/ipam/models/vrfs.py:24-31), so an empty string is a
	// reachable value distinct from NULL and `?rd__empty=true` would match it. The sentinel means
	// NULL and only NULL, so it is what a null pin sends.
	NullColumnChar NullColumn = "char"

	// NullColumnNumeric is a plain numeric column -- an integer that is not a foreign key,
	// which is what the id half of a generic FK is. It is the one case that takes the
	// suffix: `?scope_id__empty=true`.
	//
	// The sentinel is not available: a numeric filter's form field casts each value with
	// the real field class (netbox/utilities/filters.py:39-46), so `null` fails
	// IntegerField validation and the request is rejected outright rather than widened. The
	// suffix comes from FILTER_NUMERIC_BASED_LOOKUP_MAP's `empty='isnull'`
	// (netbox/utilities/constants.py:26): `empty` is the parameter suffix and `isnull` is
	// the ORM lookup it maps to, which is the confusion behind #206 -- `?scope_id__isnull=`
	// is registered nowhere.
	NullColumnNumeric NullColumn = "numeric"
)

const (
	// nullSentinel is the value that means NULL to a model-choice or char filter:
	// FILTERS_NULL_CHOICE_VALUE (netbox/netbox/settings.py:771). It is a value rather than
	// a lookup, which is why Params.Null writes the bare filter name for those columns.
	nullSentinel = "null"

	// emptyLookup is the suffix a numeric column's null filter is registered under.
	emptyLookup = "empty"

	// emptyValue is what the BooleanFilter behind that suffix accepts
	// (netbox/netbox/filtersets.py:264-266 -> django_filters.BooleanFilter, whose field is
	// forms.NullBooleanField). NullBooleanField.to_python takes `true`, `True` and `1` and
	// turns anything else into None (django/forms/fields.py:838-852) while validate() is a
	// no-op (:854), so a wrong value here is an empty value and leaves the filter
	// unapplied -- indistinguishable from omitting it.
	emptyValue = "true"
)

// netboxLookups is every filter-expression suffix NetBox 4.6.8 registers: the union of the
// four lookup maps in netbox/utilities/constants.py -- char (:5), numeric (:20), negation
// (:29) and tag (:33). Which map a column gets depends on its filter class
// (netbox/netbox/filtersets.py:142-181 -> BaseFilterSet._get_filter_lookup_dict), so a
// suffix listed here is not registered on *every* column; this is the coarse check.
var netboxLookups = []string{
	// char
	"ic", "nic", "iew", "niew", "isw", "nisw", "ie", "nie", "empty", "regex", "iregex",
	// numeric
	"lte", "lt", "gte", "gt",
	// negation, shared by every map
	"n",
	// tag
	"any",
}

// LookupRegistered reports whether NetBox registers the filter expression on param.
//
// It is exported for the fake NetBox servers in this repo's tests, which have no other way
// to tell a filter NetBox would honour from one it would drop. That is not a detail those
// fakes get to invent: django-filter silently ignores a parameter it does not recognise
// and NetBox 4.6.8 has no strict-filter validation, so a misspelled lookup returns the
// *unfiltered* set. A fake that answers whatever it is asked is why every test asserted
// the query the operator built and none of them noticed that `__isnull` was not a
// parameter (#206).
//
// Coarse on purpose: it checks the suffix, not whether the column exists or carries that
// filter class. A per-column check needs the filter registry NetBox builds at import time,
// which this operator does not have.
func LookupRegistered(param string) bool {
	_, lookup, found := strings.Cut(param, "__")
	if !found {
		return true
	}

	return slices.Contains(netboxLookups, lookup)
}

// Params is one lookup's query string: NetBox parameter name to value.
//
// It stays a `map[string]string` rather than becoming a struct of filters and modifiers,
// because in NetBox's filter language a lookup modifier is part of the parameter *name*:
// `?name__ie=dns` is one key and one value, structurally identical to `?name=dns`. So a
// modifier needs no second axis in the type and no builder to express one. Defined over
// `map[string]string` so every existing call site still compiles, with Match and Null as
// the only place the `__` spellings and the null spellings are written down -- otherwise
// each call site concatenates them and each one can get it wrong.
type Params map[string]string

// Match requires filter to equal value under lookup, and returns p so a lookup can be
// built in one expression.
func (p Params) Match(filter string, lookup Lookup, value string) Params {
	p[param(filter, lookup)] = value

	return p
}

// Null pins filter to null rather than leaving it out, in whichever spelling column's
// filter class registers, and returns p.
//
// The distinction is load-bearing. ipam.IPAddress has no `meta.constraints`
// (docs/netbox-schema.md -> ipam.IPAddress), so the same address exists once globally and
// once in every VRF; a lookup that merely omits `vrf_id` matches all of them, and the
// global address adopts whichever copy comes back first.
//
// Numeric is the guarded case rather than the fall-through so that a column whose class
// was never declared fails in the loud direction. A `NullColumn` this does not know emits
// the sentinel, which a numeric filter rejects with a 400; the other way round it would
// emit a suffix a model-choice filter drops without a word, and the lookup would quietly
// match every row again. registry.NaturalKey.Validate rejects the unknown value at boot,
// so neither should be reachable.
func (p Params) Null(filter string, column NullColumn) Params {
	if column == NullColumnNumeric {
		p[filter+"__"+emptyLookup] = emptyValue

		return p
	}

	p[filter] = nullSentinel

	return p
}

// param renders one query parameter name with its modifier attached.
func param(filter string, lookup Lookup) string {
	if lookup == LookupExact {
		return filter
	}

	return filter + "__" + string(lookup)
}
