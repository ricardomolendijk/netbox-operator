package controller

import (
	"context"
	"net/http"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// wirelessLinkKind points the shared stub at wireless.WirelessLink.
//
// The first kind here whose natural key is two references rather than one scalar, so `key`
// alone cannot identify a row: `refKeys` is what makes the stub match the pair the way NetBox
// does, `?interface_a_id=9` against the `interface_a: 9` the payload wrote. Without both, the
// forward candidate would match on one endpoint and adopt a link to the wrong radio.
var wirelessLinkKind = stubKind{
	endpoint: "wireless/wireless-links",
	key:      "ssid",
	refKeys:  []string{"interface_a_id", "interface_b_id"},
}

// forbiddenLinkColumns must never appear in a request body sent to `wireless/wireless-links`.
//
// `_abs_distance` is DistanceMixin's normalised metres, recomputed on every save
// (netbox/netbox/models/mixins.py:108-117); `_interface_a_device` and `_interface_b_device` are
// recomputed from the two interfaces (netbox/wireless/models.py:222-227). Writing any of the
// three does not fail -- NetBox drops it -- so the next reconcile finds the same difference and
// PATCHes again, forever.
//
// `auth_psk` is the different kind of failure: a pre-shared key that reached a request body
// from a spec field would mean the key had been stored in plain text in etcd and in whatever
// git repository the manifest lives in. No spec field maps onto it, and this asserts it from
// the outside.
var forbiddenLinkColumns = []string{
	"auth_psk", "_abs_distance", "_interface_a_device", "_interface_b_device",
}

func makeWirelessLink(t *testing.T, ns, name string, a, b int64,
	mutate func(*netboxv1alpha1.NetBoxWirelessLink),
) {
	t.Helper()

	link := &netboxv1alpha1.NetBoxWirelessLink{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxWirelessLinkSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			InterfaceARef:    netboxv1alpha1.InterfaceRef{ID: idOf(a)},
			InterfaceBRef:    netboxv1alpha1.InterfaceRef{ID: idOf(b)},
			Status:           netboxv1alpha1.LinkStatusConnected,
		},
	}
	if mutate != nil {
		mutate(link)
	}
	if err := k8sClient.Create(context.Background(), link); err != nil {
		t.Fatalf("creating wireless link %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), link) })
}

func fetchWirelessLink(ns, name string) *netboxv1alpha1.NetBoxWirelessLink {
	link := &netboxv1alpha1.NetBoxWirelessLink{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, link); err != nil {
		return nil
	}

	return link
}

func wirelessLinkCondition(ns, name, kind string) metav1.Condition {
	link := fetchWirelessLink(ns, name)
	if link == nil {
		return metav1.Condition{}
	}
	for _, c := range link.Status.Conditions {
		if c.Type == kind {
			return c
		}
	}

	return metav1.Condition{}
}

func wirelessLinkIsReady(ns, name string) bool {
	return wirelessLinkCondition(ns, name, netboxv1alpha1.ConditionReady).Status == metav1.ConditionTrue
}

// firstLinkPost is the body of the first POST the engine sent, which on these tests is the
// create. Fails rather than returns nil, so a test that asserts on a payload cannot pass by
// finding none.
func firstLinkPost(t *testing.T, stub *netboxStubServer) netbox.Object {
	t.Helper()

	for _, write := range stub.recorded() {
		if write.Method == http.MethodPost {
			return write.Payload
		}
	}
	t.Fatal("no POST was recorded, so this assertion proves nothing")

	return nil
}

// assertNoLinkCachesWritten checks every request the engine made, rather than reading the
// descriptor back.
func assertNoLinkCachesWritten(t *testing.T, stub *netboxStubServer) {
	t.Helper()

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range forbiddenLinkColumns {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: %v", i, write.Method, column, write.Payload)
			}
		}
	}
}

// TestWirelessLinkTerminatesOnTwoInterfaces is the plain case: a link between two interfaces
// applies, both endpoints and the defaulted status reach NetBox in one POST, and none of the
// three recomputed caches -- or the pre-shared key -- is ever sent.
//
// Asserted on the POST body as well as on the stored row, because the row is where a *cache*
// NetBox recomputed would look identical to one the operator wrote.
func TestWirelessLinkTerminatesOnTwoInterfaces(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newWirelessStub(t, wirelessLinkKind)
	readyEndpoint(t, ns, target)

	makeWirelessLink(t, ns, "backhaul", 9, 10, func(l *netboxv1alpha1.NetBoxWirelessLink) {
		l.Spec.SSID = "Donkersloot-Backhaul"
		l.Spec.Distance = "1.5"
		l.Spec.DistanceUnit = netboxv1alpha1.DistanceUnitKilometer
	})
	eventually(t, "the link to be Ready", func() bool { return wirelessLinkIsReady(ns, "backhaul") })

	live := stub.get(fetchWirelessLink(ns, "backhaul").Status.ID)
	if live["interface_a"] != float64(9) || live["interface_b"] != float64(10) {
		t.Errorf("interface_a/interface_b = %v/%v, want 9/10", live["interface_a"], live["interface_b"])
	}

	post := firstLinkPost(t, stub)
	// The default has to reach the payload, or the operator can never correct the column.
	if post["status"] != string(netboxv1alpha1.LinkStatusConnected) {
		t.Errorf("POST status = %v, want connected", post["status"])
	}
	if post["distance"] != "1.5" {
		t.Errorf("POST distance = %v (%T), want the string \"1.5\": NetBox's DecimalField(8,2) "+
			"round-trips as a string, and a JSON number would go through IEEE-754 on the way in",
			post["distance"], post["distance"])
	}
	if post["distance_unit"] != string(netboxv1alpha1.DistanceUnitKilometer) {
		t.Errorf("POST distance_unit = %v, want km", post["distance_unit"])
	}

	assertNoLinkCachesWritten(t, stub)
}

// TestWirelessLinkReversePairIsAConflictAndWritesNothing is the acceptance criterion for an
// *ordered* unique constraint.
//
// NetBox's one constraint is `unique(interface_a, interface_b)`
// (netbox/wireless/models.py:190-195) with no expression and no second conditional form, so
// Postgres stores `(a,b)` and `(b,a)` as two rows for one radio path. `WirelessLink.clean`
// says nothing about the reverse pair either (:205-220).
//
// The reverse-orientation natural-key candidate is what closes that gap with no engine code:
// the second CR's forward candidate matches nothing, its reverse candidate finds the first
// CR's row, and under the default `onConflict: Fail` finding an object this CR did not create
// is a refusal rather than an adoption. One physical link, one NetBox row, and the second CR
// says why it is not Ready.
func TestWirelessLinkReversePairIsAConflictAndWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newWirelessStub(t, wirelessLinkKind)
	readyEndpoint(t, ns, target)

	makeWirelessLink(t, ns, "forward", 9, 10, nil)
	eventually(t, "the first link to be Ready", func() bool { return wirelessLinkIsReady(ns, "forward") })

	writesBefore := len(stub.recorded())

	makeWirelessLink(t, ns, "reversed", 10, 9, nil)

	eventually(t, "the reversed link to report Conflict", func() bool {
		c := wirelessLinkCondition(ns, "reversed", netboxv1alpha1.ConditionReady)

		return c.Status == metav1.ConditionFalse && c.Reason == netboxv1alpha1.ReasonConflict
	})

	if got := stub.recorded()[writesBefore:]; len(got) != 0 {
		t.Errorf("the engine wrote %d times for the reverse of an existing link, want none: %+v",
			len(got), got)
	}

	if link := fetchWirelessLink(ns, "reversed"); link.Status.ID != 0 {
		t.Errorf("status.id = %d on a Conflict, want 0: nothing was adopted", link.Status.ID)
	}
}

// TestWirelessLinkMovingAnEndpointIsAPatch is the contrast with dcim.Cable: both endpoints here
// are plain foreign keys rather than terminations, so re-pointing one is an ordinary PATCH and
// the Descriptor needs no RecreateOn.
//
// Asserted on the recorded methods, because a recreate and a PATCH leave NetBox in the same
// place and differ only in whether the row -- and every reference anybody else holds to its id
// -- survived.
func TestWirelessLinkMovingAnEndpointIsAPatch(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newWirelessStub(t, wirelessLinkKind)
	readyEndpoint(t, ns, target)

	makeWirelessLink(t, ns, "moving", 9, 10, nil)
	eventually(t, "the link to be Ready", func() bool { return wirelessLinkIsReady(ns, "moving") })

	before := fetchWirelessLink(ns, "moving")
	writesBefore := len(stub.recorded())

	before.Spec.InterfaceBRef = netboxv1alpha1.InterfaceRef{ID: idOf(11)}
	if err := k8sClient.Update(context.Background(), before); err != nil {
		t.Fatalf("moving the B side: %v", err)
	}

	eventually(t, "the move to reach NetBox", func() bool {
		return stub.get(before.Status.ID)["interface_b"] == float64(11)
	})

	writes := stub.recorded()[writesBefore:]
	if len(writes) != 1 {
		t.Fatalf("the move took %d writes, want exactly 1: %+v", len(writes), writes)
	}
	if writes[0].Method != http.MethodPatch {
		t.Errorf("the move was a %s, want PATCH: changing an endpoint must not destroy the row",
			writes[0].Method)
	}

	// The row is the same row, which is the whole of what "not a recreate" means.
	if after := fetchWirelessLink(ns, "moving"); after.Status.ID != before.Status.ID {
		t.Errorf("status.id moved from %d to %d, so the link was recreated",
			before.Status.ID, after.Status.ID)
	}

	assertNoLinkCachesWritten(t, stub)
}
