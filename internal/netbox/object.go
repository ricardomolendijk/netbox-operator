package netbox

import (
	"sort"
	"strconv"
)

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

// toFloat coerces a JSON scalar to a float. NetBox returns decimal columns as strings
// (u_height "1.00", vcpus "2.00"), so a string that parses as a number is a number.
func toFloat(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	default:
		return 0, false
	}
}

// nestedIDs pulls the ids out of a list of nested objects, which is how NetBox returns
// an M2M field on read.
func nestedIDs(v any) []int {
	list := asList(v)
	ids := make([]int, 0, len(list))
	for _, item := range list {
		if id, ok := asInt(item["id"]); ok {
			ids = append(ids, id)
		}
	}
	return ids
}

// idsOf reads a list of bare ids, which is how an M2M field is written.
func idsOf(v any) []int {
	switch list := v.(type) {
	case []int:
		return list
	case []any:
		ids := make([]int, 0, len(list))
		for _, item := range list {
			// A desired M2M list may still contain nested objects when it came from a
			// live object rather than from a spec, so accept both shapes.
			if obj, ok := item.(map[string]any); ok {
				if id, ok := asInt(obj["id"]); ok {
					ids = append(ids, id)
				}
				continue
			}
			if id, ok := asInt(item); ok {
				ids = append(ids, id)
			}
		}
		return ids
	default:
		return nil
	}
}

// stringsOf reads a list of strings, accepting the nested-object shape NetBox uses for
// object-type lists on some endpoints.
func stringsOf(v any) []string {
	list, ok := v.([]any)
	if !ok {
		if typed, ok := v.([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(list))
	for _, item := range list {
		if s, ok := item.(string); ok {
			out = append(out, s)
			continue
		}
		if obj, ok := item.(map[string]any); ok {
			if s := asString(obj["app_label"]); s != "" {
				out = append(out, s+"."+asString(obj["model"]))
			}
		}
	}
	return out
}

func sortedInts(in []int) []int {
	out := append([]int(nil), in...)
	sort.Ints(out)
	return out
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
