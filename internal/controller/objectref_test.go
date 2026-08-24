package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// refFixtureGVK is the test-only Kind carrying both reference arities.
var refFixtureGVK = map[string]string{
	"apiVersion": "test.netbox.kubeforge.org/v1alpha1",
	"kind":       "ObjectRefFixture",
}

// TestRefCELArity is the admission half of #185: which reference the API server lets through
// empty, and which it does not.
//
// Both types in one API server and one request shape, because the whole of option B is that
// the loosening is confined to the new type. A test that only proved `optionalRef: {}` is
// admitted would pass just as well on the option this ticket rejected -- relaxing ObjectRef
// itself -- under which `siteRef: {}` and `endpointRef: {}` would become the controller's
// problem instead of `kubectl apply`'s.
func TestRefCELArity(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		name       string
		spec       map[string]any
		wantReject string
	}{
		{
			// The third state, and the reason the type exists: present, selecting nothing,
			// which the engine writes as null rather than dropping.
			name: "an optional reference accepts no mode at all",
			spec: map[string]any{"optionalRef": map[string]any{}},
		},
		{
			name: "an optional reference accepts exactly one mode",
			spec: map[string]any{"optionalRef": map[string]any{"name": "acme"}},
		},
		{
			// Relaxed to `<= 1`, not to "anything": a reference naming two objects names
			// neither, and that is as true of an optional one as of a strict one.
			name:       "an optional reference rejects two modes",
			spec:       map[string]any{"optionalRef": map[string]any{"name": "acme", "slug": "acme"}},
			wantReject: "at most one of name, slug, lookup or id",
		},
		{
			// The four rules that are not about arity are shared verbatim, so an optional
			// reference is no laxer about what a mode it *does* set may contain.
			name:       "an optional reference rejects a namespace without a name",
			spec:       map[string]any{"optionalRef": map[string]any{"slug": "acme", "namespace": "catalogue"}},
			wantReject: "namespace may only be set together with name",
		},
		{
			name: "an optional reference rejects a fuzzy lookup",
			spec: map[string]any{"optionalRef": map[string]any{
				"lookup": map[string]any{"q": "acme"},
			}},
			wantReject: "lookup may not set pagination, formatting or fuzzy-search parameters",
		},
		{
			// The control. ObjectRef is untouched, so the empty form is still refused by the
			// API server -- see TestRequiredRefIsRejectedAtAdmission for the same thing on a
			// shipped Kind.
			name:       "a strict reference rejects no mode at all",
			spec:       map[string]any{"strictRef": map[string]any{}},
			wantReject: "exactly one of name, slug, lookup or id must be set",
		},
		{
			name:       "a strict reference rejects two modes",
			spec:       map[string]any{"strictRef": map[string]any{"name": "acme", "slug": "acme"}},
			wantReject: "exactly one of name, slug, lookup or id must be set",
		},
		{
			name: "a strict reference accepts exactly one mode",
			spec: map[string]any{"strictRef": map[string]any{"name": "acme"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			obj := &unstructured.Unstructured{Object: map[string]any{
				"apiVersion": refFixtureGVK["apiVersion"],
				"kind":       refFixtureGVK["kind"],
				"metadata":   map[string]any{"namespace": ns, "generateName": "ref-"},
				"spec":       tc.spec,
			}}

			assertAdmission(t, obj, tc.wantReject)
		})
	}
}

// TestRequiredRefIsRejectedAtAdmission is #185's other acceptance criterion, on the shipped
// CRDs rather than on a fixture.
//
// A required reference has to keep failing in the API server. The distinction is not academic:
// a manifest the API server rejects fails `kubectl apply`, and one the controller rejects is
// applied successfully and becomes a condition somebody has to go and read -- and in a GitOps
// pipeline, a green sync of an object that will never reconcile.
//
// Two shapes, because the API has two kinds of required reference and only one of them is an
// ObjectRef. `siteRef` on a NetBoxLocation is the one this ticket is about, refused by the CEL
// rule that stays `== 1`. `endpointRef` is a plain string -- #185 describes it as an
// ObjectRef, which it is not -- so it is refused by the structural schema instead, which is
// stricter still and worth pinning so a future change to its type cannot quietly lose that.
func TestRequiredRefIsRejectedAtAdmission(t *testing.T) {
	ns := newNamespace(t)

	location := func(spec map[string]any) *unstructured.Unstructured {
		full := map[string]any{"endpointRef": "homelab", "name": "Row A", "slug": "row-a"}
		for k, v := range spec {
			full[k] = v
		}

		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "netbox.kubeforge.org/v1alpha1",
			"kind":       "NetBoxLocation",
			"metadata":   map[string]any{"namespace": ns, "generateName": "loc-"},
			"spec":       full,
		}}
	}

	for _, tc := range []struct {
		name       string
		spec       map[string]any
		wantReject string
	}{
		{
			name:       "an empty required siteRef is rejected",
			spec:       map[string]any{"siteRef": map[string]any{}},
			wantReject: "exactly one of name, slug, lookup or id must be set",
		},
		{
			name:       "an empty endpointRef is rejected",
			spec:       map[string]any{"siteRef": map[string]any{"name": "ams"}, "endpointRef": map[string]any{}},
			wantReject: "endpointRef",
		},
		{
			// The control, again: without it the two above would pass on a CRD the API
			// server refuses for some unrelated reason.
			name: "a siteRef with one mode is accepted",
			spec: map[string]any{"siteRef": map[string]any{"name": "ams"}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			assertAdmission(t, location(tc.spec), tc.wantReject)
		})
	}
}

// assertAdmission creates obj as a server-side dry run, so admission runs in full and nothing
// is stored, and holds the answer to what the case expects.
func assertAdmission(t *testing.T, obj *unstructured.Unstructured, wantReject string) {
	t.Helper()

	err := apiClient.Create(context.Background(), obj, client.DryRunAll)

	if wantReject == "" {
		if err != nil {
			t.Fatalf("Create was rejected: %v", err)
		}

		return
	}

	if err == nil {
		t.Fatalf("Create was accepted, want a rejection naming %q", wantReject)
	}

	if !strings.Contains(err.Error(), wantReject) {
		t.Errorf("rejection %q does not name %q", err, wantReject)
	}
}

// TestObjectRefFixtureMatchesTheAPIType keeps the fixture honest.
//
// The fixture stands in for rules it does not own, so a fixture that drifted from the Go type
// would prove the API server enforces something the API does not ask for -- which is worse
// than no test, because it reads as coverage.
//
// Compared as strings and counted, both halves needed: every rule on the two Go types has to
// appear in the fixture verbatim, and the fixture has to carry no rule beyond those ten, or it
// could be quietly stricter than the API and the arity cases above would pass for the wrong
// reason.
func TestObjectRefFixtureMatchesTheAPIType(t *testing.T) {
	source, err := os.ReadFile(filepath.Join("..", "..", "api", "v1alpha1", "objectref.go"))
	if err != nil {
		t.Fatalf("reading the reference's Go source: %v", err)
	}

	fixture, err := os.ReadFile(filepath.Join("testdata", "crd", "objectref_fixture.yaml"))
	if err != nil {
		t.Fatalf("reading the fixture CRD: %v", err)
	}

	rules := celRule.FindAllStringSubmatch(string(source), -1)
	if len(rules) != 10 {
		t.Fatalf("objectref.go declares %d CEL rules, want the five on ObjectRef and the five "+
			"on OptionalRef", len(rules))
	}

	for _, rule := range rules {
		if !strings.Contains(string(fixture), rule[1]) {
			t.Errorf("the fixture does not carry this rule from objectref.go:\n  %s", rule[1])
		}
	}

	if got := strings.Count(string(fixture), "- rule:"); got != len(rules) {
		t.Errorf("the fixture carries %d CEL rules and objectref.go %d", got, len(rules))
	}
}
