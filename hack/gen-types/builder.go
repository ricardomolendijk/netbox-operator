package main

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// header is the generated-file preamble every output carries. It holds no timestamp,
// hostname, username or absolute path: a header that changes per machine makes `--check`
// fail in CI for reasons unrelated to the change.
type header struct {
	// NetBoxVersion is the release the IR was taken from.
	NetBoxVersion string

	// IRSHA is the SHA-256 of the IR *file*, so it moves only when the inputs move.
	IRSHA string

	// IRPath is the IR file's base name, never its path. The base name is version-stamped
	// (`ir-4.6.8.json.gz`) so it still says which release the file holds, and dropping the
	// directory is what keeps the header identical whether the run spelled `--ir` relatively
	// or absolutely -- a header that changes with the caller's working directory makes
	// `--check` fail in CI for reasons unrelated to the change. The reviewable provenance is
	// IRSHA either way.
	IRPath string
}

// builder turns the IR into the template views, accumulating the shared files' contents as it
// goes: an enum or a typed reference is emitted once, in one file, keyed by the NetBox name
// rather than by the kind that happened to reach it first.
type builder struct {
	ir     *ir
	over   *overrides
	names  namer
	header header

	// enums is every ChoiceSet some emitted field cites, keyed by class name.
	enums map[string]irEnum

	// refs is every typed ObjectRef alias some emitted field needs, keyed by alias name.
	refs map[string]string

	// collisions are the enum members two of whose values spell one Go constant. Collected
	// rather than returned, because enumView runs inside a template view build and a
	// collision is a fact about the IR that has to stop the run rather than one file.
	collisions []error

	// shorts maps a claimed short name to the kind that claimed it, so a clash is caught
	// across kinds rather than within one.
	shorts map[string]string
}

// newBuilder wires a builder over one IR.
func newBuilder(loaded *ir, over *overrides, head header) *builder {
	return &builder{
		ir: loaded, over: over, names: namer{acronyms: over.Acronyms}, header: head,
		enums: map[string]irEnum{}, refs: map[string]string{}, shorts: map[string]string{},
	}
}

// reach walks every kind for its enums and typed references, discarding the views. It is what
// makes the two shared files complete regardless of which kinds a run emits.
func (b *builder) reach(kinds []string) error {
	var errs []error

	for _, target := range b.over.ExtraRefs {
		b.refs[b.names.refTypeName(target)] = target
	}

	for _, name := range kinds {
		if _, err := b.buildKind(name); err != nil {
			errs = append(errs, err)
		}
	}

	// Short names are claimed during the walk, so the claims are cleared for the emitting
	// pass that follows -- otherwise every kind collides with itself.
	b.shorts = map[string]string{}

	return errors.Join(errs...)
}

// enumView is one choices class as a Go string type.
type enumView struct {
	Name   string
	Class  string
	Source string

	// Closed reports that the members are a closed set, so the type may carry a
	// `+kubebuilder:validation:Enum` marker.
	//
	// False for the 26 of 89 classes that declare a FIELD_CHOICES `key`: a deployment can
	// replace or extend those, so their members are a default and a CRD that pins them
	// rejects a value that deployment considers legal (utilities/choices.py,
	// ChoiceSetMeta.__new__). The const block is still emitted -- it is the documentation
	// and the spelling every manifest should use -- but the schema stays as open as NetBox
	// is.
	Closed bool

	// Key is the FIELD_CHOICES key, present exactly when Closed is false. Emitted in the
	// doc comment so a reader knows which setting widens this type.
	Key string

	// MaxLength bounds an open string type, since dropping the enum marker would otherwise
	// leave it unbounded. Zero on a numeric type, which is bounded by int32.
	MaxLength int

	// Base is the Go type the string type is defined over. NetBox declares a handful of sets
	// with integer members -- `ConsolePortSpeedChoices` is bits per second, `IKEVersion` is
	// 1 or 2 -- and a string type over those would send `"1200"` where the API wants 1200.
	Base string

	Values []enumValue
}

// enumValue is one member.
type enumValue struct {
	Const  string
	Value  string
	Label  string
	Quoted bool
}

// enumBase is the Go type a set's members fit in. Every member has to agree: a set mixing a
// number and a string is a set no single Go type holds, and it is reported rather than coerced.
func enumBase(set irEnum) string {
	for _, choice := range set.Values {
		if _, numeric := choice.Value.(float64); !numeric {
			return "string"
		}
	}

	if len(set.Values) == 0 {
		return "string"
	}

	return "int32"
}

// refView is one typed ObjectRef alias.
type refView struct {
	Name      string
	Kind      string
	Target    string
	Endpoint  string
	Generated bool
}

// sharedView is what the two shared files read.
type sharedView struct {
	Header header
	Enums  []enumView
	Refs   []refView
}

// shared collects the enums and references the emitted kinds reached, sorted. Order is by
// name and never by map iteration: a header or a body that reorders per run makes `--check`
// meaningless.
func (b *builder) shared() sharedView {
	out := sharedView{Header: b.header}

	for class, set := range b.enums {
		out.Enums = append(out.Enums, b.enumView(class, set))
	}

	slices.SortFunc(out.Enums, func(a, c enumView) int { return strings.Compare(a.Name, c.Name) })

	for name, target := range b.refs {
		app, model, _ := strings.Cut(target, ".")
		kind, known := b.ir.Kinds[target]

		out.Refs = append(out.Refs, refView{
			Name: name, Kind: kubeKind(model), Target: target,
			Endpoint: kind.Endpoint, Generated: known && app != "contenttypes",
		})
	}

	slices.SortFunc(out.Refs, func(a, c refView) int { return strings.Compare(a.Name, c.Name) })

	return out
}

// enumView is one class as the template reads it.
func (b *builder) enumView(class string, set irEnum) enumView {
	name := b.names.enumTypeName(class)

	out := enumView{
		Name: name, Class: class, Source: set.Source,
		Closed: !set.Extendable, Key: set.Key, Base: enumBase(set),
	}

	seen := map[string]string{}

	for _, choice := range set.Values {
		value := choice.String()

		if constName := b.names.enumConstName(name, value); seen[constName] != "" {
			b.collisions = append(b.collisions, fmt.Errorf(
				"%w: %s spells both %q and %q as %s",
				errEnumCollision, class, seen[constName], value, constName))
		} else {
			seen[constName] = value
		}

		out.Values = append(out.Values, enumValue{
			Const: b.names.enumConstName(name, value),
			Value: value, Label: choice.LabelText(), Quoted: out.Base == "string",
		})

		out.MaxLength = max(out.MaxLength, len(value))
	}

	// An open type is unbounded without a length, and NetBox's own column is 50 or 100
	// characters; twice the longest known member is the honest bound for a set a deployment
	// may extend with a name nobody here has seen.
	out.MaxLength *= 2

	if out.Base != "string" {
		out.MaxLength = 0
	}

	return out
}
