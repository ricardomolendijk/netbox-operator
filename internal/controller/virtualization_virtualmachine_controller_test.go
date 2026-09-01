package controller

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// virtualMachineKind points the shared stub at virtualization.VirtualMachine.
var virtualMachineKind = stubKind{endpoint: "virtualization/virtual-machines", key: "name"}

// unwritableVMColumns are the keys that must never appear in a request body sent to
// `virtualization/virtual-machines`, for three different reasons.
//
// `primary_ip4` and `primary_ip6` are deferred: the ring `VM -> IPAddress -> VMInterface -> VM`
// means neither can resolve on a create, so both are stripped from it and applied by a later
// PATCH. `interface_count` and `virtual_disk_count` are CounterCacheFields NetBox maintains
// from the child rows and drops on write, so sending one is a PATCH repeated on every resync
// forever. `mac_address` and `primary_mac_address` are not columns on this model at all --
// NetBox 4.2 moved the MAC to dcim.MACAddress -- and NetBox ignores a field name it does not
// know rather than rejecting it, so a write containing one reports success and sets nothing.
var unwritableVMColumns = []string{
	"primary_ip4", "primary_ip6",
	"interface_count", "virtual_disk_count",
	"mac_address", "primary_mac_address",
}

// newVMNetBoxStub is the VM stub fronted by the same minimal dcim responder the prefix tests
// use, so an id-mode `siteRef` is verifiable.
//
// The shared stub serves one endpoint by design -- it is parameterised by the kind under
// test, not by that kind's references -- and a VM's `site` lives at `dcim/sites`. This adds
// the smallest thing that makes an id-mode ref resolvable and nothing that can serve a write,
// so a test that accidentally started managing a Site through this path fails rather than
// passing quietly.
func newVMNetBoxStub(t *testing.T) (*netboxStubServer, string) {
	t.Helper()

	stub, _ := newNetBoxStub(t, virtualMachineKind)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id, ok := dcimObjectID(r); ok {
			writeStubJSON(w, http.StatusOK, netbox.Object{"id": float64(id), "url": r.URL.Path})

			return
		}

		stub.route(w, r)
	}))
	t.Cleanup(srv.Close)

	return stub, srv.URL
}

// makeVirtualMachine applies a NetBoxVirtualMachine and removes it afterwards so the
// finalizer does not outlive the stub it needs in order to come off.
//
// The default host is an id-mode `siteRef`, which is the one of the three the CEL rule accepts
// that resolves in this build: NetBoxCluster is NBO-028 and NetBoxDevice is NBO-030.
func makeVirtualMachine(t *testing.T, ns, name string, mutate func(*netboxv1alpha1.NetBoxVirtualMachine)) {
	t.Helper()

	vm := &netboxv1alpha1.NetBoxVirtualMachine{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: netboxv1alpha1.NetBoxVirtualMachineSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             name,
			SiteRef:          &netboxv1alpha1.SiteRef{ID: idOf(41)},
		},
	}
	if mutate != nil {
		mutate(vm)
	}
	if err := k8sClient.Create(context.Background(), vm); err != nil {
		t.Fatalf("creating virtual machine %s/%s: %v", ns, name, err)
	}

	t.Cleanup(func() { removeObject(t, vm) })
}

func fetchVirtualMachine(ns, name string) *netboxv1alpha1.NetBoxVirtualMachine {
	vm := &netboxv1alpha1.NetBoxVirtualMachine{}
	if err := k8sClient.Get(context.Background(), client.ObjectKey{Namespace: ns, Name: name}, vm); err != nil {
		return nil
	}

	return vm
}

func virtualMachineIsReady(ns, name string) bool {
	vm := fetchVirtualMachine(ns, name)
	if vm == nil {
		return false
	}
	for _, c := range vm.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}

func virtualMachineRefsReason(ns, name string) string {
	vm := fetchVirtualMachine(ns, name)
	if vm == nil {
		return ""
	}
	for _, c := range vm.Status.Conditions {
		if c.Type == netboxv1alpha1.ConditionRefsResolved {
			return c.Reason
		}
	}

	return ""
}

// TestVirtualMachineCreateOmitsTheDeferredAndDerivedColumns is the payload assertion, made
// against what was actually sent rather than against a condition.
//
// A VM reaches Ready with no `primaryIP4Ref` at all, so `status.deferredPending` is empty and
// the create carries neither address column -- which is the same body a VM *with* an
// unresolved address would send, and the reason a first pass that included them could never
// succeed. The counter caches and the MAC columns must be absent from every request, POST and
// PATCH alike.
func TestVirtualMachineCreateOmitsTheDeferredAndDerivedColumns(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newVMNetBoxStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
		vm.Spec.Status = netboxv1alpha1.VirtualMachineStatusActive
		vm.Spec.StartOnBoot = netboxv1alpha1.VirtualMachineStartOnBootOn
		vm.Spec.VCPUs = "2"
		vm.Spec.Description = "Recursive resolver"
	})

	eventually(t, "the virtual machine to be Ready", func() bool { return virtualMachineIsReady(ns, "dns") })

	writes := stub.recorded()
	if len(writes) == 0 {
		t.Fatal("no request was recorded, so this assertion proves nothing")
	}

	for i, write := range writes {
		for _, column := range unwritableVMColumns {
			if _, present := write.Payload[column]; present {
				t.Errorf("request %d (%s) carries %q: %v", i, write.Method, column, write.Payload)
			}
		}
	}

	// The positive half: the columns that *must* be there, including the two whose spec name
	// a camelCase convention would get wrong.
	post := writes[0]
	if post.Method != http.MethodPost {
		t.Fatalf("the first request was %s, want POST", post.Method)
	}

	for column, want := range map[string]any{
		"name": "dns", "status": "active", "start_on_boot": "on",
		"vcpus": "2", "site": float64(41), "description": "Recursive resolver",
	} {
		if got := post.Payload[column]; got != want {
			t.Errorf("POST %q = %v, want %v (whole body: %v)", column, got, want, post.Payload)
		}
	}

	// Nothing is pending, because nothing was deferred *and* declared. A non-empty list here
	// would mean the engine had reported an outstanding write it was never asked to make.
	if pending := fetchVirtualMachine(ns, "dns").Status.DeferredPending; len(pending) != 0 {
		t.Errorf("status.deferredPending = %v on a VM with no primary addresses, want empty", pending)
	}
}

// TestVirtualMachineWaitsForAnUnavailableCluster is the coordination assertion, and the
// expected outcome is a refusal rather than a workaround.
//
// NetBoxCluster is NBO-028 and is not in this build, so a name-mode `clusterRef` reports
// `RefKindUnavailable`: the manifest is correct and the operator is short a Kind. What matters
// beyond the condition is that **nothing is written**. `clusterRef` is declared, so every
// candidate that pins `cluster_id` to null is inapplicable and every candidate that matches on
// it is unresolved -- so the engine cannot establish identity and must not guess. Falling
// through to the site-only candidate would adopt whatever VM of that name is at the site and
// then PATCH a cluster onto it.
func TestVirtualMachineWaitsForAnUnavailableCluster(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newVMNetBoxStub(t)
	readyEndpoint(t, ns, target)

	makeVirtualMachine(t, ns, "dns", func(vm *netboxv1alpha1.NetBoxVirtualMachine) {
		vm.Spec.ClusterRef = &netboxv1alpha1.ClusterRef{Name: "proxmox-home"}
	})

	// NBO-028 landed the cluster Kinds while this branch was in flight, so `clusterRef` now
	// has a Descriptor and no longer reports RefKindUnavailable. The property under test is
	// unchanged and is the one that matters -- a declared reference the engine cannot resolve
	// must not be guessed past -- so the reason is now RefNotFound: the Kind exists and the
	// named cluster does not.
	eventually(t, "the VM to report the unresolvable cluster", func() bool {
		return virtualMachineRefsReason(ns, "dns") == netboxv1alpha1.ReasonRefNotFound
	})

	if virtualMachineIsReady(ns, "dns") {
		t.Error("the VM is Ready with a cluster that was never written")
	}

	for i, write := range stub.recorded() {
		t.Errorf("request %d (%s) was sent for a VM whose identity cannot be established: %v",
			i, write.Method, write.Payload)
	}
}

// TestVirtualMachineCELRequiresAHost is the rule the schema digest cannot express.
//
// `site`, `cluster` and `device` are all nullable columns, and NetBox's own `clean()` requires
// at least one of the three (`netbox/virtualization/models/virtualmachines.py` lines 291-295,
// NetBox 4.6.8). Three, not the two NBO-029's spec table names: a VM pinned to a standalone
// host device with no cluster and no site is legal in NetBox, and rejecting it at admission
// would make a working manifest un-appliable.
func TestVirtualMachineCELRequiresAHost(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		name       string
		mutate     func(*netboxv1alpha1.NetBoxVirtualMachineSpec)
		wantReject string
	}{
		{
			name:       "no host at all",
			mutate:     func(*netboxv1alpha1.NetBoxVirtualMachineSpec) {},
			wantReject: "at least one of clusterRef, siteRef or deviceRef",
		},
		{
			name: "cluster only",
			mutate: func(s *netboxv1alpha1.NetBoxVirtualMachineSpec) {
				s.ClusterRef = &netboxv1alpha1.ClusterRef{Name: "proxmox-home"}
			},
		},
		{
			name: "site only",
			mutate: func(s *netboxv1alpha1.NetBoxVirtualMachineSpec) {
				s.SiteRef = &netboxv1alpha1.SiteRef{Name: "home"}
			},
		},
		{
			name: "device only",
			mutate: func(s *netboxv1alpha1.NetBoxVirtualMachineSpec) {
				s.DeviceRef = &netboxv1alpha1.DeviceRef{Name: "nuc-01"}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := netboxv1alpha1.NetBoxVirtualMachineSpec{
				NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
				Name:             "dns",
			}
			tc.mutate(&spec)

			assertTypedAdmission(t, &netboxv1alpha1.NetBoxVirtualMachine{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "cel-"},
				Spec:       spec,
			}, tc.wantReject)
		})
	}
}

// TestVirtualMachineVCPUsMustBeADecimal is the other half of "a decimal is a string".
//
// The string is what keeps `"2"` and `"2.00"` from becoming three spellings through a float,
// and a bare string would accept `two`. The CEL rule bounds it to the column's own
// `decimal(6,2)` (docs/netbox-schema.md -> virtualization.VirtualMachine) and admits `""`,
// because clearing the field is a state the API has to be able to express.
func TestVirtualMachineVCPUsMustBeADecimal(t *testing.T) {
	ns := newNamespace(t)

	for _, tc := range []struct {
		vcpus      string
		wantReject string
	}{
		{vcpus: ""},
		{vcpus: "2"},
		{vcpus: "2.00"},
		{vcpus: "0.25"},
		{vcpus: "1234.56"},

		{vcpus: "two", wantReject: "decimal"},
		{vcpus: "2.000", wantReject: "decimal"},
		{vcpus: "-2", wantReject: "decimal"},
		{vcpus: "12345", wantReject: "decimal"},
	} {
		t.Run(tc.vcpus, func(t *testing.T) {
			assertTypedAdmission(t, &netboxv1alpha1.NetBoxVirtualMachine{
				ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "vcpus-"},
				Spec: netboxv1alpha1.NetBoxVirtualMachineSpec{
					NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
					Name:             "dns",
					SiteRef:          &netboxv1alpha1.SiteRef{Name: "home"},
					VCPUs:            tc.vcpus,
				},
			}, tc.wantReject)
		})
	}
}

// TestVMComponentsRequireTheirParentAndName is the required-field half for the two component
// kinds, and the reason it is one test is that their requirements come from one model:
// `virtual_machine ForeignKey REQ` and `name CharField REQ` on virtualization.ComponentModel,
// plus `size PositiveIntegerField REQ` on virtualization.VirtualDisk
// (docs/netbox-schema.md).
//
// `size` is asserted through a manifest with the key absent rather than through the typed
// struct: a required non-pointer `int32` has no absent form in Go, and the API server is the
// thing being tested.
func TestVMComponentsRequireTheirParentAndName(t *testing.T) {
	ns := newNamespace(t)

	t.Run("interface without a name", func(t *testing.T) {
		assertTypedAdmission(t, &netboxv1alpha1.NetBoxVMInterface{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "vmif-"},
			Spec: netboxv1alpha1.NetBoxVMInterfaceSpec{
				NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
				VirtualMachineRef: netboxv1alpha1.VirtualMachineRef{
					Name: "dns",
				},
			},
		}, "name")
	})

	t.Run("interface without a virtual machine", func(t *testing.T) {
		assertTypedAdmission(t, &netboxv1alpha1.NetBoxVMInterface{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "vmif-"},
			Spec: netboxv1alpha1.NetBoxVMInterfaceSpec{
				NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
				Name:             "eth0",
			},
		}, "exactly one of name, slug, lookup or id")
	})

	t.Run("interface with both", func(t *testing.T) {
		assertTypedAdmission(t, &netboxv1alpha1.NetBoxVMInterface{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, GenerateName: "vmif-"},
			Spec: netboxv1alpha1.NetBoxVMInterfaceSpec{
				NetBoxObjectSpec:  netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
				VirtualMachineRef: netboxv1alpha1.VirtualMachineRef{Name: "dns"},
				Name:              "eth0",
			},
		}, "")
	})

	t.Run("disk without a size", func(t *testing.T) {
		assertUnstructuredAdmission(t, ns, map[string]any{
			"apiVersion": netboxv1alpha1.GroupVersion.String(),
			"kind":       "NetBoxVirtualDisk",
			"spec": map[string]any{
				"endpointRef":       "homelab",
				"virtualMachineRef": map[string]any{"name": "dns"},
				"name":              "disk0",
			},
		}, "size")
	})

	t.Run("disk with a size", func(t *testing.T) {
		assertUnstructuredAdmission(t, ns, map[string]any{
			"apiVersion": netboxv1alpha1.GroupVersion.String(),
			"kind":       "NetBoxVirtualDisk",
			"spec": map[string]any{
				"endpointRef":       "homelab",
				"virtualMachineRef": map[string]any{"name": "dns"},
				"name":              "disk0",
				"size":              int64(20480),
			},
		}, "")
	})
}

// assertAdmission creates obj as a server-side dry run, so admission -- schema, enums,
// patterns, required fields, CEL -- runs in full and nothing is stored. An empty wantReject
// means the object must be accepted.
// assertTypedAdmission is the typed sibling of objectref_test.go's assertAdmission, which
// takes an *unstructured.Unstructured. Both landed the same day from different branches;
// renamed rather than merged because a VM is applied as a typed object here and the CEL rules
// under test are on the Go type.
func assertTypedAdmission(t *testing.T, obj client.Object, wantReject string) {
	t.Helper()

	err := apiClient.Create(context.Background(), obj, client.DryRunAll)

	if wantReject == "" {
		if err != nil {
			t.Fatalf("Create was rejected: %v", err)
		}

		return
	}

	if err == nil {
		t.Fatalf("Create was accepted, want a rejection naming %q", wantReject)
	}
	if !strings.Contains(err.Error(), wantReject) {
		t.Errorf("rejection %q does not name %q", err, wantReject)
	}
}

// assertUnstructuredAdmission is assertAdmission for a body that has to be built as a map.
//
// A required scalar with no zero-value distinction -- `size` on NetBoxVirtualDisk -- cannot be
// left out through the typed struct, because a non-pointer `int32` always marshals. The API
// server is the thing under test, so the request is built the way `kubectl apply` builds one.
func assertUnstructuredAdmission(t *testing.T, ns string, body map[string]any, wantReject string) {
	t.Helper()

	obj := &unstructured.Unstructured{Object: body}
	obj.SetNamespace(ns)
	obj.SetGenerateName("vdisk-")

	assertTypedAdmission(t, obj, wantReject)
}
