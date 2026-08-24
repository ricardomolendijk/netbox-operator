package reconciler

import (
	"encoding/json"
	"reflect"
	"strings"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

// FieldManager is the name every write the operator makes is attributed to, and it is
// load-bearing rather than cosmetic.
//
// The engine decides which spec fields a user set by elimination: a `metadata.managedFields`
// entry that is not this manager's is somebody else's opinion about the spec. Left unset,
// controller-runtime lets the API server derive a manager name from the process's user agent
// -- `manager`, `controller.test`, whatever the binary happens to be called -- so "not the
// operator" would stop being decidable the first time the binary was renamed. One constant,
// wired in internal/controller and cmd/manager.
//
// It also makes the never-write-spec invariant checkable from outside the process: if this
// manager ever appears owning anything under `f:spec`, ADR-0005 §1 has been broken, and that
// is an assertion a test can make against a real API server rather than a promise
// (docs/decisions/0005-gitops-coexistence.md, internal/controller/gitops_test.go).
const FieldManager = "netbox-operator"

// specOwnership is which spec fields somebody other than the operator has claimed.
//
// It answers a question a Go struct cannot: `description: ""` and an absent `description`
// decode to the same string, so intent cannot be read off the value. The API server has
// tracked the difference per field, for every client that has ever written the object, since
// field management went GA -- so the engine reads that instead of guessing
// (docs/concepts/field-ownership.md).
type specOwnership struct {
	// fields are the immediate children of `spec` that some other manager owns, by their
	// JSON names -- the same spelling registry.Field.Spec uses.
	fields map[string]bool

	// tracked reports that at least one manager claims the spec at all.
	//
	// Not the same as len(fields) > 0: an entry can own `spec` itself and no field under it.
	// The distinction separates "nobody set anything" from "nobody is telling us", and only
	// the second is a degraded mode worth reporting.
	tracked bool
}

// managedFieldsSpec is the part of one managed-fields entry the engine reads.
//
// The encoding is a set rather than a list: a field name is prefixed with `f:`, a list entry
// key with `k:` or `v:`, and the containing map itself is named `.`. Only the immediate
// children of `spec` are wanted, so a nested `f:` deeper down is never reached.
type managedFieldsSpec struct {
	Spec map[string]json.RawMessage `json:"f:spec"`
}

// ownershipOf reads which spec fields somebody claimed off obj's managed-fields metadata.
//
// Every entry that is not the operator's own counts, whatever its operation. Server-side
// apply records exactly the fields the applier sent, which is the signal this exists for; an
// ordinary `Update` -- `kubectl edit`, or a client-side `kubectl apply` -- records the fields
// that request changed, which the API server has tracked since field management went GA in
// 1.18. Both are somebody stating an intent about a field, and a rule that admitted only
// `Apply` would silently manage nothing for every client that does not use it.
//
// Subresource entries are skipped: the operator's own status writes appear there, and
// nothing under `status` can name a spec field.
func ownershipOf(obj client.Object) specOwnership {
	owned := specOwnership{fields: map[string]bool{}}

	for _, entry := range obj.GetManagedFields() {
		if entry.Manager == FieldManager || entry.Subresource != "" || entry.FieldsV1 == nil {
			continue
		}

		var decoded managedFieldsSpec
		if err := json.Unmarshal(entry.FieldsV1.Raw, &decoded); err != nil || decoded.Spec == nil {
			// Unreadable ownership is no ownership, not a failed reconcile: the fallback is
			// exactly the behaviour the operator had before it read this at all, and
			// refusing to reconcile over metadata nobody asked for would be worse than the
			// bug it fixes.
			continue
		}
		owned.tracked = true

		for key := range decoded.Spec {
			if name, ok := strings.CutPrefix(key, "f:"); ok {
				owned.fields[name] = true
			}
		}
	}

	return owned
}

// restoreEmpty puts back the spec fields somebody claimed that `omitempty` dropped, at their
// empty value.
//
// This is the whole of NBO-079's mechanism. `omitempty` stays on the CR structs -- taking it
// off inverts the bug, because a typed Go client then marshals an unset string as `""` and
// claims it, so adopting a pre-existing NetBox object would wipe every value the user had
// not restated, the operator's own materialised children included (ADR-0005 §2). So the spec
// arrives with the empty fields missing, and what is missing yet owned is put back:
// `description: ""` becomes an empty string in the payload, which is what clears a NetBox
// description.
//
// The empty value comes from the CR's own Go type, reflected once per pass, so a kind added
// tomorrow is covered by having a spec struct. Nothing here knows a Kind: it is the same
// reflection the envelope's field list already uses (payload.go, envelopeFields).
//
// A field the reflection cannot give an empty form for is left out rather than guessed at. A
// pointer and a struct are the cases, and neither needs this: a nil pointer marshals to
// `null` and is already a state of its own, and a reference is a whole object rather than a
// value with an empty form.
func (s specFields) restoreEmpty(obj Object, owned specOwnership) {
	if len(owned.fields) == 0 {
		return
	}

	for name, empty := range emptyValues(obj) {
		if !owned.fields[name] {
			continue
		}

		if _, present := s[name]; !present {
			s[name] = empty
		}
	}
}

// emptyValues is the empty JSON value of every field of obj's spec that has one, by JSON
// name.
func emptyValues(obj Object) map[string]any {
	spec, ok := specStructType(obj)
	if !ok {
		return nil
	}

	empties := make(map[string]any, spec.NumField())
	collectEmptyValues(spec, empties)

	return empties
}

// collectEmptyValues fills empties from t, descending into an inline embedded struct.
//
// The descent is what makes the shared envelope's own fields visible, and it is the shape
// every kind uses: `NetBoxObjectSpec json:",inline"`. Without it a field on an inlined struct
// would silently have no empty form, which is a field that cannot be cleared -- the exact
// failure this file exists to remove.
func collectEmptyValues(t reflect.Type, empties map[string]any) {
	for i := range t.NumField() {
		field := t.Field(i)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")

		if name == "" && field.Anonymous && field.Type.Kind() == reflect.Struct {
			collectEmptyValues(field.Type, empties)

			continue
		}

		if empty, ok := emptyValueOf(field.Type); ok {
			empties[name] = empty
		}
	}
}

// specStructType returns the Go type behind obj's `spec` field, found by its JSON name
// rather than by position or by a per-kind accessor.
func specStructType(obj Object) (reflect.Type, bool) {
	t := reflect.TypeOf(obj)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	if t == nil || t.Kind() != reflect.Struct {
		return nil, false
	}

	for i := range t.NumField() {
		if name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ","); name == "spec" {
			return t.Field(i).Type, t.Field(i).Type.Kind() == reflect.Struct
		}
	}

	return nil, false
}

// scalarEmpties is the empty JSON value for each scalar Go kind a spec field can have. A
// number is float64 because that is what every JSON number decodes to, and the payload has
// to look the same whether the value came from the wire or from here.
var scalarEmpties = map[reflect.Kind]any{
	reflect.String:  "",
	reflect.Bool:    false,
	reflect.Int:     float64(0),
	reflect.Int8:    float64(0),
	reflect.Int16:   float64(0),
	reflect.Int32:   float64(0),
	reflect.Int64:   float64(0),
	reflect.Uint:    float64(0),
	reflect.Uint8:   float64(0),
	reflect.Uint16:  float64(0),
	reflect.Uint32:  float64(0),
	reflect.Uint64:  float64(0),
	reflect.Float32: float64(0),
	reflect.Float64: float64(0),
}

// emptyValueOf returns the empty JSON value of a Go type, and whether it has one.
//
// A fresh collection every call rather than a shared one from the table: the value goes
// straight into a payload the drift comparison and the create path both read, and a slice
// aliased across every object of a kind is the kind of sharing nobody looks for.
func emptyValueOf(t reflect.Type) (any, bool) {
	if t.Kind() == reflect.Slice {
		return []any{}, true
	}

	if t.Kind() == reflect.Map {
		return map[string]any{}, true
	}

	empty, ok := scalarEmpties[t.Kind()]

	return empty, ok
}
