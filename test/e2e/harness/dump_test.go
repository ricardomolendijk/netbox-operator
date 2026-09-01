package harness

import (
	"strings"
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The canonical dump is what the whole ordering gate rests on: two runs are declared equal
// because their dumps are byte-identical, so a canonicaliser that let a volatile field
// through would fail every run, and one that stripped too much would pass a run that wrote
// the wrong thing. Both directions are cheap to test without a NetBox, and this is the only
// part of the harness that runs in `make test`.

func TestCanonicaliseStripsEveryVolatileField(t *testing.T) {
	// The same object as two runs would return it: different ids, different timestamps,
	// different cached columns, same state.
	first := netbox.Object{
		"id": float64(7), "url": "http://nb/api/dcim/regions/7/",
		"display": "EMEA", "display_url": "http://nb/dcim/regions/7/",
		"created": "2026-01-01T00:00:00Z", "last_updated": "2026-01-01T00:00:01Z",
		"_depth": float64(0),
		"name":   "EMEA", "slug": "e2e-emea", "description": "",
	}
	second := netbox.Object{
		"id": float64(41), "url": "http://nb/api/dcim/regions/41/",
		"display": "EMEA", "display_url": "http://nb/dcim/regions/41/",
		"created": "2026-06-30T09:00:00Z", "last_updated": "2026-06-30T09:00:02Z",
		"_depth": float64(0),
		"name":   "EMEA", "slug": "e2e-emea", "description": "",
	}

	firstLine, err := canonicalise("dcim/regions", first)
	if err != nil {
		t.Fatalf("canonicalise(first) = %v", err)
	}
	secondLine, err := canonicalise("dcim/regions", second)
	if err != nil {
		t.Fatalf("canonicalise(second) = %v", err)
	}

	if firstLine != secondLine {
		t.Errorf("two dumps of one state differ:\n%s\n%s", firstLine, secondLine)
	}
	for _, volatile := range []string{"\"id\"", "\"url\"", "\"created\"", "\"last_updated\"", "_depth"} {
		if strings.Contains(firstLine, volatile) {
			t.Errorf("canonical line still carries %s: %s", volatile, firstLine)
		}
	}
	if !strings.Contains(firstLine, `"slug":"e2e-emea"`) {
		t.Errorf("canonical line dropped the identifying slug: %s", firstLine)
	}
}

func TestCanonicaliseStripsTheIDHalfOfAGenericFK(t *testing.T) {
	// The failure this exists for: the forward and reverse dumps differed only in `scope_id`
	// and `object_id`, which is the sequence having moved on and nothing about state. The
	// `*_type` half and the reduced embedded object have to survive, because between them
	// they are what carries the identity.
	first := netbox.Object{
		"prefix":   "10.117.0.0/24",
		"scope":    map[string]any{"id": float64(2), "slug": "e2e-hall-1"},
		"scope_id": float64(2), "scope_type": "dcim.location",
	}
	second := netbox.Object{
		"prefix":   "10.117.0.0/24",
		"scope":    map[string]any{"id": float64(3), "slug": "e2e-hall-1"},
		"scope_id": float64(3), "scope_type": "dcim.location",
	}

	firstLine, err := canonicalise("ipam/prefixes", first)
	if err != nil {
		t.Fatalf("canonicalise(first) = %v", err)
	}
	secondLine, err := canonicalise("ipam/prefixes", second)
	if err != nil {
		t.Fatalf("canonicalise(second) = %v", err)
	}

	if firstLine != secondLine {
		t.Errorf("scope_id survived canonicalisation:\n%s\n%s", firstLine, secondLine)
	}
	if !strings.Contains(firstLine, `"scope_type":"dcim.location"`) {
		t.Errorf("the type half of the pair was stripped too: %s", firstLine)
	}
	if !strings.Contains(firstLine, `"scope":"e2e-hall-1"`) {
		t.Errorf("the embedded object was stripped: %s", firstLine)
	}
}

func TestCanonicaliseKeepsWhatDistinguishesTwoStates(t *testing.T) {
	scoped := netbox.Object{
		"id": float64(1), "prefix": "10.117.0.0/24",
		// An embedded foreign key, as NetBox renders one: everything in it varies between
		// runs except the name.
		"scope": map[string]any{"id": float64(3), "url": "http://nb/api/dcim/locations/3/", "name": "Hall 1"},
	}
	unscoped := netbox.Object{
		"id": float64(1), "prefix": "10.117.0.0/24", "scope": nil,
	}

	scopedLine, err := canonicalise("ipam/prefixes", scoped)
	if err != nil {
		t.Fatalf("canonicalise(scoped) = %v", err)
	}
	unscopedLine, err := canonicalise("ipam/prefixes", unscoped)
	if err != nil {
		t.Fatalf("canonicalise(unscoped) = %v", err)
	}

	if scopedLine == unscopedLine {
		t.Fatal("a prefix with a scope and one without canonicalise the same, so the dump " +
			"cannot tell a run that wrote the scope from one that did not")
	}
	if !strings.Contains(scopedLine, `"scope":"Hall 1"`) {
		t.Errorf("the embedded FK was not reduced to its name: %s", scopedLine)
	}
	if strings.Contains(scopedLine, "locations/3") {
		t.Errorf("the embedded FK's url survived: %s", scopedLine)
	}
}

func TestCanonicaliseSortsAToManyBecauseOrderIsNotData(t *testing.T) {
	// NetBox returns a many-to-many in its own order, which is not the order the operator
	// sent and is not guaranteed to be stable between two identical states.
	forward := netbox.Object{"name": "E2E NOC", "groups": []any{
		map[string]any{"id": float64(1), "slug": "a"},
		map[string]any{"id": float64(2), "slug": "b"},
	}}
	reversed := netbox.Object{"name": "E2E NOC", "groups": []any{
		map[string]any{"id": float64(9), "slug": "b"},
		map[string]any{"id": float64(8), "slug": "a"},
	}}

	forwardLine, err := canonicalise("tenancy/contacts", forward)
	if err != nil {
		t.Fatalf("canonicalise(forward) = %v", err)
	}
	reversedLine, err := canonicalise("tenancy/contacts", reversed)
	if err != nil {
		t.Fatalf("canonicalise(reversed) = %v", err)
	}

	if forwardLine != reversedLine {
		t.Errorf("a to-many compared as ordered:\n%s\n%s", forwardLine, reversedLine)
	}
}

func TestDiffNamesOnlyTheDifferingLines(t *testing.T) {
	want := Dump{Text: "a\nb\nc", Count: 3}
	got := Dump{Text: "a\nc\nd", Count: 3}

	diff := Diff(want, got)
	if !strings.Contains(diff, "- b") {
		t.Errorf("Diff did not report the missing line:\n%s", diff)
	}
	if !strings.Contains(diff, "+ d") {
		t.Errorf("Diff did not report the extra line:\n%s", diff)
	}
	if strings.Contains(diff, "a") && strings.Contains(diff, "c") {
		t.Errorf("Diff printed lines the two dumps agree on:\n%s", diff)
	}
	if same := Diff(want, want); !strings.Contains(same, "no line-level difference") {
		t.Errorf("Diff of a dump against itself = %q", same)
	}
}

func TestManagedEndpointsAreDeduplicatedAndSorted(t *testing.T) {
	// A claim kind shares its endpoint with the object kind it allocates from, so the raw
	// registry list has repeats -- and the dump would list those objects twice.
	endpoints := managedEndpoints()
	if len(endpoints) == 0 {
		t.Fatal("the registry offered no endpoints to dump")
	}

	seen := map[string]bool{}
	for i, endpoint := range endpoints {
		if seen[endpoint] {
			t.Errorf("endpoint %q appears more than once", endpoint)
		}
		seen[endpoint] = true
		if i > 0 && endpoints[i-1] > endpoint {
			t.Errorf("endpoints are not sorted: %q before %q", endpoints[i-1], endpoint)
		}
	}
}

func TestLogSinceReturnsOnlyTheNewLines(t *testing.T) {
	cases := []struct {
		name   string
		before string
		after  string
		want   string
	}{
		{"the ordinary case", "one\ntwo\n", "one\ntwo\nthree\n", "three\n"},
		{"nothing new", "one\n", "one\n", ""},
		// A restarted pod's log does not extend the old one, and asserting on nothing would
		// hide whatever the replacement logged.
		{"the log was replaced", "one\ntwo\n", "fresh\n", "fresh\n"},
		{"the first read", "", "one\n", "one\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := LogSince(tc.before, tc.after); got != tc.want {
				t.Errorf("LogSince() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestErrorLogLinesReadsTheLevelField(t *testing.T) {
	log := strings.Join([]string{
		`{"level":"info","msg":"reconciling","kind":"NetBoxRegion"}`,
		`{"level":"debug","msg":"nothing to do"}`,
		`{"level":"info","msg":"an object called error-handler is Ready"}`,
		`{"level":"error","msg":"manager exited","error":"boom"}`,
	}, "\n")

	lines := ErrorLogLines(log)
	if len(lines) != 1 {
		t.Fatalf("ErrorLogLines() returned %d lines, want 1: %v", len(lines), lines)
	}
	if !strings.Contains(lines[0], "manager exited") {
		t.Errorf("ErrorLogLines() matched the wrong line: %s", lines[0])
	}
}
