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
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxipaddressclaim.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxiprange.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxprefixclaim.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxiprangeclaim.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxcustomfield.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxcustomfieldchoiceset.yaml"),
	filepath.Join("..", "..", "config", "samples", "netbox_v1alpha1_netboxsavedfilter.yaml"),
	filepath.Join("..", "..", "docs", "examples", "tag.yaml"),
	filepath.Join("..", "..", "docs", "examples", "contacts.yaml"),
	filepath.Join("..", "..", "docs", "examples", "extras.yaml"),
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

// noClearableFields are the kinds that legitimately carry no clearable field, by filename.
//
// A hand-written list rather than a rule, deliberately: adding a kind with an optional
// `description` and no tri-state note has to fail the test, so the only way out is writing
// down here why this kind is different. Same reason stampedObjectTypes is a literal.
//
// A claim's spec is three fields, and none of them is a pass-through to NetBox: two are
// required and the third has two states rather than three, because absent and empty both mean
// "derive the allocation identity" and nothing in NetBox is cleared by either
// (docs/reference/netboxipaddressclaim.md). The fields a NetBox address actually has --
// dnsName, role, description -- live on NetBoxIPAddress, which is the kind that can maintain
// them (NBO-025).
// NBO-064's two claim kinds are the same argument with two more fields. A prefix claim's
// `prefixLength` and a range claim's `size` are required, its `alignment` is defaulted and
// changes nothing in NetBox, and its two `mark*` booleans are tri-state through a pointer
// rather than through an empty string -- there is no empty value for a boolean to document.
// The fields a NetBox range actually has live on NetBoxIPRange, which does carry the note.
//
// A contact assignment is a join object: `objectRef`, `contactRef` and `roleRef` are the whole
// of its identity and all three are required, so the only optional field it has is `priority`
// -- and that one is an enum. `""` is a member of the enum, because NetBox's column is
// blank-able, so the field genuinely is clearable; what it cannot carry is the note, since
// forbidsEmptyValue treats any `enum` as validation that rejects the empty value. The
// statement lives on the ContactPriority type instead
// (api/v1alpha1/tenancy_contactassignment.go, docs/reference/netboxcontactassignment.md).
var noClearableFields = map[string]bool{
	"netbox.kubeforge.org_netboxipaddressclaims.yaml":    true,
	"netbox.kubeforge.org_netboxprefixclaims.yaml":       true,
	"netbox.kubeforge.org_netboxiprangeclaims.yaml":      true,
	"netbox.kubeforge.org_netboxcontactassignments.yaml": true,
}

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

			if documented == 0 && !noClearableFields[filepath.Base(path)] {
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

// objectCRDs are the generated CRDs of the kinds the engine reconciles: the ones whose spec
// embeds NetBoxObjectSpec, which is what makes a kind a NetBox object rather than operator
// configuration.
//
// Keyed on `onConflict` rather than on `endpointRef`. An endpoint reference alone is not the
// distinction: NetBoxSweep names one and reconciles nothing (NBO-046), so it has no field a
// user clears and no NetBox column to leave alone -- and every object kind gets `onConflict`
// from the shared envelope, so the marker cannot be forgotten by a kind that does reconcile.
func objectCRDs(t *testing.T) []string {
	t.Helper()

	paths, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("globbing the generated CRDs = %v (%d found)", err, len(paths))
	}

	out := make([]string, 0, len(paths))

	for _, path := range paths {
		properties, _ := crdSpecSchema(t, path)["properties"].(map[string]any)
		if _, ok := properties["onConflict"]; ok {
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
