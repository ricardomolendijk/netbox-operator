package registry

import (
	"errors"
	"fmt"
	"slices"
)

// Natural-key validation failures. Each is a distinct sentinel so callers and tests
// classify by type rather than by matching a message.
var (
	// ErrNoKeyFields is returned for a candidate that pins filters to null without
	// matching any value.
	ErrNoKeyFields = errors.New("natural-key candidate has no value-bearing field")

	// ErrEmptyFilter is returned for a key field with no query parameter or no spec field
	// behind it.
	ErrEmptyFilter = errors.New("natural-key field is missing its filter or spec name")

	// ErrUnknownLookup is returned for a lookup modifier the client cannot emit.
	ErrUnknownLookup = errors.New("unknown lookup modifier")

	// ErrNullFieldConflict is returned when one filter is both pinned to null and matched
	// against a value.
	ErrNullFieldConflict = errors.New("filter is both null-pinned and value-matched")
)

// Lookup is a NetBox filter-expression modifier, appended to a query parameter after a
// double underscore as in `?name__ie=dns`.
type Lookup string

const (
	// LookupExact matches the value exactly: `?name=dns`. It is the zero value, so a
	// field that declares nothing gets the conservative behaviour.
	LookupExact Lookup = ""

	// LookupIExact matches case-insensitively: `?name__ie=dns`. dcim.Device is unique on
	// `Lower('name')`, as is virtualization.VirtualMachine (docs/netbox-schema.md ->
	// dcim.Device.meta.constraints, virtualization.VirtualMachine.meta.constraints), so an
	// exact lookup does not find `DNS` for a CR named `dns` and the engine goes on to
	// create a second object, which NetBox either rejects or accepts under a different
	// case. This modifier is the difference between adopting the existing device and
	// duplicating it.
	LookupIExact Lookup = "ie"
)

// knownLookups is what a natural key may use. Substring, prefix and negation lookups are
// deliberately absent: a natural key has to identify at most one object and those cannot.
var knownLookups = []Lookup{LookupExact, LookupIExact}

// KeyField is one filter of a natural-key candidate, matched against a value.
type KeyField struct {
	// Filter is the NetBox query parameter. For a foreign key that is the column plus
	// `_id` (`vrf_id`, `parent_id`), which is not the name the field is written under
	// (`vrf`, `parent`).
	Filter string

	// Spec is the CR spec field the value comes from (`vrfRef`, `name`). The engine reads
	// the value from there, and Applicable uses it to decide whether the candidate can be
	// used at all.
	Spec string

	// Lookup modifies how the value is matched.
	Lookup Lookup
}

// Param is the query parameter to send for this field, lookup modifier included. It
// exists so the `__ie` spelling is written down exactly once.
func (f KeyField) Param() string {
	if f.Lookup == LookupExact {
		return f.Filter
	}

	return f.Filter + "__" + string(f.Lookup)
}

// NullField pins a filter to null rather than omitting it.
//
// The distinction is the whole point of the type. NetBox's nested-group kinds are unique
// on `(parent, name)` plus a separate `name WHERE parent IS NULL`
// (docs/netbox-schema.md -> dcim.Region.meta.constraints), and ipam.Prefix has no
// constraint at all, so `(prefix)` with `vrf_id` merely omitted matches that prefix in
// every VRF. An omitted filter makes every top-level Region, and every per-VRF prefix,
// adopt an unrelated object.
type NullField struct {
	// Filter is the NetBox query parameter to pin, without the `__isnull` suffix.
	Filter string

	// Spec is the CR spec field this filter corresponds to. A candidate that asserts the
	// field is null is only usable while that spec field is empty, and the engine needs to
	// be told which field to look at; without it a child Region matches the top-level
	// candidate and the follow-up write reparents somebody else's data.
	Spec string
}

// Param is the query parameter that pins this filter to null. NetBox spells it as a
// Django `isnull` lookup and it is sent with the value `true`.
func (f NullField) Param() string {
	return f.Filter + "__isnull"
}

// NaturalKey is one lookup candidate: the filters that together identify at most one
// NetBox object. A candidate is data rather than a closure so that a generated kind ships
// no hand-written code, and so that a wrong key is visible in a diff.
type NaturalKey struct {
	// Fields are the filters matched against a value.
	Fields []KeyField

	// NullFields are the filters pinned to null.
	NullFields []NullField
}

// SpecState is what the engine knows about one object's spec fields when it goes looking
// for the live NetBox object.
type SpecState struct {
	// Declared are the spec fields the user set, whether or not they resolve yet.
	Declared []string

	// Resolved are the spec fields that currently hold a value the client can filter on.
	// A reference whose target does not exist yet is declared but not resolved.
	Resolved []string
}

// Applicable reports whether this candidate can be used for state.
//
// A candidate matches on a field only when that field resolves, and asserts a field is
// null only when the field was never declared. Both halves are load-bearing:
//
//   - Resolved, not declared, for the matched fields: an optional key the user left unset
//     makes the candidate inapplicable, which is how ipam.VRF falls from `(rd)` to
//     `(name)` and dcim.Device from `(name, site, tenant)` to the tenant-is-null variant.
//   - Declared, not resolved, for the null pins: a Region whose parent is declared but has
//     not been created yet must *not* fall through to `name WHERE parent IS NULL`. That
//     candidate would find an unrelated top-level Region and adopt it, and the follow-up
//     PATCH would reparent somebody else's data. With nothing applicable the engine waits,
//     which is the correct outcome (NBO-015).
func (k NaturalKey) Applicable(state SpecState) bool {
	for _, field := range k.Fields {
		if !slices.Contains(state.Resolved, field.Spec) {
			return false
		}
	}

	for _, field := range k.NullFields {
		if slices.Contains(state.Declared, field.Spec) {
			return false
		}
	}

	return true
}

// Validate reports every way this candidate is malformed.
func (k NaturalKey) Validate() error {
	return errors.Join(k.validateFields(), k.validateNullFields())
}

func (k NaturalKey) validateFields() error {
	if len(k.Fields) == 0 {
		return ErrNoKeyFields
	}

	errs := make([]error, 0, len(k.Fields))

	for _, field := range k.Fields {
		if field.Filter == "" || field.Spec == "" {
			errs = append(errs, fmt.Errorf("%w: %+v", ErrEmptyFilter, field))
		}

		if !slices.Contains(knownLookups, field.Lookup) {
			errs = append(errs, fmt.Errorf("%w: %q on %q", ErrUnknownLookup, field.Lookup, field.Filter))
		}
	}

	return errors.Join(errs...)
}

func (k NaturalKey) validateNullFields() error {
	errs := make([]error, 0, len(k.NullFields))

	for _, field := range k.NullFields {
		if field.Filter == "" || field.Spec == "" {
			errs = append(errs, fmt.Errorf("%w: %+v", ErrEmptyFilter, field))

			continue
		}

		if slices.ContainsFunc(k.Fields, func(f KeyField) bool { return f.Filter == field.Filter }) {
			errs = append(errs, fmt.Errorf("%w: %s", ErrNullFieldConflict, field.Filter))
		}
	}

	return errors.Join(errs...)
}
