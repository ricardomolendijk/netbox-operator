package controller

import (
	"context"
	"net/http"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// configContextKind points the shared stub at extras.ConfigContext, whose natural key is
// `name` rather than the `slug` most kinds use.
var configContextKind = stubKind{endpoint: "extras/config-contexts", key: "name"}

// makeConfigContext applies a NetBoxConfigContext with no assignment set populated.
//
// No references by default, and that is the honest default for this harness rather than a
// shortcut: the stub serves one endpoint, so a populated set would be a test of how it 404s
// somebody else's. That the thirteen sets are thirteen to-many references is asserted as data in
// internal/registry, and the machinery that resolves them is the machinery every kind uses.
func makeConfigContext(t *testing.T, ns, name string,
	mutate func(*netboxv1alpha1.NetBoxConfigContext),
) {
	t.Helper()

	object := &netboxv1alpha1.NetBoxConfigContext{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxConfigContextSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			Data:             apiextensionsv1.JSON{Raw: []byte(`{"dns":{"servers":["10.0.0.53"]}}`)},
		},
	}
	if mutate != nil {
		mutate(object)
	}

	if err := k8sClient.Create(context.Background(), object); err != nil {
		t.Fatalf("creating config context %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), object) })
}

func fetchConfigContext(ns, name string) *netboxv1alpha1.NetBoxConfigContext {
	out := &netboxv1alpha1.NetBoxConfigContext{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, out); err != nil {
		return nil
	}

	return out
}

func configContextCondition(ns, name, condType string) metav1.Condition {
	object := fetchConfigContext(ns, name)
	if object == nil {
		return metav1.Condition{}
	}

	for _, condition := range object.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// TestConfigContextCarriesNoProvenanceStamp is the one thing about this kind that is dangerous
// to get wrong, asserted where it is observable: in the request body.
//
// `extras.ConfigContext` mixes in neither `TagsMixin` nor `CustomFieldsMixin`, so neither half
// of the stamp has a column to go in -- and `tags` *does* exist on the model, as a plain
// many-to-many selecting **which tagged objects the context applies to**. A stamp written there
// would silently change which objects in NetBox receive the configuration. The endpoint here has
// `spec.managedBy` switched on deliberately: with provenance off the assertion would pass for
// the wrong reason (docs/reference/netboxconfigcontext.md).
func TestConfigContextCarriesNoProvenanceStamp(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, configContextKind)
	stub.withProvenance()

	readyEndpointWith(t, ns, target, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.ManagedBy = managedBy(nil)
	})

	makeConfigContext(t, ns, "eu-dns", nil)

	eventually(t, "Ready=True", func() bool {
		ready := configContextCondition(ns, "eu-dns", netboxv1alpha1.ConditionReady)

		return ready.Status == metav1.ConditionTrue
	})

	created := lastPostPayload(t, stub)

	if _, stamped := created["tags"]; stamped {
		t.Errorf("the create carries a `tags` key (%#v); on extras.ConfigContext that column "+
			"selects which tagged objects the context applies to, not this object's own tags",
			created["tags"])
	}

	if _, stamped := created["custom_fields"]; stamped {
		t.Error("the create carries a `custom_fields` key; extras.ConfigContext mixes in no " +
			"CustomFieldsMixin, so netbox would ignore it and the operator would report a stamp " +
			"that is not there")
	}

	// `data` is a JSONField written as a whole document. Anything but a nested object here is
	// the ClassJSON declaration having been lost, which is a PATCH loop rather than an error.
	data, ok := created["data"].(map[string]any)
	if !ok {
		t.Fatalf("data = %#v, want the JSON document itself", created["data"])
	}

	if _, ok := data["dns"]; !ok {
		t.Errorf("data = %#v, want the `dns` key the spec declared", data)
	}

	object := fetchConfigContext(ns, "eu-dns")
	if object == nil || object.Status.ID == 0 {
		t.Fatalf("status.id is unset on a Ready object: %#v", object)
	}

	// The other half of "no stamp": status.provenance has nothing to report, which is the state
	// NetBoxSweep and multi-writer detection are both blind to.
	if object.Status.Provenance != nil && object.Status.Provenance.Tag != "" {
		t.Errorf("status.provenance records the tag %q on a kind whose tags column is a selector",
			object.Status.Provenance.Tag)
	}
}

// TestConfigContextWithholdsTheWriteWhileASetIsUnresolved is the rule that makes a
// full-replacement set safe, on the kind that has thirteen of them.
//
// Writing the members that did resolve would be a full-list replacement with a shorter list --
// a deletion, reported as a success. So an unresolved element means **no request at all**, and
// the assertion that matters is the absence of a POST rather than the condition next to it.
func TestConfigContextWithholdsTheWriteWhileASetIsUnresolved(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, configContextKind)
	readyEndpoint(t, ns, target)

	makeConfigContext(t, ns, "us-dns", func(c *netboxv1alpha1.NetBoxConfigContext) {
		// A NetBoxSite CR that does not exist. `name` mode resolves against the cluster, so
		// this never reaches NetBox at all.
		c.Spec.SiteRefs = []netboxv1alpha1.SiteRef{{Name: "nowhere"}}
	})

	eventually(t, "RefsResolved=False", func() bool {
		refs := configContextCondition(ns, "us-dns", netboxv1alpha1.ConditionRefsResolved)

		return refs.Status == metav1.ConditionFalse
	})

	for _, write := range stub.recorded() {
		if write.Method == http.MethodPost {
			t.Errorf("a config context with an unresolved member of `sites` was created anyway "+
				"(%#v); a partially resolved set written as a full replacement is a deletion "+
				"reported as a success", write.Payload)
		}
	}

	object := fetchConfigContext(ns, "us-dns")
	if object == nil || object.Status.ID != 0 {
		t.Fatalf("status.id = %v, want 0: nothing was written", object)
	}
}

// lastPostPayload is the body of the most recent POST the stub saw.
func lastPostPayload(t *testing.T, stub *netboxStubServer) netbox.Object {
	t.Helper()

	writes := stub.recorded()
	for i := len(writes) - 1; i >= 0; i-- {
		if writes[i].Method == http.MethodPost {
			return writes[i].Payload
		}
	}

	t.Fatal("the stub saw no POST at all")

	return nil
}
