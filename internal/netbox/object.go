package netbox

import "strconv"

// Object is a decoded NetBox API object.
//
// The client is deliberately untyped: one generic reconcile engine can only exist if
// every Kind's payload has the same Go type. Typed models would push per-Kind knowledge
// into the client, which is exactly what internal/registry holds instead.
type Object map[string]any

// dryRunMarker flags an Object that the client fabricated instead of sending. It is a
// double-underscore key so it can never collide with a NetBox field name.
const dryRunMarker = "__dryRun"

// Suppressed reports whether obj was produced by a DryRun client rather than by NetBox.
// Such an Object carries the payload that would have been sent and has no id, because
// nothing was created. Callers must not treat it as proof the object exists.
func Suppressed(obj Object) bool {
	suppressed, ok := obj[dryRunMarker].(bool)
	return ok && suppressed
}

// ID returns the object's NetBox id. The second result is false when the field is
// absent or not a number, which is the normal case for a suppressed DryRun object.
func (o Object) ID() (int, bool) {
	return asInt(o["id"])
}

// asList coerces a decoded JSON array into a slice of Objects, skipping any element
// that is not an object. NetBox never mixes types inside a results array, so a skipped
// element means the response was not what we asked for.
func asList(v any) []Object {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]Object, 0, len(raw))
	for _, item := range raw {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		out = append(out, Object(obj))
	}
	return out
}

// asString returns v as a string, or "" if it is absent or another type.
func asString(v any) string {
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return s
}

// asInt returns v as an int. JSON numbers decode to float64, and NetBox ids also arrive
// as strings in a few nested representations, so both are accepted.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case string:
		parsed, err := strconv.Atoi(n)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}
