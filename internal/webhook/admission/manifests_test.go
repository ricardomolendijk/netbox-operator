package admission

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	admissionv1 "k8s.io/api/admissionregistration/v1"
	"sigs.k8s.io/yaml"
)

// TestShippedWebhookConfiguration asserts the deployed configuration against the decisions it
// encodes, because every one of them is a property of a YAML field rather than of any Go code:
// a generator change or a hand edit could reverse them all without failing a single other
// test.
//
// The same file the envtest suite installs, so the tests above cannot be passing against a
// webhook scoped differently from the one that ships.
func TestShippedWebhookConfiguration(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "..", "config", "webhook", "manifests.yaml"))
	if err != nil {
		t.Fatalf("reading the webhook configuration: %v", err)
	}

	config := &admissionv1.ValidatingWebhookConfiguration{}
	if err := yaml.Unmarshal(body, config); err != nil {
		t.Fatalf("decoding the webhook configuration: %v", err)
	}

	if len(config.Webhooks) != 1 {
		t.Fatalf("%d webhooks; one path serves the whole API group so that adding a Kind "+
			"changes nothing here", len(config.Webhooks))
	}

	hook := config.Webhooks[0]

	assertFailOpen(t, hook)
	assertScope(t, hook)
}

// assertFailOpen is the failurePolicy decision, and the two settings that make it defensible.
//
// Ignore, because every rule has a reconcile-time backstop that is the authority anyway,
// while Fail on a webhook backed by this operator's own Deployment turns an image-pull
// failure or a stale caBundle into a total write outage for the API group -- including the
// apply that would fix the operator. sideEffects: None is what makes a dry run honest, and the
// API server trusts it. timeoutSeconds caps what a slow review costs an apply.
func assertFailOpen(t *testing.T, hook admissionv1.ValidatingWebhook) {
	t.Helper()

	if hook.FailurePolicy == nil || *hook.FailurePolicy != admissionv1.Ignore {
		t.Errorf("failurePolicy = %v; want Ignore, see docs/operations/admission-webhooks.md",
			hook.FailurePolicy)
	}

	if hook.SideEffects == nil || *hook.SideEffects != admissionv1.SideEffectClassNone {
		t.Errorf("sideEffects = %v; want None", hook.SideEffects)
	}

	if hook.MatchPolicy == nil || *hook.MatchPolicy != admissionv1.Equivalent {
		t.Errorf("matchPolicy = %v; want Equivalent", hook.MatchPolicy)
	}

	if hook.TimeoutSeconds == nil || *hook.TimeoutSeconds != 5 {
		t.Errorf("timeoutSeconds = %v; want 5", hook.TimeoutSeconds)
	}

	if hook.ClientConfig.URL != nil {
		t.Error("clientConfig.url is set; the shipped configuration must reach the Service")
	}
}

// assertScope is the blast radius. `Ignore` is only safe because nothing outside this API
// group depends on this webhook, so the rules must not reach outside it -- and they must not
// reach the status subresource, or every status write a controller makes would pass back
// through admission.
func assertScope(t *testing.T, hook admissionv1.ValidatingWebhook) {
	t.Helper()

	for _, rule := range hook.Rules {
		if !slices.Equal(rule.APIGroups, []string{"netbox.kubeforge.org"}) {
			t.Errorf("apiGroups = %v; the rules must not reach outside this operator's group",
				rule.APIGroups)
		}

		if !slices.Equal(rule.Resources, []string{"*"}) {
			t.Errorf("resources = %v; want [*], so a new Kind is covered with no edit here",
				rule.Resources)
		}

		for _, resource := range rule.Resources {
			if strings.Contains(resource, "/") {
				t.Errorf("resources includes the subresource %q; a status write must not be "+
					"reviewed", resource)
			}
		}

		if !slices.Equal(rule.Operations, []admissionv1.OperationType{
			admissionv1.Create, admissionv1.Update,
		}) {
			t.Errorf("operations = %v; want CREATE and UPDATE. DELETE is another way to make "+
				"an object undeletable, and there is nothing to validate on one",
				rule.Operations)
		}
	}
}
