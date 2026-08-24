package main

import (
	"fmt"
	"os"
	"slices"

	"sigs.k8s.io/yaml"
)

// overrides is the per-kind data no reading of NetBox can supply: the `kubectl` short name,
// which columns a human wants in `kubectl get`, and which members of a polymorphic reference
// the server cascades from. Everything else is derived, and a key here that could have been
// derived is a bug in the derivation rather than a convenience.
//
// It is data and never code, and no template may branch on a kind name: a per-kind branch in
// a template is a switch on kind with extra indirection, which is the thing the generator
// exists to remove (specs/NBO-042-codegen-emitters.md).
type overrides struct {
	// Acronyms are the initialisms that stay upper-case in a Go identifier, so `vrf` is
	// `VRFRef` and not `VrfRef`. Longest match first, so SVLAN beats VLAN.
	Acronyms []string `json:"acronyms"`

	// ExtraRefs are typed reference aliases no NetBox foreign key produces, listed as
	// `app.Model`. The operator's own kinds need a few: NetBoxIPAddressClaim points at a
	// prefix to allocate from, and nothing in NetBox has a foreign key to ipam.Prefix, so
	// there is no FK for the alias to be derived from.
	ExtraRefs []string `json:"extraRefs"`

	// Kinds is keyed by `app.Model`, the same key the IR uses.
	Kinds map[string]kindOverride `json:"kinds"`
}

// kindOverride is one kind's undecidable facts.
type kindOverride struct {
	// HandWritten keeps a kind out of a full run. It is still ingested and validated, so a
	// hand-written kind that stops matching its IR is still a failure -- it just is not
	// overwritten. Naming the kind in --kinds emits it anyway, which is how a hand-written
	// kind is diffed against what the generator would produce.
	HandWritten bool `json:"handWritten"`

	// ShortNames is the `kubectl get` abbreviation. Defaults to `nb` + the lower-cased
	// model, which is right for most kinds and wrong for every long one (ADR-0001).
	ShortNames []string `json:"shortNames"`

	// PrinterColumns are the kind-specific `kubectl get` columns, before the shared
	// ID/Ready/Age set. Which columns matter to a human is not in any schema.
	PrinterColumns []printerColumn `json:"printerColumns"`

	// Cascades says, per union member spec field, whether the server deletes this kind's
	// rows when that target goes. It cannot be derived from the referring model: half the
	// answer is a GenericRelation on the *target* and half is an `on_delete=CASCADE`
	// denormalised column on the referrer (internal/registry/scope.go, #214).
	Cascades map[string]bool `json:"cascades"`

	// ContainmentRef is the one reference that earns an owner reference. One per kind,
	// because Kubernetes garbage collection waits for every owner (ADR-0003 rule 4).
	ContainmentRef string `json:"containmentRef"`

	// Inherited are columns the embedded NetBoxObjectSpec already supplies, so redeclaring
	// them would shadow rather than add.
	Inherited []string `json:"inherited"`

	// Omit are columns deliberately absent from the CRD, each with its reason in the
	// comment next to it in overrides.yaml.
	Omit []string `json:"omit"`

	// ReadOnlyExtra are columns the operator must never write that the IR does not carry,
	// because they come from a base outside the NetBox source tree. `mptt.MPTTModel` gives
	// every nested-group kind `_depth` and `_children`, and the AST walk never sees them --
	// writing one does not fail, it silently no-ops, so the next reconcile PATCHes forever.
	ReadOnlyExtra []string `json:"readOnlyExtra"`

	// GoTypes overrides the Go type of a column whose shape is not derivable -- a Postgres
	// ArrayField, whose element type is an argument the AST walk does not carry.
	GoTypes map[string]string `json:"goTypes"`

	// NaturalKeys replaces the derived candidates outright, for a kind whose identity is a
	// NetBox *convention* rather than a UNIQUE. ipam.Prefix is the case: it carries no
	// meta.constraints at all and `prefix` is not unique, because duplicates are legal when
	// the enclosing VRF has `enforce_unique=false`. `(vrf, prefix)` is the ordering tuple and
	// nothing in the schema says it identifies a row, so more than one match is a legitimate
	// server state -- which is why it is reported as a Conflict and nothing is written.
	//
	// A replacement and not an addition: a kind that needs this needs the derived candidates
	// gone, or the wrong one is tried first.
	NaturalKeys []naturalKeyOverride `json:"naturalKeys"`

	// ExtraCEL are XValidation rules that are facts about NetBox rather than about the
	// column, emitted verbatim.
	ExtraCEL []celRule `json:"extraCEL"`
}

// naturalKeyOverride is one hand-declared lookup candidate.
type naturalKeyOverride struct {
	// Doc must cite what makes the candidate identify at most one object. A key with no
	// citation is a key nobody can review, and a wrong key adopts somebody else's row.
	Doc        string              `json:"doc"`
	Fields     []keyFieldOverride  `json:"fields"`
	NullFields []nullFieldOverride `json:"nullFields"`
}

// keyFieldOverride is one matched filter.
type keyFieldOverride struct {
	Filter string `json:"filter"`
	Spec   string `json:"spec"`
	Lookup string `json:"lookup"`
}

// nullFieldOverride is one pinned filter. Column is `ref`, `char` or `numeric`, the same three
// classes registry.NullColumn declares -- there is no fourth and no default, because NetBox
// spells a null pin differently per class.
type nullFieldOverride struct {
	Filter string `json:"filter"`
	Spec   string `json:"spec"`
	Column string `json:"column"`
}

// printerColumn is one `+kubebuilder:printcolumn` marker.
type printerColumn struct {
	Name string `json:"name"`
	Type string `json:"type"`
	Path string `json:"path"`
}

// celRule is one XValidation marker.
type celRule struct {
	Rule    string `json:"rule"`
	Message string `json:"message"`
}

// loadOverrides reads the file and sorts the acronym table longest-first, so a longer
// initialism wins over a shorter one that prefixes it.
func loadOverrides(path string) (*overrides, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	out := &overrides{}
	if err := yaml.Unmarshal(raw, out); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	slices.SortFunc(out.Acronyms, func(a, b string) int { return len(b) - len(a) })

	return out, nil
}

// of returns a kind's overrides, zero-valued when it has none.
func (o *overrides) of(kind string) kindOverride { return o.Kinds[kind] }
