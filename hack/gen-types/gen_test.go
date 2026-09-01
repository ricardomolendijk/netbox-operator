package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// proofKinds are the three kinds the tests emit: one nested-group kind with a self-referential
// FK and a null pin on it, one with a polymorphic union and two tri-state booleans, and one
// with two many-to-many relations and a char null pin.
var proofKinds = []string{"dcim.Region", "ipam.Prefix", "ipam.VRF"}

// testOptions is a run against the committed IR, writing nowhere.
func testOptions(t *testing.T, kinds string) options {
	t.Helper()

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolving the repository root: %v", err)
	}

	return options{
		ir:        filepath.Join(root, "hack", "testdata", "ir-4.6.8.json.gz"),
		overrides: filepath.Join(root, "hack", "gen-types", "overrides.yaml"),
		out:       t.TempDir(),
		kinds:     kinds,
		stamped:   "",
	}
}

// TestEveryOutputStartsWithTheMarker is what the header guard depends on. A generated file
// without the marker is one the next run refuses to overwrite, so the whole pipeline stops.
func TestEveryOutputStartsWithTheMarker(t *testing.T) {
	outputs, err := plan(testOptions(t, strings.Join(proofKinds, ",")))
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}

	if len(outputs) != 3*len(proofKinds)+2 {
		t.Fatalf("emitted %d files, want three per kind plus the two shared files", len(outputs))
	}

	for _, out := range outputs {
		if !bytes.HasPrefix(out.Body, []byte(doNotEdit)) {
			t.Errorf("%s does not begin with %q", out.Path, doNotEdit)
		}
	}
}

// TestEmitIsDeterministic is the property `--check` rests on: nothing in a header or a body
// may depend on map order, the clock or the host.
func TestEmitIsDeterministic(t *testing.T) {
	opts := testOptions(t, strings.Join(proofKinds, ","))

	first, err := plan(opts)
	if err != nil {
		t.Fatalf("first plan() = %v", err)
	}

	second, err := plan(opts)
	if err != nil {
		t.Fatalf("second plan() = %v", err)
	}

	if len(first) != len(second) {
		t.Fatalf("two runs emitted %d and %d files", len(first), len(second))
	}

	for i := range first {
		if first[i].Path != second[i].Path {
			t.Fatalf("output %d is %s then %s", i, first[i].Path, second[i].Path)
		}

		if !bytes.Equal(first[i].Body, second[i].Body) {
			t.Errorf("%s differs between two runs of the same input", first[i].Path)
		}
	}
}

// TestHeaderGuardAbortsTheWholeRun plants a hand-written file at one output path and asserts
// that no output is written, not merely that one is skipped: a partial regeneration leaves the
// tree in a state no diff explains.
func TestHeaderGuardAbortsTheWholeRun(t *testing.T) {
	opts := testOptions(t, strings.Join(proofKinds, ","))

	outputs, err := plan(opts)
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}

	planted := outputs[len(outputs)-1].Path
	if err := os.MkdirAll(filepath.Dir(planted), 0o755); err != nil {
		t.Fatalf("creating %s: %v", filepath.Dir(planted), err)
	}

	if err := os.WriteFile(planted, []byte("package v1alpha1 // written by a human\n"), 0o600); err != nil {
		t.Fatalf("planting %s: %v", planted, err)
	}

	if err := write(outputs); !errors.Is(err, errHandWritten) {
		t.Fatalf("write() = %v, want errHandWritten", err)
	}

	for _, out := range outputs {
		if out.Path == planted {
			continue
		}

		if _, err := os.Stat(out.Path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s was written even though the run aborted", out.Path)
		}
	}
}

// TestCheckReportsAHandEditedFile covers both directions of `--check`: silent on a tree that
// matches, and naming the file on one that does not.
func TestCheckReportsAHandEditedFile(t *testing.T) {
	opts := testOptions(t, strings.Join(proofKinds, ","))

	outputs, err := plan(opts)
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}

	if err := write(outputs); err != nil {
		t.Fatalf("write() = %v", err)
	}

	if stale := check(outputs); len(stale) != 0 {
		t.Errorf("check() on a freshly written tree = %v, want nothing", stale)
	}

	edited := outputs[0].Path
	if err := os.WriteFile(edited, append(outputs[0].Body, []byte("\n// edited\n")...), 0o600); err != nil {
		t.Fatalf("editing %s: %v", edited, err)
	}

	if stale := check(outputs); !slices.Contains(stale, edited) {
		t.Errorf("check() = %v, want it to name %s", stale, edited)
	}
}

// TestTemplatesNameNoKind is the rule the whole design rests on. A template that knows which
// kind it is emitting is a switch on kind with extra indirection, and it defeats the reason the
// generator exists -- that adding a kind is data entry.
func TestTemplatesNameNoKind(t *testing.T) {
	loaded, _, err := loadIR(testOptions(t, "").ir)
	if err != nil {
		t.Fatalf("loadIR() = %v", err)
	}

	entries, err := templateFS.ReadDir("templates")
	if err != nil {
		t.Fatalf("reading the templates: %v", err)
	}

	if len(entries) == 0 {
		t.Fatal("no templates found; this test just passed by checking nothing")
	}

	for _, entry := range entries {
		body, err := templateFS.ReadFile("templates/" + entry.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", entry.Name(), err)
		}

		for name, kind := range loaded.Kinds {
			// The model name on its own, since `Prefix` also has to be absent, not only
			// `ipam.Prefix`. Word-bounded, or `Cable` matches `CableLengthUnit` in a
			// comment that is about no kind at all.
			for _, needle := range []string{name, kind.ObjectType, kubeKind(kind.Model)} {
				if bytes.Contains(body, []byte(needle)) {
					t.Errorf("templates/%s names the kind %q; the fact belongs in the IR "+
						"or in overrides.yaml, never in a template", entry.Name(), needle)
				}
			}
		}
	}
}

// TestNullPinClassFollowsTheColumnType is the decision registry.NullColumn cannot default:
// NetBox spells a null pin differently per filter class, and getting it wrong does not fail --
// django-filter drops the parameter and returns the unfiltered result set (#206).
func TestNullPinClassFollowsTheColumnType(t *testing.T) {
	opts := testOptions(t, "")

	loaded, _, err := loadIR(opts.ir)
	if err != nil {
		t.Fatalf("loadIR() = %v", err)
	}

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		t.Fatalf("loadOverrides() = %v", err)
	}

	build := newBuilder(loaded, over, header{})

	for _, tc := range []struct {
		kind, column, wantFilter, wantClass string
	}{
		// A foreign key: NetBox registers only negation on an FK filter, so neither
		// `__isnull` nor `__empty` exists and the pin is the sentinel value (#216).
		{"dcim.Region", "parent", "parent_id", nullColumnRef},
		{"dcim.Device", "tenant", "tenant_id", nullColumnRef},
		// A char column takes the sentinel under its own name.
		{"ipam.VRF", "rd", "rd", nullColumnChar},
	} {
		t.Run(tc.kind+"."+tc.column, func(t *testing.T) {
			kind := loaded.Kinds[tc.kind]
			specOf := map[string]string{tc.column: "spec"}

			pin, ok, err := build.nullPin(kind, irNullField{Column: tc.column}, specOf)
			if err != nil || !ok {
				t.Fatalf("nullPin() = %+v, %v, %v", pin, ok, err)
			}

			if pin.Filter != tc.wantFilter || pin.Column != tc.wantClass {
				t.Errorf("nullPin() = %s/%s, want %s/%s",
					pin.Filter, pin.Column, tc.wantFilter, tc.wantClass)
			}
		})
	}
}

// TestNullPinRedirectsAContentTypeColumn covers the one column that has no spelling and cannot
// get one. `scope_type`'s filter is MultiValueContentTypeFilter, which registers neither
// spelling, and the sentinel is worse than dropped: it makes the filter `scope_type__in=[]`,
// which matches nothing, so the engine would create a duplicate instead of adopting. The pin
// moves to the paired id half, which asks the same question.
func TestNullPinRedirectsAContentTypeColumn(t *testing.T) {
	opts := testOptions(t, "")

	loaded, _, err := loadIR(opts.ir)
	if err != nil {
		t.Fatalf("loadIR() = %v", err)
	}

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		t.Fatalf("loadOverrides() = %v", err)
	}

	build := newBuilder(loaded, over, header{})
	kind := loaded.Kinds["ipam.Prefix"]

	pin, ok, err := build.nullPin(kind, irNullField{Column: "scope_type"}, map[string]string{"scope_type": "scope"})
	if err != nil || !ok {
		t.Fatalf("nullPin(scope_type) = %+v, %v, %v", pin, ok, err)
	}

	if pin.Filter != "scope_id" || pin.Column != nullColumnNumeric {
		t.Errorf("nullPin(scope_type) = %s/%s, want scope_id/%s", pin.Filter, pin.Column, nullColumnNumeric)
	}
}

// TestEnumConstantsAreUniquePerType guards the transform, not the data. Dropping the decimal
// point makes `dcim.InterfaceTypeChoices` spell both `25gbase-t` and `2.5gbase-t` as one
// constant, which is a compile error a hundred kinds downstream of the cause.
func TestEnumConstantsAreUniquePerType(t *testing.T) {
	opts := testOptions(t, "")

	loaded, _, err := loadIR(opts.ir)
	if err != nil {
		t.Fatalf("loadIR() = %v", err)
	}

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		t.Fatalf("loadOverrides() = %v", err)
	}

	build := newBuilder(loaded, over, header{})
	checked := 0

	for class, set := range loaded.Enums {
		view := build.enumView(class, set)
		seen := map[string]string{}

		for _, value := range view.Values {
			if other, clash := seen[value.Const]; clash {
				t.Errorf("%s spells both %q and %q as %s", class, other, value.Value, value.Const)
			}

			seen[value.Const] = value.Value
			checked++
		}
	}

	if checked == 0 {
		t.Error("no choice values examined; this test just passed by checking nothing")
	}

	if err := errors.Join(build.collisions...); err != nil {
		t.Errorf("enumView reported collisions: %v", err)
	}
}

// TestExtendableEnumsAreNotPinned is the extendability decision, asserted in both directions. A
// set declaring a FIELD_CHOICES key may be replaced or extended by a deployment, so a CRD that
// pins its members rejects a value that deployment considers legal -- and an admission
// rejection is not a state the operator can report its way out of.
func TestExtendableEnumsAreNotPinned(t *testing.T) {
	opts := testOptions(t, "")

	loaded, _, err := loadIR(opts.ir)
	if err != nil {
		t.Fatalf("loadIR() = %v", err)
	}

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		t.Fatalf("loadOverrides() = %v", err)
	}

	build := newBuilder(loaded, over, header{})
	open, closed := 0, 0

	for class, set := range loaded.Enums {
		view := build.enumView(class, set)

		switch {
		case set.Extendable && view.Closed:
			t.Errorf("%s declares key %q and is still emitted closed", class, set.Key)
		case set.Extendable:
			open++

			if view.Base == "string" && view.MaxLength == 0 {
				t.Errorf("%s is open and unbounded; it needs a MaxLength", class)
			}
		case !view.Closed:
			t.Errorf("%s declares no key and is still emitted open", class)
		default:
			closed++
		}
	}

	if open == 0 || closed == 0 {
		t.Errorf("examined %d open and %d closed sets; both must be non-zero", open, closed)
	}
}

// TestGoNamesFollowTheAcronymTable pins the identifiers the shipped kinds already publish. A
// rename here is an API break, so the table is asserted rather than trusted.
func TestGoNamesFollowTheAcronymTable(t *testing.T) {
	over, err := loadOverrides(testOptions(t, "").overrides)
	if err != nil {
		t.Fatalf("loadOverrides() = %v", err)
	}

	names := namer{acronyms: over.Acronyms}

	for _, tc := range []struct{ column, wantGo, wantJSON string }{
		{"vrf", "VRF", "vrf"},
		{"mark_utilized", "MarkUtilized", "markUtilized"},
		{"qinq_svlan", "QinQSVLAN", "qinqSVLAN"},
		{"import_targets", "ImportTargets", "importTargets"},
		{"mac_address", "MACAddress", "macAddress"},
		{"vid", "VID", "vid"},
	} {
		if got := names.goName(tc.column); got != tc.wantGo {
			t.Errorf("goName(%q) = %q, want %q", tc.column, got, tc.wantGo)
		}

		if got := names.jsonName(tc.column); got != tc.wantJSON {
			t.Errorf("jsonName(%q) = %q, want %q", tc.column, got, tc.wantJSON)
		}
	}
}

// TestReconcileBodyIsOneStatement is the review rule for a generated controller, asserted
// instead of reviewed: a generated controller that needs a second statement means the engine is
// missing a Descriptor field.
func TestReconcileBodyIsOneStatement(t *testing.T) {
	outputs, err := plan(testOptions(t, proofKinds[0]))
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}

	found := 0

	for _, out := range outputs {
		if !strings.HasSuffix(out.Path, "_controller.go") {
			continue
		}

		found++

		body := string(out.Body)
		if strings.Count(body, "func ") != 1 || !strings.Contains(body, "func init() { registerObjectKind(") {
			t.Errorf("%s declares more than the one init(); the engine is missing a Descriptor "+
				"field:\n%s", out.Path, body)
		}
	}

	if found == 0 {
		t.Error("no controller emitted; this test just passed by checking nothing")
	}
}
