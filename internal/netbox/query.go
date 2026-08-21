package netbox

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

// isNullLookup pins a filter to null: `?vrf_id__isnull=true`. It is not one of the
// Lookup constants because it carries no value from the spec -- the value is always the
// literal `true`, which is why Params.Null takes no value argument.
const isNullLookup = "isnull"

// isNullValue is what NetBox expects for an `__isnull` filter. Sending `1`, `True` or an
// empty string leaves the filter silently unapplied, which reads as "this address is not
// in any VRF" while actually matching every VRF.
const isNullValue = "true"

// Params is one lookup's query string: NetBox parameter name to value.
//
// It stays a `map[string]string` rather than becoming a struct of filters and modifiers,
// because in NetBox's filter language a lookup modifier is part of the parameter *name*:
// `?name__ie=dns` is one key and one value, structurally identical to `?name=dns`. So a
// modifier needs no second axis in the type and no builder to express one. Defined over
// `map[string]string` so every existing call site still compiles, with Match and Null as
// the only place the `__` spellings and the `isnull` value are written down -- otherwise
// each call site concatenates them and each one can get it wrong.
type Params map[string]string

// Match requires filter to equal value under lookup, and returns p so a lookup can be
// built in one expression.
func (p Params) Match(filter string, lookup Lookup, value string) Params {
	p[param(filter, lookup)] = value

	return p
}

// Null pins filter to null rather than leaving it out, and returns p.
//
// The distinction is load-bearing. ipam.IPAddress has no `meta.constraints`
// (docs/netbox-schema.md -> ipam.IPAddress), so the same address exists once globally and
// once in every VRF; a lookup that merely omits `vrf_id` matches all of them, and the
// global address adopts whichever copy comes back first.
func (p Params) Null(filter string) Params {
	p[param(filter, isNullLookup)] = isNullValue

	return p
}

// param renders one query parameter name with its modifier attached.
func param(filter string, lookup Lookup) string {
	if lookup == LookupExact {
		return filter
	}

	return filter + "__" + string(lookup)
}
