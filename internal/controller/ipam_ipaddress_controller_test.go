package controller

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// ipKind points the shared stub at ipam.IPAddress. `address` is the natural key, which is
// the first kind here whose key is not a slug -- and the reason the stub was parameterised.
var ipKind = stubKind{endpoint: "ipam/ip-addresses", key: "address"}

// makeIP applies a NetBoxIPAddress and removes it afterwards. Nothing is returned: every
// assertion below reads the object back through the cache, because what a test is about is
// always the state after a reconcile rather than the object it applied.
func makeIP(t *testing.T, ns, name, address string, mutate func(*netboxv1alpha1.NetBoxIPAddress)) {
	t.Helper()

	ip := &netboxv1alpha1.NetBoxIPAddress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxIPAddressSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Address:          address,
			Status:           netboxv1alpha1.IPAddressStatusActive,
		},
	}
	if mutate != nil {
		mutate(ip)
	}

	if err := k8sClient.Create(context.Background(), ip); err != nil {
		t.Fatalf("creating ip %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { removeObject(t, ip) })
}

// fetchIP reads an address through the manager's cache, and returns nil when it is not there
// yet: every poller below runs while the object is being created, so a miss is a state to
// wait in rather than a failure.
func fetchIP(ns, name string) *netboxv1alpha1.NetBoxIPAddress {
	ip := &netboxv1alpha1.NetBoxIPAddress{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, ip); err != nil {
		return nil
	}

	return ip
}

func mustFetchIP(t *testing.T, ns, name string) *netboxv1alpha1.NetBoxIPAddress {
	t.Helper()

	ip := fetchIP(ns, name)
	if ip == nil {
		t.Fatalf("ip %s/%s not found", ns, name)
	}

	return ip
}

// directIP reads an address straight from the API server, bypassing the manager's cache. It
// is only needed where a test writes the status itself: the cache legitimately lags, and
// polling it would answer with the value the test just replaced.
func directIP(t *testing.T, ns, name string) *netboxv1alpha1.NetBoxIPAddress {
	t.Helper()

	ip := &netboxv1alpha1.NetBoxIPAddress{}
	key := client.ObjectKey{Namespace: ns, Name: name}

	if err := apiClient.Get(context.Background(), key, ip); err != nil {
		t.Fatalf("fetching ip %s/%s directly: %v", ns, name, err)
	}

	return ip
}

// ipCondition returns one condition of an address, or the zero value when the object or the
// condition is not there.
func ipCondition(ns, name, condType string) metav1.Condition {
	ip := fetchIP(ns, name)
	if ip == nil {
		return metav1.Condition{}
	}

	for _, condition := range ip.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

func ipIsReady(ns, name string) bool {
	return ipCondition(ns, name, netboxv1alpha1.ConditionReady).Status == metav1.ConditionTrue
}

// ipHasReason waits for the address's Ready condition to be False with one reason, and
// returns the condition so a test can read its message.
func ipHasReason(t *testing.T, ns, name, reason string) metav1.Condition {
	t.Helper()

	eventually(t, "Ready=False/"+reason+" on "+name, func() bool {
		c := ipCondition(ns, name, netboxv1alpha1.ConditionReady)

		return c.Status == metav1.ConditionFalse && c.Reason == reason
	})

	return ipCondition(ns, name, netboxv1alpha1.ConditionReady)
}

// stampingEndpointFast is readyEndpoint with provenance switched on: the stamp is this kind's
// identity under spec.allowDuplicate, so every duplicate test needs one. Unlike
// stampingEndpoint it waits for a usable client and resyncs on the suite's one-second
// interval.
func stampingEndpointFast(t *testing.T, ns, target string) {
	t.Helper()

	readyEndpointWith(t, ns, target, func(e *netboxv1alpha1.NetBoxEndpoint) {
		e.Spec.ManagedBy = managedBy(nil)
	})
}

// posts are the create requests the stub received, which is where every payload assertion
// below reads from: the recorded request body, not the stored object and not the CR.
func posts(stub *netboxStubServer) []netbox.Object {
	out := make([]netbox.Object, 0, 1)

	for _, write := range stub.recorded() {
		if write.Method == http.MethodPost {
			out = append(out, write.Payload)
		}
	}

	return out
}

// onlyPost is the single create the engine made, and fails the test when it made none or
// several.
func onlyPost(t *testing.T, stub *netboxStubServer) netbox.Object {
	t.Helper()

	sent := posts(stub)
	if len(sent) != 1 {
		t.Fatalf("the engine sent %d POSTs, want exactly 1: %+v", len(sent), sent)
	}

	return sent[0]
}

// TestIPAddressCreatePreservesHostBitsAndDoesNotDrift is NBO-025's third acceptance
// criterion, on the recorded request body: `10.0.20.1/24` is sent exactly as written, and
// the second reconcile finds nothing to correct.
//
// The host bits are the whole point of an ipam.IPAddress -- it records a host *and* its mask
// -- so an operator that "helpfully" masked to 10.0.20.0/24 would be describing a different
// object. NetBoxPrefix masks; this kind must not.
func TestIPAddressCreatePreservesHostBitsAndDoesNotDrift(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	readyEndpoint(t, ns, target)

	makeIP(t, ns, "gateway", "10.0.20.1/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.Role = netboxv1alpha1.IPAddressRoleLoopback
		ip.Spec.DNSName = "gw.home.arpa"
		ip.Spec.Description = "House gateway"
	})
	eventually(t, "the address to be Ready", func() bool { return ipIsReady(ns, "gateway") })

	body := onlyPost(t, stub)

	want := map[string]any{
		"address":     "10.0.20.1/24",
		"status":      "active",
		"role":        "loopback",
		"dns_name":    "gw.home.arpa",
		"description": "House gateway",
	}
	for field, value := range want {
		if body[field] != value {
			t.Errorf("POST %s = %v, want %v", field, body[field], value)
		}
	}

	// A reference nobody declared must not reach the payload as null: an absent field means
	// "do not manage", and sending null would clear whatever NetBox holds.
	for _, absent := range []string{"vrf", "nat_inside", "assigned_object_type", "assigned_object_id"} {
		if _, present := body[absent]; present {
			t.Errorf("POST carries %s = %v, and nothing declared it", absent, body[absent])
		}
	}

	// Several resyncs at the suite's one-second interval. A choice column comes back as
	// {"value","label"} and an address is canonicalised by NetBox, so a comparison that got
	// either wrong would PATCH on every pass -- which is only observable over time.
	writes := len(stub.recorded())
	time.Sleep(3 * time.Second)

	if got := len(stub.recorded()); got != writes {
		t.Errorf("the engine wrote %d more times over three resyncs, want none: %+v",
			got-writes, stub.recorded()[writes:])
	}
}

// TestIPAddressAcceptsV6AndSingleHostMasks is the IPv6 half of every case above, plus the
// two masks that make an operator want to add validation NetBox does not have: a /32 and a
// /128 are ordinary loopbacks, not mistakes.
func TestIPAddressAcceptsV6AndSingleHostMasks(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	readyEndpoint(t, ns, target)

	addresses := map[string]string{
		"v6-host":     "2001:db8:20::1/64",
		"v6-loopback": "2001:db8::1/128",
		"v4-loopback": "10.255.0.1/32",
	}
	for name, address := range addresses {
		makeIP(t, ns, name, address, nil)
	}

	for name, address := range addresses {
		eventually(t, "the address "+name+" to be Ready", func() bool { return ipIsReady(ns, name) })

		if got := stub.get(mustFetchIP(t, ns, name).Status.ID)["address"]; got != address {
			t.Errorf("netbox holds %v for %s, want %s", got, name, address)
		}
	}
}

// TestIPAddressAssignmentIsWrittenAsAPairOrNotAtAll is the generic FK's whole contract, read
// off the request body: NetBox interprets an id against whatever type the column already
// holds, so half a pair is a reference to a different object that happens to share a primary
// key.
//
// Both halves of the contract are here. An empty union clears both columns, and a member
// whose Kind this build does not carry writes neither -- and says so rather than reporting
// success over an assignment that never happened.
func TestIPAddressAssignmentIsWrittenAsAPairOrNotAtAll(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	readyEndpoint(t, ns, target)

	// An empty union: the field was written and selects nothing, which is an instruction to
	// clear -- distinct from omitting it, which leaves NetBox's own assignment alone.
	makeIP(t, ns, "unassigned", "10.0.21.1/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.AssignedObject = &netboxv1alpha1.IPAssignment{}
	})
	eventually(t, "the cleared address to be Ready", func() bool { return ipIsReady(ns, "unassigned") })

	body := onlyPost(t, stub)
	for _, column := range []string{"assigned_object_type", "assigned_object_id"} {
		value, present := body[column]
		if !present || value != nil {
			t.Errorf("POST %s = %v (present=%v), want an explicit null: an empty union clears both",
				column, value, present)
		}
	}

	// A member pointing at a Kind that does not exist until M4. Reported, never written:
	// NBO-019's promise is that a union member the operator cannot resolve is not silently
	// dropped.
	makeIP(t, ns, "on-eth0", "10.0.21.2/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.AssignedObject = &netboxv1alpha1.IPAssignment{
			InterfaceRef: &netboxv1alpha1.InterfaceRef{Name: "eth0"},
		}
	})

	ready := ipHasReason(t, ns, "on-eth0", netboxv1alpha1.ReasonWaitingForRef)
	if !strings.Contains(ready.Message, "assignedObject") {
		t.Errorf("Ready message = %q, want it to name the field that is waiting", ready.Message)
	}

	// RefNotFound, not RefKindUnavailable: NBO-030 registers dcim.interface, so the union's
	// `interfaceRef` member now has a Descriptor and the Kind exists. What is missing is the
	// named interface itself. The property under test is unchanged -- an unresolved member
	// writes neither column of the pair.
	refs := ipCondition(ns, "on-eth0", netboxv1alpha1.ConditionRefsResolved)
	if refs.Reason != netboxv1alpha1.ReasonRefNotFound {
		t.Errorf("RefsResolved reason = %q, want %q",
			refs.Reason, netboxv1alpha1.ReasonRefNotFound)
	}

	// Nothing is written for it. This test asserted the opposite until NBO-195 (answered C):
	// an unresolved *optional* reference used to be left out of the write and the object
	// created without it, which is what #132 established. A declared reference is now a
	// precondition for the write, so a declared-but-unresolvable `assignedObject` means the
	// address is not created at all.
	//
	// The reason that is better here specifically: the pair is this object's whole assignment.
	// Creating the address unassigned and assigning it on a later pass is two writes and a
	// window in which NetBox holds a floating address, and #195 removed the accident that made
	// the behaviour depend on whether the reference happened to be in the natural key.
	for _, sent := range posts(stub) {
		if sent["address"] == "10.0.21.2/24" {
			t.Errorf("POST for the assigned address = %+v, want none: its declared "+
				"assignedObject did not resolve", sent)
		}
	}
}

// TestIPAddressDuplicateWithoutAllowDuplicateIsAConflict is the default half of decision
// #177: the operator defers to NetBox, and two matches is an error naming the candidates
// rather than a guess.
//
// ipam.IPAddress has no meta.constraints (docs/netbox-schema.md), so this is a state NetBox
// itself permits whenever the enclosing VRF does not enforce uniqueness.
func TestIPAddressDuplicateWithoutAllowDuplicateIsAConflict(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	readyEndpoint(t, ns, target)

	first := stub.seed(netbox.Object{"address": "10.0.22.1/24"})
	second := stub.seed(netbox.Object{"address": "10.0.22.1/24"})

	makeIP(t, ns, "anycast", "10.0.22.1/24", nil)

	ready := ipHasReason(t, ns, "anycast", netboxv1alpha1.ReasonConflict)

	// #108's requirement: the ids, not a count. The next step is a human choosing between
	// them, and a count leaves them to reproduce the query by hand.
	for _, id := range []int64{first, second} {
		if !strings.Contains(ready.Message, "id "+strconv.FormatInt(id, 10)) {
			t.Errorf("Conflict message = %q, want it to name netbox id %d", ready.Message, id)
		}
	}

	if sent := posts(stub); len(sent) != 0 {
		t.Errorf("the engine created %d objects while refusing to guess: %+v", len(sent), sent)
	}
}

// TestIPAddressAllowDuplicateRequiresProvenance is the answer to the question decision #177
// left open, in its strictest reading: with no stamp there is nothing that could identify
// this object's own row, so nothing is written at all.
//
// Refused before the first create rather than at the first collision. An object the operator
// cannot recognise again is one it would duplicate on the next reconcile that lost
// status.id, which is the shape of the double-allocation hazard in issue #167.
func TestIPAddressAllowDuplicateRequiresProvenance(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	readyEndpoint(t, ns, target)

	makeIP(t, ns, "vrrp", "10.0.23.1/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.AllowDuplicate = true
		ip.Spec.Role = netboxv1alpha1.IPAddressRoleVRRP
	})

	ready := ipHasReason(t, ns, "vrrp", netboxv1alpha1.ReasonInvalid)
	if !strings.Contains(ready.Message, "managedBy") {
		t.Errorf("Invalid message = %q, want it to name spec.managedBy", ready.Message)
	}

	if sent := posts(stub); len(sent) != 0 {
		t.Errorf("the engine created %d unidentifiable objects: %+v", len(sent), sent)
	}
}

// TestIPAddressAllowDuplicateCreatesAnotherBesideSomebodyElsesIsThePoint is what the field
// is for: a VRRP virtual address exists once per participating router, so an existing row
// that provably belongs to another CR is not this CR's object.
func TestIPAddressAllowDuplicateCreatesAnotherBesideSomebodyElsesIsThePoint(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	stub.withProvenance()
	stampingEndpointFast(t, ns, target)

	for range 2 {
		stub.seed(netbox.Object{
			"address": "10.0.24.1/24",
			// Another CR's stamp: same cluster, different object.
			"custom_fields": map[string]any{provenance.DefaultUIDField: "somebody-else"},
		})
	}

	makeIP(t, ns, "vrrp", "10.0.24.1/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.AllowDuplicate = true
		ip.Spec.Role = netboxv1alpha1.IPAddressRoleVRRP
	})
	eventually(t, "the third address to be Ready", func() bool { return ipIsReady(ns, "vrrp") })

	ip := mustFetchIP(t, ns, "vrrp")
	if got := stub.countByKey("10.0.24.1/24"); got != 3 {
		t.Errorf("netbox holds %d objects for the address, want 3", got)
	}

	// Its own object, stamped with its own uid -- which is how the next pass finds it again.
	body := onlyPost(t, stub)

	fields, _ := body["custom_fields"].(map[string]any)
	if fields[provenance.DefaultUIDField] != string(ip.UID) {
		t.Errorf("POST custom_fields[%s] = %v, want this CR's uid %s",
			provenance.DefaultUIDField, fields[provenance.DefaultUIDField], ip.UID)
	}

	if ip.Status.Adopted {
		t.Error("status.adopted is true on an object this CR created")
	}

	// And the stamped object settles. `tags` is written as bare ids and read back as nested
	// objects, so a stamp compared in the wrong shape is drift on every pass -- one PATCH per
	// object per resync, for every stamped object in the cluster, which only a clock can show.
	writes := len(stub.recorded())
	time.Sleep(3 * time.Second)

	if got := len(stub.recorded()); got != writes {
		t.Errorf("the stamped address was written %d more times over three resyncs, want none: %+v",
			got-writes, stub.recorded()[writes:])
	}
}

// TestIPAddressAllowDuplicateRefusesAnUnstampedMatch is the case decision #177 asked to have
// answered explicitly: an address created before the operator, or by another tool.
//
// It refuses rather than creating a third copy, because with allowDuplicate the stamp *is*
// the identity and an unstamped match may well be the object this CR meant. The way out is
// named in the message: drop the field and adopt.
func TestIPAddressAllowDuplicateRefusesAnUnstampedMatch(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	stub.withProvenance()
	stampingEndpointFast(t, ns, target)

	existing := stub.seed(netbox.Object{"address": "10.0.25.1/24", "description": "made by hand"})

	makeIP(t, ns, "vip", "10.0.25.1/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.AllowDuplicate = true
		ip.Spec.Role = netboxv1alpha1.IPAddressRoleVIP
	})

	ready := ipHasReason(t, ns, "vip", netboxv1alpha1.ReasonConflict)
	if !strings.Contains(ready.Message, "id "+strconv.FormatInt(existing, 10)) {
		t.Errorf("Conflict message = %q, want it to name netbox id %d", ready.Message, existing)
	}

	if !strings.Contains(ready.Message, "onConflict") {
		t.Errorf("Conflict message = %q, want it to name the way out", ready.Message)
	}

	if sent := posts(stub); len(sent) != 0 {
		t.Errorf("the engine created %d objects it could not tell apart: %+v", len(sent), sent)
	}

	if got := stub.get(existing)["description"]; got != "made by hand" {
		t.Errorf("the untouched object's description = %v, want it left alone", got)
	}
}

// TestIPAddressAllowDuplicateReclaimsItsOwnObjectByItsStamp is the other half of the same
// rule, and the one that makes the field safe: the CR's own row is found again by the stamp
// after status.id is lost, rather than duplicated.
//
// status.id going missing is not hypothetical -- a restore, a namespace re-applied from Git
// without its status, or the NetBox object being recreated all produce it, and it is the
// step at which issue #167's second claim appears.
func TestIPAddressAllowDuplicateReclaimsItsOwnObjectByItsStamp(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, ipKind)
	stub.withProvenance()
	stampingEndpointFast(t, ns, target)

	makeIP(t, ns, "vip", "10.0.26.1/24", func(ip *netboxv1alpha1.NetBoxIPAddress) {
		ip.Spec.AllowDuplicate = true
		ip.Spec.Role = netboxv1alpha1.IPAddressRoleVIP
	})
	eventually(t, "the address to be Ready", func() bool { return ipIsReady(ns, "vip") })

	created := mustFetchIP(t, ns, "vip").Status.ID

	// The status the operator wrote, taken away. Written through the status subresource,
	// which is the only thing the operator itself may write.
	lost := mustFetchIP(t, ns, "vip")
	lost.Status.ID = 0
	if err := apiClient.Status().Update(context.Background(), lost); err != nil {
		t.Fatalf("clearing status.id: %v", err)
	}

	// Read through the direct client rather than the manager's cache: the cache still holds
	// the status this test just replaced, so a poll through it would pass on the old value
	// before the operator had done anything at all.
	version := lost.ResourceVersion

	eventually(t, "status.id to come back", func() bool {
		fresh := directIP(t, ns, "vip")

		return fresh.ResourceVersion != version && fresh.Status.ID != 0
	})

	ip := directIP(t, ns, "vip")
	if ip.Status.ID != created {
		t.Errorf("status.id = %d, want the object it created (%d)", ip.Status.ID, created)
	}

	// Not an adoption: an object carrying this CR's own uid was created by this CR, so
	// reclaiming it must not need spec.onConflict -- which is still Fail here.
	if ip.Status.Adopted {
		t.Error("status.adopted is true; an object stamped with this CR's uid is not adopted")
	}

	if got := stub.countByKey("10.0.26.1/24"); got != 1 {
		t.Errorf("netbox holds %d objects for the address, want 1: the CR duplicated its own", got)
	}

	if sent := posts(stub); len(sent) != 1 {
		t.Errorf("the engine sent %d POSTs, want 1: the second pass reclaimed rather than created",
			len(sent))
	}
}
