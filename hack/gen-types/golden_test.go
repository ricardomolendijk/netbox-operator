package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// update rewrites the golden files instead of comparing against them:
//
//	go test ./hack/gen-types -run Golden -update
//
// A golden file is only worth having if regenerating it is one command, and only worth
// trusting if the diff is reviewed -- which is why this writes the files and never commits
// them (docs/regenerating.md).
var update = flag.Bool("update", false, "rewrite the golden files from the current emitter output")

// goldenKinds are the three kinds the golden files pin, chosen so that between them they cover
// every part of an emitted file that has gone wrong in this repository before:
//
//   - ipam.VRF: two to-many references, so the MaxItems bound CONTRIBUTING.md requires is in
//     the bytes rather than in an argument; an optional char column with the tri-state note;
//     and a natural key with a char null pin.
//   - ipam.VLANGroup: a polymorphic union with per-member cascades and a containment ref, a
//     Postgres ArrayField whose Go type comes from overrides.yaml, and a scope pair with no
//     cached columns.
//   - virtualization.VMInterface: two derived self-referential deferrals next to one declared
//     in overrides.yaml, a to-many VLAN list, an enum and a required reference.
var goldenKinds = []string{"ipam.VRF", "ipam.VLANGroup", "virtualization.VMInterface"}

// TestEmittedFilesMatchTheGoldenFiles is the emitter's own regression test.
//
// It pins bytes rather than properties because bytes are what NBO-043 gates on: a template
// change that reflows a comment is harmless and a template change that drops a marker is a CRD
// the API server refuses at install, and only a byte comparison tells a reviewer which one
// they are looking at.
func TestEmittedFilesMatchTheGoldenFiles(t *testing.T) {
	outputs, err := plan(testOptions(t, strings.Join(goldenKinds, ",")))
	if err != nil {
		t.Fatalf("plan() = %v", err)
	}

	for _, out := range outputs {
		// The two shared files are deliberately not pinned by bytes. They are the whole
		// catalogue's enums and typed references -- 200 kB that moves whenever any of 134
		// kinds does -- so a golden copy would be repo bulk nobody could review as a diff.
		// Their properties are pinned instead, by TestEnumConstantsAreUniquePerType,
		// TestExtendableEnumsAreNotPinned and TestEmitIsDeterministic.
		if strings.HasPrefix(filepath.Base(out.Path), "zz_generated") {
			continue
		}

		golden := filepath.Join("testdata", "golden", goldenName(out.Path))

		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatalf("creating %s: %v", filepath.Dir(golden), err)
			}

			if err := os.WriteFile(golden, out.Body, 0o644); err != nil {
				t.Fatalf("writing %s: %v", golden, err)
			}

			continue
		}

		want, err := os.ReadFile(golden)
		if err != nil {
			t.Errorf("reading %s: %v; run `go test ./hack/gen-types -run Golden -update`", golden, err)

			continue
		}

		if string(want) != string(out.Body) {
			t.Errorf("%s differs from %s at %s; run "+
				"`go test ./hack/gen-types -run Golden -update` and review the diff",
				out.Path, golden, firstDifferingLine(string(want), string(out.Body)))
		}
	}
}

// goldenName flattens an output path into one golden file name, so `api/v1alpha1/ipam_vrf.go`
// and `internal/registry/ipam_vrf.go` do not collide in one directory.
func goldenName(path string) string {
	dir, file := filepath.Split(filepath.ToSlash(path))

	switch {
	case strings.HasSuffix(dir, "api/v1alpha1/"):
		return "api." + file + ".golden"
	case strings.HasSuffix(dir, "internal/registry/"):
		return "registry." + file + ".golden"
	default:
		return "controller." + file + ".golden"
	}
}

// firstDifferingLine names the earliest line at which two files disagree. A diff of a
// 300-line generated file is unreadable and the line number is the useful part.
func firstDifferingLine(want, got string) string {
	wantLines, gotLines := strings.Split(want, "\n"), strings.Split(got, "\n")

	for i := range max(len(wantLines), len(gotLines)) {
		switch {
		case i >= len(gotLines):
			return fmt.Sprintf("line %d: the output ends, the golden file has %q", i+1, wantLines[i])
		case i >= len(wantLines):
			return fmt.Sprintf("line %d: the golden file ends, the output has %q", i+1, gotLines[i])
		case wantLines[i] != gotLines[i]:
			return fmt.Sprintf("line %d: golden %q, output %q", i+1, wantLines[i], gotLines[i])
		}
	}

	return "nowhere: the files are equal"
}

// refListField matches an emitted to-many reference field: `TaggedVLANs []VLANRef `json:...“.
var refListField = regexp.MustCompile("^\t[A-Z]\\w* \\[\\]\\w+Ref `json:")

// TestEveryRefManyFieldCarriesTheListBound is CONTRIBUTING.md's generator requirement, checked
// over the whole catalogue rather than over a sample.
//
// ObjectRef carries five CEL rules and the API server costs each at the list's maximum length,
// so a to-many reference with no bound is costed as unbounded and the *whole CRD is refused at
// install* -- while controller-gen, `kustomize build` and `make verify` all stay green. The
// first sign of it is a failed deploy, which is why it is asserted here on the emitted bytes
// and again in api/v1alpha1/reflistbounds_test.go on the generated CRDs.
func TestEveryRefManyFieldCarriesTheListBound(t *testing.T) {
	bound := fmt.Sprintf("MaxItems=%d", refListBound)

	for _, kind := range everyEmittableKind(t) {
		outputs, err := plan(testOptions(t, kind))
		if err != nil {
			continue // Reported by TestEveryHandWrittenKindEmits; not this test's subject.
		}

		for _, out := range outputs {
			if !strings.Contains(filepath.ToSlash(out.Path), "/api/v1alpha1/") {
				continue
			}

			for _, field := range unboundedRefLists(string(out.Body), bound) {
				t.Errorf("%s: %s is a list of references and carries no %s; the API server "+
					"refuses the whole CRD at install (CONTRIBUTING.md)", kind, field, bound)
			}
		}
	}
}

// unboundedRefLists returns the to-many reference fields in one emitted file whose marker
// block does not carry the bound. The block is the run of comment lines above the field, which
// is exactly what controller-gen reads.
func unboundedRefLists(body, bound string) []string {
	var (
		out     []string
		markers []string
	)

	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			markers = append(markers, line)

			continue
		}

		if refListField.MatchString(line) && !slices.ContainsFunc(markers,
			func(m string) bool { return strings.Contains(m, bound) }) {
			out = append(out, strings.Fields(strings.TrimSpace(line))[0])
		}

		markers = nil
	}

	return out
}

// TestOptionalFieldsKeepOmitEmpty guards the inverse of the tri-state bug.
//
// Without `omitempty` a typed Go client marshals every unset string as `""` and claims it, so
// adopting a pre-existing NetBox object would wipe every value the user had not restated
// (CONTRIBUTING.md, "Optional spec fields have three states"). A generator emits a hundred
// fields at once, so the mistake would land everywhere before anyone read one file.
func TestOptionalFieldsKeepOmitEmpty(t *testing.T) {
	for _, kind := range goldenKinds {
		view := buildView(t, kind)

		for _, f := range view.Fields {
			optional := slices.Contains(f.Markers, "+optional")
			omits := strings.Contains(f.Tag, ",omitempty")

			if optional != omits {
				t.Errorf("%s.%s: +optional is %v and omitempty is %v; they are one decision",
					kind, f.Name, optional, omits)
			}
		}
	}
}

// TestClearableFieldsCarryTheEmptyStateNote is the other direction of the same rule, and the
// one internal/controller/manifests_test.go fails a kind for: an optional field whose schema
// can hold the empty value has to say so, because that comment is what `kubectl explain`
// prints -- and a field whose schema *forbids* the empty value must not claim it can.
func TestClearableFieldsCarryTheEmptyStateNote(t *testing.T) {
	note := triStateNote()[0]

	for _, kind := range goldenKinds {
		view := buildView(t, kind)

		for _, f := range view.Fields {
			documented := slices.ContainsFunc(f.Doc,
				func(line string) bool { return strings.Contains(line, note) })

			forbidden := slices.ContainsFunc(f.Markers, func(m string) bool {
				return strings.Contains(m, ":default=") || strings.Contains(m, "MinLength=") ||
					strings.Contains(m, "Pattern=")
			})

			if documented && forbidden {
				t.Errorf("%s.%s documents an empty state its own schema rejects", kind, f.Name)
			}

			clearable := f.Type == "string" && slices.Contains(f.Markers, "+optional") && !forbidden
			if clearable && !documented {
				t.Errorf("%s.%s can hold the empty value and does not document it", kind, f.Name)
			}
		}
	}
}

// TestDescriptorFactsSurviveEmission is the rule that emitted output may not *lose* a per-kind
// fact a hand-written Descriptor carried.
//
// Every one of these is a policy judgement with no schema behind it, so each arrives from
// overrides.yaml -- and a template that silently dropped one would produce a Descriptor that
// boots, reconciles, and does the wrong thing. `RetainOnDelete` is the sharp case: dropped, an
// IPAM kind starts deleting NetBox rows on `kubectl delete`.
func TestDescriptorFactsSurviveEmission(t *testing.T) {
	for _, tc := range []struct{ kind, want string }{
		{"ipam.IPRange", "RetainOnDelete: true"},
		{"ipam.VLAN", `{APIField: "qinq_svlan", Mode: DeferAlways}`},
		{"virtualization.VirtualMachine", `{APIField: "primary_ip4", Mode: DeferAlways}`},
		{"virtualization.VMInterface", `{APIField: "parent", Mode: DeferIfUnresolved}`},
		{"ipam.VLANGroup", `ContainmentRef: "scope"`},
		{"ipam.VRF", "UpdateStrategy: UpdatePatch"},
		{"dcim.Site", "CustomFieldable: true"},
	} {
		t.Run(tc.kind+" "+tc.want, func(t *testing.T) {
			if body := emittedRegistry(t, tc.kind); !strings.Contains(body, tc.want) {
				t.Errorf("the emitted Descriptor for %s does not carry %q", tc.kind, tc.want)
			}
		})
	}
}

// TestSelfReferenceInANaturalKeyIsNotDeferred is why the derived deferral is safe rather than
// merely plausible.
//
// dcim.DeviceRole keys on `(parent_id, slug)` and on `slug` with `parent_id` pinned null, so
// stripping `parent` from the create would change the identity the lookup had already decided
// on -- registry.Descriptor.Validate refuses that pair outright (ErrDeferredNaturalKey), and a
// generator that derived it anyway would ship a descriptor that cannot boot.
func TestSelfReferenceInANaturalKeyIsNotDeferred(t *testing.T) {
	if body := emittedRegistry(t, "dcim.DeviceRole"); strings.Contains(body, "Deferred:") {
		t.Error("dcim.DeviceRole defers a column its own natural key matches on")
	}

	// The contrast, so the test fails if the derivation stops working altogether rather than
	// only when it over-reaches: tenancy.TenantGroup keys on `slug` alone, so its self-
	// reference is deferrable and is derived without an overrides entry.
	if body := emittedRegistry(t, "tenancy.TenantGroup"); !strings.Contains(body,
		`{APIField: "parent", Mode: DeferIfUnresolved}`) {
		t.Error("tenancy.TenantGroup does not defer `parent`, whose natural key does not read it")
	}
}

// emittedRegistry is one kind's rendered Descriptor file.
func emittedRegistry(t *testing.T, kind string) string {
	t.Helper()

	outputs, err := plan(testOptions(t, kind))
	if err != nil {
		t.Fatalf("plan(%s) = %v", kind, err)
	}

	for _, out := range outputs {
		if strings.Contains(filepath.ToSlash(out.Path), "/internal/registry/") {
			return string(out.Body)
		}
	}

	t.Fatalf("plan(%s) emitted no descriptor", kind)

	return ""
}

// buildView is one kind's template view, for a test about a field rather than about bytes.
func buildView(t *testing.T, kind string) kindView {
	t.Helper()

	opts := testOptions(t, kind)

	loaded, sha, err := loadIR(opts.ir)
	if err != nil {
		t.Fatalf("loading the IR: %v", err)
	}

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		t.Fatalf("loading the overrides: %v", err)
	}

	view, err := newBuilder(loaded, over, header{IRSHA: sha}).buildKind(kind)
	if err != nil {
		t.Fatalf("buildKind(%s) = %v", kind, err)
	}

	return view
}

// everyEmittableKind is every kind overrides.yaml has triaged, hand-written ones included: the
// list-bound rule is about what the emitter produces and not about what is committed.
func everyEmittableKind(t *testing.T) []string {
	t.Helper()

	opts := testOptions(t, "")

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		t.Fatalf("loading the overrides: %v", err)
	}

	out := make([]string, 0, len(over.Kinds))
	for name := range over.Kinds {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

// blockedKinds are the hand-written kinds the emitter cannot build yet, each with the reason.
//
// All three are gaps in the IR rather than in the emitter, and the emitter refuses rather than
// guessing because every one of them would otherwise ship a Descriptor that cannot boot:
//
//   - ipam.IPAddress and tenancy.ContactAssignment: the legal `app_label.model` strings for a
//     polymorphic reference live only in the serializer's `ContentTypeField(queryset=...)`, and
//     for these two the queryset is built dynamically rather than written as a literal the AST
//     walk can evaluate. An empty AllowedTypes means "the type half accepts anything", which is
//     the opposite of what a union is for.
//   - extras.Tag: `name` and `slug` are declared on taggit's `TagBase`, which lives outside the
//     NetBox source tree, so the AST walk sees neither and the IR carries neither. Same class of
//     gap as `mptt.MPTTModel`'s `_depth` and `_children`, which `readOnlyExtra` covers -- except
//     that these two are the Kind's whole identity rather than columns to exclude.
var blockedKinds = map[string]string{
	"ipam.IPAddress":            "`assigned_object` union resolved no object types",
	"tenancy.ContactAssignment": "`object` union resolved no object types",
	"extras.Tag":                "keys on `slug`, which no column of that kind produces",
}

// TestEveryHandWrittenKindEmits is the NBO-043 progress meter, pinned so that both directions
// are a test failure: a kind that stops emitting names itself, and a kind that starts emitting
// says so rather than sitting unnoticed in a list nobody rereads.
func TestEveryHandWrittenKindEmits(t *testing.T) {
	over, err := loadOverrides(testOptions(t, "").overrides)
	if err != nil {
		t.Fatalf("loading the overrides: %v", err)
	}

	for kind, override := range over.Kinds {
		if !override.HandWritten {
			continue
		}

		_, err := plan(testOptions(t, kind))
		reason, blocked := blockedKinds[kind]

		switch {
		case err == nil && blocked:
			t.Errorf("%s emits now; drop it from blockedKinds and generate it (%s)", kind, reason)
		case err != nil && !blocked:
			t.Errorf("%s no longer emits: %v", kind, err)
		case err != nil && !strings.Contains(err.Error(), reason):
			t.Errorf("%s fails for a new reason: %v, want one mentioning %q", kind, err, reason)
		}
	}
}
