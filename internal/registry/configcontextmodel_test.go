package registry

import (
	"slices"
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// configContextModelKinds are the two Kinds whose model mixes in NetBox's ConfigContextModel,
// and therefore the only two `local_context_data` columns in the catalogue
// (docs/netbox-schema.md -> dcim.Device and virtualization.VirtualMachine, both recording the
// column as `local_context_data (ConfigContextModel) JSONField`).
//
// Written out rather than derived from the IR, because the point of this list is to be
// contradicted: a third ConfigContextModel Kind arriving without the column would leave these
// tests passing while a whole column went unmapped, and the coverage audit is what catches
// that. What these tests hold is the shape of the two that exist.
var configContextModelKinds = []string{"NetBoxDevice", "NetBoxVirtualMachine"}

// TestLocalContextDataIsMappedAsAWholeDocument is #241's descriptor half, asserted from
// outside the descriptor for the reason TestExtrasJSONColumnsAreClassJSON is: nothing about
// getting this wrong is visible in a diff of the field map.
//
// Left at ClassValue -- the zero value, so the mistake is an omission rather than a typo --
// the column is compared with the scalar rule, which unwraps any JSON object carrying an `id`
// or a `value` key because that is how NetBox renders a foreign key and a choice on read. An
// `id` key inside a local context is ordinary inventory data, so the document would be
// compared against its own unwrapped self, never settle, and be PATCHed on every reconcile.
func TestLocalContextDataIsMappedAsAWholeDocument(t *testing.T) {
	for _, kind := range configContextModelKinds {
		t.Run(kind, func(t *testing.T) {
			d := descriptorFor(t, kind)

			field, ok := d.FieldFor("localContextData")
			if !ok {
				t.Fatalf("no `localContextData` in %s's field map; NetBox drops a column it "+
					"does not know rather than rejecting it, so the spec field would report "+
					"success having written nothing", kind)
			}

			if field.API != "local_context_data" {
				t.Errorf("localContextData writes %q, want \"local_context_data\"", field.API)
			}

			if !slices.Contains(d.JSONFields(), "local_context_data") {
				t.Errorf("local_context_data is a JSONField and is not ClassJSON; "+
					"JSONFields() = %v", d.JSONFields())
			}
		})
	}
}

// TestLocalContextDataDoesNotDriftAgainstADocumentCarryingAnID is the PATCH loop that ClassJSON
// exists to prevent, demonstrated rather than described.
//
// The document below carries an `id` key of its own -- an inventory identifier that has
// nothing to do with a NetBox foreign key, and the scalar rule has no way to tell the two
// apart: it unwraps any object with that key to the key's value, because that is how NetBox
// renders a foreign key on read. Live and desired are the same value here, so any drift at all
// is the operator preparing to PATCH a column it has just read back unchanged.
//
// The second half is the same comparison with the rules emptied. It is what makes the first
// half mean something: without it, a test that passes on an unchanged document would pass just
// as happily if every rule were dropped.
func TestLocalContextDataDoesNotDriftAgainstADocumentCarryingAnID(t *testing.T) {
	document := map[string]any{
		"id": "spine-01",
		"ntp": map[string]any{
			"servers": []any{"10.0.0.1", "10.0.0.2"},
		},
	}

	live := netbox.Object{"local_context_data": document}
	desired := netbox.Object{"local_context_data": document}

	for _, kind := range configContextModelKinds {
		t.Run(kind, func(t *testing.T) {
			d := descriptorFor(t, kind)

			rules := netbox.FieldRules{JSON: map[string]bool{}}
			for _, column := range d.JSONFields() {
				rules.JSON[column] = true
			}

			if drift := netbox.Drift(live, desired, rules); len(drift) != 0 {
				t.Errorf("an unchanged local context drifts: %v -- this is an infinite loop", drift)
			}

			if drift := netbox.Drift(live, desired, netbox.FieldRules{}); len(drift) == 0 {
				t.Error("the scalar rule agrees with the JSON rule on this document, so this " +
					"test would pass with ClassJSON removed; pick a document the unwrapping " +
					"actually mangles")
			}
		})
	}
}

// TestLocalContextDataIsAnOpaqueObjectInTheCRD is the schema half: the descriptor can only be
// right about a spec field the CRD actually serves.
//
// Two properties, and both are load-bearing. `type: object` is NetBox's own rule --
// ConfigContextModel.clean() refuses a `local_context_data` that is not a mapping -- moved
// forward to admission, where the rejection names the field instead of arriving as a 400 from
// a write the user cannot see. `x-kubernetes-preserve-unknown-fields` is what makes the field
// a pipe: without it the API server prunes every key the schema does not declare, which here
// is all of them, and the operator would faithfully write `{}` while reporting success.
func TestLocalContextDataIsAnOpaqueObjectInTheCRD(t *testing.T) {
	schemas := crdSchemas(t)

	for _, kind := range configContextModelKinds {
		t.Run(kind, func(t *testing.T) {
			properties, ok := schemas[kind]
			if !ok {
				t.Fatalf("no generated CRD for %s", kind)
			}

			property, ok := properties["localContextData"].(map[string]any)
			if !ok {
				t.Fatalf("%s's CRD has no `localContextData` spec property; run `make manifests`", kind)
			}

			if property["type"] != "object" {
				t.Errorf("localContextData is typed %v, want object: NetBox refuses a "+
					"local_context_data that is not a mapping", property["type"])
			}

			if preserve, _ := property["x-kubernetes-preserve-unknown-fields"].(bool); !preserve {
				t.Errorf("localContextData does not preserve unknown fields (%v), so the API "+
					"server would prune the whole document before the operator read it",
					property)
			}
		})
	}
}
