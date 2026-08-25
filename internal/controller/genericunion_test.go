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
			// The scope union: four members, same nullable shape. An unscoped object is
			// legal, which is why the rule is `<= 1` and not `== 1`.
			name: "scope pair accepts no member at all",
			spec: map[string]any{
				"scopePair":    map[string]any{},
				"requiredPair": map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			},
		},
		{
			name: "scope pair accepts exactly one member",
			spec: map[string]any{
				"scopePair":    map[string]any{"siteRef": map[string]any{"name": "ams"}},
				"requiredPair": map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			},
		},
		{
			// The failure ScopeRef exists to make unrepresentable: two scopes at once, of
			// which the operator would otherwise have to silently pick one.
			name: "scope pair rejects two members",
			spec: map[string]any{
				"scopePair": map[string]any{
					"regionRef": map[string]any{"name": "emea"},
					"siteRef":   map[string]any{"name": "ams"},
				},
				"requiredPair": map[string]any{"deviceRef": map[string]any{"name": "sw1"}},
			},
			wantReject: "at most one of regionRef",
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

// celRuleOnType is celRule keyed by the type the marker block sits on, for the files that
// declare more than one: api/v1alpha1/genericref.go carries IPAssignment, FHRPInterface and
// ServiceParent since NBO-055, so "the only rule in the file" stopped identifying anything.
//
// One rule per type, which is what makes the non-greedy match safe: it consumes up to the
// `type X struct` that follows, so a type carrying *two* rules would hide the second. That is
// checked rather than assumed -- a missing type is a Fatal in the caller.
var celRuleOnType = regexp.MustCompile(`XValidation:rule="([^"]*)"[\s\S]*?\ntype (\w+) struct`)

// TestUnionCELRuleMatchesTheAPIType keeps the fixture honest.
//
// The fixture exists because the union shapes had no CRD to live in, so it is standing in for
// rules it does not own. A fixture that drifted from its type would prove the API server
// enforces something the API does not ask for -- which is worse than no test, because it reads
// as coverage.
//
// Two of the three shapes now have a real carrier as well: NBO-055's `NetBoxService.spec.parent`
// is the `== 1` shape on a three-member union and `NetBoxFHRPGroupAssignment.spec.interface` is
// it on a two-member one, both dry-run through real admission by docs/examples/ipam-remainder.yaml
// (shippedManifests). The fixture rows below stay because `nullablePair` and `scopePair` still
// have none.
//
// One row per union, and the comparison is on the rule *string*: the point is that the bytes
// the API server compiled are the bytes the marker on the Go type carries.
func TestUnionCELRuleMatchesTheAPIType(t *testing.T) {
	for _, tc := range []struct {
		union  string
		source string
		goType string
	}{
		{union: "nullablePair", source: "genericref.go", goType: "IPAssignment"},
		{union: "scopePair", source: "scope.go", goType: "ScopeRef"},
	} {
		t.Run(tc.union, func(t *testing.T) {
			body, err := os.ReadFile(filepath.Join("..", "..", "api", "v1alpha1", tc.source))
			if err != nil {
				t.Fatalf("reading the union's Go source: %v", err)
			}

			rules := map[string]string{}
			for _, match := range celRuleOnType.FindAllStringSubmatch(string(body), -1) {
				rules[match[2]] = match[1]
			}

			want, ok := rules[tc.goType]
			if !ok {
				t.Fatalf("%s declares no CEL rule on %s; it declares them on %v",
					tc.source, tc.goType, rules)
			}

			if got := fixtureRule(t, tc.union); got != want {
				t.Errorf("the fixture's %s rule is\n  %s\nand %s's is\n  %s",
					tc.union, got, tc.goType, want)
			}
		})
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
