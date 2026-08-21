package controller

import (
	"context"
	"os"
	"path/filepath"
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
