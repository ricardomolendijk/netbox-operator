package v1alpha1

import (
	"compress/gzip"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// TestCableEnumsMatchTheSchema pins all four of dcim.Cable's choice columns to NetBox's own
// values.
//
// Not a golden file, unlike TestInterfaceTypeEnumMatchesGolden, and the difference is the
// source available: `hack/testdata/api-schema-4.6.8.json.gz` is **machine-extracted** from
// `netbox/dcim/choices.py` by hack/extract-netbox-api-schema.py, so comparing against it
// compares the marker with NetBox rather than with a transcription of NetBox. A golden file
// would only prove the transcription had not changed; this proves it was right.
//
// 33 cable types, 26 profiles, 3 link statuses and 6 length units, in NetBox's declaration
// order -- which the marker preserves, because the order is what a `kubectl explain` reader
// sees and a reordering would be an unreviewable diff.
func TestCableEnumsMatchTheSchema(t *testing.T) {
	choices := netboxChoices(t)

	for _, tc := range []struct {
		property string
		class    string
		count    int
		blank    bool
	}{
		// `type` is `blank=True, null=True`, so `""` is a member: it is how an unknown type is
		// spelled and the only way to clear one (dcim_cable.go, CableType).
		{property: "type", class: "CableTypeChoices", count: 33, blank: true},
		// `profile` is `blank=True` and not nullable, so there is no null to clear it with and
		// the empty member is deliberately absent.
		{property: "profile", class: "CableProfileChoices", count: 26},
		// `status` carries a default and is neither blank nor null.
		{property: "status", class: "LinkStatusChoices", count: 3},
		// `length_unit`'s ChoiceField is declared `allow_null=True`, so the cleared state has a
		// null to travel as.
		{property: "lengthUnit", class: "CableLengthUnitChoices", count: 6, blank: true},
	} {
		t.Run(tc.property, func(t *testing.T) {
			want := choices[tc.class]
			if len(want) != tc.count {
				t.Fatalf("%s lists %d values in the extracted schema, want %d "+
					"(NetBox 4.6.8); re-read the marker before trusting this test",
					tc.class, len(want), tc.count)
			}

			got := enumOf(t, "netbox.kubeforge.org_netboxcables.yaml", tc.property)

			blank := len(got) > 0 && got[0] == ""
			if blank != tc.blank {
				t.Errorf("spec.%s %s the empty value; want it %s",
					tc.property, present(blank), presentWanted(tc.blank))
			}

			if blank {
				got = got[1:]
			}

			if !slices.Equal(got, want) {
				t.Errorf("spec.%s accepts %v, want %v", tc.property, got, want)
			}
		})
	}
}

// present and presentWanted keep the message above readable in both directions; the empty
// member is a decision per column rather than a property of choice fields.
func present(blank bool) string {
	if blank {
		return "accepts"
	}

	return "rejects"
}

func presentWanted(blank bool) string {
	if blank {
		return "accepted"
	}

	return "rejected"
}

// netboxChoices reads the choice values out of the machine-extracted API schema, keyed by
// ChoiceSet class name.
//
// The same file hack/build-netbox-ir.py reads, and the reason this test needs no transcription:
// it is produced by walking `netbox/*/choices.py` rather than by anybody typing the values out
// (docs/regenerating.md).
func netboxChoices(t *testing.T) map[string][]string {
	t.Helper()

	path := filepath.Join("..", "..", "hack", "testdata", "api-schema-4.6.8.json.gz")

	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening %s: %v", path, err)
	}
	defer func() { _ = file.Close() }()

	reader, err := gzip.NewReader(file)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	defer func() { _ = reader.Close() }()

	// `value` is `any` and not `string`: a few of NetBox's ChoiceSets are numeric
	// (`CableProfileChoices`' sibling position sets among them), and a typed field would fail
	// the decode of the whole file over a class this test does not read.
	var schema struct {
		Choices map[string]struct {
			Values []struct {
				Value any `json:"value"`
			} `json:"values"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(reader).Decode(&schema); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}

	out := make(map[string][]string, len(schema.Choices))

	for class, set := range schema.Choices {
		values := make([]string, 0, len(set.Values))

		for _, value := range set.Values {
			text, ok := value.Value.(string)
			if !ok {
				continue
			}

			values = append(values, text)
		}

		out[class] = values
	}

	return out
}
