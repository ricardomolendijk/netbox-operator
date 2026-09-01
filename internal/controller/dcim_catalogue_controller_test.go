package controller

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/reconciler"
)

// The two stub kinds NBO-027 needs. Each is keyed by `slug`, which is the filter every one of
// these kinds' identities leads with (docs/netbox-schema.md, the four models' meta.constraints).
var (
	manufacturerKind = stubKind{endpoint: "dcim/manufacturers", key: "slug"}
	deviceRoleKind   = stubKind{endpoint: "dcim/device-roles", key: "slug"}
)

// dcim.DeviceType gets no stub kind of its own. Its identity needs `manufacturer_id`, and every
// reference mode that produces one -- `name` through a CR, or `id`, which is verified rather
// than trusted -- needs the `dcim/manufacturers` endpoint that this stub does not serve. Its
// payload and its lookup are asserted in internal/reconciler/dcim_catalogue_test.go instead,
// against the real descriptor and a fake client that answers for both endpoints.

// TestDeviceTypeWithoutAManufacturerIsRejectedByTheAPIServer is the acceptance criterion that
// the requirement is schema, not condition.
//
// `manufacturer` is `REQ` on the NetBox model and both natural keys start at it, so a device
// type without one has no identity at all. Rejecting it at admission is what turns that into a
// message on `kubectl apply` instead of an object that sits at Ready=False forever.
//
// Applied as unstructured, because the Go struct cannot express the case: `ManufacturerRef` is
// a value rather than a pointer and marshals to `{}`, which the CEL rules reject for a
// different reason than the one under test.
func TestDeviceTypeWithoutAManufacturerIsRejectedByTheAPIServer(t *testing.T) {
	ns := newNamespace(t)

	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "netbox.kubeforge.org/v1alpha1",
		"kind":       "NetBoxDeviceType",
		"metadata":   map[string]any{"name": "ucg-ultra", "namespace": ns},
		"spec": map[string]any{
			"endpointRef": "homelab",
			"model":       "UniFi Cloud Gateway Ultra",
			"slug":        "ucg-ultra",
		},
	}}

	err := apiClient.Create(context.Background(), obj, client.DryRunAll)
	if err == nil {
		t.Fatal("a NetBoxDeviceType with no manufacturerRef was accepted; the field is required")
	}

	if !strings.Contains(err.Error(), "manufacturerRef") {
		t.Errorf("rejection = %v, want it to name manufacturerRef", err)
	}
}

// TestManufacturerIsAdoptedNotDuplicated is the catalogue kind's first-contact case: a NetBox
// somebody has already been using by hand.
//
// `slug` is column-unique on dcim.Manufacturer, so the lookup finds the existing row and the
// engine takes it over rather than creating a second one -- which NetBox would refuse anyway
// with a 409 on the unique index.
func TestManufacturerIsAdoptedNotDuplicated(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, manufacturerKind)
	readyEndpoint(t, ns, target)

	stub.seed(netbox.Object{"name": "Ubiquiti", "slug": "ubiquiti"})

	manufacturer := &netboxv1alpha1.NetBoxManufacturer{
		ObjectMeta: metav1.ObjectMeta{Name: "ubiquiti", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxManufacturerSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{
				EndpointRef: "homelab", OnConflict: netboxv1alpha1.ConflictAdopt,
			},
			Name: "Ubiquiti",
			Slug: "ubiquiti",
		},
	}
	if err := k8sClient.Create(context.Background(), manufacturer); err != nil {
		t.Fatalf("creating manufacturer: %v", err)
	}

	t.Cleanup(func() { removeObject(t, manufacturer) })

	eventually(t, "the manufacturer to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxManufacturer{}, ns, "ubiquiti")
	})

	fetched := &netboxv1alpha1.NetBoxManufacturer{}
	key := client.ObjectKey{Namespace: ns, Name: "ubiquiti"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching manufacturer: %v", err)
	}

	if !fetched.Status.Adopted {
		t.Error("status.adopted is false; the operator did not create this object")
	}

	if n := stub.countByKey("ubiquiti"); n != 1 {
		t.Errorf("%d manufacturers with slug ubiquiti, want 1: it was duplicated rather than adopted", n)
	}
}

// TestDeviceRoleWithAnUnresolvableParentWritesNothing is NBO-015's shape on a kind whose
// `parent` is part of its identity, asserted on the recorded traffic rather than on the status.
//
// A version that reported the reference and then created the role top-level would look
// identical in the conditions -- and would go on to adopt an unrelated top-level role of this
// slug and reparent it on the next pass.
func TestDeviceRoleWithAnUnresolvableParentWritesNothing(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, deviceRoleKind)
	endpointWithoutResync(t, ns, target)

	role := &netboxv1alpha1.NetBoxDeviceRole{
		ObjectMeta: metav1.ObjectMeta{Name: "access-switch", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxDeviceRoleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Access switch",
			Slug:             "access-switch",
			// A NetBoxDeviceRole that does not exist. `name` is the only mode the operator
			// can wait on, which is why an unresolvable reference is tested in it.
			ParentRef: &netboxv1alpha1.DeviceRoleRef{Name: "nowhere"},
		},
	}
	if err := k8sClient.Create(context.Background(), role); err != nil {
		t.Fatalf("creating device role: %v", err)
	}

	t.Cleanup(func() { removeObject(t, role) })

	eventually(t, "the role to report that its parent does not exist", func() bool {
		fetched := &netboxv1alpha1.NetBoxDeviceRole{}
		key := client.ObjectKey{Namespace: ns, Name: "access-switch"}
		if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
			return false
		}

		for _, c := range fetched.Status.Conditions {
			if c.Type == netboxv1alpha1.ConditionRefsResolved {
				return c.Reason == netboxv1alpha1.ReasonRefNotFound
			}
		}

		return false
	})

	if got := stub.recorded(); len(got) != 0 {
		t.Errorf("netbox writes = %v, want none: no candidate is applicable with parentRef "+
			"declared and unresolved", got)
	}
}

// TestTopLevelDeviceRoleRoundTripsAndDoesNotHotLoop is the apply round trip for a nested-group
// catalogue kind, plus the steady state that proves the null-pinned lookup finds the object it
// just created.
//
// The colour and the boolean are the shapes worth watching. `color` is defaulted by the CRD so
// it reaches every payload, and `vm_role` is a `*bool` the engine only writes when the spec sets
// it -- either compared wrongly is a difference found on every pass and a PATCH forever.
func TestTopLevelDeviceRoleRoundTripsAndDoesNotHotLoop(t *testing.T) {
	ns := newNamespace(t)
	stub, target := newNetBoxStub(t, deviceRoleKind)
	readyEndpoint(t, ns, target)

	vmRole := true
	role := &netboxv1alpha1.NetBoxDeviceRole{
		ObjectMeta: metav1.ObjectMeta{Name: "router", Namespace: ns},
		Spec: netboxv1alpha1.NetBoxDeviceRoleSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Router",
			Slug:             "router",
			VMRole:           &vmRole,
		},
	}
	if err := k8sClient.Create(context.Background(), role); err != nil {
		t.Fatalf("creating device role: %v", err)
	}

	t.Cleanup(func() { removeObject(t, role) })

	eventually(t, "the device role to be Ready", func() bool {
		return catalogueReady(t, &netboxv1alpha1.NetBoxDeviceRole{}, ns, "router")
	})

	fetched := &netboxv1alpha1.NetBoxDeviceRole{}
	key := client.ObjectKey{Namespace: ns, Name: "router"}
	if err := k8sClient.Get(context.Background(), key, fetched); err != nil {
		t.Fatalf("fetching device role: %v", err)
	}

	if fetched.Status.ID == 0 {
		t.Error("status.id is unset on a Ready role; it is only set once the object provably exists")
	}

	live := stub.get(fetched.Status.ID)
	if live["color"] != "9e9e9e" {
		t.Errorf("netbox color = %v, want the CRD default 9e9e9e reaching the payload", live["color"])
	}

	if live["vm_role"] != true {
		t.Errorf("netbox vm_role = %v, want true", live["vm_role"])
	}

	writesAfterCreate := len(stub.recorded())

	// Wait out several resync intervals. There is no way to observe a hot loop other than
	// letting time pass: a single reconcile finding a spurious difference looks identical to
	// one finding a real one.
	waitResyncs(t, 4)

	if got := len(stub.recorded()); got != writesAfterCreate {
		t.Errorf("netbox received %d writes, want %d: the lookup pinning parent_id to null has "+
			"to find the object the engine just created", got, writesAfterCreate)
	}
}

// catalogueReady reports whether one of this ticket's objects carries Ready=True. One helper
// over the four kinds, because the only thing that differs is the empty object handed in.
func catalogueReady(t *testing.T, into reconciler.Object, ns, name string) bool {
	t.Helper()

	if err := k8sClient.Get(context.Background(),
		client.ObjectKey{Namespace: ns, Name: name}, into); err != nil {
		return false
	}

	for _, c := range into.NetBoxStatus().Conditions {
		if c.Type == netboxv1alpha1.ConditionReady {
			return c.Status == metav1.ConditionTrue
		}
	}

	return false
}
