package netbox

import (
	"reflect"
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
// Every mutating method reports suppression this way: a suppressed create or patch carries
// the payload that would have been sent and no id, because nothing was created; a
// suppressed delete carries the endpoint and id it would have removed. Callers must not
// treat either as proof of what NetBox now holds.
func Suppressed(obj Object) bool {
	suppressed, ok := obj[dryRunMarker].(bool)
	return ok && suppressed
}

// ID returns the object's NetBox id. The second result is false when the field is
// absent or not a number, which is the normal case for a suppressed create or patch.
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

// IntOf returns v as an int, false when it is not a number.
//
// Exported for the allocation engine, which reads an integer out of a payload it built from a
// CR spec: the value arrives as whatever encoding/json produced -- a float64 -- and a caller
// re-deriving that coercion outside this package would be a second answer to what a NetBox
// number is.
func IntOf(v any) (int, bool) { return asInt(v) }

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
	// Fast paths for the two shapes that actually come out of encoding/json.
	switch n := v.(type) {
	case float64:
		return n, true
	case string:
		parsed, err := strconv.ParseFloat(n, 64)
		if err != nil {
			return 0, false
		}
		return parsed, true
	}

	// Every other numeric width, by kind rather than by type. Generated CRD types use
	// int32 and int64 freely, and enumerating types one at a time both reads worse and
	// leaves a width out -- which then falls through to a string comparison that happens
	// to work until it does not.
	value := reflect.ValueOf(v)
	switch value.Kind() {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return float64(value.Int()), true
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return float64(value.Uint()), true
	case reflect.Float32, reflect.Float64:
		return value.Float(), true
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

// Unwrap reduces NetBox's read representation of a related or choice field to the value
// that gets written: a foreign key's nested object to its id, a choice column's
// {"value","label"} to its value.
//
// Exported for internal/export, which has to invert a payload rather than compare one --
// and "what does this read shape mean" has to have exactly one answer, or the exporter and
// Drift disagree about a value and the round trip never settles. drift.go's unwrapNested
// is this function.
func Unwrap(v any) any { return unwrapNested(v) }

// IDOf reads a single NetBox id out of either shape a foreign key arrives in: the bare id
// it is written as, or the nested object it is read back as. The second result is false
// when the column is null, which for a nullable FK is the normal case.
func IDOf(v any) (int, bool) { return asInt(Unwrap(v)) }

// IDsOf reads a list of NetBox ids out of either shape the API uses: the bare ids an M2M
// field is written as, or the nested objects it is read back as.
//
// Exported because internal/provenance has to union the operator's tag into whichever of
// the two shapes it was handed -- the desired payload's ids, or a live object's nested
// tags -- and re-deriving that coercion outside this package would be a second, divergent
// answer to what a NetBox id list is.
func IDsOf(v any) []int {
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

// ObjectTypesOf reads a list of `app_label.model` strings, accepting the nested-object
// shape NetBox uses for object-type lists on some endpoints.
//
// Exported because internal/provenance has to read an existing extras.CustomField's
// object_types before it can widen it, and it may get either shape back depending on the
// endpoint — which is exactly the normalisation this function is.
func ObjectTypesOf(v any) []string {
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

// ChoiceOf reads a NetBox choice column's value.
//
// NetBox serialises a choice as a nested `{"value": ..., "label": ...}` object on read and
// accepts the bare value on write (docs/netbox-schema.md, choice columns), so a caller
// comparing what NetBox holds against a value a Descriptor declares has to reach inside. A
// bare string is accepted too, because the brief serialisers flatten it -- and because a
// helper that only works on one of the two shapes is one every caller has to remember which.
func ChoiceOf(v any) string {
	if nested, ok := v.(map[string]any); ok {
		return asString(nested["value"])
	}

	return asString(v)
}

// CustomFieldOf reads one custom field off an object, as a string.
//
// Empty for a field the object does not carry, for one NetBox returned as null, and for one
// holding a non-string -- all three mean the same thing to every caller there is: this
// object does not carry the value we are looking for.
func CustomFieldOf(obj Object, name string) string {
	fields, ok := obj[customFieldsKey].(map[string]any)
	if !ok || name == "" {
		return ""
	}

	return asString(fields[name])
}

// SetCustomField writes one custom field into a payload, creating the container if needed
// and leaving every other key alone.
//
// A map[string]any rather than a map[string]string, and that is not cosmetic: Changes
// compares `custom_fields` by casting the desired value to map[string]any, and a
// map[string]string falls through to a whole-value comparison that never matches -- a PATCH
// loop for the lifetime of the object. See provenance.mergeCustomFields, which is the same
// trap in the other direction.
func SetCustomField(obj Object, name, value string) {
	if name == "" {
		return
	}

	fields, ok := obj[customFieldsKey].(map[string]any)
	if !ok {
		fields = map[string]any{}
	}

	fields[name] = value
	obj[customFieldsKey] = fields
}
