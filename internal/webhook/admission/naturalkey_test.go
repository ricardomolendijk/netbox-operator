package admission

import (
	"encoding/json"
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// spec builds a specMap from JSON fragments, in the shape resolver.SpecMap produces.
func spec(fields map[string]string) specMap {
	out := specMap{}
	for name, raw := range fields {
		out[name] = json.RawMessage(raw)
	}

	return out
}

// TestDeclaredTreatsEveryEmptyAsUnset pins the definition the null pins hang off. A field
// present as `{}` is an OptionalRef saying "explicitly none", which is a column cleared rather
// than a value to match on, so it must read the same as an absent field.
func TestDeclaredTreatsEveryEmptyAsUnset(t *testing.T) {
	for raw, want := range map[string]bool{
		`"emea"`:          true,
		`{"name":"emea"}`: true,
		`0`:               true,
		`false`:           true,
		`""`:              false,
		`null`:            false,
		`{}`:              false,
		`[]`:              false,
	} {
		if _, got := spec(map[string]string{"field": raw}).declared("field"); got != want {
			t.Errorf("declared(%s) = %v; want %v", raw, got, want)
		}
	}

	if _, got := (specMap{}).declared("absent"); got {
		t.Error("an absent field read as declared")
	}
}

// nested is a two-candidate descriptor in the shape every MPTT tree has: `(parent, name)`
// first, then `(name)` with the parent pinned null. It is the shape the collision rule has to
// get right, because the two candidates share a field.
var nested = registry.Descriptor{
	NaturalKeys: []registry.NaturalKey{
		{Fields: []registry.KeyField{
			{Filter: "parent_id", Spec: "parentRef"},
			{Filter: "name", Spec: "name"},
		}},
		{
			Fields:     []registry.KeyField{{Filter: "name", Spec: "name"}},
			NullFields: []registry.NullField{{Filter: "parent_id", Spec: "parentRef", Column: registry.NullColumnRef}},
		},
	},
}

func TestKeyOfPicksTheFirstApplicableCandidate(t *testing.T) {
	for name, tc := range map[string]struct {
		spec       map[string]string
		identified bool
		candidate  int
		values     string
	}{
		"a child is identified by its parent and name": {
			spec:       map[string]string{"name": `"emea"`, "parentRef": `{"name":"world"}`},
			identified: true, candidate: 0,
			values: `parentRef={"name":"world"}, name="emea"`,
		},
		"a top-level object falls to the null-pinned candidate": {
			spec:       map[string]string{"name": `"emea"`},
			identified: true, candidate: 1,
			values: `name="emea", parentRef=null`,
		},
		"an explicitly empty parent is a top-level object": {
			spec:       map[string]string{"name": `"emea"`, "parentRef": `{}`},
			identified: true, candidate: 1,
			values: `name="emea", parentRef=null`,
		},
		"no name identifies nothing": {
			spec:       map[string]string{"parentRef": `{"name":"world"}`},
			identified: false,
		},
	} {
		t.Run(name, func(t *testing.T) {
			key, identified := keyOf(nested, spec(tc.spec))
			if identified != tc.identified {
				t.Fatalf("identified = %v; want %v", identified, tc.identified)
			}

			if !identified {
				return
			}

			if key.candidate != tc.candidate || key.values != tc.values {
				t.Errorf("key = {%d, %q}; want {%d, %q}",
					key.candidate, key.values, tc.candidate, tc.values)
			}
		})
	}
}

// TestChildAndTopLevelDoNotCollide is the false positive the candidate index exists to
// prevent, at the level the rule is decided rather than through an API server: two objects
// sharing `name` are not the same NetBox row when one is found by `(parent, name)` and the
// other by `(name) WHERE parent IS NULL`.
func TestChildAndTopLevelDoNotCollide(t *testing.T) {
	child, _ := keyOf(nested, spec(map[string]string{"name": `"emea"`, "parentRef": `{"name":"world"}`}))
	top, _ := keyOf(nested, spec(map[string]string{"name": `"emea"`}))

	if child == top {
		t.Errorf("a child and a top-level object share the key %v", child)
	}
}

// TestAllowsDuplicatesIsRegistryDriven: the field is only honoured on a Kind whose Descriptor
// names it, which is exactly the fact CEL cannot see.
func TestAllowsDuplicatesIsRegistryDriven(t *testing.T) {
	set := spec(map[string]string{"allowDuplicate": "true"})

	if allowsDuplicates(registry.Descriptor{}, set) {
		t.Error("allowDuplicate was honoured on a Kind whose Descriptor does not name it")
	}

	duplicating := registry.Descriptor{DuplicateSpec: "allowDuplicate"}

	if !allowsDuplicates(duplicating, set) {
		t.Error("allowDuplicate was ignored on the Kind that declares it")
	}

	if allowsDuplicates(duplicating, spec(map[string]string{"allowDuplicate": "false"})) {
		t.Error("allowDuplicate: false opted out of the natural key")
	}
}
