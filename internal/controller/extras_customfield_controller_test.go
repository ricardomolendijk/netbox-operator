package controller

import (
	"context"
	"net/http"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// customFieldKind points the shared stub at extras.CustomField.
//
// Note what this does to the stub's provenance store: routeExtras never claims the endpoint of
// the kind under test, so the bootstrap's own reads and writes land in the *kind's* store here
// and show up in stub.recorded() rather than recordedExtras(). That is the right shape for
// these tests -- the whole question is which writer wrote to `extras/custom-fields`, so having
// both of them recorded in one list is how "the CR added nothing" gets asserted.
var customFieldKind = stubKind{endpoint: "extras/custom-fields", key: "name"}

// stampingCustomFieldEndpoint is a ready endpoint with provenance switched on, so the
// bootstrap creates the four definitions and the reservation has something to reserve.
func stampingCustomFieldEndpoint(t *testing.T, ns, target string, stub *netboxStubServer) {
	t.Helper()

	// The tag half of the bootstrap needs `extras/tags` to answer. The custom-field half is
	// served by the kind's own store, because routeExtras never claims the endpoint under test.
	stub.withProvenance()

	readyEndpointWith(t, ns, target, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.ManagedBy = managedBy(nil)
	})
}

// makeCustomField applies a NetBoxCustomField whose CR name is its NetBox name.
func makeCustomField(t *testing.T, ns, name string,
	mutate func(*netboxv1alpha1.NetBoxCustomField),
) *netboxv1alpha1.NetBoxCustomField {
	t.Helper()

	// A DNS-safe CR name: a NetBox custom field is `k8s_uid` and a Kubernetes object cannot
	// be. The two names are independent, and this is the kind where that is most visible --
	// the natural key is spec.name, never metadata.name.
	field := &netboxv1alpha1.NetBoxCustomField{
		ObjectMeta: metav1.ObjectMeta{Name: strings.ReplaceAll(name, "_", "-"), Namespace: ns},
		Spec: netboxv1alpha1.NetBoxCustomFieldSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			ObjectTypes:      []string{"dcim.site"},
		},
	}
	if mutate != nil {
		mutate(field)
	}

	if err := k8sClient.Create(context.Background(), field); err != nil {
		t.Fatalf("creating custom field %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { removeCustomField(field) })

	return field
}

// removeCustomField deletes a custom field and lets its finalizer come off.
//
// It sets the data-loss annotation first, because without it the finalizer never releases and
// every test using this helper would leave a terminating namespace behind. That is the guard
// working, and a cleanup is not the place to prove it -- TestCustomFieldRefusesADeleteThatLosesData
// is.
func removeCustomField(field *netboxv1alpha1.NetBoxCustomField) {
	ctx := context.Background()
	key := client.ObjectKeyFromObject(field)

	current := &netboxv1alpha1.NetBoxCustomField{}
	if err := k8sClient.Get(ctx, key, current); err != nil {
		return
	}

	if current.Annotations == nil {
		current.Annotations = map[string]string{}
	}
	current.Annotations[netboxv1alpha1.AllowDataLossAnnotation] = "true"
	_ = k8sClient.Update(ctx, current)
	_ = k8sClient.Delete(ctx, current)
}

func fetchCustomField(ns, name string) *netboxv1alpha1.NetBoxCustomField {
	out := &netboxv1alpha1.NetBoxCustomField{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, out); err != nil {
		return nil
	}

	return out
}

func customFieldCondition(ns, name, condType string) metav1.Condition {
	field := fetchCustomField(ns, name)
	if field == nil {
		return metav1.Condition{}
	}

	for _, condition := range field.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// customFieldPosts counts POSTs the stub saw to extras/custom-fields, which is both writers'
// only way in.
func customFieldPosts(stub *netboxStubServer) int {
	posts := 0

	for _, write := range stub.recorded() {
		if write.Method == http.MethodPost {
			posts++
		}
	}

	return posts
}

// TestCustomFieldForAProvenanceDefinitionIsRefused is the answer to NBO-059's collision, proved
// end to end.
//
// `k8s_uid` is created by the endpoint's own bootstrap before that endpoint reports Ready, and
// every stamped object in the cluster depends on it -- its `object_types` is derived from the
// descriptor registry and widened on every upgrade, and narrowing it deletes that field's value
// from every object of the types removed. A CR for it is therefore refused outright rather than
// merged or adopted, and the assertion that matters is the *absence* of a write: the four the
// bootstrap made, and not a fifth.
func TestCustomFieldForAProvenanceDefinitionIsRefused(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, customFieldKind)
	stampingCustomFieldEndpoint(t, ns, target, stub)

	// Everything the bootstrap was going to write is written by the time the endpoint is
	// Ready, so this is the baseline the CR must not add to.
	bootstrapped := customFieldPosts(stub)
	if bootstrapped == 0 {
		t.Fatal("the bootstrap created no custom fields, so there is no collision to refuse")
	}

	makeCustomField(t, ns, provenance.DefaultUIDField,
		func(field *netboxv1alpha1.NetBoxCustomField) {
			// A narrower object_types than the bootstrap derives, which is the edit that would
			// destroy data if the CR were allowed to win.
			field.Spec.ObjectTypes = []string{"dcim.site"}
		})

	crName := strings.ReplaceAll(provenance.DefaultUIDField, "_", "-")

	eventually(t, "Ready=False, Reason=ReservedByOperator", func() bool {
		ready := customFieldCondition(ns, crName, netboxv1alpha1.ConditionReady)

		return ready.Status == metav1.ConditionFalse &&
			ready.Reason == netboxv1alpha1.ReasonReservedByOperator
	})

	field := fetchCustomField(ns, crName)
	if field == nil {
		t.Fatal("the refused custom field is gone")
	}

	// Zero, and this is the load-bearing assertion. A non-zero id would mean the engine had
	// located the bootstrap's definition and taken it over, which is the outcome the whole
	// mechanism exists to prevent -- from that point on the CR's static objectTypes would be
	// PATCHed over the derived one on every resync.
	if field.Status.ID != 0 {
		t.Errorf("status.id = %d on a refused object; the operator has adopted its own definition",
			field.Status.ID)
	}

	if got := customFieldPosts(stub); got != bootstrapped {
		t.Errorf("netbox saw %d POSTs to extras/custom-fields, want the bootstrap's %d and no more",
			got, bootstrapped)
	}

	// The message has to name the field and point at the thing that reserved it, because the
	// two fixes are on different objects: rename the CR, or change the endpoint's
	// spec.managedBy.
	message := customFieldCondition(ns, crName, netboxv1alpha1.ConditionReady).Message
	for _, want := range []string{provenance.DefaultUIDField, "spec.managedBy", "homelab"} {
		if !strings.Contains(message, want) {
			t.Errorf("Ready message = %q, want it to mention %s", message, want)
		}
	}
}

// TestCustomFieldOutsideTheReservedSetIsCreated is the other half: the Kind is useful.
//
// A refusal mechanism that refused everything would be indistinguishable from not shipping the
// Kind, which is the trade this ticket had to avoid.
func TestCustomFieldOutsideTheReservedSetIsCreated(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, customFieldKind)
	stampingCustomFieldEndpoint(t, ns, target, stub)

	bootstrapped := customFieldPosts(stub)

	makeCustomField(t, ns, "service_tier", func(field *netboxv1alpha1.NetBoxCustomField) {
		field.Spec.Type = netboxv1alpha1.CustomFieldTypeText
		field.Spec.Label = "Service tier"
		field.Spec.GroupName = "Platform"
		field.Spec.FilterLogic = netboxv1alpha1.CustomFieldFilterLogicExact
		field.Spec.ObjectTypes = []string{"dcim.site", "virtualization.virtualmachine"}
	})

	eventually(t, "Ready=True", func() bool {
		return customFieldCondition(ns, "service-tier", netboxv1alpha1.ConditionReady).Status ==
			metav1.ConditionTrue
	})

	field := fetchCustomField(ns, "service-tier")
	if field == nil || field.Status.ID == 0 {
		t.Fatalf("status.id is unset on a Ready custom field: %+v", field)
	}

	if got := customFieldPosts(stub); got != bootstrapped+1 {
		t.Errorf("netbox saw %d POSTs, want the bootstrap's %d plus one for this field",
			got, bootstrapped)
	}
}

// TestCustomFieldRefusesADeleteThatLosesData is the guard the finalizer carries, which is the
// half of NBO-059 that applies to the fields a user does own.
//
// Deleting an extras.CustomField strips its stored value from every object in NetBox that has
// one, and NetBox performs it without complaint -- so the engine's usual answer, letting NetBox
// refuse with a PROTECT, cannot fire. It refuses instead, and reversibly: the CR is still here
// and the NetBox object is still here until somebody decides.
func TestCustomFieldRefusesADeleteThatLosesData(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, customFieldKind)
	stampingCustomFieldEndpoint(t, ns, target, stub)

	field := makeCustomField(t, ns, "audit_ticket", nil)

	eventually(t, "Ready=True", func() bool {
		return customFieldCondition(ns, "audit-ticket", netboxv1alpha1.ConditionReady).Status ==
			metav1.ConditionTrue
	})

	deletes := func() int {
		count := 0

		for _, write := range stub.recorded() {
			if write.Method == http.MethodDelete {
				count++
			}
		}

		return count
	}

	if err := k8sClient.Delete(context.Background(), field); err != nil {
		t.Fatalf("deleting the custom field: %v", err)
	}

	eventually(t, "Deleting=False, Reason=DataLossBlocked", func() bool {
		return customFieldCondition(ns, "audit-ticket", netboxv1alpha1.ConditionDeleting).Reason ==
			netboxv1alpha1.ReasonDataLossBlocked
	})

	if got := deletes(); got != 0 {
		t.Errorf("netbox saw %d DELETEs while the deletion was blocked, want none", got)
	}

	// Still here, both sides. That is what makes the refusal a decision somebody can take
	// later rather than an outage now.
	if fetchCustomField(ns, "audit-ticket") == nil {
		t.Fatal("the CR is gone while its NetBox object is not; the finalizer did not hold")
	}

	// The annotation is the way through, and the same DELETE then goes out.
	blocked := fetchCustomField(ns, "audit-ticket")
	blocked.Annotations = map[string]string{netboxv1alpha1.AllowDataLossAnnotation: "true"}
	if err := k8sClient.Update(context.Background(), blocked); err != nil {
		t.Fatalf("annotating the custom field: %v", err)
	}

	eventually(t, "the CR to finish deleting", func() bool {
		err := k8sClient.Get(context.Background(),
			client.ObjectKey{Namespace: ns, Name: "audit-ticket"}, &netboxv1alpha1.NetBoxCustomField{})

		return apierrors.IsNotFound(err)
	})

	if got := deletes(); got != 1 {
		t.Errorf("netbox saw %d DELETEs after the annotation, want exactly one", got)
	}
}
