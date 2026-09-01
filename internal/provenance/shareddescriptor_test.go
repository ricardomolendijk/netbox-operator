package provenance

import (
	"slices"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// TestBootstrapPayloadSpeaksTheDescriptorsVocabulary holds the two writers of
// extras.CustomField to one set of column names.
//
// NBO-059's acceptance criterion asks for byte-identical payloads from the bootstrap and from
// the CRD. That criterion does not survive the answer this ticket gave the collision: a CR may
// not name one of the bootstrap's definitions at all (Config.Reserved), so the two writers
// never render the same object and there are no two byte strings to compare. What *is* worth
// holding -- and what the criterion was really about -- is that they describe the same NetBox
// model in the same words.
//
// The failure it catches is the quiet kind. NetBox ignores a column it does not know rather
// than rejecting it, so if the descriptor spelled `group_name` and the bootstrap spelled
// `groupName`, the bootstrap would create its definitions with no group, report success, and
// nothing anywhere would say so. Every key the bootstrap sends therefore has to be a column
// the NetBoxCustomField descriptor declares -- which is the file a human reviews against
// docs/netbox-schema.md.
func TestBootstrapPayloadSpeaksTheDescriptorsVocabulary(t *testing.T) {
	gvk := netboxv1alpha1.GroupVersion.WithKind("NetBoxCustomField")

	d, ok := registry.Get(gvk)
	if !ok {
		t.Fatalf("no descriptor is registered for %s: the bootstrap and the CRD have nothing to agree about", gvk)
	}

	declared := make([]string, 0, len(d.Fields))
	for _, field := range d.Fields {
		declared = append(declared, field.API)
	}

	payload := CustomFieldPayload(DefaultUIDField, []string{"dcim.site", "ipam.prefix"})
	if len(payload) == 0 {
		t.Fatal("CustomFieldPayload returned nothing")
	}

	for column := range payload {
		if !slices.Contains(declared, column) {
			t.Errorf("the bootstrap writes %q, which the NetBoxCustomField descriptor does not declare; "+
				"netbox ignores an unknown column rather than rejecting it, so one of the two spellings "+
				"is writing nothing and reporting success. Declared: %v", column, declared)
		}
	}
}

// TestBootstrapPayloadIsNotWritableThroughACR is the other side of that: the definitions the
// bootstrap owns are refused to a CR, so there is exactly one writer of each.
//
// Asserted against Config rather than against the engine because that is where the list lives:
// the engine's guard is a lookup into this map keyed on the descriptor's object type
// (reconciler.pass.reserved), and this is the test that the map has the right keys in it. A
// reservation under the wrong `app_label.model` string would silently reserve nothing.
func TestBootstrapPayloadIsNotWritableThroughACR(t *testing.T) {
	cfg := FromSpec(&netboxv1alpha1.ManagedBy{ClusterID: "prod-eu"})

	reserved := cfg.Reserved()

	for _, name := range cfg.CustomFieldNames() {
		if !slices.Contains(reserved[customFieldObjectType], name) {
			t.Errorf("the bootstrap creates the custom field %q and %s reserves %v: "+
				"a CR could claim it and become the second writer",
				name, customFieldObjectType, reserved[customFieldObjectType])
		}
	}

	if !slices.Contains(reserved[tagObjectType], cfg.Tag) {
		t.Errorf("the bootstrap creates the tag %q and %s reserves %v",
			cfg.Tag, tagObjectType, reserved[tagObjectType])
	}

	// The object types have to be the ones the descriptors actually claim, or the engine's
	// lookup misses and the reservation is inert.
	for objectType := range reserved {
		if _, ok := registry.ByObjectType(objectType); !ok {
			t.Errorf("%q is reserved and no descriptor claims it, so nothing will ever be refused "+
				"under it", objectType)
		}
	}

	// And an endpoint that stamps nothing must reserve nothing: there is no second writer, so
	// refusing a CR would take a legitimate Kind away for no reason.
	if off := (Config{}).Reserved(); len(off) != 0 {
		t.Errorf("a disabled config reserves %v, want nothing", off)
	}
}
