package controller

import (
	"context"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

var configContextProfileKind = stubKind{endpoint: "extras/config-context-profiles", key: "name"}

// TestConfigContextProfileIsStampedWithTheTagAndNoCustomFields is the API-versus-AST gap made
// observable.
//
// The digest has extras.ConfigContextProfile as a `PrimaryModel`, which mixes in both
// `TagsMixin` and `CustomFieldsMixin` -- so read from the AST this kind should carry a whole
// provenance stamp. The REST serializer's write path carries `tags` and **no `custom_fields`**,
// and NetBox ignores a column it does not know rather than rejecting it: a `custom_fields` key
// here would be dropped server-side while the operator reported `Ready=True` with a
// status.provenance claiming a stamp that is not there.
//
// So the request body is the assertion, not the descriptor: one key present, the other absent
// (docs/reference/netboxconfigcontextprofile.md, docs/regenerating.md).
func TestConfigContextProfileIsStampedWithTheTagAndNoCustomFields(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, configContextProfileKind)
	stub.withProvenance()

	readyEndpointWith(t, ns, target, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.ManagedBy = managedBy(nil)
	})

	profile := &netboxv1alpha1.NetBoxConfigContextProfile{
		ObjectMeta: metav1.ObjectMeta{Name: "dns-profile", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxConfigContextProfileSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "DNS settings",
			Schema: &apiextensionsv1.JSON{
				Raw: []byte(`{"type":"object","required":["dns"]}`),
			},
		},
	}
	if err := k8sClient.Create(context.Background(), profile); err != nil {
		t.Fatalf("creating the config context profile: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), profile) })

	eventually(t, "Ready=True", func() bool {
		out := &netboxv1alpha1.NetBoxConfigContextProfile{}
		if err := k8sClient.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "dns-profile"}, out); err != nil {
			return false
		}

		for _, condition := range out.Status.Conditions {
			if condition.Type == netboxv1alpha1.ConditionReady {
				return condition.Status == metav1.ConditionTrue
			}
		}

		return false
	})

	created := lastPostPayload(t, stub)

	if _, stamped := created["tags"]; !stamped {
		t.Errorf("the create carries no `tags` key (%#v); `tags` is in this endpoint's write "+
			"path and comes from TagsMixin, so the provenance tag belongs in it", created)
	}

	if _, stamped := created["custom_fields"]; stamped {
		t.Errorf("the create carries a `custom_fields` key (%#v); the REST write path for "+
			"extras/config-context-profiles has none, so netbox would drop it silently",
			created["custom_fields"])
	}

	// `schema` is a JSONField written whole rather than unwrapped.
	if _, ok := created["schema"].(map[string]any); !ok {
		t.Errorf("schema = %#v, want the JSON Schema document itself", created["schema"])
	}
}
