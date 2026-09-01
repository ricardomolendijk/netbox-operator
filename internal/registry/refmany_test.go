package registry

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/yaml"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// The representation check NBO-020 asks for, and it asserts the opposite of what NBO-020's
// spec proposed.
//
// The ticket's first acceptance criterion is that a to-many reference is a **pointer to a
// slice**, `ImportTargets *[]RouteTargetRef`, so that absent and `[]` are distinguishable in
// Go. That criterion is superseded. The engine tells absent from empty by reading
// `metadata.managedFields` -- the API server's own per-field record of who set what -- rather
// than by looking at the Go value, so the Go type is irrelevant to the distinction
// ([#121](docs/concepts/field-ownership.md), "Why not pointer types"). A pointer to a slice
// would not merely be redundant, it would be two mechanisms for one fact:
//
//   - It fights `omitempty`. A non-nil pointer to an empty slice marshals as `[]` and a nil
//     one vanishes, so the *encoded* spec would carry the empty list while every other
//     optional field on every kind is still restored from ownership metadata. Two rules for
//     one question, and the one that fires depends on the field's Go type.
//   - Taking `omitempty` off instead inverts the bug rather than fixing it: a typed Go client
//     -- the operator itself, materialising an inline child (ADR-0005 §2) -- would then
//     marshal every unset field as its empty value and *claim* it, so adopting a pre-existing
//     NetBox object would wipe every value the user had not restated (CONTRIBUTING.md,
//     "Optional spec fields have three states, not two").
//
// So the check the ticket wanted is still worth having, pointed the other way: a to-many
// reference must be a plain slice that keeps `omitempty`, and the CRD must accept a list for
// it. Both halves fail silently otherwise -- a pointer field is invisible to
// reconciler.restoreEmpty (emptyValueOf answers only slices and maps), which makes the empty
// state unreachable and the last element of a relation unremovable, and nothing else in the
// build objects.

// crdVersion is the one API version in the catalogue. The schema lookup below matches on it
// rather than taking `versions[0]`, so a second version added later cannot silently move the
// assertion onto the wrong schema.
const crdVersion = "v1alpha1"

// errNoSpecField is a Kind whose Go type has no `spec` field. Unreachable through the scheme,
// and a failure rather than a skip: a Kind the reflection cannot reach is one every assertion
// below would pass by describing nothing.
var errNoSpecField = errors.New("the Go type has no spec field")

// TestEveryToManyRefFieldIsAPlainSliceWithOmitempty walks the registry, not a list of field
// names, because the point is to cover a Kind added by somebody who has never read this file.
func TestEveryToManyRefFieldIsAPlainSliceWithOmitempty(t *testing.T) {
	descriptors := List()
	if len(descriptors) == 0 {
		t.Fatal("the registry is empty; this test would pass by describing nothing")
	}

	scheme := runtime.NewScheme()
	if err := netboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() = %v", err)
	}

	checked := 0

	for _, d := range descriptors {
		fields := toManyFields(d)
		if len(fields) == 0 {
			continue
		}

		t.Run(d.GVK.Kind, func(t *testing.T) {
			spec, err := specStructOf(scheme, d.GVK)
			if err != nil {
				t.Fatalf("reaching the spec struct of %s: %v", d.GVK.Kind, err)
			}

			for _, field := range fields {
				assertPlainSlice(t, d.GVK.Kind, field, spec)
			}
		})

		checked += len(fields)
	}

	// A guard on the walk itself. Every assertion above is inside a loop over the fields it
	// found, so a lookup that quietly stopped finding any -- a renamed class, a registry that
	// did not initialise -- would leave this test green while checking nothing.
	if checked == 0 {
		t.Error("no registered Kind declares a to-many reference; the walk found nothing to check")
	}
}

// TestFieldClassAgreesWithTheGeneratedCRD is the other direction: the descriptor's cardinality
// and the schema a user's manifest is validated against have to be the same fact.
//
// They are two hand-written declarations of one thing -- a class in internal/registry and a Go
// type in api/v1alpha1 -- and disagreement between them is refused at runtime rather than at
// build time (resolver.refsIn, TestResolveRefusesAShapeItsClassDoesNotDeclare). Refusing it at
// runtime means the object reports `Ready=False` with a decoding error and the manifest that
// caused it is admissible; catching it here means it never ships.
func TestFieldClassAgreesWithTheGeneratedCRD(t *testing.T) {
	schemas := crdSchemas(t)

	for _, d := range List() {
		properties, ok := schemas[d.GVK.Kind]
		if !ok {
			t.Errorf("%s is registered and has no generated CRD; run `make manifests`", d.GVK.Kind)

			continue
		}

		for _, field := range d.Fields {
			if !field.Class.Ref() {
				continue
			}

			assertSchemaCardinality(t, d.GVK.Kind, field, properties)
		}
	}
}

// toManyFields is the to-many references one descriptor declares.
func toManyFields(d Descriptor) []Field {
	out := make([]Field, 0, len(d.Fields))

	for _, field := range d.Fields {
		if field.Class.ToMany() {
			out = append(out, field)
		}
	}

	return out
}

// assertPlainSlice is the whole of the superseded criterion, restated.
func assertPlainSlice(t *testing.T, kind string, field Field, spec reflect.Type) {
	t.Helper()

	goField, tag, ok := goFieldFor(spec, field.Spec)
	if !ok {
		t.Errorf("%s.%s is declared %s and has no spec field with that JSON name",
			kind, field.Spec, ClassRefMany)

		return
	}

	if goField.Type.Kind() == reflect.Pointer {
		t.Errorf("%s.%s is a pointer (%s). A to-many reference is a plain slice: the three "+
			"states come from metadata.managedFields, not from the Go value, and a pointer "+
			"here is a second mechanism for one fact that reconciler.restoreEmpty cannot see "+
			"(docs/concepts/field-ownership.md)", kind, field.Spec, goField.Type)

		return
	}

	if goField.Type.Kind() != reflect.Slice {
		t.Errorf("%s.%s is declared %s and is a %s; the resolver reads it as a JSON list",
			kind, field.Spec, ClassRefMany, goField.Type.Kind())

		return
	}

	if !strings.Contains(tag, ",omitempty") {
		t.Errorf("%s.%s has no omitempty. Taking it off makes a typed Go client marshal the "+
			"unset field as `[]` and thereby claim it, so adopting a pre-existing NetBox "+
			"object would clear a relation nobody mentioned (CONTRIBUTING.md, \"Optional spec "+
			"fields have three states, not two\")", kind, field.Spec)
	}
}

// assertSchemaCardinality checks one reference field's generated schema against its class: an
// array for a to-many, an object for a to-one.
func assertSchemaCardinality(t *testing.T, kind string, field Field, properties map[string]any) {
	t.Helper()

	property, declared := properties[field.Spec].(map[string]any)
	if !declared {
		// Not a failure on its own. A descriptor may map a spec field that lives on an
		// embedded struct the walk below does not flatten, and the Go-type assertion above is
		// the load-bearing one for a to-many.
		return
	}

	want := "object"
	if field.Class.ToMany() {
		want = "array"
	}

	if got, _ := property["type"].(string); got != want {
		t.Errorf("%s.%s is declared %s and its CRD schema is type %q, want %q: the descriptor "+
			"and the CRD disagree about how many objects the field holds",
			kind, field.Spec, field.Class, got, want)

		return
	}

	// A to-many reference with no bound is a CRD the API server refuses to install, whole
	// (docs/concepts/references.md, "A list needs a bound"). TestEveryValidatedListIsBounded
	// catches it from the CRD side; this catches it from the class side, which is the side a
	// new Kind is written on.
	if field.Class.ToMany() {
		if _, bounded := property["maxItems"]; !bounded {
			t.Errorf("%s.%s is a to-many reference with no maxItems; add "+
				"+kubebuilder:validation:MaxItems=256", kind, field.Spec)
		}
	}
}

// specStructOf reaches the Go type behind one Kind's `spec` field, by JSON name rather than by
// position -- the same way reconciler.specStructType does, because a divergence between the
// two would make this test agree with something the engine does not do.
func specStructOf(scheme *runtime.Scheme, gvk schema.GroupVersionKind) (reflect.Type, error) {
	built, err := scheme.New(gvk)
	if err != nil {
		return nil, err
	}

	t := reflect.TypeOf(built)
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}

	for i := range t.NumField() {
		name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
		if name == "spec" {
			return t.Field(i).Type, nil
		}
	}

	return nil, errNoSpecField
}

// goFieldFor finds a spec field by its JSON name, descending into the inlined envelope every
// kind embeds.
func goFieldFor(t reflect.Type, jsonName string) (reflect.StructField, string, bool) {
	for i := range t.NumField() {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		name, _, _ := strings.Cut(tag, ",")

		if name == "" && field.Anonymous && field.Type.Kind() == reflect.Struct {
			if found, foundTag, ok := goFieldFor(field.Type, jsonName); ok {
				return found, foundTag, true
			}

			continue
		}

		if name == jsonName {
			return field, tag, true
		}
	}

	return reflect.StructField{}, "", false
}

// crdSchemas reads `spec.properties` out of every generated CRD, keyed by Kind.
func crdSchemas(t *testing.T) map[string]map[string]any {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("globbing the generated CRDs = %v (%d found)", err, len(paths))
	}

	out := make(map[string]map[string]any, len(paths))

	for _, path := range paths {
		raw, err := os.ReadFile(path) //nolint:gosec // a generated file under config/
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		var crd struct {
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
									Properties map[string]any `json:"properties"`
								} `json:"spec"`
							} `json:"properties"`
						} `json:"openAPIV3Schema"`
					} `json:"schema"`
				} `json:"versions"`
			} `json:"spec"`
		}

		if err := yaml.Unmarshal(raw, &crd); err != nil {
			t.Fatalf("parsing %s: %v", path, err)
		}

		for _, version := range crd.Spec.Versions {
			if version.Name != crdVersion {
				continue
			}

			out[crd.Spec.Names.Kind] = version.Schema.OpenAPIV3Schema.Properties.Spec.Properties
		}
	}

	return out
}
