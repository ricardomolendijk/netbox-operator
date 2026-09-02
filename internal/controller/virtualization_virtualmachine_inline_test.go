package controller

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// The controller half of NBO-033, against a real API server: one manifest in, four objects
// out, and the `VM -> IPAddress -> VMInterface -> VM` ring closing on its own.
//
// The unit tests in api/v1alpha1 hold the shape of the declared tree and the engine tests in
// internal/reconciler hold the materialiser's decisions. What only an API server can answer is
// here: that the derived names are names it accepts, that server-side apply of the children is
// inert on a second pass, that the CEL rules reject what they are meant to, and that the
// deferred `primary_ip4` lands in exactly one PATCH.

// The three child endpoints an inline VM writes to, beside its own.
var (
	vmInterfaceKind = stubKind{endpoint: "virtualization/interfaces", key: "name"}
	virtualDiskKind = stubKind{endpoint: "virtualization/virtual-disks", key: "name"}
)

// vmTree is the four-endpoint NetBox an inline VM needs: one stub per kind, behind one server.
//
// The shared stub serves one endpoint by design -- it is parameterised by the kind under test
// and not by that kind's children -- and inline materialisation is the first feature whose
// whole point is that four kinds converge together. So this composes four of them rather than
// generalising the stub, which keeps every existing test's `recorded()` meaning "what the
// engine did to the kind under test".
type vmTree struct {
	vms        *netboxStubServer
	interfaces *netboxStubServer
	addresses  *netboxStubServer
	disks      *netboxStubServer
}

func (t *vmTree) all() []*netboxStubServer {
	return []*netboxStubServer{t.vms, t.interfaces, t.addresses, t.disks}
}

// newVMTreeStub returns the four stubs and the URL an endpoint points at.
//
// Each stub's ids start in its own thousand, so an assertion about "the address's id" cannot
// pass against the interface's by coincidence -- which is exactly the mistake the primary
// back-patch would make if it read the wrong child.
func newVMTreeStub(t *testing.T) (*vmTree, string) {
	t.Helper()

	tree := &vmTree{
		vms:        newTreeStub(t, virtualMachineKind, 1000),
		interfaces: newTreeStub(t, vmInterfaceKind, 2000),
		addresses:  newTreeStub(t, ipKind, 3000),
		disks:      newTreeStub(t, virtualDiskKind, 4000),
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// The same minimal dcim responder the VM tests already use, so an id-mode siteRef
		// resolves without this stub being able to serve a write to dcim/sites.
		if id, ok := dcimObjectID(r); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		for _, stub := range tree.all() {
			if strings.HasPrefix(r.URL.Path, "/api/"+stub.endpoint) {
				stub.route(w, r)

				return
			}
		}

		// /api/status/ and anything nobody claimed. The VM's stub answers the version probe
		// and 404s the rest, which is what makes an unexpected endpoint visible.
		tree.vms.route(w, r)
	}))
	t.Cleanup(srv.Close)

	for _, stub := range tree.all() {
		stub.url = srv.URL
	}

	return tree, srv.URL
}

// newTreeStub is newNetBoxStub without a server of its own: the composite above serves it.
func newTreeStub(t *testing.T, kind stubKind, firstID int64) *netboxStubServer {
	t.Helper()

	return &netboxStubServer{
		t: t, endpoint: kind.endpoint, key: kind.key,
		// Carried through for the same reason newNetBoxStub carries them: a composed stub
		// that dropped the extra filters would answer a two-column lookup with a one-column
		// match. Empty for every kind in this file, and not for
		// ipam.VLANTranslationRule's.
		refKeys: kind.refKeys, altKeys: kind.altKeys,
		objects: map[int64]netbox.Object{}, nextID: firstID,
	}
}

// inlineInterface is the `eth0` of docs/concepts/inline-children.md.
func inlineInterface(addresses ...netboxv1alpha1.InlineVMAddress) netboxv1alpha1.InlineVMInterface {
	mtu := int32(1500)

	return netboxv1alpha1.InlineVMInterface{
		Name: "eth0", MTU: &mtu, Description: "mgmt", Addresses: addresses,
	}
}

// withInlineTree is the mutation every test here starts from: one interface carrying one
// primary address, and one disk.
func withInlineTree(vm *netboxv1alpha1.NetBoxVirtualMachine) {
	vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
		inlineInterface(netboxv1alpha1.InlineVMAddress{Address: "10.20.0.10/24", Primary: true}),
	}
	vm.Spec.Disks = []netboxv1alpha1.InlineVirtualDisk{{Name: "scsi0", Size: 20}}
}

// childrenOf is the VM's status.children keyed by path, which is the record the pruner and the
// finalizer both read.
func childrenOf(ns, name string) map[string]netboxv1alpha1.ChildStatus {
	vm := fetchVirtualMachine(ns, name)
	if vm == nil {
		return nil
	}

	out := make(map[string]netboxv1alpha1.ChildStatus, len(vm.Status.Children))
	for _, child := range vm.Status.Children {
		out[child.Path] = child
	}

	return out
}

// childrenReadyOf is the VM's ChildrenReady condition, or the zero value when the engine has
// not set it -- which is also what a missing object reads as, since both mean "not that status
// yet" to every caller here.
func childrenReadyOf(ns, name string) metav1.Condition {
	vm := fetchVirtualMachine(ns, name)
	if vm == nil {
		return metav1.Condition{}
	}

	found := apimeta.FindStatusCondition(vm.Status.Conditions, netboxv1alpha1.ConditionChildrenReady)
	if found == nil {
		return metav1.Condition{}
	}

	return *found
}

// TestVirtualMachineMaterialisesItsInlineChildren is the ticket's first acceptance criterion:
// one manifest, three child CRs at their derived names, each carrying the markers ADR-0005 §2
// asks for and a controller owner reference to the VM.
func TestVirtualMachineMaterialisesItsInlineChildren(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the virtual machine to materialise three children", func() bool {
		return len(childrenOf(ns, "dns")) == 3
	})

	vm := fetchVirtualMachine(ns, "dns")

	want := map[string]struct{ kind, name string }{
		"spec.interfaces[eth0]": {"NetBoxVMInterface", "dns-eth0"},
		"spec.interfaces[eth0].addresses[10.20.0.10/24]": {
			"NetBoxIPAddress", "dns-eth0-ip-10-20-0-10-24",
		},
		"spec.disks[scsi0]": {"NetBoxVirtualDisk", "dns-disk-scsi0"},
	}

	for path, expected := range want {
		child, declared := childrenOf(ns, "dns")[path]
		if !declared {
			t.Errorf("status.children carries no entry for %s", path)

			continue
		}

		if child.Kind != expected.kind || child.Name != expected.name {
			t.Errorf("%s materialised %s/%s, want %s/%s",
				path, child.Kind, child.Name, expected.kind, expected.name)
		}

		assertVMChildMarkers(t, ns, child.Kind, child.Name, path, vm)
	}
}

// assertVMChildMarkers holds everything the materialiser owns on one child: the two markers, the
// managed-by label, the Argo CD annotation and the controller owner reference -- read through
// the API server rather than the cache, so it is what was persisted.
func assertVMChildMarkers(
	t *testing.T, ns, kind, name, path string, parent *netboxv1alpha1.NetBoxVirtualMachine,
) {
	t.Helper()

	child := &metav1.PartialObjectMetadata{}
	child.SetGroupVersionKind(netboxv1alpha1.GroupVersion.WithKind(kind))

	if err := apiClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, child); err != nil {
		t.Fatalf("fetching %s %s/%s: %v", kind, ns, name, err)
	}

	labels, annotations := child.GetLabels(), child.GetAnnotations()

	for key, want := range map[string]string{
		netboxv1alpha1.ManagedByLabel: netboxv1alpha1.ManagedByValue,
		netboxv1alpha1.OwnerUIDLabel:  string(parent.GetUID()),
	} {
		if labels[key] != want {
			t.Errorf("%s %s label %s = %q, want %q; the pruner selects on the uid label",
				kind, name, key, labels[key], want)
		}
	}

	for key, want := range map[string]string{
		netboxv1alpha1.OwnedByPathAnnotation:          path,
		netboxv1alpha1.GeneratedByAnnotation:          fmt.Sprintf("netboxvirtualmachine/%s/%s", ns, parent.GetName()),
		netboxv1alpha1.ArgoCDCompareOptionsAnnotation: netboxv1alpha1.ArgoCDIgnoreExtraneous,
	} {
		if annotations[key] != want {
			t.Errorf("%s %s annotation %s = %q, want %q", kind, name, key, annotations[key], want)
		}
	}

	owner := metav1.GetControllerOf(child)
	switch {
	case owner == nil:
		t.Errorf("%s %s has no controller owner reference, so it neither cascades nor prunes", kind, name)
	case owner.UID != parent.GetUID():
		t.Errorf("%s %s is controlled by uid %s, want the VM's %s", kind, name, owner.UID, parent.GetUID())
	case owner.BlockOwnerDeletion == nil || !*owner.BlockOwnerDeletion:
		t.Errorf("%s %s does not set blockOwnerDeletion", kind, name)
	}
}

// TestVirtualMachineInlineChildrenInheritTheEndpoint is the two fields a child takes from its
// parent, and the three it must not: inheriting free text or a tag set would make a drift
// report lie about where the value came from.
func TestVirtualMachineInlineChildrenInheritTheEndpoint(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
		withInlineTree(vm)
		vm.Spec.DeletionPolicy = netboxv1alpha1.DeletionDelete
		vm.Spec.Description = "the VM's own description"
	})

	eventually(t, "the interface child to exist", func() bool {
		return fetchVMInterface(ns, "dns-eth0") != nil
	})

	iface := fetchVMInterface(ns, "dns-eth0")

	if iface.Spec.EndpointRef != "homelab" {
		t.Errorf("the interface child's endpointRef = %q, want the parent's homelab", iface.Spec.EndpointRef)
	}

	if iface.Spec.DeletionPolicy != netboxv1alpha1.DeletionDelete {
		t.Errorf("the interface child's deletionPolicy = %q, want the parent's Delete",
			iface.Spec.DeletionPolicy)
	}

	if iface.Spec.Description != "mgmt" {
		t.Errorf("the interface child's description = %q, want its own inline entry's `mgmt` "+
			"rather than the parent's", iface.Spec.Description)
	}

	if iface.Spec.VirtualMachineRef.Name != "dns" {
		t.Errorf("the interface child's virtualMachineRef = %+v, want name dns",
			iface.Spec.VirtualMachineRef)
	}
}

// TestVirtualMachineInlineAddressIsAssignedToItsInterface is the criterion that proves the
// sibling reference resolves: the materialised address's `assignedObject` has to reach NetBox
// as `virtualization.vminterface` plus the interface child's own id.
func TestVirtualMachineInlineAddressIsAssignedToItsInterface(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the address child to be written to netbox", func() bool {
		return len(tree.addresses.recorded()) > 0
	})

	iface := fetchVMInterface(ns, "dns-eth0")
	if iface == nil || iface.Status.ID == 0 {
		t.Fatal("the interface child has no netbox id, so the address could not have resolved it")
	}

	post := tree.addresses.recorded()[0]

	if post.Method != http.MethodPost {
		t.Fatalf("the first address request was %s, want POST", post.Method)
	}

	if got := post.Payload["assigned_object_type"]; got != "virtualization.vminterface" {
		t.Errorf("assigned_object_type = %v, want virtualization.vminterface", got)
	}

	if got, _ := netbox.IntOf(post.Payload["assigned_object_id"]); int64(got) != iface.Status.ID {
		t.Errorf("assigned_object_id = %v, want the interface child's id %d",
			post.Payload["assigned_object_id"], iface.Status.ID)
	}
}

// TestVirtualMachineInlinePrimaryLandsInOneDeferredPatch is requirement 7 of the ticket, and
// the reason `primary_ip4` is deferred at all.
//
// The ring is `VM -> IPAddress -> VMInterface -> VM`, and no apply order breaks it: the VM's
// POST therefore carries no `primary_ip4`, the children are materialised once the VM has an id,
// and the follow-up PATCH lands once the address child has one. Exactly one PATCH, and none
// afterwards -- a second would mean the differ is comparing a column the create never sent, or
// re-sending a value NetBox already holds, which is the loop docs/concepts/drift.md opens with.
func TestVirtualMachineInlinePrimaryLandsInOneDeferredPatch(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the virtual machine to be Ready with its primary address applied", func() bool {
		vm := fetchVirtualMachine(ns, "dns")

		return vm != nil && len(vm.Status.DeferredPending) == 0 && virtualMachineIsReady(ns, "dns")
	})

	address := fetchIP(ns, "dns-eth0-ip-10-20-0-10-24")
	if address == nil || address.Status.ID == 0 {
		t.Fatal("the address child has no netbox id")
	}

	writes := tree.vms.recorded()
	if len(writes) < 2 {
		t.Fatalf("the VM took %d writes, want a POST and one deferred PATCH: %+v", len(writes), writes)
	}

	if _, carried := writes[0].Payload["primary_ip4"]; carried {
		t.Errorf("the POST carries primary_ip4: %v", writes[0].Payload)
	}

	patches := 0

	for _, write := range writes[1:] {
		if write.Method != http.MethodPatch {
			t.Errorf("an unexpected %s reached virtualization/virtual-machines: %v",
				write.Method, write.Payload)

			continue
		}

		patches++

		if got, _ := netbox.IntOf(write.Payload["primary_ip4"]); int64(got) != address.Status.ID {
			t.Errorf("the deferred PATCH set primary_ip4 = %v, want the address child's id %d",
				write.Payload["primary_ip4"], address.Status.ID)
		}
	}

	if patches != 1 {
		t.Errorf("the VM took %d PATCHes, want exactly 1: every extra one is a resync writing "+
			"a value netbox already holds", patches)
	}

	// The VM's own condition is the other half of "one convergence": Ready=True means the VM
	// *and* its three children, and status.children says so per child.
	for path, child := range childrenOf(ns, "dns") {
		if !child.Ready {
			t.Errorf("%s reports Ready=false while the VM reports Ready=true", path)
		}
	}
}

// TestVirtualMachineIsNotReadyWhileAChildIsNot is the `kubectl wait` promise: a VM whose
// interface cannot be written is not Ready, however healthy the VM's own row is.
func TestVirtualMachineIsNotReadyWhileAChildIsNot(t *testing.T) {
	ns := newNamespace(t)
	tree, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	// The interface's POST is refused, so that child never reaches Ready. The VM's own row is
	// created normally, which is what makes the assertion about the parent's gating rather than
	// about a broken endpoint.
	tree.interfaces.createStatus = http.StatusBadRequest

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the VM to report its children are not ready", func() bool {
		return childrenReadyOf(ns, "dns").Status == metav1.ConditionFalse
	})

	if virtualMachineIsReady(ns, "dns") {
		t.Error("the VM reports Ready=true while its interface child is not Ready")
	}

	if got := childrenReadyOf(ns, "dns").Reason; got != netboxv1alpha1.ReasonPendingChildren {
		t.Errorf("ChildrenReady reason = %s, want %s", got, netboxv1alpha1.ReasonPendingChildren)
	}
}

// TestVirtualMachinePrunesARemovedEntry is the middle row of pruning's three cases, and the
// scope assertion with it: removing the disk entry deletes exactly its child and touches
// neither the interface nor the address.
func TestVirtualMachinePrunesARemovedEntry(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the disk child to exist", func() bool {
		return fetchVirtualDisk(ns, "dns-disk-scsi0") != nil
	})

	address := fetchIP(ns, "dns-eth0-ip-10-20-0-10-24")
	if address == nil {
		t.Fatal("the address child was never materialised")
	}

	// metadata.generation rather than resourceVersion, and the difference is not pedantry: the
	// address child has its own controller writing its own status, and a status write bumps
	// resourceVersion. Generation moves only when the *spec* does, which is exactly the
	// question -- did the materialiser rewrite a child it was not asked to touch. The
	// resourceVersion form of the same claim, isolated from any controller, is
	// TestChildApplyIsIdempotent in children_test.go.
	kept := address.GetGeneration()

	vm := fetchVirtualMachine(ns, "dns")
	vm.Spec.Disks = nil

	if err := k8sClient.Update(context.Background(), vm); err != nil {
		t.Fatalf("removing the disk entry: %v", err)
	}

	eventually(t, "the disk child to be pruned", func() bool {
		return fetchVirtualDisk(ns, "dns-disk-scsi0") == nil
	})

	if fetchVMInterface(ns, "dns-eth0") == nil {
		t.Error("pruning the disk took the interface child with it")
	}

	again := fetchIP(ns, "dns-eth0-ip-10-20-0-10-24")
	if again == nil {
		t.Fatal("pruning the disk took the address child with it")
	}

	if again.GetGeneration() != kept {
		t.Errorf("the address child's generation moved from %d to %d while an unrelated entry "+
			"was removed, so its spec was rewritten by a pass that had nothing to say about it",
			kept, again.GetGeneration())
	}

	if _, still := childrenOf(ns, "dns")["spec.disks[scsi0]"]; still {
		t.Error("status.children still records the pruned disk")
	}
}

// TestVirtualMachineNeverHijacksAHandWrittenChild is the third row, and the property that
// makes the whole sugar walk-backable: a CR already at a derived name is never adopted, never
// patched and never labelled, and the *parent* is what reports it. The other entries still
// materialise, because one conflicting entry is not a reason to stop declaring the rest.
func TestVirtualMachineNeverHijacksAHandWrittenChild(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	// A hand-written interface CR sitting at exactly the name the inline entry derives.
	squatter := &netboxv1alpha1.NetBoxVMInterface{
		ObjectMeta: metav1.ObjectMeta{Name: "dns-eth0", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxVMInterfaceSpec{
			NetBoxObjectSpec:  netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			VirtualMachineRef: netboxv1alpha1.VirtualMachineRef{Name: "dns"},
			Name:              "written-by-a-human",
		},
	}
	if err := k8sClient.Create(context.Background(), squatter); err != nil {
		t.Fatalf("creating the hand-written interface: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), squatter) })

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the VM to report the collision", func() bool {
		return childrenReadyOf(ns, "dns").Reason ==
			netboxv1alpha1.ReasonConflict
	})

	message := childrenReadyOf(ns, "dns").Message
	for _, want := range []string{"spec.interfaces[eth0]", "dns-eth0", "unowned"} {
		if !strings.Contains(message, want) {
			t.Errorf("the Conflict message does not mention %q: %q", want, message)
		}
	}

	live := fetchVMInterface(ns, "dns-eth0")
	if live == nil {
		t.Fatal("the hand-written interface is gone")
	}

	if live.Spec.Name != "written-by-a-human" {
		t.Errorf("the hand-written interface's spec.name is now %q: it was overwritten", live.Spec.Name)
	}

	if _, labelled := live.GetLabels()[netboxv1alpha1.OwnerUIDLabel]; labelled {
		t.Error("the hand-written interface was labelled as ours")
	}

	if metav1.GetControllerOf(live) != nil {
		t.Error("the hand-written interface was given a controller owner reference")
	}

	// The disk entry did not collide with anything, so it is still materialised: a Conflict on
	// one entry stops that entry and nothing else.
	eventually(t, "the disk child to be materialised anyway", func() bool {
		return fetchVirtualDisk(ns, "dns-disk-scsi0") != nil
	})
}

// TestVirtualMachineLeavesALonghandInterfaceAlone is the two support questions the feature
// generates, in one test: a hand-written NetBoxVMInterface pointing at the VM is not
// materialised, is never pruned, and never appears in status.children -- while the inline one
// beside it is all three.
func TestVirtualMachineLeavesALonghandInterfaceAlone(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", withInlineTree)

	eventually(t, "the inline interface to be materialised", func() bool {
		return fetchVMInterface(ns, "dns-eth0") != nil
	})

	longhand := &netboxv1alpha1.NetBoxVMInterface{
		ObjectMeta: metav1.ObjectMeta{Name: "extra-nic", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxVMInterfaceSpec{
			NetBoxObjectSpec:  netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			VirtualMachineRef: netboxv1alpha1.VirtualMachineRef{Name: "dns"},
			Name:              "eth9",
		},
	}
	if err := k8sClient.Create(context.Background(), longhand); err != nil {
		t.Fatalf("creating the longhand interface: %v", err)
	}
	t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), longhand) })

	// Force a materialisation pass with the longhand object in place, by editing the VM.
	vm := fetchVirtualMachine(ns, "dns")
	vm.Spec.Description = "touched"

	if err := k8sClient.Update(context.Background(), vm); err != nil {
		t.Fatalf("touching the VM: %v", err)
	}

	eventually(t, "the VM to reconcile again", func() bool {
		return fetchVirtualMachine(ns, "dns").Status.ObservedGeneration >= vm.Generation
	})

	if fetchVMInterface(ns, "extra-nic") == nil {
		t.Error("the longhand interface was pruned; only materialised children are ever pruned")
	}

	for path, child := range childrenOf(ns, "dns") {
		if child.Name == "extra-nic" {
			t.Errorf("status.children claims authorship of the longhand interface at %s", path)
		}
	}
}

// TestVirtualMachineInlineClaimFromMaterialisesAClaim is ADR-0004's inline form: `claimFrom`
// materialises a real NetBoxIPAddressClaim child, which is the same Kind, the same controller
// and the same allocation path as one written by hand.
//
// The two inherited fields are the assertion, and `endpointRef` is the load-bearing one: it
// carries MinLength=1, so a claim child that inherited nothing would be refused by the API
// server rather than merely under-configured -- which is what NBO-033 found missing in the
// materialiser's inheritance step.
func TestVirtualMachineInlineClaimFromMaterialisesAClaim(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
		vm.Spec.DeletionPolicy = netboxv1alpha1.DeletionDelete
		vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
			inlineInterface(netboxv1alpha1.InlineVMAddress{
				ClaimFrom: &netboxv1alpha1.InlineAddressClaim{
					PrefixRef: netboxv1alpha1.PrefixRef{Name: "mgmt-net"},
				},
			}),
		}
	})

	eventually(t, "the claim child to be materialised", func() bool {
		return fetchIPAddressClaim(ns, "dns-eth0-claim-mgmt-net") != nil
	})

	claim := fetchIPAddressClaim(ns, "dns-eth0-claim-mgmt-net")

	if claim.Spec.EndpointRef != "homelab" {
		t.Errorf("the claim child's endpointRef = %q, want the parent's homelab", claim.Spec.EndpointRef)
	}

	if claim.Spec.DeletionPolicy != netboxv1alpha1.DeletionDelete {
		t.Errorf("the claim child's deletionPolicy = %q, want the parent's Delete: the chain "+
			"`VM deleted -> claim deleted -> netbox address freed` is what makes an inline "+
			"allocation not leak", claim.Spec.DeletionPolicy)
	}

	if claim.Spec.PrefixRef.Name != "mgmt-net" {
		t.Errorf("the claim child's prefixRef = %+v, want name mgmt-net", claim.Spec.PrefixRef)
	}

	if owner := metav1.GetControllerOf(claim); owner == nil || owner.Kind != "NetBoxVirtualMachine" {
		t.Errorf("the claim child's controller owner reference is %+v, want the VM's", owner)
	}

	if _, declared := childrenOf(ns, "dns")["spec.interfaces[eth0].addresses[mgmt-net]"]; !declared {
		t.Errorf("status.children does not record the claim: %v", childrenOf(ns, "dns"))
	}
}

// TestVirtualMachineInlineAdmissionRules is the layer-1 half of the primary rules, plus the two
// shapes an inline address may not take. Every case here is rejected by the API server, so it
// never reaches a controller at all.
func TestVirtualMachineInlineAdmissionRules(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	cases := []struct {
		name   string
		mutate func(*netboxv1alpha1.NetBoxVirtualMachine)
		want   string
	}{{
		name: "two interfaces named eth0",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(), inlineInterface(),
			}
		},
		want: `Duplicate value: {"name":"eth0"}`,
	}, {
		name: "an address that states neither an address nor a claimFrom",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(netboxv1alpha1.InlineVMAddress{Status: netboxv1alpha1.IPAddressStatusActive}),
			}
		},
		want: "exactly one of address or claimFrom",
	}, {
		name: "an address that states both",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(netboxv1alpha1.InlineVMAddress{
					Address: "10.20.0.10/24",
					ClaimFrom: &netboxv1alpha1.InlineAddressClaim{
						PrefixRef: netboxv1alpha1.PrefixRef{Name: "mgmt-net"},
					},
				}),
			}
		},
		want: "exactly one of address or claimFrom",
	}, {
		name: "a claimFrom marked primary",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(netboxv1alpha1.InlineVMAddress{
					Primary: true,
					ClaimFrom: &netboxv1alpha1.InlineAddressClaim{
						PrefixRef: netboxv1alpha1.PrefixRef{Name: "mgmt-net"},
					},
				}),
			}
		},
		want: "primary needs a literal address",
	}, {
		name: "a claimFrom that also describes the address it would get",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(netboxv1alpha1.InlineVMAddress{
					DNSName: "dns.home.arpa",
					ClaimFrom: &netboxv1alpha1.InlineAddressClaim{
						PrefixRef: netboxv1alpha1.PrefixRef{Name: "mgmt-net"},
					},
				}),
			}
		},
		want: "describe an address, not a request for one",
	}, {
		name: "two primary IPv4 addresses on one interface",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(
					netboxv1alpha1.InlineVMAddress{Address: "10.20.0.10/24", Primary: true},
					netboxv1alpha1.InlineVMAddress{Address: "10.20.0.11/24", Primary: true},
				),
			}
		},
		want: "at most one inline IPv4 address per interface",
	}, {
		name: "two primary IPv4 addresses on two interfaces",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(netboxv1alpha1.InlineVMAddress{Address: "10.20.0.10/24", Primary: true}),
				{
					Name: "eth1",
					Addresses: []netboxv1alpha1.InlineVMAddress{
						{Address: "10.20.1.10/24", Primary: true},
					},
				},
			}
		},
		want: "primary_ip4 has one source",
	}, {
		name: "an explicit primaryIP4Ref beside an inline primary",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			withInlineTree(vm)
			vm.Spec.PrimaryIP4Ref = &netboxv1alpha1.IPAddressRef{Name: "somebody-elses"}
		},
		want: "primary_ip4 has one source",
	}, {
		name: "two primary IPv6 addresses",
		mutate: func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
			vm.Spec.Interfaces = []netboxv1alpha1.InlineVMInterface{
				inlineInterface(
					netboxv1alpha1.InlineVMAddress{Address: "2001:db8::10/64", Primary: true},
					netboxv1alpha1.InlineVMAddress{Address: "2001:db8::11/64", Primary: true},
				),
			}
		},
		want: "at most one inline IPv6 address per interface",
	}}

	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			vm := &netboxv1alpha1.NetBoxVirtualMachine{
				ObjectMeta: metav1.ObjectMeta{Name: fmt.Sprintf("refused-%d", i), Namespace: ns},
				Spec: netboxv1alpha1.NetBoxVirtualMachineSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					Name:             fmt.Sprintf("refused-%d", i),
					SiteRef:          &netboxv1alpha1.SiteRef{ID: idOf(41)},
				},
			}
			tc.mutate(vm)

			err := k8sClient.Create(context.Background(), vm)
			if err == nil {
				t.Cleanup(func() { _ = k8sClient.Delete(context.Background(), vm) })
				t.Fatalf("the api server accepted %s", tc.name)
			}

			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the rejection does not mention %q: %v", tc.want, err)
			}
		})
	}
}

// TestVirtualMachineInlineAddressHasNoDuplicateFlag is issue #167 at the schema level: the
// field a materialised address must never carry is one the CRD does not accept, so a manifest
// asking for it is refused before any controller sees it.
func TestVirtualMachineInlineAddressHasNoDuplicateFlag(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	// Written as unstructured, because the Go type cannot express the field -- which is the
	// point being asserted, and the reason the assertion has to bypass it.
	vm := unstructuredVM(ns, "duplicate-asked", map[string]any{
		"interfaces": []any{map[string]any{
			"name": "eth0",
			"addresses": []any{map[string]any{
				"address":        "10.20.0.10/24",
				"allowDuplicate": true,
			}},
		}},
	})

	// Strict field validation, which is what a server-side apply sends. A CRD's structural
	// schema *prunes* an unknown field by default, so a lenient client would have the field
	// silently dropped instead: the same outcome for the object, and a much worse one for
	// whoever wrote the manifest.
	err := apiClient.Create(context.Background(), vm, client.FieldValidation("Strict"))
	if err == nil {
		t.Cleanup(func() { _ = apiClient.Delete(context.Background(), vm) })
		t.Fatal("the api server accepted allowDuplicate on an inline address: a stamped child " +
			"that loses status.id creates a second netbox address (issue #167)")
	}

	if !apierrors.IsBadRequest(err) && !apierrors.IsInvalid(err) {
		t.Errorf("allowDuplicate was refused for the wrong reason: %v", err)
	}

	if !strings.Contains(err.Error(), "allowDuplicate") {
		t.Errorf("the rejection does not name allowDuplicate: %v", err)
	}
}

// TestVirtualMachineInlineAddressHasNoFromPrefixRef is the acceptance criterion that names the
// spelling this API does *not* have. ADR-0004 settled on the nested `claimFrom`, so
// `fromPrefixRef` has to be an unknown field rather than an accepted synonym.
func TestVirtualMachineInlineAddressHasNoFromPrefixRef(t *testing.T) {
	ns := newNamespace(t)
	_, target := newVMTreeStub(t)
	readyEndpoint(t, ns, target)

	vm := unstructuredVM(ns, "from-prefix-ref", map[string]any{
		"interfaces": []any{map[string]any{
			"name": "eth0",
			"addresses": []any{map[string]any{
				"fromPrefixRef": map[string]any{"name": "mgmt-net"},
			}},
		}},
	})

	err := apiClient.Create(context.Background(), vm, client.FieldValidation("Strict"))
	if err == nil {
		t.Cleanup(func() { _ = apiClient.Delete(context.Background(), vm) })
		t.Fatal("the api server accepted fromPrefixRef; the inline key is claimFrom (ADR-0004)")
	}

	if !strings.Contains(err.Error(), "fromPrefixRef") &&
		!strings.Contains(err.Error(), "exactly one of address or claimFrom") {
		t.Errorf("fromPrefixRef was refused for an unrelated reason: %v", err)
	}
}

// fetchVMInterface, fetchVirtualDisk and fetchIPAddressClaim read a materialised child, and a
// missing one reads as nil -- which is what "pruned" looks like to every caller here.
func fetchVMInterface(ns, name string) *netboxv1alpha1.NetBoxVMInterface {
	iface := &netboxv1alpha1.NetBoxVMInterface{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, iface); err != nil {
		return nil
	}

	return iface
}

func fetchVirtualDisk(ns, name string) *netboxv1alpha1.NetBoxVirtualDisk {
	disk := &netboxv1alpha1.NetBoxVirtualDisk{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, disk); err != nil {
		return nil
	}

	return disk
}

func fetchIPAddressClaim(ns, name string) *netboxv1alpha1.NetBoxIPAddressClaim {
	claim := &netboxv1alpha1.NetBoxIPAddressClaim{}
	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, claim); err != nil {
		return nil
	}

	return claim
}

// unstructuredVM is a VM built as unstructured, for the two assertions about fields the Go
// type deliberately cannot express: `allowDuplicate` on an inline address and `fromPrefixRef`.
// A typed client could not send either, which is the property being tested and the reason the
// test has to go round it.
func unstructuredVM(ns, name string, spec map[string]any) *unstructured.Unstructured {
	body := map[string]any{
		"endpointRef": "homelab",
		"name":        name,
		"siteRef":     map[string]any{"id": int64(41)},
	}
	for key, value := range spec {
		body[key] = value
	}

	vm := &unstructured.Unstructured{Object: map[string]any{"spec": body}}
	vm.SetGroupVersionKind(netboxv1alpha1.GroupVersion.WithKind("NetBoxVirtualMachine"))
	vm.SetNamespace(ns)
	vm.SetName(name)

	return vm
}
