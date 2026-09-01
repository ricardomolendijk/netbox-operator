package harness

import (
	"os"
	"path/filepath"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// fixtureWithSpec builds a Fixture around one spec, for the tests that exercise the generic
// spec walk rather than a shipped manifest.
func fixtureWithSpec(t *testing.T, namespace string, spec map[string]any) Fixture {
	t.Helper()

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "netbox.kubeforge.org/v1alpha1",
		"kind":       "NetBoxRegion",
		"metadata":   map[string]any{"name": "under-test", "namespace": namespace},
		"spec":       spec,
	}}
	return Fixture{File: "under-test.yaml", Object: obj}
}

func writeFixture(t *testing.T, dir, name, body string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
		t.Fatalf("writing %s: %v", name, err)
	}
}
