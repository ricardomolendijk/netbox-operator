// Command gen-types emits the three per-kind files plus the two shared files from the NetBox
// IR that hack/build-netbox-ir.py produces.
//
// A new kind is data entry: a row in hack/gen-types/overrides.yaml for the handful of facts no
// reading of NetBox can supply, and nothing else. No template branches on a kind name -- a
// per-kind branch is a switch on kind with extra indirection, and it defeats the reason the
// generator exists (specs/NBO-042-codegen-emitters.md).
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "gen-types:", err)
		os.Exit(1)
	}
}

// options are the CLI flags.
type options struct {
	ir        string
	overrides string
	out       string
	kinds     string
	check     bool
	stamped   string
}

// run is main() with errors, so every failure path is one `return err` and the exit code is
// decided in one place.
func run() error {
	opts := options{}

	flag.StringVar(&opts.ir, "ir", "hack/testdata/ir-4.6.8.json.gz", "path to the IR, gzipped or plain")
	flag.StringVar(&opts.overrides, "overrides", "hack/gen-types/overrides.yaml", "path to the per-kind overrides")
	flag.StringVar(&opts.out, "out", ".", "repository root to write into")
	flag.StringVar(&opts.kinds, "kinds", "", "comma-separated app.Model list; default is every generated kind")
	flag.StringVar(&opts.stamped, "stamped", "internal/controller/provenance_test.go",
		"file carrying the hand-written stampedObjectTypes literal; empty disables the check")
	flag.BoolVar(&opts.check, "check", false, "write nothing; exit non-zero if any output differs from the tree")
	flag.Parse()

	outputs, err := plan(opts)
	if err != nil {
		return err
	}

	if opts.check {
		stale := check(outputs)
		if len(stale) == 0 {
			return nil
		}

		return fmt.Errorf("%d generated file(s) differ from the tree; run `make gen-kinds`:\n\t%s",
			len(stale), strings.Join(stale, "\n\t"))
	}

	if err := write(outputs); err != nil {
		return err
	}

	fmt.Fprintf(os.Stderr, "gen-types: wrote %d files\n", len(outputs))

	return nil
}

// plan renders every output without touching the disk, so `--check` and a real run share one
// code path and cannot disagree about what would be written.
func plan(opts options) ([]output, error) {
	loaded, sha, err := loadIR(opts.ir)
	if err != nil {
		return nil, err
	}

	over, err := loadOverrides(opts.overrides)
	if err != nil {
		return nil, err
	}

	head := header{NetBoxVersion: loaded.NetBoxVersion, IRSHA: sha, IRPath: filepath.ToSlash(opts.ir)}
	build := newBuilder(loaded, over, head)

	emit, err := newEmitter(opts.out)
	if err != nil {
		return nil, err
	}

	// Every generatable kind, always, and not just the selected ones: the two shared files are
	// keyed by NetBox name rather than by kind, so building them from a --kinds subset would
	// delete the other hundred kinds' enums and typed references. A kind that does not build
	// yet contributes nothing, which is consistent -- the per-kind file that would cite its
	// enum does not exist either.
	// Reported only on a full run. A --kinds run is someone iterating on one template, and the
	// other hundred kinds' missing overrides are not what they are looking at.
	reached := build.reach(everyKind(loaded))
	if opts.kinds == "" {
		warn(reached)
	}

	selected, explicit := selectKinds(loaded, over, opts.kinds)

	outputs, err := perKind(build, emit, selected)

	// A kind named on the command line must emit or the run fails; a kind merely picked up by
	// a full run may not be ready yet, which is the normal state while overrides.yaml is being
	// filled in and blocks nothing that *is* being emitted.
	if err != nil && explicit {
		return nil, err
	}

	if err != nil {
		warn(err)
	}

	if err := verifyStamped(opts, build, selected); err != nil {
		return nil, err
	}

	shared, err := sharedFiles(build, emit)
	if err != nil {
		return nil, err
	}

	if err := errors.Join(build.collisions...); err != nil {
		return nil, err
	}

	return append(outputs, shared...), nil
}

// perKind renders the three files for each selected kind.
//
// Every kind is attempted and the failures are joined, rather than stopping at the first. A run
// that adds ninety kinds needs the whole list of missing overrides in one pass; reporting one
// per invocation turns a morning of data entry into ninety builds.
func perKind(build *builder, emit *emitter, kinds []string) ([]output, error) {
	outputs := make([]output, 0, 3*len(kinds))

	var errs []error

	for _, name := range kinds {
		view, err := build.buildKind(name)
		if err != nil {
			errs = append(errs, err)

			continue
		}

		for _, target := range []struct{ tmpl, dir string }{
			{"types.go.tmpl", "api/v1alpha1"},
			{"registry.go.tmpl", "internal/registry"},
			{"controller.go.tmpl", "internal/controller"},
		} {
			suffix := ".go"
			if target.dir == "internal/controller" {
				suffix = "_controller.go"
			}

			out, err := emit.render(target.tmpl, filepath.Join(target.dir, view.FileStem+suffix), view)
			if err != nil {
				errs = append(errs, err)

				continue
			}

			outputs = append(outputs, out)
		}
	}

	return outputs, errors.Join(errs...)
}

// sharedFiles renders the two files keyed by NetBox name rather than by kind.
func sharedFiles(build *builder, emit *emitter) ([]output, error) {
	view := build.shared()

	refs, err := emit.render("refs.go.tmpl", "api/v1alpha1/zz_generated_refs.go", view)
	if err != nil {
		return nil, err
	}

	enums, err := emit.render("enums.go.tmpl", "api/v1alpha1/zz_generated_enums.go", view)
	if err != nil {
		return nil, err
	}

	return []output{refs, enums}, nil
}

// everyKind is the whole catalogue, hand-written kinds included: the typed reference aliases
// and the choices types are the catalogue's, not the generated subset's, and a hand-written
// kind refers to `VMInterfaceRef` exactly as a generated one does.
func everyKind(loaded *ir) []string {
	out := make([]string, 0, len(loaded.Kinds))

	for name := range loaded.Kinds {
		out = append(out, name)
	}

	slices.Sort(out)

	return out
}

// warn reports on stderr what a kind outside the selection could not contribute. Not fatal: a
// kind whose overrides have not been written yet is the normal state while the catalogue is
// being filled in, and it blocks nothing that is being emitted.
func warn(err error) {
	if err == nil {
		return
	}

	fmt.Fprintf(os.Stderr, "gen-types: kinds not yet emittable:\n%v\n", err)
}

// selectKinds resolves the --kinds flag, and reports whether the caller named a subset.
//
// A hand-written kind is skipped by default and emitted when named, which is the only way to
// diff what the generator would produce against what a human wrote -- the case NBO-043
// measures. It writes into --out, so the diff run points that somewhere other than the tree.
func selectKinds(loaded *ir, over *overrides, flagValue string) ([]string, bool) {
	if flagValue != "" {
		named := strings.Split(flagValue, ",")
		slices.Sort(named)

		return named, true
	}

	var out []string

	for name := range loaded.Kinds {
		if !over.of(name).HandWritten {
			out = append(out, name)
		}
	}

	slices.Sort(out)

	return out, false
}

// verifyStamped fails the run when a CustomFieldable kind is absent from the hand-written
// stampedObjectTypes literal in internal/controller/provenance_test.go.
//
// The literal is deliberately not generated. It is the one independent copy of the set, and it
// is what catches a kind dropped from the provenance stamp; emitting it would make the
// assertion a generator agreeing with itself. Checking it here instead of leaving it to the
// test is a judgement made for this ticket: the test needs envtest and reports one kind at a
// time, and a run that adds ninety kinds needs to name all ninety at once.
func verifyStamped(opts options, build *builder, kinds []string) error {
	if opts.stamped == "" {
		return nil
	}

	raw, err := os.ReadFile(filepath.Join(opts.out, opts.stamped))
	if err != nil {
		return fmt.Errorf("reading %s: %w", opts.stamped, err)
	}

	body := string(raw)

	missing := make([]string, 0, len(kinds))

	for _, name := range kinds {
		kind := build.ir.Kinds[name]
		if !mixesIn(kind, "CustomFieldsMixin") || strings.Contains(body, `"`+kind.ObjectType+`"`) {
			continue
		}

		missing = append(missing, kind.ObjectType)
	}

	if len(missing) == 0 {
		return nil
	}

	return fmt.Errorf("%w: add these %d object types to stampedObjectTypes in %s, keeping the "+
		"list sorted (or pass -stamped= to skip the check on an exploratory run):\n\t%q",
		errUnstamped, len(missing), opts.stamped, missing)
}

// loadIR reads the IR and the SHA-256 of the file it came from. The digest is of the *file*,
// so it moves only when the inputs move -- a header that changed per run would make `--check`
// fail for reasons unrelated to the change.
func loadIR(path string) (*ir, string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", fmt.Errorf("reading %s: %w", path, err)
	}

	sum := sha256.Sum256(raw)

	body, err := maybeGunzip(raw)
	if err != nil {
		return nil, "", fmt.Errorf("decompressing %s: %w", path, err)
	}

	out := &ir{}
	if err := json.Unmarshal(body, out); err != nil {
		return nil, "", fmt.Errorf("parsing %s: %w", path, err)
	}

	if out.NetBoxVersion == "" {
		return nil, "", fmt.Errorf("%s carries no netbox_version; it is not an IR", path)
	}

	return out, hex.EncodeToString(sum[:]), nil
}
