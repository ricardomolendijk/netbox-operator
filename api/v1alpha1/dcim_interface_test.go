package v1alpha1

import (
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestInterfaceTypeEnumMatchesGolden pins the 207 values of `spec.type` to a checked-in list.
//
// The list is the one thing about this Kind that cannot be derived from anything in this
// repository: `docs/netbox-schema.md` records the *choice class* and not its members, because
// the AST walk behind the digest cannot evaluate a Django `ChoiceSet`. So the values are
// transcribed from `netbox/dcim/choices.py` (4.6.8, lines 889-1508), and a transcription is
// exactly the kind of thing that loses an entry quietly.
//
// The two sides are the golden file and the *generated CRD*, not the golden file and the Go
// marker: comparing against the marker would compare the transcription with itself, while
// comparing against the CRD covers the whole path a value travels -- marker, controller-gen,
// the schema the API server actually enforces. A value dropped anywhere along it fails here.
//
// It also fails loudly on a NetBox upgrade that adds a transceiver type, which is the point:
// NBO-042 regenerates the marker, this test says whether the golden file was regenerated with
// it, and NBO-043 inherits both.
func TestInterfaceTypeEnumMatchesGolden(t *testing.T) {
	want := goldenValues(t, filepath.Join("testdata", "interface-types.txt"))

	// A number in the test as well as in the file: a truncation that also truncated the
	// golden file would otherwise pass, and 207 is the count this Kind shipped with.
	if len(want) != 207 {
		t.Fatalf("testdata/interface-types.txt lists %d values, want 207 "+
			"(netbox/dcim/choices.py, InterfaceTypeChoices, NetBox 4.6.8)", len(want))
	}

	got := enumOf(t, "netbox.kubeforge.org_netboxinterfaces.yaml", "type")

	if !slices.Equal(got, want) {
		t.Errorf("spec.type accepts %d values and the golden file lists %d; "+
			"first difference at %s", len(got), len(want), firstDifference(got, want))
	}
}

// firstDifference names the earliest index at which two value lists disagree, because a
// diff of 207 strings is unreadable and the index is the whole of the useful information.
func firstDifference(got, want []string) string {
	for i := range max(len(got), len(want)) {
		switch {
		case i >= len(got):
			return "index " + strconv.Itoa(i) + ": the CRD ends, the golden file has " + want[i]
		case i >= len(want):
			return "index " + strconv.Itoa(i) + ": the golden file ends, the CRD has " + got[i]
		case got[i] != want[i]:
			return "index " + strconv.Itoa(i) + ": the CRD has " + got[i] + ", the golden file " + want[i]
		}
	}

	return "nowhere: the lists are equal"
}

// goldenValues reads one value per line, ignoring `#` comments and blank lines.
func goldenValues(t *testing.T, path string) []string {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	out := make([]string, 0, 256)

	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		out = append(out, line)
	}

	return out
}

// enumOf returns the `enum` of one top-level spec property of one generated CRD.
func enumOf(t *testing.T, file, property string) []string {
	t.Helper()

	path := filepath.Join("..", "..", "config", "crd", "bases", file)

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Enum []string `json:"enum"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &crd); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}

	if len(crd.Spec.Versions) == 0 {
		t.Fatalf("%s serves no version", path)
	}

	schema, ok := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties[property]
	if !ok {
		t.Fatalf("%s has no spec.%s", file, property)
	}

	if len(schema.Enum) == 0 {
		t.Fatalf("%s: spec.%s carries no enum, so this test would pass by checking nothing",
			file, property)
	}

	return schema.Enum
}

// TestWirelessChannelEnumIsWholeAndQuoted is the cheap half of the same guard for
// `spec.rfChannel`.
//
// No golden file: 197 channel names carry no information a reader can check, and the failure
// mode that matters is not a wrong value but a *truncated list*. Which is what happened the
// first time this Kind was generated -- every unquoted value beginning with a digit made
// controller-gen parse `2.4g-1-2412-22` as the number `2.4` and discard the rest of the
// marker, and it said so in a way that was easy to mistake for one bad entry. A count and the
// two ends of the list catch that in one line each.
func TestWirelessChannelEnumIsWholeAndQuoted(t *testing.T) {
	got := enumOf(t, "netbox.kubeforge.org_netboxinterfaces.yaml", "rfChannel")

	if len(got) != 197 {
		t.Errorf("spec.rfChannel accepts %d values, want 197 "+
			"(netbox/wireless/choices.py lines 34-236, WirelessChannelChoices, NetBox 4.6.8)",
			len(got))
	}

	// The first and last values in declaration order. A parser that swallowed the head or
	// truncated the tail loses one of these.
	if got[0] != "2.4g-1-2412-22" {
		t.Errorf("spec.rfChannel[0] = %q, want 2.4g-1-2412-22", got[0])
	}

	if got[len(got)-1] != "60g-27-65880-6480" {
		t.Errorf("spec.rfChannel[last] = %q, want 60g-27-65880-6480", got[len(got)-1])
	}
}
