package export

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

// ErrOutputExists is returned when a file the export would write is already there and
// --force was not given. Returned before anything is written, so a mistyped -o cannot
// half-overwrite a directory of reviewed manifests.
var ErrOutputExists = errors.New("output file already exists")

// Run exports NetBox into opts.OutputDir and reports what it did.
//
// Read-only with respect to NetBox -- client is a Lister, so there is no mutating method
// to call -- and it touches no cluster at all. The only side effect is files, for a human
// to read and commit (docs/decisions/0005-gitops-coexistence.md section 4).
func Run(ctx context.Context, client Lister, opts Options) (Result, error) {
	files, result, err := Build(ctx, client, opts)
	if err != nil {
		return result, err
	}

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	slices.Sort(names)

	if err := checkClear(opts, names); err != nil {
		return result, err
	}

	for _, name := range names {
		path := filepath.Join(opts.OutputDir, name)
		result.Files = append(result.Files, path)
		if opts.DryRun {
			continue
		}
		if err := os.WriteFile(path, []byte(files[name]), 0o644); err != nil { //nolint:gosec // manifests are reviewed and committed, not secrets
			return result, fmt.Errorf("writing %s: %w", path, err)
		}
	}

	return result, nil
}

// checkClear refuses to overwrite, and creates the output directory. Both happen before
// the first write: a run that fails halfway through leaves a directory whose manifests are
// half from one NetBox and half from another, which is the one output nobody can review.
func checkClear(opts Options, names []string) error {
	for _, name := range names {
		path := filepath.Join(opts.OutputDir, name)
		if _, err := os.Stat(path); err == nil && !opts.Force {
			return fmt.Errorf("%w: %s (pass --force to overwrite)", ErrOutputExists, path)
		}
	}

	if opts.DryRun {
		return nil
	}

	if err := os.MkdirAll(opts.OutputDir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", opts.OutputDir, err)
	}

	return nil
}
