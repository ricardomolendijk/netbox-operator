package netbox

import (
	"testing"
)

// TestJSONFieldsAreComparedWhole is why FieldRules.JSON exists.
//
// The scalar rule reduces any JSON object carrying an `id` or a `value` key to that key,
// because that is how NetBox renders a foreign key and a choice on read (unwrapNested). A
// JSONField's value is neither: it is a document that may perfectly well *contain* those keys.
// `parameters: {"id": ["3"]}` is an ordinary extras.SavedFilter, and compared as a scalar it
// would read as `[3]` against the whole document, differ forever, and be PATCHed on every
// reconcile -- the hot loop docs/concepts/drift.md opens by warning about.
//
// The `id` cases are the ones that would have shipped broken. The rest are here because a rule
// that only fires on the pathological shape is a rule nobody would notice had regressed.
func TestJSONFieldsAreComparedWhole(t *testing.T) {
	rules := FieldRules{JSON: map[string]bool{"parameters": true}}

	tests := []struct {
		name       string
		live       Object
		desired    Object
		wantDrift  bool
		wantScalar bool // what the scalar rule would have concluded, when it differs
	}{
		{
			name:      "a document with an id key is equal to itself",
			live:      Object{"parameters": map[string]any{"id": []any{"3"}}},
			desired:   Object{"parameters": map[string]any{"id": []any{"3"}}},
			wantDrift: false,
			// Without the JSON rule this is drift: unwrapNested turns the live side into
			// `["3"]` and compares it against the whole map.
			wantScalar: true,
		},
		{
			name:       "a document with a value key is equal to itself",
			live:       Object{"parameters": map[string]any{"value": "active"}},
			desired:    Object{"parameters": map[string]any{"value": "active"}},
			wantDrift:  false,
			wantScalar: true,
		},
		{
			name:      "a changed document is drift",
			live:      Object{"parameters": map[string]any{"status": []any{"active"}}},
			desired:   Object{"parameters": map[string]any{"status": []any{"reserved"}}},
			wantDrift: true,
		},
		{
			name:      "key order is not data",
			live:      Object{"parameters": map[string]any{"a": "1", "b": "2"}},
			desired:   Object{"parameters": map[string]any{"b": "2", "a": "1"}},
			wantDrift: false,
		},
		{
			name: "a number written as an int compares equal to the float json decoding gives",
			// The CRD side can hand over either, depending on which decoder saw it; NetBox's
			// response body always decodes to float64. canonicalise widens both.
			live:      Object{"parameters": map[string]any{"limit": float64(50)}},
			desired:   Object{"parameters": map[string]any{"limit": 50}},
			wantDrift: false,
		},
		{
			name:      "an emptied document is drift, which is how a JSONField gets cleared",
			live:      Object{"parameters": map[string]any{"status": []any{"active"}}},
			desired:   Object{"parameters": map[string]any{}},
			wantDrift: true,
		},
		{
			name: "a scalar document is compared as one, not unwrapped",
			// extras.CustomField.default is any JSON value at all, a bare string included.
			live:      Object{"parameters": "bronze"},
			desired:   Object{"parameters": "bronze"},
			wantDrift: false,
		},
		{
			name:      "null on both sides is equal",
			live:      Object{"parameters": nil},
			desired:   Object{"parameters": nil},
			wantDrift: false,
		},
		{
			name:      "clearing a document to null is drift",
			live:      Object{"parameters": map[string]any{"status": []any{"active"}}},
			desired:   Object{"parameters": nil},
			wantDrift: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := len(Drift(tc.live, tc.desired, rules)) > 0; got != tc.wantDrift {
				t.Errorf("Drift() reports drift = %v, want %v", got, tc.wantDrift)
			}

			if !tc.wantScalar {
				return
			}

			// The same comparison with the rule taken away, to prove the rule is what is
			// deciding rather than the values happening to agree.
			if got := len(Drift(tc.live, tc.desired, FieldRules{})) > 0; !got {
				t.Errorf("without the JSON rule this case is expected to look like drift; " +
					"if it no longer does, the rule may no longer be needed")
			}
		})
	}
}
