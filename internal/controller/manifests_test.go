package controller

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/yaml"
)

// shippedManifests are the manifests this repository promises are applicable:
// config/samples, and every runnable example. docs/examples/README.md states that nothing
// under that directory is aspirational, and this is what holds it to that.
var shippedManifests = []string{
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxendpoint.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxtag.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxrefgrant.yaml"),
	filepath.Join("..", "..", "docs", "examples", "tag.yaml"),
}

// TestShippedManifestsAreAccepted applies every sample and example against the real CRDs.
//
// A server-side dry run, so admission -- schema, enums, patterns, required fields,
// defaulting -- runs in full and nothing is stored: no controller wakes up, no finalizer is
// taken, and no namespace is left terminating. A tightened validation marker that
// invalidates a manifest the README calls runnable fails here rather than in somebody's
// cluster.
func TestShippedManifestsAreAccepted(t *testing.T) {
	ns := newNamespace(t)

	for _, path := range shippedManifests {
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}

		for i, doc := range strings.Split(string(body), "\n---") {
			obj, ok := decodeManifest(t, path, i, doc)
			if !ok {
				continue
			}

			// Same namespace for all of them, so the manifests do not have to agree with
			// the test about which one they live in.
			obj.SetNamespace(ns)

			if err := apiClient.Create(context.Background(), obj, client.DryRunAll); err != nil {
				t.Errorf("%s document %d (%s/%s) was rejected: %v",
					path, i, obj.GetKind(), obj.GetName(), err)
			}
		}
	}
}

// decodeManifest decodes one YAML document, reporting false for a comment-only or empty
// one -- a leading licence banner or a trailing separator is not an object.
func decodeManifest(t *testing.T, path string, index int, doc string) (*unstructured.Unstructured, bool) {
	t.Helper()

	obj := &unstructured.Unstructured{}
	if err := yaml.Unmarshal([]byte(doc), obj); err != nil {
		t.Fatalf("%s document %d is not valid YAML: %v", path, index, err)
	}

	return obj, obj.GetKind() != ""
}

// triStateNote is the sentence an optional field carries when omitting it and emptying it
// are two different instructions. It is the `kubectl explain` half of NBO-079: the
// distinction lives in metadata.managedFields, and the only place a user can read that it
// exists is the field's own description.
const triStateNote = "Omit it to leave NetBox's own value alone"

// TestClearableFieldsDocumentBothStatesInTheSchema is NBO-079's third acceptance criterion:
// absent and explicitly-empty are distinguishable in the CRD schema, so `kubectl explain`
// tells the truth.
//
// Two directions, because either one alone is satisfiable by a lie. Every field carrying the
// note must really be clearable -- optional, undefaulted, and with no validation that
// rejects the empty value -- or the schema promises something the API server refuses. And
// every object kind must carry the note somewhere, so that a kind added tomorrow with an
// optional `description` and no note fails here rather than shipping a field a user cannot
// clear and cannot find out about.
func TestClearableFieldsDocumentBothStatesInTheSchema(t *testing.T) {
	for _, path := range objectCRDs(t) {
		t.Run(filepath.Base(path), func(t *testing.T) {
			spec := crdSpecSchema(t, path)
			required := requiredNames(spec)
			documented := 0

			for name, raw := range spec["properties"].(map[string]any) {
				property, _ := raw.(map[string]any)
				if !strings.Contains(descriptionOf(property), triStateNote) {
					continue
				}
				documented++

				if required[name] {
					t.Errorf("%s is required but documents an empty state it can never be in", name)
				}

				if reason, ok := forbidsEmptyValue(property); ok {
					t.Errorf("%s documents an empty state its own schema rejects: %s", name, reason)
				}
			}

			if documented == 0 {
				t.Errorf("no spec field documents the difference between omitted and emptied; "+
					"every optional field a user can clear needs %q", triStateNote)
			}
		})
	}
}

// forbidsEmptyValue reports whether a property's own validation rejects the empty value,
// with the reason. A default is included: a defaulted field is never absent, so "omit it"
// is not an instruction a user can follow.
func forbidsEmptyValue(property map[string]any) (string, bool) {
	for _, keyword := range []string{"default", "enum", "minLength", "minItems", "minProperties"} {
		if _, present := property[keyword]; present {
			return keyword, true
		}
	}

	pattern, ok := property["pattern"].(string)
	if !ok {
		return "", false
	}

	compiled, err := regexp.Compile(pattern)
	if err != nil {
		// A pattern Go cannot compile is one this test cannot judge. The API server uses
		// ECMA 262, so a rejection here would be about the difference between the dialects
		// rather than about the field.
		return "", false
	}

	if compiled.MatchString("") {
		return "", false
	}

	return "pattern " + pattern + " does not match the empty string", true
}

// objectCRDs are the generated CRDs of the kinds the engine reconciles: the ones with a
// `spec.endpointRef`, which is what makes a kind a NetBox object rather than operator
// configuration.
func objectCRDs(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("globbing the generated CRDs = %v (%d found)", err, len(paths))
	}

	out := make([]string, 0, len(paths))

	for _, path := range paths {
		properties, _ := crdSpecSchema(t, path)["properties"].(map[string]any)
		if _, ok := properties["endpointRef"]; ok {
			out = append(out, path)
		}
	}

	if len(out) == 0 {
		t.Fatal("no object CRD found; this test would pass by describing nothing")
	}

	return out
}

// crdSpecSchema returns the `spec` schema of the served version of one generated CRD.
func crdSpecSchema(t *testing.T, path string) map[string]any {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec map[string]any `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &crd); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}

	if len(crd.Spec.Versions) == 0 {
		t.Fatalf("%s serves no version", path)
	}

	return crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec
}

func requiredNames(spec map[string]any) map[string]bool {
	required, _ := spec["required"].([]any)
	names := make(map[string]bool, len(required))

	for _, name := range required {
		if text, ok := name.(string); ok {
			names[text] = true
		}
	}

	return names
}

func descriptionOf(property map[string]any) string {
	text, _ := property["description"].(string)

	return text
}
