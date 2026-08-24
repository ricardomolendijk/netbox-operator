package v1alpha1

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"sigs.k8s.io/yaml"
)

// TestEveryValidatedListIsBounded fails when a list whose items carry CEL rules declares no
// `maxItems`.
//
// The API server costs a CEL rule at the list's *maximum* length, so an unbounded list is
// costed as unbounded and the whole CRD is refused at install:
//
//	spec...items.x-kubernetes-validations[3].rule: Forbidden:
//	  estimated rule cost exceeds budget by factor of 18.1x
//
// `ObjectRef` carries five rules, so this is what happens to every `[]ObjectRef` field that
// forgets `+kubebuilder:validation:MaxItems`. Nothing in the build notices: controller-gen
// emits the schema happily, `kustomize build` renders it and `make verify` passes. Only an
// API server rejects it, which without this test means the first time anybody finds out is
// `kubectl apply -f config/crd`.
//
// The rule is derived from the generated CRDs rather than from a list of field names, so a
// Kind added by somebody who has never read this comment is covered, and so is a new
// reference type with a different rule set (#185).
//
// The condition is "the item subtree contains rules", not "the items are an ObjectRef":
// cost multiplies through every level, so a list of structs that merely *contains* a
// validated field is costed the same way.
func TestEveryValidatedListIsBounded(t *testing.T) {
	paths, err := filepath.Glob(filepath.Join("..", "..", "config", "crd", "bases", "*.yaml"))
	if err != nil || len(paths) == 0 {
		t.Fatalf("globbing the generated CRDs = %v (%d found)", err, len(paths))
	}

	bounded := 0

	for _, path := range paths {
		nodes := schemaNodes(t, path)

		for at, node := range nodes {
			if _, isArray := node["items"].(map[string]any); !isArray {
				continue
			}

			if !validatedBelow(nodes, at) {
				continue
			}

			if _, ok := node["maxItems"]; !ok {
				t.Errorf("%s: %s is a list whose items carry CEL rules and declares no maxItems, "+
					"so the API server will refuse this CRD; add "+
					"+kubebuilder:validation:MaxItems (docs/concepts/references.md, \"A list needs a bound\")",
					filepath.Base(path), at)

				continue
			}

			bounded++
		}
	}

	// Without this the test passes by finding nothing -- a broken walker and a clean
	// codebase are the same green.
	if bounded == 0 {
		t.Error("no bounded list of validated items found in any generated CRD; " +
			"this test just passed by checking nothing")
	}
}

// validatedBelow reports whether any schema at or below `at`'s items carries CEL rules.
func validatedBelow(nodes map[string]map[string]any, at string) bool {
	prefix := at + "[]"

	for other, node := range nodes {
		if other != prefix && !strings.HasPrefix(other, prefix+".") && !strings.HasPrefix(other, prefix+"[") {
			continue
		}

		if _, ok := node["x-kubernetes-validations"]; ok {
			return true
		}
	}

	return false
}

// schemaNodes flattens every schema node of every version of one generated CRD, keyed by a
// path that spells out how it was reached: `.spec.tags[]` is the item schema of the `tags`
// list.
func schemaNodes(t *testing.T, path string) map[string]map[string]any {
	t.Helper()

	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	var crd struct {
		Spec struct {
			Versions []struct {
				Name   string `json:"name"`
				Schema struct {
					OpenAPIV3Schema map[string]any `json:"openAPIV3Schema"`
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

	nodes := make(map[string]map[string]any)
	for _, version := range crd.Spec.Versions {
		collectSchema(nodes, version.Name, version.Schema.OpenAPIV3Schema)
	}

	return nodes
}

func collectSchema(nodes map[string]map[string]any, at string, node map[string]any) {
	nodes[at] = node

	properties, _ := node["properties"].(map[string]any)
	for name, child := range properties {
		collectChildSchema(nodes, at+"."+name, child)
	}

	// The two other ways a schema nests: list items and map values.
	collectChildSchema(nodes, at+"[]", node["items"])
	collectChildSchema(nodes, at+"{}", node["additionalProperties"])
}

func collectChildSchema(nodes map[string]map[string]any, at string, child any) {
	node, ok := child.(map[string]any)
	if !ok {
		return
	}

	collectSchema(nodes, at, node)
}
