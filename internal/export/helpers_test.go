package export_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// namePattern is the pattern api/v1alpha1/objectref.go puts on ObjectRef.Name, which is
// what an exported metadata.name has to satisfy for a reference to it to be admissible.
var namePattern = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`)

func legalName(in string) bool {
	return in != "" && len(in) <= 253 && namePattern.MatchString(in)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()

	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshalling %v: %v", value, err)
	}

	return string(encoded)
}

// equalRef compares a reference through its JSON form, so a float64 from YAML and an int
// from a literal do not read as different references.
func equalRef(got, want any) bool {
	gotJSON, err := json.Marshal(got)
	if err != nil {
		return false
	}
	wantJSON, err := json.Marshal(want)
	if err != nil {
		return false
	}

	return string(gotJSON) == string(wantJSON)
}

func hasNote(notes []string, substring string) bool {
	for _, note := range notes {
		if strings.Contains(note, substring) {
			return true
		}
	}

	return false
}

func atoi(in string) int {
	value, err := strconv.Atoi(in)
	if err != nil {
		return 0
	}

	return value
}

// cidr is a distinct /32 per index, so 2500 synthetic prefixes are 2500 distinct objects.
func cidr(i int) string {
	return fmt.Sprintf("10.%d.%d.%d/32", i/65536, (i/256)%256, i%256)
}
