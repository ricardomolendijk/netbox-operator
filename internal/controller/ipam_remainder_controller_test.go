package controller

import (
	"context"
	"fmt"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// rirKind and serviceTemplateKind point the shared stub at the two Kinds these tests drive.
//
// Both are chosen for having no reference of any kind, which the single-endpoint stub cannot
// serve: a resolved reference needs a GET on the *target's* endpoint, and the stub answers one.
// The reference-bearing Kinds in this group are asserted as data in
// internal/registry/ipam_remainder_test.go, and the mechanism behind a resolved generic FK
// reaching a query parameter is asserted in internal/reconciler/ipam_vlangroup_test.go.
var (
	rirKind             = stubKind{endpoint: "ipam/rirs", key: "slug"}
	serviceTemplateKind = stubKind{endpoint: "ipam/service-templates", key: "name"}
)

// conditionIsTrue reads one condition out of a status block. Named for what it asks rather
// than for a kind, because these two tests drive two kinds.
func conditionIsTrue(conditions []metav1.Condition, want string) bool {
	for _, c := range conditions {
		if c.Type == want {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

// TestRIRWritesTheSnakeCaseColumn is a payload assertion on the recorded request body, and it
// is the whole reason Descriptor.Fields is an explicit table rather than a naming convention.
//
// NetBox **ignores** a field name it does not know rather than rejecting it, so a payload
// carrying `isPrivate` gets a 201 back, writes nothing, and the next read finds the column
// still false -- which the operator then PATCHes again, forever. Asserting the condition or
// the status would not see it; only the body does.
func TestRIRWritesTheSnakeCaseColumn(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, rirKind)
	readyEndpoint(t, ns, target)

	private := true
	rir := &netboxv1alpha1.NetBoxRIR{
		ObjectMeta: metav1.ObjectMeta{Name: "rfc-1918", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxRIRSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "RFC 1918",
			Slug:             "rfc-1918",
			IsPrivate:        &private,
			Description:      "Private IPv4 address space",
		},
	}
	if err := k8sClient.Create(context.Background(), rir); err != nil {
		t.Fatalf("creating rir: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), rir) })

	eventually(t, "the RIR to be Ready", func() bool {
		fetched := &netboxv1alpha1.NetBoxRIR{}
		key := client.ObjectKey{Namespace: ns, Name: "rfc-1918"}
		if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
			return false
		}

		return conditionIsTrue(fetched.Status.Conditions, netboxv1alpha1.ConditionReady)
	})

	created := firstWrite(t, stub, http.MethodPost)

	if got, ok := created.Payload["is_private"]; !ok || got != true {
		t.Errorf("payload is_private = %v (present=%v), want true", got, ok)
	}

	if _, ok := created.Payload["isPrivate"]; ok {
		t.Error("payload carries isPrivate; NetBox ignores that name and writes nothing")
	}

	if got := created.Payload["slug"]; got != "rfc-1918" {
		t.Errorf("payload slug = %v, want rfc-1918", got)
	}

	if got := stub.countByKey("rfc-1918"); got != 1 {
		t.Errorf("netbox holds %d RIRs with slug rfc-1918, want exactly 1", got)
	}
}

// TestServiceTemplatePortsAreAnOrderedArrayOnTheWire is the `ports` decision, asserted on the
// recorded request bodies rather than on a condition.
//
// Three claims, and the third is the one that pays for the test:
//
//  1. `ports` reaches NetBox as a JSON array of numbers, in the order the spec gives.
//  2. A steady object writes nothing on resync -- an array compared by the wrong rule
//     PATCHes forever, and only letting time pass can show that.
//  3. **Reordering the list is one PATCH, not a second object.** NBO-055's acceptance
//     criterion asks for zero writes on a reorder, and that is not what ships: a Postgres
//     ArrayField preserves the order it is given, NetBox does not sort it on save
//     (netbox/ipam/models/services.py:41-47 recomputes only the `_ports_lowest` cache), and
//     internal/netbox/drift.go already names `Service.ports` under the order-sensitive
//     Arrays rule. An unordered-array comparison would be a new FieldClass and a new rule in
//     the differ -- shared-logic changes, which adding a Kind is not allowed to be. What the
//     descriptor *does* guarantee is the half that matters for correctness: `ports` is not in
//     the natural key, so a reorder finds the same row and becomes a PATCH instead of a
//     duplicate.
func TestServiceTemplatePortsAreAnOrderedArrayOnTheWire(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, serviceTemplateKind)
	readyEndpoint(t, ns, target)

	template := &netboxv1alpha1.NetBoxServiceTemplate{
		ObjectMeta: metav1.ObjectMeta{Name: "https", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxServiceTemplateSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "https",
			Protocol:         netboxv1alpha1.ServiceProtocolTCP,
			Ports:            []int32{443, 8443},
		},
	}
	if err := k8sClient.Create(context.Background(), template); err != nil {
		t.Fatalf("creating service template: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), template) })

	fetch := func() *netboxv1alpha1.NetBoxServiceTemplate {
		out := &netboxv1alpha1.NetBoxServiceTemplate{}
		key := client.ObjectKey{Namespace: ns, Name: "https"}
		if err := k8sClient.Get(context.Background(), key, out); err != nil {
			return nil
		}

		return out
	}

	eventually(t, "the service template to be Ready", func() bool {
		got := fetch()

		return got != nil && conditionIsTrue(got.Status.Conditions, netboxv1alpha1.ConditionReady)
	})

	created := firstWrite(t, stub, http.MethodPost)

	if got := portList(created.Payload["ports"]); got != "[443 8443]" {
		t.Errorf("payload ports = %s, want [443 8443] in that order", got)
	}

	if got := created.Payload["protocol"]; got != "tcp" {
		t.Errorf("payload protocol = %v, want tcp (the value, never the label)", got)
	}

	// Claim 2: nothing to do on resync. The suite's resyncPeriod is one second.
	steady := len(stub.recorded())
	waitResyncs(t, 3)

	if got := len(stub.recorded()); got != steady {
		t.Fatalf("netbox received %d writes on resync, want %d: an array is comparing unequal to itself",
			got, steady)
	}

	// Claim 3: a reorder is one PATCH against the same object, never a second row.
	current := fetch()
	id := current.Status.ID
	current.Spec.Ports = []int32{8443, 443}

	if err := k8sClient.Update(context.Background(), current); err != nil {
		t.Fatalf("reordering ports: %v", err)
	}

	eventually(t, "the reordered ports to reach netbox", func() bool {
		return portList(stub.get(id)["ports"]) == "[8443 443]"
	})

	if got := stub.countByKey("https"); got != 1 {
		t.Errorf("netbox holds %d service templates named https, want exactly 1: "+
			"ports is not in the natural key, so a reorder must not create a second object", got)
	}

	if fetch().Status.ID != id {
		t.Error("status.id changed; a reorder must be a PATCH against the same object")
	}
}

// firstWrite is the first recorded request with a given method, or a fatal.
func firstWrite(t *testing.T, stub *netboxStubServer, method string) stubWrite {
	t.Helper()

	for _, write := range stub.recorded() {
		if write.Method == method {
			return write
		}
	}

	t.Fatalf("no %s recorded; writes were %v", method, stub.recorded())

	return stubWrite{}
}

// portList renders a decoded JSON array of numbers without the float64 fractions, so an
// assertion can be read at a glance.
func portList(value any) string {
	items, ok := value.([]any)
	if !ok {
		return fmt.Sprintf("%v (not a list)", value)
	}

	out := make([]int64, 0, len(items))

	for _, item := range items {
		number, ok := item.(float64)
		if !ok {
			return fmt.Sprintf("%v (not all numbers)", value)
		}

		out = append(out, int64(number))
	}

	return fmt.Sprint(out)
}
