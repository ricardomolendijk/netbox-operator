package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// macKind points the shared stub at dcim.MACAddress. Keyed by `mac_address`, which
// MACAddressFilterSet declares as a MultiValueMACAddressFilter
// (netbox/dcim/filtersets.py:2030).
var macKind = stubKind{endpoint: "dcim/mac-addresses", key: "mac_address"}

// interfaceEndpoints are the two REST endpoints an id-mode member of this union is verified
// against: one per member, and both of them real, because `dcim.Interface` and
// `virtualization.VMInterface` are the two models deriving from `dcim.BaseInterface`.
var interfaceEndpoints = []string{"/api/dcim/interfaces/", "/api/virtualization/interfaces/"}

// newAssignedStub is the MAC stub fronted by a handler that answers the reads an id-mode
// `interfaceRef` or `vmInterfaceRef` is verified against.
//
// The shared stub serves one endpoint, so the two interface endpoints are handled in front of
// it and deliberately only for `GET`: a test that accidentally started *managing* an interface
// through this path would fail rather than pass quietly.
func newAssignedStub(t *testing.T) (*netboxStubServer, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, macKind)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := interfaceObjectID(r); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, srv.URL
}

// interfaceObjectID reports the primary key of a `GET` on either interface endpoint, and false
// for anything else.
func interfaceObjectID(r *http.Request) (int64, bool) {
	if r.Method != http.MethodGet {
		return 0, false
	}

	for _, prefix := range interfaceEndpoints {
		if !strings.HasPrefix(r.URL.Path, prefix) {
			continue
		}

		id, err := strconv.ParseInt(strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"), 10, 64)

		return id, err == nil
	}

	return 0, false
}

func makeMAC(t *testing.T, ns, name, address string, mutate func(*netboxv1alpha1.NetBoxMACAddress)) error {
	t.Helper()

	mac := &netboxv1alpha1.NetBoxMACAddress{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxMACAddressSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			MACAddress:       address,
		},
	}
	if mutate != nil {
		mutate(mac)
	}

	err := k8sClient.Create(context.Background(), mac)
	if err == nil {
		t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), mac) })
	}

	return err
}

func fetchMAC(ns, name string) *netboxv1alpha1.NetBoxMACAddress {
	mac := &netboxv1alpha1.NetBoxMACAddress{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, mac); err != nil {
		return nil
	}

	return mac
}

func macCondition(ns, name, kind string) metav1.Condition {
	mac := fetchMAC(ns, name)
	if mac == nil {
		return metav1.Condition{}
	}
	for _, c := range mac.Status.Conditions {
		if c.Type == kind {
			return c
		}
	}

	return metav1.Condition{}
}

func macIsReady(ns, name string) bool {
	return macCondition(ns, name, netboxv1alpha1.ConditionReady).Status == metav1.ConditionTrue
}

// TestMACAddressAssignmentIsWrittenAsAPairOrNotAtAll walks the states of the union on the
// recorded request body, which is the only place the difference is visible: each member
// resolved, the union omitted, and the union declared but unresolvable.
//
// Both members resolve -- dcim.Interface and virtualization.VMInterface both have Descriptors --
// so the `assigned_object_type` half is asserted per member. That is the half a reuse of
// ipam.IPAddress's three-member IPAssignment would not have caught: the spelling comes from the
// resolved member's own Descriptor.ObjectType, and the narrowing is what NetBox's
// MACADDRESS_ASSIGNMENT_MODELS demands (netbox/dcim/constants.py:156-159).
func TestMACAddressAssignmentIsWrittenAsAPairOrNotAtAll(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newAssignedStub(t)
	readyEndpoint(t, ns, target)

	// A resolved member: both columns, together, from one resolution.
	ifaceID := int64(8)
	if err := makeMAC(t, ns, "on-vm", "AA:BB:CC:DD:EE:01", func(m *netboxv1alpha1.NetBoxMACAddress) {
		m.Spec.AssignedObject = &netboxv1alpha1.MACAssignment{
			VMInterfaceRef: &netboxv1alpha1.VMInterfaceRef{ID: &ifaceID},
		}
	}); err != nil {
		t.Fatalf("creating the assigned MAC: %v", err)
	}
	eventually(t, "the assigned MAC to be Ready", func() bool { return macIsReady(ns, "on-vm") })

	mac := fetchMAC(ns, "on-vm")
	live := stub.get(mac.Status.ID)
	if live["assigned_object_type"] != "virtualization.vminterface" {
		t.Errorf("assigned_object_type = %v, want virtualization.vminterface -- and note the spelling: "+
			"lowercased and unpunctuated, which is Django's model attribute", live["assigned_object_type"])
	}
	if live["assigned_object_id"] != float64(ifaceID) {
		t.Errorf("assigned_object_id = %v, want %d", live["assigned_object_id"], ifaceID)
	}

	// The union omitted: "do not manage the assignment". The null-pinned candidate applies, so
	// the lookup asks for the *unattached* MAC of that address
	// (`?mac_address=...&assigned_object_id__empty=true`), finds none, and creates one with
	// neither column in the body -- which is how NetBox stores an unattached MAC.
	if err := makeMAC(t, ns, "unassigned", "AA:BB:CC:DD:EE:02", nil); err != nil {
		t.Fatalf("creating the unattached MAC: %v", err)
	}
	eventually(t, "the unattached MAC to be Ready", func() bool { return macIsReady(ns, "unassigned") })

	unattached := postFor(t, stub, "mac_address", "AA:BB:CC:DD:EE:02")
	for _, column := range []string{"assigned_object_type", "assigned_object_id"} {
		if value, present := unattached[column]; present {
			t.Errorf("POST %s = %v, want the column absent: the union was not written at all", column, value)
		}
	}

	// The other member, resolved. Both models deriving from dcim.BaseInterface are real Kinds,
	// so the union is resolvable end to end and the *type* half has to come out right for each
	// one -- which is the half a shared union would have got wrong.
	deviceIfaceID := int64(9)
	if err := makeMAC(t, ns, "on-eth1", "AA:BB:CC:DD:EE:04", func(m *netboxv1alpha1.NetBoxMACAddress) {
		m.Spec.AssignedObject = &netboxv1alpha1.MACAssignment{
			InterfaceRef: &netboxv1alpha1.InterfaceRef{ID: &deviceIfaceID},
		}
	}); err != nil {
		t.Fatalf("creating the device-interface MAC: %v", err)
	}
	eventually(t, "the device-interface MAC to be Ready", func() bool { return macIsReady(ns, "on-eth1") })

	onDevice := stub.get(fetchMAC(ns, "on-eth1").Status.ID)
	if onDevice["assigned_object_type"] != "dcim.interface" {
		t.Errorf("assigned_object_type = %v, want dcim.interface", onDevice["assigned_object_type"])
	}
	if onDevice["assigned_object_id"] != float64(deviceIfaceID) {
		t.Errorf("assigned_object_id = %v, want %d", onDevice["assigned_object_id"], deviceIfaceID)
	}

	// A member declared and *unresolvable* -- here a name-mode reference to a NetBoxInterface
	// CR that does not exist. Reported, never silently dropped, and on this kind never written
	// either.
	//
	// That last part is the difference from ipam.IPAddress, which carries the same two members
	// and still creates the row: there the assignment is not part of the natural key, so an
	// unresolved member is simply left out of the payload. Here `(assigned_object_type,
	// assigned_object_id, mac_address)` *is* the identity, so a declared-but-unresolved union
	// leaves no applicable candidate and the engine has nothing to look the object up by.
	// Creating anyway would mean POSTing an unattached MAC and then attaching it, which for an
	// address NetBox does not police is how a duplicate row gets made.
	writesBefore := len(stub.recorded())

	if err := makeMAC(t, ns, "on-ghost", "AA:BB:CC:DD:EE:03", func(m *netboxv1alpha1.NetBoxMACAddress) {
		m.Spec.AssignedObject = &netboxv1alpha1.MACAssignment{
			InterfaceRef: &netboxv1alpha1.InterfaceRef{Name: "no-such-interface"},
		}
	}); err != nil {
		t.Fatalf("creating the unresolvable MAC: %v", err)
	}

	eventually(t, "the unresolvable MAC to report the missing reference", func() bool {
		return macCondition(ns, "on-ghost", netboxv1alpha1.ConditionRefsResolved).Reason ==
			netboxv1alpha1.ReasonRefNotFound
	})

	if got := stub.recorded()[writesBefore:]; len(got) != 0 {
		t.Errorf("the engine wrote %d times for a MAC it could not identify, want none: %+v", len(got), got)
	}

	if mac := fetchMAC(ns, "on-ghost"); mac.Status.ID != 0 {
		t.Errorf("status.id = %d, want 0: the assignment is half of this kind's identity", mac.Status.ID)
	}
}

// TestMACAddressRejectsANonCanonicalAddress is the drift-loop guard, asserted at admission
// because that is the only place it can be caught.
//
// NetBox normalises every MAC it stores to `EUI(value, version=48,
// dialect=mac_unix_expanded_uppercase)` (netbox/dcim/fields.py:40-48), so a read always comes
// back uppercase and colon-separated whatever was sent. The differ compares strings and
// normalises no case, so a spec holding `aa:bb:cc:dd:ee:ff` would differ from NetBox's
// `AA:BB:CC:DD:EE:FF` on every pass and PATCH forever without converging -- a hot loop that
// reports Ready=True the whole time and is visible only on a clock.
//
// The CRD pattern is therefore narrower than what NetBox accepts on write, on purpose: the
// spelling that cannot converge is not admissible.
func TestMACAddressRejectsANonCanonicalAddress(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		name    string
		address string
		wantOK  bool
	}{
		{"NetBox's own canonical form", "AA:BB:CC:DD:EE:FF", true},
		{"lowercase, which NetBox would rewrite", "aa:bb:cc:dd:ee:ff", false},
		{"hyphen-separated, which NetBox would rewrite", "AA-BB-CC-DD-EE-FF", false},
		{"Cisco dotted, which NetBox would rewrite", "aabb.ccdd.eeff", false},
		{"unseparated, which NetBox would rewrite", "AABBCCDDEEFF", false},
		{"too few octets", "AA:BB:CC:DD:EE", false},
		{"not hex", "GG:BB:CC:DD:EE:FF", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := makeMAC(t, ns, "mac-"+strconv.Itoa(len(tc.name)), tc.address, nil)
			if tc.wantOK && err != nil {
				t.Errorf("creating %q = %v, want it admitted", tc.address, err)
			}
			if !tc.wantOK && err == nil {
				t.Errorf("creating %q was admitted; NetBox would rewrite it and the operator would PATCH forever",
					tc.address)
			}
		})
	}
}

// TestMACAddressUnionRejectsTwoMembers is the CEL half. Both columns are nullable
// (netbox/dcim/models/devices.py:1364-1374), so the rule is `<= 1` and an absent union stays
// legal -- but two members at once is not a state either column can hold.
func TestMACAddressUnionRejectsTwoMembers(t *testing.T) {
	ns := newNamespace(t)

	err := makeMAC(t, ns, "both", "AA:BB:CC:DD:EE:0A", func(m *netboxv1alpha1.NetBoxMACAddress) {
		m.Spec.AssignedObject = &netboxv1alpha1.MACAssignment{
			InterfaceRef:   &netboxv1alpha1.InterfaceRef{Name: "eth0"},
			VMInterfaceRef: &netboxv1alpha1.VMInterfaceRef{Name: "eth0"},
		}
	})
	if err == nil {
		t.Fatal("a union with two members was admitted; the CEL rule is not on the CRD")
	}
	if !strings.Contains(err.Error(), "at most one") {
		t.Errorf("rejection = %v, want the union's own message", err)
	}
}

// postFor is the single POST whose `field` equals `value`, for a test that applies several
// objects to one stub. onlyPost's "exactly one" is the wrong assertion there.
func postFor(t *testing.T, stub *netboxStubServer, field, value string) netbox.Object {
	t.Helper()

	var found netbox.Object

	for _, sent := range posts(stub) {
		if sent[field] == value {
			if found != nil {
				t.Fatalf("two POSTs carry %s=%s: %+v", field, value, posts(stub))
			}
			found = sent
		}
	}

	if found == nil {
		t.Fatalf("no POST carries %s=%s; posts = %+v", field, value, posts(stub))
	}

	return found
}

// TestMACAddressEmptyAssignmentEstablishesNoIdentity pins down a state that has no candidate,
// because it is a trap and the alternative is worse.
//
// `assignedObject: {}` is a legitimate instruction -- clear the assignment -- but it is not an
// *identity*. The value-matching candidate needs the two columns resolved, and a cleared union
// resolves neither; the null-pinned candidate needs `assignedObject` never declared, and this
// declares it. With nothing applicable the engine waits, which is the same behaviour
// ipam.VLANGroup has had since #180 for an empty `scope: {}`
// (internal/reconciler/ipam_vlangroup_test.go).
//
// Making the pin applicable here instead -- by keying it on the injected column name rather
// than on the union's spec field -- would make the null-pinned candidate applicable in *every*
// state, including when the union resolves. A MAC whose interface exists but whose row does not
// would then fall through and adopt an unrelated unattached MAC of the same address, and the
// follow-up PATCH would attach somebody else's row to this interface. That is the NBO-015
// failure, and it is worse than waiting.
//
// So the useful spelling of "an unattached MAC" is to *omit* the field, which the test above
// proves converges. Widening this is a change to what an empty union means for identity, which
// is shared semantics and belongs to its own ticket rather than to a kind.
func TestMACAddressEmptyAssignmentEstablishesNoIdentity(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newAssignedStub(t)
	readyEndpoint(t, ns, target)

	writesBefore := len(stub.recorded())

	if err := makeMAC(t, ns, "cleared", "AA:BB:CC:DD:EE:0B", func(m *netboxv1alpha1.NetBoxMACAddress) {
		m.Spec.AssignedObject = &netboxv1alpha1.MACAssignment{}
	}); err != nil {
		t.Fatalf("creating the cleared MAC: %v", err)
	}

	// Ready=False, and specifically not Ready=True-with-nothing-written: the object says why.
	eventually(t, "the cleared MAC to report that it cannot be identified", func() bool {
		c := macCondition(ns, "cleared", netboxv1alpha1.ConditionReady)

		return c.Status == metav1.ConditionFalse && c.Reason != ""
	})

	if got := stub.recorded()[writesBefore:]; len(got) != 0 {
		t.Errorf("the engine wrote %d times with no usable natural key, want none: %+v", len(got), got)
	}
}
