package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// The three endpoints a device with inline children touches, one stub each. A device's
// interfaces and their addresses are three Kinds writing three NetBox models, which is the
// first test in this package that needs more than one endpoint at once -- and the point of
// NBO-034 is precisely that the second and third Kind take no engine code, so the stub they
// are served by is the shared one three times over rather than a hand-written NetBox.
var (
	deviceKind    = stubKind{endpoint: "dcim/devices", key: "name"}
	interfaceKind = stubKind{endpoint: "dcim/interfaces", key: "name"}
	addressKind   = stubKind{endpoint: "ipam/ip-addresses", key: "address"}
)

// catalogueEndpoints are the paths an `id`-mode reference verifies against.
//
// The device's three required references are written as raw NetBox ids in these tests, so that
// a test about children does not also have to stand up a site, a device type and a device
// role. An id is *verified* rather than trusted (internal/resolver, byID), so the ids still
// have to answer a GET -- which is all inlineNetBox does for them.
var catalogueEndpoints = []string{"dcim/sites", "dcim/device-types", "dcim/device-roles"}

// inlineNetBox is one NetBox serving the three kinds a device's inline lists produce, plus
// GET-by-id on the catalogue endpoints its required references point at.
//
// A multiplexer in front of three of the shared stubs rather than a fourth stub type: each one
// keeps its own natural key, its own object store and its own write log, so `interfaces.recorded()`
// still means "what the engine did to dcim.Interface" -- which is what a test about the order
// of a deferred PATCH has to be able to ask.
type inlineNetBox struct {
	devices    *netboxStubServer
	interfaces *netboxStubServer
	addresses  *netboxStubServer
	url        string
}

func newInlineNetBox(t *testing.T) *inlineNetBox {
	t.Helper()

	nb := &inlineNetBox{}
	nb.devices, _ = newNetBoxStub(t, deviceKind)
	nb.interfaces, _ = newNetBoxStub(t, interfaceKind)
	nb.addresses, _ = newNetBoxStub(t, addressKind)

	srv := httptest.NewServer(http.HandlerFunc(nb.route))
	t.Cleanup(srv.Close)
	nb.url = srv.URL

	// Every object's self `url` has to point at the address the operator actually talks to,
	// because status.url is copied straight out of a write response. Set before the
	// multiplexer serves anything, so nothing reads these concurrently.
	nb.devices.url, nb.interfaces.url, nb.addresses.url = srv.URL, srv.URL, srv.URL

	return nb
}

// route dispatches on the endpoint prefix, and answers a catalogue GET itself.
func (nb *inlineNetBox) route(w http.ResponseWriter, r *http.Request) {
	for _, endpoint := range catalogueEndpoints {
		if !strings.HasPrefix(r.URL.Path, "/api/"+endpoint+"/") {
			continue
		}

		nb.catalogue(w, r, endpoint)

		return
	}

	switch {
	case strings.HasPrefix(r.URL.Path, "/api/"+interfaceKind.endpoint):
		nb.interfaces.route(w, r)
	case strings.HasPrefix(r.URL.Path, "/api/"+addressKind.endpoint):
		nb.addresses.route(w, r)
	default:
		// The device's own endpoint, and /api/status/, which every stub answers.
		nb.devices.route(w, r)
	}
}

// catalogue answers the one request an id-mode reference makes: does this primary key exist.
func (nb *inlineNetBox) catalogue(w http.ResponseWriter, r *http.Request, endpoint string) {
	tail := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"+endpoint), "/")

	id, err := strconv.Atoi(tail)
	if err != nil {
		writeStubJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}

	writeStubJSON(w, http.StatusOK, netbox.Object{
		"id":      float64(id),
		"url":     fmt.Sprintf("%s/api/%s/%d/", nb.url, endpoint, id),
		"display": endpoint,
	})
}

// writes is the total number of mutating requests this NetBox has received, across all three
// endpoints. The instrument for "a second pass is a no-op": a hot loop is only observable as a
// count that keeps climbing while nothing changes.
func (nb *inlineNetBox) writes() int {
	return len(nb.devices.recorded()) + len(nb.interfaces.recorded()) + len(nb.addresses.recorded())
}

// makeDevice applies a NetBoxDevice with id-mode required references and whatever inline
// interfaces the test needs.
//
// The cleanup deletes the children before the device, in that order and by hand. envtest runs
// no garbage collector, so an owner reference cascades nothing here: without this the device's
// own finalizer would wait forever on child CRs nothing was ever going to remove
// (internal/reconciler/finalizer.go, pendingDependents).
func makeDevice(t *testing.T, ns, name string, entries ...netboxv1alpha1.InlineInterface) {
	t.Helper()

	site, deviceType, role := int64(11), int64(12), int64(13)
	device := &netboxv1alpha1.NetBoxDevice{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxDeviceSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			SiteRef:          netboxv1alpha1.SiteRef{ID: &site},
			DeviceTypeRef:    netboxv1alpha1.DeviceTypeRef{ID: &deviceType},
			RoleRef:          netboxv1alpha1.DeviceRoleRef{ID: &role},
			Interfaces:       entries,
		},
	}

	if err := k8sClient.Create(context.Background(), device); err != nil {
		t.Fatalf("creating device %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() {
		ctx := context.Background()

		interfaces := &netboxv1alpha1.NetBoxInterfaceList{}
		if err := apiClient.List(ctx, interfaces, client.InNamespace(ns)); err == nil {
			for i := range interfaces.Items {
				_ = apiClient.Delete(ctx, &interfaces.Items[i])
			}
		}

		addresses := &netboxv1alpha1.NetBoxIPAddressList{}
		if err := apiClient.List(ctx, addresses, client.InNamespace(ns)); err == nil {
			for i := range addresses.Items {
				_ = apiClient.Delete(ctx, &addresses.Items[i])
			}
		}

		_ = k8sClient.Delete(ctx, device)
	})
}

// setInterfaces rewrites a device's inline list, which is what removing an entry looks like
// from a user's side.
func setInterfaces(t *testing.T, ns, name string, entries ...netboxv1alpha1.InlineInterface) {
	t.Helper()

	device := fetchDevice(ns, name)
	if device == nil {
		t.Fatalf("device %s/%s is gone", ns, name)
	}

	device.Spec.Interfaces = entries
	if err := k8sClient.Update(context.Background(), device); err != nil {
		t.Fatalf("updating device %s/%s: %v", ns, name, err)
	}
}

func fetchDevice(ns, name string) *netboxv1alpha1.NetBoxDevice {
	device := &netboxv1alpha1.NetBoxDevice{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, device); err != nil {
		return nil
	}

	return device
}

func fetchInterface(ns, name string) *netboxv1alpha1.NetBoxInterface {
	iface := &netboxv1alpha1.NetBoxInterface{}
	if err := apiClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, iface); err != nil {
		return nil
	}

	return iface
}

// deviceCondition returns one condition of a device, or the zero value when the device or the
// condition is absent -- so a test can assert on a Reason without a nil check per line.
func deviceCondition(ns, name, conditionType string) metav1.Condition {
	device := fetchDevice(ns, name)
	if device == nil {
		return metav1.Condition{}
	}

	for _, condition := range device.Status.Conditions {
		if condition.Type == conditionType {
			return condition
		}
	}

	return metav1.Condition{}
}

// conditionReason is the same read on any object's condition list, for the children.
func conditionReason(conditions []metav1.Condition, conditionType string) string {
	for _, condition := range conditions {
		if condition.Type == conditionType {
			return condition.Reason
		}
	}

	return ""
}

// ethernet is the inline entry every test here starts from: the two required fields and
// nothing else.
func ethernet(name string, addresses ...string) netboxv1alpha1.InlineInterface {
	entry := netboxv1alpha1.InlineInterface{Name: name, Type: "10gbase-t"}

	for _, address := range addresses {
		entry.Addresses = append(entry.Addresses, netboxv1alpha1.InlineIPAddress{Address: address})
	}

	return entry
}

// TestDeviceMaterialisesItsInterfacesAndTheirAddresses is NBO-034 end to end: one manifest,
// three CRs, three NetBox objects, and the device not Ready until the other two are.
//
// It is also the generality test NBO-032 was built for. Nothing under internal/reconciler
// changed to make a second parent Kind with two different child Kinds work, so what this
// asserts is that the engine's per-kind knowledge really is the one type assertion.
func TestDeviceMaterialisesItsInterfacesAndTheirAddresses(t *testing.T) {
	ns := newNamespace(t)
	nb := newInlineNetBox(t)
	readyEndpoint(t, ns, nb.url)

	makeDevice(t, ns, "rtmrpi0001", ethernet("eth0", "10.0.20.10/24"))

	eventually(t, "the device's children to be ready", func() bool {
		return deviceCondition(ns, "rtmrpi0001", netboxv1alpha1.ConditionChildrenReady).Reason ==
			netboxv1alpha1.ReasonAllReady
	})

	device := fetchDevice(ns, "rtmrpi0001")

	// status.children is the record the finalizer and the next pass's pruner both read.
	want := map[string]string{
		"spec.interfaces[eth0]":                          "rtmrpi0001-eth0",
		"spec.interfaces[eth0].addresses[10.0.20.10/24]": "rtmrpi0001-eth0-ip-10-0-20-10-24",
	}
	if len(device.Status.Children) != len(want) {
		t.Fatalf("status.children has %d entries, want %d: %+v",
			len(device.Status.Children), len(want), device.Status.Children)
	}

	for _, child := range device.Status.Children {
		if name := want[child.Path]; name != child.Name {
			t.Errorf("status.children[%s].name = %q, want %q", child.Path, child.Name, name)
		}

		if !child.Ready {
			t.Errorf("status.children[%s] is not ready", child.Path)
		}
	}

	// The device is not Ready while a declared child is not, so AllReady above already
	// implies this -- asserted anyway, because `kubectl wait` on a device is the whole reason
	// ChildrenReady downgrades Ready.
	if got := deviceCondition(ns, "rtmrpi0001", netboxv1alpha1.ConditionReady); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True once every child is ready", got.Status, got.Reason)
	}

	iface := fetchInterface(ns, "rtmrpi0001-eth0")
	if iface == nil {
		t.Fatal("no NetBoxInterface rtmrpi0001-eth0 was materialised")
	}

	assertMaterialised(t, iface, device, "spec.interfaces[eth0]")

	// Inherited, and the only two spec fields that are: an inline entry cannot set either,
	// and a child that reached Ready proves the endpoint reference arrived.
	if iface.Spec.EndpointRef != "homelab" {
		t.Errorf("the child's endpointRef = %q, want the parent's homelab", iface.Spec.EndpointRef)
	}

	if iface.Spec.DeviceRef.Name != "rtmrpi0001" {
		t.Errorf("the child's deviceRef = %+v, want the parent by name", iface.Spec.DeviceRef)
	}

	address := &netboxv1alpha1.NetBoxIPAddress{}
	if err := apiClient.Get(context.Background(), client.ObjectKey{
		Namespace: ns, Name: "rtmrpi0001-eth0-ip-10-0-20-10-24",
	}, address); err != nil {
		t.Fatalf("no NetBoxIPAddress was materialised: %v", err)
	}

	assertMaterialised(t, address, device, "spec.interfaces[eth0].addresses[10.0.20.10/24]")

	// The counterpart of NBO-033's `virtualization.vminterface` assertion, and the one fact
	// that proves the address is on a *device* interface: NetBox stores the union member as an
	// object type string, and the operator writes the pair atomically or not at all.
	assertAssignedTo(t, nb, "dcim.interface")
}

// TestDeviceInlineChildrenSettleAndStopWriting is the deferred self-reference converging, and
// the PATCH loop it could have been.
//
// An inline `lag` naming a sibling is an ordinary deferred field on the child
// (`DeferIfUnresolved`): the sibling has no id on the first pass, so `lag` is left out of the
// POST and applied by a follow-up PATCH. The failure this rules out is the one
// docs/concepts/drift.md opens with -- a field stripped from the write and left in the diff
// PATCHes forever -- which is only observable as a write count that keeps climbing.
func TestDeviceInlineChildrenSettleAndStopWriting(t *testing.T) {
	ns := newNamespace(t)
	nb := newInlineNetBox(t)
	readyEndpoint(t, ns, nb.url)

	bond := netboxv1alpha1.InlineInterface{Name: "bond0", Type: "lag"}
	member := ethernet("eth0", "10.0.20.10/24")
	member.LAG = "bond0"

	// The member first, so the list order is the opposite of the dependency order: which
	// entry appears first must not matter, because identity is the key and the reference is
	// deferred until the sibling exists.
	makeDevice(t, ns, "sw1", member, bond)

	eventually(t, "the device's children to be ready", func() bool {
		return deviceCondition(ns, "sw1", netboxv1alpha1.ConditionChildrenReady).Reason ==
			netboxv1alpha1.ReasonAllReady
	})

	iface := fetchInterface(ns, "sw1-eth0")
	if iface == nil || iface.Spec.LagRef == nil || iface.Spec.LagRef.Name != "sw1-bond0" {
		t.Fatalf("the member's lagRef did not resolve to the sibling's derived name: %+v", iface)
	}

	// `lag` reaches NetBox exactly once, and *which* write carries it is deliberately not
	// asserted. `DeferIfUnresolved` means the create carries the reference when it already
	// resolves and a follow-up PATCH carries it when it does not, so both are correct outcomes
	// -- and which one happens depends on whether the sibling's own reconcile got there first,
	// which is a scheduling race rather than a property. Pinning it would be a test that fails
	// on a fast machine. What is a property is that it arrives, and that it arrives once:
	// exactly the two halves of NBO-015 (docs/concepts/drift.md).
	var carried, patches int

	for _, write := range nb.interfaces.recorded() {
		if _, has := write.Payload["lag"]; !has {
			continue
		}

		carried++

		if write.Method == http.MethodPatch {
			patches++
		}
	}

	if carried == 0 {
		t.Error("no write to dcim/interfaces ever carried `lag`, so the LAG membership never " +
			"reached NetBox")
	}

	if patches > 1 {
		t.Errorf("%d PATCHes carried `lag`; a deferred field stripped from the create and left "+
			"in the diff PATCHes forever", patches)
	}

	// The endpoint's resyncPeriod is one second in this suite, so this covers several
	// reconciles of the device and of both children that should each find nothing to do.
	settled := nb.writes()
	time.Sleep(4 * time.Second)

	if after := nb.writes(); after != settled {
		t.Errorf("netbox took %d writes after everything settled, want none; a deferred field "+
			"stripped from the create and left in the diff PATCHes forever", after-settled)
	}
}

// TestDeviceInlineEntryRemovalPrunesOnlyThatChild is the prune, and the two things it must not
// touch: a sibling that is still declared, and a NetBoxInterface a human wrote.
func TestDeviceInlineEntryRemovalPrunesOnlyThatChild(t *testing.T) {
	ns := newNamespace(t)
	nb := newInlineNetBox(t)
	readyEndpoint(t, ns, nb.url)

	makeDevice(t, ns, "sw1", ethernet("eth0"), ethernet("eth1"))

	eventually(t, "both interfaces to be materialised", func() bool {
		return fetchInterface(ns, "sw1-eth0") != nil && fetchInterface(ns, "sw1-eth1") != nil
	})

	// A hand-written interface on the same device, at a name no inline entry derives. It gets
	// a *containment* owner reference from its own pass and no controller one, which is
	// exactly what makes it invisible to the pruner (ADR-0003 rules 4 and 5).
	handWritten := &netboxv1alpha1.NetBoxInterface{
		ObjectMeta: metav1.ObjectMeta{Name: "sw1-vlan20", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxInterfaceSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			DeviceRef:        netboxv1alpha1.DeviceRef{Name: "sw1"},
			Name:             "vlan20",
			Type:             "virtual",
		},
	}
	if err := k8sClient.Create(context.Background(), handWritten); err != nil {
		t.Fatalf("creating the hand-written interface: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), handWritten) })

	keep := fetchInterface(ns, "sw1-eth0")
	setInterfaces(t, ns, "sw1", ethernet("eth0"))

	eventually(t, "the removed entry's child to be pruned", func() bool {
		return fetchInterface(ns, "sw1-eth1") == nil
	})

	if got := fetchInterface(ns, "sw1-eth0"); got == nil {
		t.Error("the sibling that is still declared was pruned too")
	} else if got.ResourceVersion != keep.ResourceVersion {
		// Not a strict requirement of pruning, but the measurable form of "an identical apply
		// is inert": the pass that pruned eth1 re-applied eth0 unchanged.
		t.Logf("sw1-eth0 moved from resourceVersion %s to %s across the prune",
			keep.ResourceVersion, got.ResourceVersion)
	}

	if fetchInterface(ns, "sw1-vlan20") == nil {
		t.Error("the hand-written interface was deleted; a CR with no owned-by-path is never pruned")
	}

	// And it is absent from the record the parent keeps, because the parent did not declare
	// it: status.children is the inline set, not "every child of this device".
	for _, child := range fetchDevice(ns, "sw1").Status.Children {
		if child.Name == "sw1-vlan20" {
			t.Error("the hand-written interface reached status.children")
		}
	}
}

// TestDeviceInlineInterfacesDifferingOnlyInCaseConflict is the case-sensitivity asymmetry, at
// the only place it is visible: NetBox holds `Eth0` and `eth0` as two interfaces on one device,
// and a derived CR name is slugified, so the two collapse to one.
//
// The materialiser reports a Conflict naming both entries and writes **nothing at all** -- not
// even the entries that did not collide -- because two entries applying one name in turn would
// each overwrite the other on alternate reconciles, forever. Failing closed is the answer; a
// partial write would be a device whose interface list flapped.
func TestDeviceInlineInterfacesDifferingOnlyInCaseConflict(t *testing.T) {
	ns := newNamespace(t)
	nb := newInlineNetBox(t)
	readyEndpoint(t, ns, nb.url)

	makeDevice(t, ns, "sw1", ethernet("eth0"), ethernet("Eth0"), ethernet("eth1"))

	eventually(t, "the device to report a child conflict", func() bool {
		return deviceCondition(ns, "sw1", netboxv1alpha1.ConditionChildrenReady).Reason ==
			netboxv1alpha1.ReasonConflict
	})

	message := deviceCondition(ns, "sw1", netboxv1alpha1.ConditionChildrenReady).Message
	for _, path := range []string{"spec.interfaces[eth0]", "spec.interfaces[Eth0]"} {
		if !strings.Contains(message, path) {
			t.Errorf("the Conflict message does not name %s, so a reader cannot tell which two "+
				"entries collided: %q", path, message)
		}
	}

	// Nothing at all, the third entry included.
	interfaces := &netboxv1alpha1.NetBoxInterfaceList{}
	if err := apiClient.List(context.Background(), interfaces, client.InNamespace(ns)); err != nil {
		t.Fatalf("listing interfaces: %v", err)
	}

	if len(interfaces.Items) != 0 {
		t.Errorf("%d interface CRs were written despite the collision; the collision check runs "+
			"before the first apply precisely so that none is", len(interfaces.Items))
	}

	if writes := len(nb.interfaces.recorded()); writes != 0 {
		t.Errorf("netbox took %d writes to dcim/interfaces despite the collision", writes)
	}
}

// TestDeviceInlineDoesNotHijackAHandWrittenName is the property that makes the whole sugar
// walk-backable: the materialiser never adopts and never overwrites, so it can never take over
// an object somebody else declared.
func TestDeviceInlineDoesNotHijackAHandWrittenName(t *testing.T) {
	ns := newNamespace(t)
	nb := newInlineNetBox(t)
	readyEndpoint(t, ns, nb.url)

	// Sitting exactly where the inline entry `eth0` would materialise, written by a human and
	// describing a different interface.
	squatter := &netboxv1alpha1.NetBoxInterface{
		ObjectMeta: metav1.ObjectMeta{Name: "sw1-eth0", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxInterfaceSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			DeviceRef:        netboxv1alpha1.DeviceRef{Name: "sw1"},
			Name:             "hand-written",
			Type:             "virtual",
		},
	}
	if err := k8sClient.Create(context.Background(), squatter); err != nil {
		t.Fatalf("creating the pre-existing interface: %v", err)
	}

	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), squatter) })

	makeDevice(t, ns, "sw1", ethernet("eth0"), ethernet("eth1"))

	eventually(t, "the device to report the occupied name", func() bool {
		return deviceCondition(ns, "sw1", netboxv1alpha1.ConditionChildrenReady).Reason ==
			netboxv1alpha1.ReasonConflict
	})

	message := deviceCondition(ns, "sw1", netboxv1alpha1.ConditionChildrenReady).Message
	if !strings.Contains(message, "sw1-eth0") || !strings.Contains(message, "unowned") {
		t.Errorf("the Conflict message does not say which object is in the way and that it is "+
			"unowned: %q", message)
	}

	live := fetchInterface(ns, "sw1-eth0")
	if live == nil {
		t.Fatal("the hand-written interface is gone")
	}

	// No PATCH, no label, no owner reference. Its spec is not the operator's to touch
	// (ADR-0005 §1), and the two markers are what a hand-written CR must never acquire by
	// accident.
	if live.Spec.Name != "hand-written" {
		t.Errorf("the hand-written spec.name is now %q; the materialiser overwrote it", live.Spec.Name)
	}

	if _, labelled := live.Labels[netboxv1alpha1.OwnerUIDLabel]; labelled {
		t.Error("the hand-written interface acquired the owner-uid label")
	}

	if controller := metav1.GetControllerOf(live); controller != nil {
		t.Errorf("the hand-written interface acquired a controller owner reference to %s %s",
			controller.Kind, controller.Name)
	}

	// The entry that did *not* collide still materialises: a Conflict is per entry, and a
	// device with one occupied name still converges the rest of its list.
	eventually(t, "the other entry to be materialised", func() bool {
		return fetchInterface(ns, "sw1-eth1") != nil
	})

	// It does *not* reach NetBox while the conflict stands, and that is worth writing down
	// because it is not obvious from either half on its own. ChildrenReady=False downgrades the
	// parent's Ready (ADR-0003 rule 5), and a child's declared `deviceRef` is a precondition for
	// its write that only resolves against a Ready target -- so the sibling reports
	// `RefsResolved=False, Reason=RefTargetFailed` carrying the device's own Conflict message,
	// and writes nothing. One occupied name therefore stalls the whole device's NetBox side
	// until somebody renames or deletes the object in the way, which is fail-closed and legible:
	// every stalled child names the cause.
	eventually(t, "the other entry to report why it cannot write", func() bool {
		other := fetchInterface(ns, "sw1-eth1")

		return other != nil && conditionReason(other.Status.Conditions,
			netboxv1alpha1.ConditionRefsResolved) == netboxv1alpha1.ReasonRefTargetFailed
	})

	if writes := len(nb.interfaces.recorded()); writes != 0 {
		t.Errorf("netbox took %d writes to dcim/interfaces while the device was not Ready", writes)
	}
}

// assertMaterialised checks the markers every materialised child carries, whatever its Kind.
//
// One helper for both Kinds because none of it is per-Kind: the two annotations the operator
// reads back, the label the pruner selects on, the standard managed-by label, the GitOps
// annotation ADR-0005 §5 ships on, and the controller owner reference. A child of a Kind added
// tomorrow carries the identical set.
func assertMaterialised(t *testing.T, child client.Object, parent *netboxv1alpha1.NetBoxDevice, path string) {
	t.Helper()

	labels := child.GetLabels()
	if labels[netboxv1alpha1.ManagedByLabel] != netboxv1alpha1.ManagedByValue {
		t.Errorf("%s carries managed-by %q", child.GetName(), labels[netboxv1alpha1.ManagedByLabel])
	}

	if labels[netboxv1alpha1.OwnerUIDLabel] != string(parent.UID) {
		t.Errorf("%s carries owner-uid %q, want the parent's uid %q -- the label the pruner "+
			"lists on", child.GetName(), labels[netboxv1alpha1.OwnerUIDLabel], parent.UID)
	}

	annotations := child.GetAnnotations()
	if annotations[netboxv1alpha1.OwnedByPathAnnotation] != path {
		t.Errorf("%s carries owned-by-path %q, want %q",
			child.GetName(), annotations[netboxv1alpha1.OwnedByPathAnnotation], path)
	}

	generated := "netboxdevice/" + parent.Namespace + "/" + parent.Name
	if annotations[netboxv1alpha1.GeneratedByAnnotation] != generated {
		t.Errorf("%s carries generated-by %q, want %q",
			child.GetName(), annotations[netboxv1alpha1.GeneratedByAnnotation], generated)
	}

	if annotations[netboxv1alpha1.ArgoCDCompareOptionsAnnotation] != netboxv1alpha1.ArgoCDIgnoreExtraneous {
		t.Errorf("%s is missing the Argo CD annotation; an Application holding the parent would "+
			"report OutOfSync forever", child.GetName())
	}

	controller := metav1.GetControllerOf(child)
	if controller == nil || controller.UID != parent.UID {
		t.Fatalf("%s has no controller owner reference to the device, so pruning could never "+
			"claim it", child.GetName())
	}

	if controller.BlockOwnerDeletion == nil || !*controller.BlockOwnerDeletion {
		t.Errorf("%s does not block owner deletion, which is what orders a foreground cascade",
			child.GetName())
	}

	// The materialiser's own field manager, which is what lets it own the fields it sets and
	// leave the rest -- and what makes ADR-0005 §1 checkable from outside.
	var managed bool

	for _, entry := range child.GetManagedFields() {
		if entry.Manager == reconciler.ChildFieldManager && entry.Subresource == "" {
			managed = true
		}

		if entry.Manager == reconciler.FieldManager && entry.Subresource == "" && entry.FieldsV1 != nil {
			if strings.Contains(entry.FieldsV1.GetRawString(), `"f:spec"`) {
				t.Errorf("%s has spec fields owned by %s, which would mean the operator wrote a "+
					"spec under its plain manager", child.GetName(), reconciler.FieldManager)
			}
		}
	}

	if !managed {
		t.Errorf("%s has no %s entry in managedFields", child.GetName(), reconciler.ChildFieldManager)
	}
}

// assertAssignedTo checks the object type NetBox recorded for the address's assignment.
func assertAssignedTo(t *testing.T, nb *inlineNetBox, objectType string) {
	t.Helper()

	for _, write := range nb.addresses.recorded() {
		if write.Method != http.MethodPost && write.Method != http.MethodPatch {
			continue
		}

		got, carried := write.Payload["assigned_object_type"]
		if !carried {
			continue
		}

		if got != objectType {
			t.Errorf("netbox was sent assigned_object_type %v, want %q", got, objectType)
		}

		return
	}

	t.Errorf("no write to %s ever carried assigned_object_type, so the address is unassigned "+
		"in NetBox", addressKind.endpoint)
}
