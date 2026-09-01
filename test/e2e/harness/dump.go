package harness

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// volatileFields are stripped from every dumped object.
//
// Each is legitimately different between two runs that produced the same state: `id` and
// `url` because the sequence moved on, the timestamps because time passed, `display` because
// it is derived, and anything beginning with `_` because those are NetBox's denormalised
// cached columns -- `_site` maintained from (scope_type, scope_id) and its siblings. Leaving
// any of them in would make the dumps differ on every run and the equality assertion
// worthless.
var volatileFields = []string{"id", "url", "display", "display_url", "created", "last_updated"}

// idSuffix marks the *other* place a raw primary key appears: the id half of a generic-FK
// pair, rendered as a sibling column rather than inside the embedded object -- `scope_id`
// next to `scope`, `object_id` next to `object`, `assigned_object_id` next to
// `assigned_object`.
//
// Stripped for the same reason `id` is, and it took a failing run to notice: the forward and
// reverse dumps differed only in `scope_id` and `object_id`, which is the sequence having
// moved on and nothing about state. Safe as a blanket rule because NetBox renders an ordinary
// foreign key as a nested object (`vrf`, `tenant`) and never as `vrf_id`, so the only keys
// this matches are the generic-FK halves -- and the `*_type` half and the reduced embedded
// object both survive, which is what carries the identity.
const idSuffix = "_id"

// Dump is a canonicalised snapshot of what a NetBox holds, comparable byte for byte between
// runs.
//
// The point of the whole gate: apply order may change the sequence of writes and may not
// change the result, and only a canonical form can say so. An end-state check per object
// would pass a run that also created something it should not have.
type Dump struct {
	// Text is the comparable form: one object per line, sorted, volatile fields stripped.
	Text string

	// Count is how many objects the dump covers, for a legible failure message.
	Count int
}

// DumpNetBox reads every object of every registered kind and canonicalises it.
//
// Every registered kind and not only the graph's, deliberately: an ordering bug that created
// a spurious object of some *other* kind would be invisible to a dump of the kinds the
// fixtures name.
func DumpNetBox(ctx context.Context, client *netbox.Client) (Dump, error) {
	lines, err := dumpLines(ctx, client)
	if err != nil {
		return Dump{}, err
	}
	sort.Strings(lines)
	return Dump{Text: strings.Join(lines, "\n"), Count: len(lines)}, nil
}

func dumpLines(ctx context.Context, client *netbox.Client) ([]string, error) {
	var lines []string
	for _, endpoint := range managedEndpoints() {
		objects, err := client.List(ctx, endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", endpoint, err)
		}
		for _, object := range objects {
			line, err := canonicalise(endpoint, object)
			if err != nil {
				return nil, err
			}
			lines = append(lines, line)
		}
	}
	return lines, nil
}

// managedEndpoints is the set of REST paths the operator can write, read off the registry
// and deduplicated -- a claim kind shares its endpoint with the object kind it allocates
// from, so the raw list has repeats.
func managedEndpoints() []string {
	descriptors := registry.List()
	seen := make(map[string]bool, len(descriptors))
	endpoints := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		if seen[descriptor.Endpoint] {
			continue
		}
		seen[descriptor.Endpoint] = true
		endpoints = append(endpoints, descriptor.Endpoint)
	}
	sort.Strings(endpoints)
	return endpoints
}

// canonicalise renders one object as one line: its endpoint, then its fields as sorted JSON
// with the volatile ones removed.
//
// Nested objects are reduced to what identifies them rather than kept whole. A NetBox
// response embeds each foreign key as a brief object carrying the target's `id` and `url`,
// which are exactly the values that legitimately differ between runs -- so an embedded
// object is replaced by its `name`, `slug` or `display`, whichever it has.
func canonicalise(endpoint string, object netbox.Object) (string, error) {
	reduced := make(map[string]any, len(object))
	for key, value := range object {
		if strings.HasPrefix(key, "_") || isVolatile(key) {
			continue
		}
		reduced[key] = reduceValue(value)
	}

	// encoding/json writes a map's keys in sorted order, which is what makes two dumps of
	// the same state byte-identical rather than merely equal as sets.
	body, err := json.Marshal(reduced)
	if err != nil {
		return "", fmt.Errorf("canonicalising an object from %s: %w", endpoint, err)
	}
	return endpoint + " " + string(body), nil
}

func isVolatile(key string) bool {
	if strings.HasSuffix(key, idSuffix) {
		return true
	}
	for _, name := range volatileFields {
		if key == name {
			return true
		}
	}
	return false
}

func reduceValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return reduceNested(typed)
	case []any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			items = append(items, reduceValue(item))
		}
		// A to-many is a set in NetBox and its order is not data, so it is sorted by its
		// rendered form rather than compared in the order the API happened to return.
		sortAny(items)
		return items
	default:
		return value
	}
}

// reduceNested keeps the identifying field of an embedded object and drops the rest. A
// choice field -- {"value","label"} -- keeps its value, which is what the operator sends.
func reduceNested(nested map[string]any) any {
	for _, key := range []string{"slug", "name", "value", "display", "address", "prefix"} {
		if value, ok := nested[key]; ok {
			return value
		}
	}
	// A custom-fields map, or any other plain object: keep it, minus the volatile keys.
	reduced := make(map[string]any, len(nested))
	for key, value := range nested {
		if strings.HasPrefix(key, "_") || isVolatile(key) {
			continue
		}
		reduced[key] = reduceValue(value)
	}
	return reduced
}

func sortAny(items []any) {
	sort.Slice(items, func(i, j int) bool {
		return fmt.Sprintf("%v", items[i]) < fmt.Sprintf("%v", items[j])
	})
}

// Diff returns a readable difference between two dumps: the lines only one of them has.
// Only the differing lines, because a NetBox holding a couple of dozen objects still makes a
// full side-by-side unreadable in a CI log.
func Diff(want, got Dump) string {
	wantLines := map[string]bool{}
	for _, line := range strings.Split(want.Text, "\n") {
		wantLines[line] = true
	}
	gotLines := map[string]bool{}
	for _, line := range strings.Split(got.Text, "\n") {
		gotLines[line] = true
	}

	var out []string
	for line := range wantLines {
		if !gotLines[line] {
			out = append(out, "- "+line)
		}
	}
	for line := range gotLines {
		if !wantLines[line] {
			out = append(out, "+ "+line)
		}
	}
	sort.Strings(out)
	if len(out) == 0 {
		return "(no line-level difference)"
	}
	return strings.Join(out, "\n")
}

// NetBoxEmpty reports the objects still in NetBox, so a teardown assertion can name them.
func NetBoxEmpty(ctx context.Context, client *netbox.Client) (bool, string, error) {
	dump, err := DumpNetBox(ctx, client)
	if err != nil {
		return false, "", err
	}
	if dump.Count == 0 {
		return true, "", nil
	}
	return false, fmt.Sprintf("%d objects remain:\n%s", dump.Count, dump.Text), nil
}
