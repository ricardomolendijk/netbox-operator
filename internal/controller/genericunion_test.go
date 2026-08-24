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

// fixtureGVK is the test-only Kind carrying both generic-FK union shapes.
var fixtureGVK = map[string]string{
	"apiVersion": "test.netbox.kubeforge.org/v1alpha1",
	"kind":       "GenericUnionFixture",
}

// TestUnionCELShapes is the admission half of NBO-019: a malformed polymorphic union is
// rejected by the API server, not reported as a condition afterwards.
//
// Both shapes, because both exist in NetBox and the difference is not cosmetic. A nullable
// pair -- ipam.IPAddress's `assigned_object_*` -- has to accept an empty union, or an
// unassigned address becomes illegal. A REQ pair -- ipam.Service's `parent_object_*` -- has to
// reject one, or the operator sends a payload it already knows NetBox will refuse.
func TestUnionCELShapes(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		name       string
		spec       map[string]any
		wantReject string
	}{
		{
			name: "nullable pair accepts no member at all",
			spec: map[string]any{
				"nullablePair": map[string]any{},
				"requiredPair": map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			},
		},
		{
			name: "nullable pair accepts exactly one member",
			spec: map[string]any{
				"nullablePair": map[string]any{"interfaceRef": map[string]any{"name": "eth0"}},
				"requiredPair": map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			},
		},
		{
			name: "nullable pair rejects two members",
			spec: map[string]any{
				"nullablePair": map[string]any{
					"interfaceRef":   map[string]any{"name": "eth0"},
					"vmInterfaceRef": map[string]any{"name": "eth0"},
				},
				"requiredPair": map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			},
			wantReject: "at most one of interfaceRef",
		},
		{
			name:       "required pair rejects no member at all",
			spec:       map[string]any{"requiredPair": map[string]any{}},
			wantReject: "exactly one of deviceRef",
		},
		{
			name: "required pair rejects two members",
			spec: map[string]any{"requiredPair": map[string]any{
				"deviceRef":         map[string]any{"name": "sw1"},
				"virtualMachineRef": map[string]any{"name": "vm1"},
			}},
			wantReject: "exactly one of deviceRef",
		},
		{
			// The union field itself is required, which is the other half of the REQ shape:
			// a rule on an absent field is never evaluated, so the field has to be there for
			// the rule to have anything to say.
			name:       "required pair rejects an absent union",
			spec:       map[string]any{},
			wantReject: "requiredPair",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": fixtureGVK["apiVersion"],
				"kind":       fixtureGVK["kind"],
				"metadata":   map[string]any{"namespace": ns, "generateName": "union-"},
				"spec":       tc.spec,
			}}

			// A server-side dry run, so admission runs in full and nothing is stored.
			err := apiClient.Create(context.Background(), obj, client.DryRunAll)

			if tc.wantReject == "" {
				if err != nil {
					t.Fatalf("Create was rejected: %v", err)
				}

				return
			}

			if err == nil {
				t.Fatalf("Create was accepted, want a rejection naming %q", tc.wantReject)
			}

			if !strings.Contains(err.Error(), tc.wantReject) {
				t.Errorf("rejection %q does not name %q", err, tc.wantReject)
			}
		})
	}
}

// celRule extracts every `XValidation:rule=` from a Go source file.
var celRule = regexp.MustCompile(`XValidation:rule="([^"]*)"`)

// TestUnionCELRuleMatchesTheAPIType keeps the fixture honest.
//
// The fixture exists only because IPAssignment is on no CRD yet, so it is standing in for a
// rule it does not own. A fixture that drifted from the type would prove the API server
// enforces something the API does not ask for -- which is worse than no test, because it reads
// as coverage.
func TestUnionCELRuleMatchesTheAPIType(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "api", "v1alpha1", "genericref.go"))
	if err != nil {
		t.Fatalf("reading the union's Go source: %v", err)
	}

	rules := celRule.FindAllStringSubmatch(string(source), -1)
	if len(rules) != 1 {
		t.Fatalf("genericref.go declares %d CEL rules, want the one on IPAssignment", len(rules))
	}

	if got := fixtureRule(t, "nullablePair"); got != rules[0][1] {
		t.Errorf("the fixture's nullable rule is\n  %s\nand IPAssignment's is\n  %s", got, rules[0][1])
	}
}

// fixtureRule reads one union's CEL rule out of the fixture CRD.
func fixtureRule(t *testing.T, union string) string {
	t.Helper()

	body, err := os.ReadFile(filepath.Join("testdata", "crd", "genericunion_fixture.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture CRD: %v", err)
	}

	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties map[string]struct {
									Validations []struct {
										Rule string `json:"rule"`
									} `json:"x-kubernetes-validations"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(body, &crd); err != nil {
		t.Fatalf("decoding the fixture CRD: %v", err)
	}

	validations := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties[union].Validations
	if len(validations) != 1 {
		t.Fatalf("the fixture's %s declares %d CEL rules, want one", union, len(validations))
	}

	return validations[0].Rule
}
