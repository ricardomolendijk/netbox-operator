package reconciler

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// specFields is one object's spec as the engine reads it: JSON names to JSON values.
//
// The engine reads a spec through its JSON form rather than through reflection over the
// Go struct or a per-kind accessor. The JSON name is the spelling the user writes in YAML
// and the spelling registry.KeyField.Spec names, so this is the one representation where
// both ends of the field map agree, and it costs a generated kind no code at all.
type specFields map[string]any

// envelopeFields are the spec fields the engine owns rather than sends. Derived from the
// struct instead of listed, so that adding one -- NBO-007's deletionPolicy is next --
// cannot leak into a NetBox payload as an unknown field.
var envelopeFields = jsonNames(reflect.TypeFor[netboxv1alpha1.NetBoxObjectSpec]())

func jsonNames(t reflect.Type) map[string]bool {
	names := make(map[string]bool, t.NumField())

	for i := range t.NumField() {
		tag, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if tag != "" && tag != "-" {
			names[tag] = true
		}
	}

	return names
}

// specOf returns obj's spec in the form it would be sent to the API server.
func specOf(obj Object) (specFields, error) {
	encoded, err := json.Marshal(obj)
	if err != nil {
		return nil, fmt.Errorf("encoding %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	var decoded struct {
		Spec specFields `json:"spec"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		return nil, fmt.Errorf("decoding the spec of %s/%s: %w", obj.GetNamespace(), obj.GetName(), err)
	}

	return decoded.Spec, nil
}

// desired renders the spec into the payload to send, and reports what it could not render.
//
// What arrives here has already been through specFields.restoreEmpty, so a field the user
// explicitly emptied is present and empty rather than absent (NBO-079,
// docs/concepts/field-ownership.md). Everything below then treats it like any other value,
// which is why nothing here had to learn about field ownership.
//
// Read-only columns need no filtering here: a spec field may not map onto one, enforced by
// registry.Descriptor.Validate at boot. So the only thing left out is a reference, and that
// is returned rather than dropped.
//
// Deferred fields are deliberately *not* filtered here either, and that is the load-bearing
// half of NBO-015. What this returns is the desired state, which is what every later pass
// diffs the live object against; only the create payload has a deferral stripped from it, by
// deferral.createPayload. Filtering here instead would leave the field never compared and so
// never written -- and filtering here while leaving it in the diff is a PATCH that can never
// satisfy its own diff, which is the hot loop docs/concepts/drift.md opens by warning about.
func (s specFields) desired(d registry.Descriptor) (netbox.Object, registry.SpecState, []string, error) {
	desired := make(netbox.Object, len(s))
	state := registry.SpecState{}
	refs := make([]string, 0, len(s))

	// Sorted, because the order decides which unmapped field is named in the error and how
	// the reported references read; both are compared in tests and read by humans.
	for _, name := range slices.Sorted(maps.Keys(s)) {
		value := s[name]
		if value == nil || envelopeFields[name] {
			continue
		}
		state.Declared = append(state.Declared, name)

		field, mapped := d.FieldFor(name)

		switch {
		case mapped && field.Class.Ref(), !mapped && isGenericFK(d, name):
			refs = append(refs, name)
		case mapped:
			desired[field.API] = value

			if _, filterable := filterValue(value); filterable {
				state.Resolved = append(state.Resolved, name)
			}
		default:
			// NetBox ignores a field it does not know rather than rejecting it, so a spec
			// field with no mapping would be silently dropped and the object would report
			// itself synced while missing a value. Refuse instead.
			return nil, registry.SpecState{}, nil, fmt.Errorf("%w: %s", errUnmappedField, name)
		}
	}

	return desired, state, refs, nil
}

// params renders one natural-key candidate as a query string.
//
// A filter with no usable value cannot be omitted: an omitted filter matches more objects
// rather than fewer, so the lookup would adopt something else entirely. Descriptor.
// Candidates already excludes such a candidate; this is the guard that keeps the two
// definitions of "usable" from drifting apart silently.
func (s specFields) params(k registry.NaturalKey) (netbox.Params, error) {
	params := make(netbox.Params, len(k.Fields)+len(k.NullFields))

	for _, field := range k.Fields {
		value, ok := filterValue(s[field.Spec])
		if !ok {
			return nil, fmt.Errorf("%w: %s has no value to filter on", errUnfilterable, field.Spec)
		}

		params.Match(field.Filter, netbox.Lookup(field.Lookup), value)
	}

	for _, field := range k.NullFields {
		params.Null(field.Filter)
	}

	return params, nil
}

// filterValue renders a spec value as a query-string value. Only scalars qualify: a list
// or an object has no single value a filter could match, and a reference has not become an
// id yet.
func filterValue(value any) (string, bool) {
	switch typed := value.(type) {
	case string:
		return typed, typed != ""
	case bool:
		return strconv.FormatBool(typed), true
	case float64:
		// Every JSON number decodes to float64, so a VLAN id arrives as 4094.0 and has to
		// be rendered without the fraction NetBox would reject.
		return strconv.FormatFloat(typed, 'f', -1, 64), true
	default:
		return "", false
	}
}

func isGenericFK(d registry.Descriptor, spec string) bool {
	_, ok := d.GenericFKFor(spec)

	return ok
}

// fieldRules translates the descriptor's field classes into the comparison rules Drift
// needs. It is the whole of the engine's knowledge about how a field is compared, and it
// is data every time.
func fieldRules(d registry.Descriptor) netbox.FieldRules {
	m2m := set(d.M2MFields())

	// `tags` is not in any descriptor's M2M list because no spec field maps onto it: it is
	// the engine's own column, written by the provenance stamp (NBO-075). It still needs the
	// M2M rule, and getting that wrong is the loud kind of wrong -- NetBox returns tags as
	// nested objects and takes them as bare ids, so compared as scalars they never match and
	// the operator PATCHes the same list forever.
	if d.Taggable {
		if m2m == nil {
			m2m = map[string]bool{}
		}
		m2m[provenance.TagsField] = true
	}

	rules := netbox.FieldRules{
		M2M:             m2m,
		ObjectTypeLists: set(d.ObjectTypeListFields()),
		Arrays:          set(d.ArrayFields()),
		GenericFKs:      make([]netbox.GenericFK, 0, len(d.GenericFKs)),
	}

	for _, generic := range d.GenericFKs {
		rules.GenericFKs = append(rules.GenericFKs,
			netbox.GenericFK{TypeField: generic.TypeField, IDField: generic.IDField})
	}

	return rules
}

func set(fields []string) map[string]bool {
	if len(fields) == 0 {
		return nil
	}

	out := make(map[string]bool, len(fields))
	for _, field := range fields {
		out[field] = true
	}

	return out
}
