package registry

// Decision #194 kept `allowDuplicate` a per-kind spec field rather than moving it onto the
// shared NetBoxObjectSpec envelope, so nothing in the API server checks that a kind's
// Descriptor.DuplicateSpec is honest. These two tests are that check, moved to build time:
// the ticket's option C would have caught the same mistakes at admission, against the user
// who set the field, and these catch them against the descriptor that lied about it.
//
// Two invariants, one per test:
//
//  1. The name is a boolean the kind's CRD really declares. The engine reads it off the
//     decoded spec by that name (`p.spec[p.desc.DuplicateSpec].(bool)`,
//     internal/reconciler/duplicate.go), and both failures are silent in the same
//     direction: a misspelled name and a non-boolean field each read as `false` forever, so
//     a CR asking for duplicate handling gets the default and reports a Conflict nobody can
//     act on.
//
//  2. The kind is one where duplicates are actually possible. `allowDuplicate` makes the
//     provenance stamp part of the natural key, which on a kind whose identity the database
//     enforces is a change with no upside: NetBox refuses the second row, so the field can
//     only ever turn a clean Conflict into a rejected write.
//
// The second invariant is *not* derivable from the Descriptor. Nothing in it records
// uniqueness -- NaturalKey and KeyField carry the filters to send and nothing about what
// backs them, which is why every kind's own test cites `meta.constraints` in prose instead.
// Rather than add a Descriptor field speculatively, this joins the schema IR that TestCoverage
// already joins, which records both halves of the answer: the `meta.constraints` candidates
// under `natural_keys`, and column-level `unique=True` under each field's `sql`.

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// crdSchemaDir holds the CRDs `make manifests` generates, which is the only place a spec
// field's JSON name and type exist together.
const crdSchemaDir = "../../config/crd/bases"

// TestDuplicateSpecNamesABooleanTheCRDDeclares fails when a kind's DuplicateSpec is not a
// boolean property of that kind's generated CRD.
//
// Read off the CRD rather than by reflecting over the Go struct, because the CRD is what the
// engine sees: it reads a decoded spec keyed by JSON name, so a field whose Go name matches
// and whose `json:` tag does not is exactly as unreachable as one that does not exist.
func TestDuplicateSpecNamesABooleanTheCRDDeclares(t *testing.T) {
	schemas := crdSpecProperties(t)
	exercised := 0

	for _, d := range List() {
		if d.DuplicateSpec == "" {
			continue
		}

		exercised++

		t.Run(d.GVK.Kind, func(t *testing.T) {
			properties, generated := schemas[d.GVK.Kind+"/"+d.GVK.Version]
			if !generated {
				t.Fatalf("no generated CRD for %s %s in %s", d.GVK.Version, d.GVK.Kind, crdSchemaDir)
			}

			property, declared := properties[d.DuplicateSpec]
			if !declared {
				t.Fatalf("DuplicateSpec = %q, which is not a spec property of the generated CRD; "+
					"the engine would read it as false on every object that set it "+
					"(docs/concepts/lookups.md, \"Giving another kind allowDuplicate\")", d.DuplicateSpec)
			}

			if property.Type != "boolean" {
				t.Errorf("spec.%s is %q in the generated CRD, want boolean: the engine type-asserts it to bool "+
					"and a failed assertion is indistinguishable from an unset field", d.DuplicateSpec, property.Type)
			}

			// In the field map it would also be sent to NetBox, which ignores a column it does
			// not know rather than rejecting it -- so the field would silently vanish on the wire
			// instead of failing (internal/reconciler/payload.go).
			if _, mapped := d.FieldFor(d.DuplicateSpec); mapped {
				t.Errorf("%q is in the field map, so it configures the operator and describes a NetBox column both",
					d.DuplicateSpec)
			}
		})
	}

	// Without this the loop passes on a registry where nothing declares the field, which is
	// the state the invariant is useless in and the one a bad rebase produces.
	if exercised == 0 {
		t.Fatal("no registered descriptor declares DuplicateSpec, so neither invariant was exercised")
	}
}

// TestDuplicateSpecOnlyOnAKindTheDatabaseDoesNotKeepUnique fails when a kind declaring
// DuplicateSpec has a natural key the database enforces.
//
// That is what "duplicates are possible here" means, and it is a property of NetBox rather
// than of this operator: `ipam.IPAddress` has no `meta.constraints` at all and no UNIQUE
// column, so two rows holding one address is legal server state and `allowDuplicate` is the
// only way a manifest can say which of them is its own. On `extras.Tag` or `dcim.Site`, whose
// `slug` is `unique=True`, the same field would promise something NetBox refuses.
func TestDuplicateSpecOnlyOnAKindTheDatabaseDoesNotKeepUnique(t *testing.T) {
	ir := loadIR(t)

	models := map[string]string{}
	for model, kind := range ir.Kinds {
		models[kind.ObjectType] = model
	}

	declaring, enforced := 0, 0

	for _, d := range List() {
		model, known := models[d.ObjectType]
		if !known {
			continue // A registry that disagrees with the schema is TestCoverage's failure, not this one's.
		}

		backed, unanswered := keyUniqueness(ir.Kinds[model], d)

		if d.DuplicateSpec == "" {
			if len(backed) > 0 {
				enforced++
			}

			continue
		}

		declaring++

		t.Run(d.GVK.Kind, func(t *testing.T) {
			if len(backed) > 0 {
				t.Errorf("declares DuplicateSpec %q, but %s's identity is enforced by %s: NetBox refuses the "+
					"second row, so the field can only turn a Conflict into a rejected write "+
					"(docs/concepts/lookups.md, \"Giving another kind allowDuplicate\")",
					d.DuplicateSpec, model, strings.Join(backed, ", "))
			}

			// An unanswerable column is a pass this test has not earned: the IR carries no entry
			// for a column a base class outside the digest declares, and "no unique flag found"
			// then means "not looked at" rather than "not unique".
			if len(unanswered) > 0 {
				t.Errorf("declares DuplicateSpec %q, and %s carries no schema entry for %s, so whether the "+
					"database keeps the key unique cannot be read from %s",
					d.DuplicateSpec, model, strings.Join(unanswered, ", "), irPath)
			}
		})
	}

	if declaring == 0 {
		t.Fatal("no registered descriptor declares DuplicateSpec, so the invariant was not exercised")
	}

	// The mutation guard. Every check above is a *negative* -- "no constraint backs this key"
	// -- so a keyUniqueness that found nothing anywhere would pass it while catching nothing.
	// Several shipped kinds are keyed on a UNIQUE column (`dcim.Site.slug`, `ipam.VRF.rd`), so
	// finding none of them means the join, not the registry, is broken.
	if enforced == 0 {
		t.Fatalf("keyUniqueness found no kind whose natural key %s enforces, so it would pass a kind that "+
			"declared DuplicateSpec on a UNIQUE column", irPath)
	}
}

// keyUniqueness reports what the schema IR says about a kind's identity: the constraints that
// back the natural key, and the columns the IR cannot answer for.
//
// Two sources, because NetBox spells uniqueness two ways and the IR keeps them apart:
// `natural_keys` is built from `meta.constraints` alone, so a model whose identity is one
// UNIQUE column has an empty list and the flag on the column is the only evidence there is.
//
// Matched columns only. A null pin asserts the column holds nothing, and Postgres permits any
// number of NULLs in a UNIQUE column, so a pinned column says nothing about whether the
// identity it is part of is enforced.
func keyUniqueness(kind irKind, d Descriptor) (backed, unanswered []string) {
	for _, key := range kind.NaturalKeys {
		backed = append(backed, "meta.constraints "+key.Constraint)
	}

	for _, column := range keyColumns(d) {
		at := slices.IndexFunc(kind.Fields, func(f irField) bool { return f.Name == column })
		if at < 0 {
			unanswered = append(unanswered, column)

			continue
		}

		if kind.Fields[at].SQL.Unique {
			backed = append(backed, "column UNIQUE on "+column)
		}
	}

	return backed, unanswered
}

// keyColumns are the NetBox columns this descriptor's natural keys match on, first-seen order.
func keyColumns(d Descriptor) []string {
	var out []string

	for _, key := range d.NaturalKeys {
		for _, field := range key.Fields {
			column := keyColumn(d, field)
			if !slices.Contains(out, column) {
				out = append(out, column)
			}
		}
	}

	return out
}

// keyColumn is the NetBox column behind one key field, taken from the field map because that
// is the one bridge between the two vocabularies -- `vrfRef` is filtered as `vrf_id` and
// written as `vrf`, and neither name is derivable from the other.
func keyColumn(d Descriptor, field KeyField) string {
	if mapped, ok := d.FieldFor(field.Spec); ok {
		return mapped.API
	}

	// The halves of a generic FK are not in the field map -- one spec field writes both
	// columns -- so the filter is the column, less the `_id` a foreign key is filtered by.
	return strings.TrimSuffix(field.Filter, "_id")
}

// crdSpecProperty is the part of a generated CRD's schema these tests read.
type crdSpecProperty struct {
	Type string `json:"type"`
}

// crdSchemaFile is one file under config/crd/bases, decoded down to its top-level spec
// properties.
type crdSchemaFile struct {
	Spec struct {
		Names struct {
			Kind string `json:"kind"`
		} `json:"names"`
		Versions []struct {
			Name   string `json:"name"`
			Schema struct {
				OpenAPIV3Schema struct {
					Properties struct {
						Spec struct {
							Properties map[string]crdSpecProperty `json:"properties"`
						} `json:"spec"`
					} `json:"properties"`
				} `json:"openAPIV3Schema"`
			} `json:"schema"`
		} `json:"versions"`
	} `json:"spec"`
}

// crdSpecProperties are the top-level spec properties of every generated CRD, keyed
// `Kind/version` so a Descriptor.GVK finds its own schema and nothing else's.
func crdSpecProperties(t *testing.T) map[string]map[string]crdSpecProperty {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join(crdSchemaDir, "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("globbing the generated CRDs = %v (%d found); run `make manifests`", err, len(paths))
	}

	out := map[string]map[string]crdSpecProperty{}

	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		crd := crdSchemaFile{}
		if err := yaml.Unmarshal(body, &crd); err != nil {
			t.Fatalf("decoding %s: %v", path, err)
		}

		for _, version := range crd.Spec.Versions {
			out[crd.Spec.Names.Kind+"/"+version.Name] = version.Schema.OpenAPIV3Schema.Properties.Spec.Properties
		}
	}

	return out
}
