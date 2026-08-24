// Package export turns a live NetBox into CR manifests on disk.
//
// It is the reverse of the engine, and it is deliberately not a controller. The operator
// never writes a CR spec and never writes to Git
// (docs/decisions/0005-gitops-coexistence.md sections 1 and 4), so the only supported way
// to make NetBox's current contents into desired state is a human running this, reading
// the diff, and committing it. This package writes files. Nothing else.
//
// Everything per-kind comes out of internal/registry. A Descriptor already says which CR
// spec field is written to which NetBox column, with what cardinality, and which columns
// NetBox maintains for itself -- so an exporter is that table read right to left. There is
// no per-kind code here and no switch on Kind: the ~90 kinds still to come inherit this
// one for free (CONTRIBUTING.md, "Extensibility").
package export

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// Lister is the half of the NetBox client this package uses. One method, defined here
// rather than taken from internal/netbox, so that export cannot mutate NetBox even by
// mistake: there is no Create, Patch or Delete to call.
type Lister interface {
	List(ctx context.Context, endpoint string, params netbox.Params) ([]netbox.Object, error)
}

// Options is everything one export run needs. Every field is set from a flag by
// cmd/nbctl.
type Options struct {
	// OutputDir is the directory the manifests are written into. It is created if it does
	// not exist.
	OutputDir string

	// Namespace is the namespace every emitted object is written into.
	Namespace string

	// EndpointRef is the NetBoxEndpoint name written into every object's
	// spec.endpointRef. It is a property of the cluster the manifests are destined for,
	// not of NetBox, so it cannot be discovered here and has no default.
	EndpointRef string

	// Kinds restricts the export to these Kubernetes Kinds. Empty means every registered
	// kind.
	Kinds []string

	// IDRefs emits every reference as a literal NetBox id rather than as the name of
	// another exported CR. See docs/operations/exporting.md for the trade.
	IDRefs bool

	// Full keeps fields whose value is empty. The default drops them, because an omitted
	// spec field means "leave this column alone", which for a value that is already empty
	// is the same state in a tenth of the lines.
	Full bool

	// Single writes one file instead of one per Kind.
	Single bool

	// IncludeManaged exports objects that already carry the operator's provenance. They
	// are skipped by default: some CR in Git already describes them, and a second CR
	// claiming the same NetBox object is a conflict rather than a backup.
	IncludeManaged bool

	// ManagedTag is the provenance tag's slug, from NetBoxEndpoint.spec.managedBy.tag.
	ManagedTag string

	// DryRun reports what would be written and writes nothing.
	DryRun bool

	// Force permits overwriting a file that is already there.
	Force bool
}

// Result is what one run did, for the caller to report.
type Result struct {
	// Files are the paths written, in write order.
	Files []string

	// Objects is how many CRs were emitted.
	Objects int

	// SkippedManaged is how many NetBox objects were left out because the operator
	// already manages them.
	SkippedManaged int

	// Notes are the things a human has to look at: name collisions that took a hash
	// suffix, references emitted as raw ids, scopes that could not be expressed.
	Notes []string
}

// object is one NetBox object on its way to becoming a CR.
type object struct {
	desc registry.Descriptor
	raw  netbox.Object
	id   int
	name string
}

// Build reads NetBox through client and returns the manifests as file contents keyed by
// file name, plus what a human needs to know about the result.
//
// Nothing is written here and nothing is written by the caller until every kind has been
// listed successfully. A list that hits the client's page cap returns *netbox.TruncatedError
// and aborts the whole run: a partially exported NetBox is indistinguishable from a
// complete one once it is in Git, and the next `kubectl apply` would then be a request to
// delete everything the export missed.
func Build(ctx context.Context, client Lister, opts Options) (map[string]string, Result, error) {
	var result Result

	descs, err := descriptors(opts.Kinds)
	if err != nil {
		return nil, result, err
	}

	objects, err := index(ctx, client, descs, opts, &result)
	if err != nil {
		return nil, result, err
	}

	names := nameIndex(objects)
	files := map[string][]string{}

	for _, obj := range objects {
		doc, notes := manifest(obj, names, opts)
		result.Notes = append(result.Notes, notes...)
		result.Objects++
		files[fileName(obj.desc, opts)] = append(files[fileName(obj.desc, opts)], doc)
	}

	return join(files), result, nil
}

// descriptors are the kinds to export, sorted by Kind so the output order never depends
// on registration order.
func descriptors(kinds []string) ([]registry.Descriptor, error) {
	all := registry.List()
	slices.SortFunc(all, func(a, b registry.Descriptor) int {
		return strings.Compare(a.GVK.Kind, b.GVK.Kind)
	})

	if len(kinds) == 0 {
		return all, nil
	}

	out := make([]registry.Descriptor, 0, len(kinds))
	for _, desc := range all {
		if slices.Contains(kinds, desc.GVK.Kind) {
			out = append(out, desc)
		}
	}

	if len(out) != len(kinds) {
		return nil, fmt.Errorf("no registered kind matches one of %s", strings.Join(kinds, ", "))
	}

	return out, nil
}

// index is the first pass: page through every kind, drop what must not be exported, and
// give every remaining object its CR name. It has to finish before anything is emitted,
// because a reference is only expressible by name once its target has one.
func index(
	ctx context.Context, client Lister, descs []registry.Descriptor, opts Options, result *Result,
) ([]object, error) {
	var objects []object

	for _, desc := range descs {
		raws, err := client.List(ctx, desc.Endpoint, nil)
		if err != nil {
			return nil, fmt.Errorf("listing %s: %w", desc.Endpoint, err)
		}

		for _, raw := range raws {
			id, ok := raw.ID()
			if !ok {
				return nil, fmt.Errorf("object from %s has no id", desc.Endpoint)
			}
			if !opts.IncludeManaged && managed(desc, raw, opts.ManagedTag) {
				result.SkippedManaged++
				continue
			}
			objects = append(objects, object{desc: desc, raw: raw, id: id})
		}
	}

	assignNames(objects, result)

	return objects, nil
}

// join renders the accumulated documents per file, in a fixed order.
func join(files map[string][]string) map[string]string {
	out := make(map[string]string, len(files))
	for name, docs := range files {
		out[name] = header + strings.Join(docs, "---\n")
	}

	return out
}

// header is on every file. No timestamp and no host: two exports of an unchanged NetBox
// have to be byte-identical, or the diff a human is supposed to read is noise.
const header = "# Generated by `nbctl export` from a live NetBox.\n" +
	"# Review it, then commit it: nothing in the operator writes to Git.\n" +
	"# See docs/operations/exporting.md.\n"

// fileName is the file one object's manifest goes into.
func fileName(desc registry.Descriptor, opts Options) string {
	if opts.Single {
		return "export.yaml"
	}

	return strings.ToLower(desc.GVK.Kind) + ".yaml"
}
