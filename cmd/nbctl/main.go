// Command nbctl is the operator's companion CLI. Its one subcommand so far is `export`,
// which reads a live NetBox and writes CR manifests for a human to review and commit.
//
// It writes files. It does not write to NetBox, it does not write to a cluster, and it
// does not write to Git -- see docs/operations/exporting.md and
// docs/decisions/0005-gitops-coexistence.md.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/go-logr/logr"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/ricardomolendijk/netbox-operator/internal/export"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// tokenEnv is where the NetBox token comes from. Never a flag: a flag value is in the
// shell history and in every `ps` listing on the machine.
const tokenEnv = "NETBOX_TOKEN"

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "nbctl: %v\n", err)
		os.Exit(1)
	}
}

// run is main with the exits taken out, so the whole command is testable.
func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 || args[0] != "export" {
		line(stderr, "%s", usage)

		return errors.New("expected the export subcommand")
	}

	opts, url, err := parseExport(args[1:], stderr)
	if err != nil {
		return err
	}

	// internal/netbox logs through the logger on the context. A CLI has no log stream, and
	// the one thing worth reporting -- a truncated list -- comes back as an error and is
	// printed below, so discarding is not losing anything.
	logf.SetLogger(logr.Discard())

	client, err := netbox.New(netbox.Config{URL: url, Token: os.Getenv(tokenEnv), Mode: netbox.ModeDryRun})
	if err != nil {
		return fmt.Errorf("connecting to %s: %w", url, err)
	}
	defer client.CloseIdleConnections()

	result, err := export.Run(context.Background(), client, opts)
	if err != nil {
		return err
	}

	report(stdout, stderr, opts, result)

	return nil
}

// parseExport turns the flags into Options, refusing the ones that have no safe default.
func parseExport(args []string, stderr io.Writer) (export.Options, string, error) {
	var (
		opts  export.Options
		url   string
		kinds string
		split string
	)

	flags := flag.NewFlagSet("nbctl export", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.StringVar(&url, "url", "", "NetBox base URL to read from. The token comes from "+tokenEnv+".")
	flags.StringVar(&opts.EndpointRef, "endpoint", "",
		"NetBoxEndpoint name to write into every object's spec.endpointRef.")
	flags.StringVar(&opts.Namespace, "namespace", "", "Namespace to write every object into.")
	flags.StringVar(&opts.Namespace, "n", "", "Short form of --namespace.")
	flags.StringVar(&opts.OutputDir, "o", "", "Directory to write manifests into.")
	flags.StringVar(&kinds, "kinds", "", "Comma-separated Kinds to export. Default: every registered Kind.")
	flags.StringVar(&split, "split", "kind", "One file per `kind`, or a `single` file.")
	flags.BoolVar(&opts.Full, "full", false, "Keep fields whose value is empty.")
	flags.BoolVar(&opts.IDRefs, "id-refs", false, "Emit every reference as a NetBox id instead of a CR name.")
	flags.BoolVar(&opts.IncludeManaged, "include-managed", false,
		"Also export objects the operator already manages. They already have manifests in Git.")
	flags.StringVar(&opts.ManagedTag, "managed-tag", provenance.DefaultTag,
		"Provenance tag marking an operator-managed object, from NetBoxEndpoint.spec.managedBy.tag.")
	flags.BoolVar(&opts.DryRun, "dry-run", false, "List the files that would be written and write nothing.")
	flags.BoolVar(&opts.Force, "force", false, "Overwrite manifests that are already there.")

	if err := flags.Parse(args); err != nil {
		return opts, "", fmt.Errorf("parsing flags: %w", err)
	}

	if kinds != "" {
		opts.Kinds = strings.Split(kinds, ",")
	}
	opts.Single = split == "single"

	return opts, url, validate(opts, url, split)
}

// validate is the guard on the four flags that cannot be defaulted.
//
// --endpoint is required rather than guessed. spec.endpointRef names a NetBoxEndpoint in
// the destination cluster, which is not something a NetBox URL can be turned into: a
// derived default would produce manifests that apply cleanly and then sit forever waiting
// for an endpoint nobody created.
func validate(opts export.Options, url, split string) error {
	if url == "" {
		return errors.New("--url is required")
	}
	if os.Getenv(tokenEnv) == "" {
		return fmt.Errorf("%s is required", tokenEnv)
	}
	if opts.Namespace == "" {
		return errors.New("--namespace is required: every kind is namespaced in v1alpha1")
	}
	if opts.OutputDir == "" {
		return errors.New("-o is required")
	}
	if opts.EndpointRef == "" {
		return errors.New("--endpoint is required: it names the NetBoxEndpoint in the destination cluster")
	}
	if split != "kind" && split != "single" {
		return fmt.Errorf("--split must be kind or single, not %q", split)
	}

	return nil
}

// report prints the summary a human acts on: what was written, and everything that needed
// a decision the export could only make one way.
func report(stdout, stderr io.Writer, opts export.Options, result export.Result) {
	verb := "wrote"
	if opts.DryRun {
		verb = "would write"
	}

	for _, path := range result.Files {
		line(stdout, "%s %s\n", verb, path)
	}
	line(stdout, "%d objects, %d files\n", result.Objects, len(result.Files))

	if result.SkippedManaged > 0 {
		line(stdout,
			"%d objects the operator already manages were skipped; --include-managed exports them too\n",
			result.SkippedManaged)
	}

	for _, note := range result.Notes {
		line(stderr, "note: %s\n", note)
	}

	line(stdout, "review the manifests, then commit them: nothing in the operator writes to Git\n")
}

// line writes one line of human-facing output.
//
// The error is dropped deliberately and in exactly one place: a CLI whose stdout has gone
// away has nothing useful to do about it, and threading that through every report line
// would be five error paths for a condition the shell has already handled.
func line(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

const usage = `nbctl export -- read a live NetBox and write CR manifests.

Usage:
  NETBOX_TOKEN=... nbctl export --url URL --endpoint NAME -n NAMESPACE -o DIR [flags]

It writes files for a human to review and commit. It never writes to NetBox, to a
cluster, or to Git. See docs/operations/exporting.md.
`
