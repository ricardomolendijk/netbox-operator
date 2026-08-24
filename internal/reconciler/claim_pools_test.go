package reconciler

import (
	"context"
	"errors"
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// NBO-064's two claim kinds go through the same engine as NBO-036's, so the fixtures here
// differ from newClaimFixture's in exactly two ways: the pool and the allocated object live at
// the *same* endpoint for a prefix claim -- both are `ipam/prefixes` -- which the
// endpoint-keyed fake cannot express, and the range claim's allocation is not an
// advisory-locked sub-path at all.
var (
	_ Claim = (*netboxv1alpha1.NetBoxPrefixClaim)(nil)
	_ Claim = (*netboxv1alpha1.NetBoxIPRangeClaim)(nil)
)

// The claim descriptors' Endpoint has to agree with the path the client creates a range at.
// Declared in two packages that deliberately do not import each other -- internal/registry
// holds per-kind data, internal/netbox builds requests -- so this is where the two meet.
func TestRangeClaimEndpointMatchesTheClient(t *testing.T) {
	desc, ok := registry.Claim(netboxv1alpha1.GroupVersion.WithKind("NetBoxIPRangeClaim"))
	if !ok {
		t.Fatal("no claim descriptor is registered for NetBoxIPRangeClaim")
	}

	if desc.Endpoint != netbox.IPRangeEndpoint {
		t.Errorf("the descriptor reads from %q and the client creates at %q; the"+
			" read-after-write and the identity search would look in the wrong place",
			desc.Endpoint, netbox.IPRangeEndpoint)
	}

	if desc.PoolSubPath != netbox.PlaceRange {
		t.Errorf("poolSubPath = %q, want %q", desc.PoolSubPath, netbox.PlaceRange)
	}
}

// idClient is a NetBox that answers per (endpoint, id), which the prefix claim needs: its pool
// and the object it allocates are both ipam.Prefix, so a fake keyed on the endpoint alone
// cannot tell the parent from the child and every read-after-write would compare the parent
// against itself.
type idClient struct {
	objects map[string]netbox.Object

	list      []netbox.Object
	allocated netbox.Object
	allocErr  error

	posts   int
	payload netbox.Object
	subs    []string
}

func (c *idClient) URL() string { return testURL }

func (c *idClient) GetByID(_ context.Context, endpoint string, id int) (netbox.Object, error) {
	return c.objects[fmt.Sprintf("%s/%d", endpoint, id)], nil
}

func (c *idClient) GetOne(_ context.Context, endpoint string, params netbox.Params) (netbox.Object, error) {
	return netbox.One(endpoint, params, c.list)
}

func (c *idClient) Allocate(
	_ context.Context, endpoint string, id int, sub string, payload netbox.Object,
) (netbox.Object, error) {
	c.subs = append(c.subs, fmt.Sprintf("%s/%d/%s", endpoint, id, sub))
	c.payload = payload

	if c.allocErr != nil {
		return nil, c.allocErr
	}

	c.posts++

	return c.allocated, nil
}

func (c *idClient) Create(_ context.Context, _ string, _ netbox.Object) (netbox.Object, error) {
	return nil, errors.New("the allocation path must not create by name")
}

func (c *idClient) Patch(_ context.Context, _ string, _ int, _ netbox.Object) (netbox.Object, error) {
	return nil, errors.New("the allocation path must not patch")
}

func (c *idClient) Delete(_ context.Context, _ string, _ int) (netbox.Object, error) {
	return nil, errors.New("the allocation path must not delete")
}

// poolClaimEngine is the engine wired for one of NBO-064's kinds, reading the *registered*
// descriptor rather than a fixture's copy of one: a fixture that drifts from the shipped data
// tests the fixture.
func poolClaimEngine(t *testing.T, kind string, nb *idClient) *ClaimEngine {
	t.Helper()

	desc, ok := registry.Claim(netboxv1alpha1.GroupVersion.WithKind(kind))
	if !ok {
		t.Fatalf("no claim descriptor is registered for %s", kind)
	}

	return &ClaimEngine{
		Claims: fakeClaims{descriptor: desc, registered: true},
		Pools:  fakeDescriptors{descriptor: poolDescriptor(), registered: true},
		Endpoints: fakeEndpoints{ready: true, endpoint: Endpoint{
			Client:     nb,
			Allocator:  nb,
			Provenance: provenance.Stamp{Config: identityConfig(), TagID: 7, Fields: identityFields()},
		}},
		Refs:       &fakePool{id: 11},
		Status:     &fakeStatus{},
		Finalizers: &fakeFinalizers{},
		Events:     &fakeRecorder{},
		Scheme:     claimScheme(t),
	}
}

// --- NetBoxPrefixClaim -------------------------------------------------------------------

// prefixClaimFixture is a /26 claim against a 10.0.0.0/16 container.
func prefixClaimFixture(t *testing.T, parentStatus string) (
	*netboxv1alpha1.NetBoxPrefixClaim, *ClaimEngine, *idClient,
) {
	t.Helper()

	identity := AllocationIdentity(testURL, "homelab", "NetBoxPrefixClaim", "tenant-a-net")
	child := allocatedPrefix(512, "10.0.64.0/26", identity)

	nb := &idClient{
		objects: map[string]netbox.Object{
			"ipam/prefixes/11":  {"prefix": "10.0.0.0/16", "status": map[string]any{"value": parentStatus}},
			"ipam/prefixes/512": child,
		},
		allocated: child,
	}

	claim := &netboxv1alpha1.NetBoxPrefixClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "homelab", Name: "tenant-a-net", UID: "uid-1", Generation: 1,
		},
		Spec: netboxv1alpha1.NetBoxPrefixClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
			ParentPrefixRef: netboxv1alpha1.PrefixRef{Name: "container-10-0"},
			PrefixLength:    26,
		},
	}

	return claim, poolClaimEngine(t, "NetBoxPrefixClaim", nb), nb
}

// allocatedPrefix is what NetBox returns for a prefix carved out of a container.
func allocatedPrefix(id int, prefix, identity string) netbox.Object {
	obj := netbox.Object{
		"id":     float64(id),
		"prefix": prefix,
		"url":    fmt.Sprintf("%s/api/ipam/prefixes/%d/", testURL, id),
	}

	netbox.SetCustomField(obj, provenance.DefaultAllocationIdentityField, identity)
	netbox.SetCustomField(obj, provenance.DefaultUIDField, "uid-1")

	return obj
}

// TestPrefixClaimAllocatesOnceOutOfTheLockedSubPath is the whole of the easy kind.
//
// Fifty passes, one POST, and the POST goes to `available-prefixes` carrying `prefix_length` --
// the wire name, not the spec's `prefixLength`, which NetBox would accept and ignore.
func TestPrefixClaimAllocatesOnceOutOfTheLockedSubPath(t *testing.T) {
	claim, engine, nb := prefixClaimFixture(t, "container")

	for pass := range 50 {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	if nb.posts != 1 {
		t.Errorf("%d POSTs to the allocation endpoint, want exactly 1", nb.posts)
	}

	if claim.Status.Prefix != "10.0.64.0/26" || claim.Status.NetBoxID != 512 {
		t.Errorf("status = %q/%d, want 10.0.64.0/26 and 512", claim.Status.Prefix, claim.Status.NetBoxID)
	}

	if len(nb.subs) != 1 || nb.subs[0] != "ipam/prefixes/11/available-prefixes" {
		t.Errorf("allocated through %v, want one POST to ipam/prefixes/11/available-prefixes", nb.subs)
	}

	if got, ok := netbox.IntOf(nb.payload["prefix_length"]); !ok || got != 26 {
		t.Errorf("the payload carried prefix_length=%v, want the integer 26", nb.payload["prefix_length"])
	}

	if _, wrong := nb.payload["prefixLength"]; wrong {
		t.Error("the payload carried the spec's own field name, which netbox ignores silently")
	}

	if got := netbox.CustomFieldOf(nb.payload, provenance.DefaultAllocationIdentityField); got !=
		claim.Status.AllocationIdentity {
		t.Errorf("the payload carried identity %q, want %q", got, claim.Status.AllocationIdentity)
	}

	if got := readyOfPrefixClaim(claim); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", got.Status, got.Reason)
	}
}

// TestPrefixClaimAllocatesOutOfANonContainerAndSaysSo is the asymmetry this ticket is about.
//
// `status: container` refuses an address claim and is what a prefix claim expects. A prefix
// claim against an `active` parent is unusual and legitimate -- somebody is subdividing a
// network that is in service -- so it allocates *and* warns, where the address claim refuses.
func TestPrefixClaimAllocatesOutOfANonContainerAndSaysSo(t *testing.T) {
	claim, engine, nb := prefixClaimFixture(t, "active")

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.posts != 1 || claim.Status.Prefix == "" {
		t.Fatalf("%d POSTs and status.prefix = %q; an active parent must not refuse",
			nb.posts, claim.Status.Prefix)
	}

	recorder, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(recorder, "Warning/"+netboxv1alpha1.EventPoolUnexpectedStatus) {
		t.Errorf("events = %v, want a %s warning",
			recorder.events, netboxv1alpha1.EventPoolUnexpectedStatus)
	}
}

// TestPrefixClaimIsSilentAboutAContainer is the other half: the expected state says nothing.
func TestPrefixClaimIsSilentAboutAContainer(t *testing.T) {
	claim, engine, _ := prefixClaimFixture(t, "container")

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	recorder, _ := engine.Events.(*fakeRecorder)
	if hasEvent(recorder, "Warning/"+netboxv1alpha1.EventPoolUnexpectedStatus) {
		t.Errorf("events = %v; a container is exactly what this kind expects", recorder.events)
	}
}

// TestPrefixClaimRefusesALengthThePoolCannotSatisfy is the guard that costs zero requests.
//
// Both cases are accepted by something and rejected by nothing without it. A /16 out of a /16
// is *accepted by NetBox*: `available-prefixes` hands out the parent itself, so the claim would
// hold a duplicate of its own container and report success. A /64 out of an IPv4 parent is
// rejected by NetBox, but with a 400 that reads as a payload bug rather than as the two numbers
// that disagree.
func TestPrefixClaimRefusesALengthThePoolCannotSatisfy(t *testing.T) {
	cases := map[string]int32{
		"a length equal to the pool's":   16,
		"a length shorter than the pool": 8,
		"a length outside the family":    64,
	}

	for name, length := range cases {
		t.Run(name, func(t *testing.T) {
			claim, engine, nb := prefixClaimFixture(t, "container")
			claim.Spec.PrefixLength = length

			if _, err := engine.Reconcile(context.Background(), claim); err != nil {
				t.Fatal(err)
			}

			if nb.posts != 0 || len(nb.subs) != 0 {
				t.Errorf("%d POSTs (%v), want 0", nb.posts, nb.subs)
			}

			if claim.Status.Prefix != "" {
				t.Errorf("status.prefix = %q, want empty", claim.Status.Prefix)
			}

			if got := readyOfPrefixClaim(claim); got.Reason != netboxv1alpha1.ReasonInvalid ||
				got.Status != metav1.ConditionFalse {
				t.Errorf("Ready = %s/%s, want False/Invalid", got.Status, got.Reason)
			}
		})
	}
}

func readyOfPrefixClaim(claim *netboxv1alpha1.NetBoxPrefixClaim) metav1.Condition {
	for _, condition := range claim.Status.Conditions {
		if condition.Type == netboxv1alpha1.ConditionReady {
			return condition
		}
	}

	return metav1.Condition{}
}

// --- NetBoxIPRangeClaim ------------------------------------------------------------------

// rangeClaimFixture is a 64-address claim inside 10.0.30.0/24.
func rangeClaimFixture(t *testing.T) (
	*netboxv1alpha1.NetBoxIPRangeClaim, *ClaimEngine, *idClient,
) {
	t.Helper()

	identity := AllocationIdentity(testURL, "homelab", "NetBoxIPRangeClaim", "dhcp-pool")
	created := netbox.Object{
		"id":            float64(77),
		"start_address": "10.0.30.64/24",
		"end_address":   "10.0.30.127/24",
		"size":          float64(64),
		"url":           testURL + "/api/ipam/ip-ranges/77/",
	}

	netbox.SetCustomField(created, provenance.DefaultAllocationIdentityField, identity)
	netbox.SetCustomField(created, provenance.DefaultUIDField, "uid-1")

	nb := &idClient{
		objects: map[string]netbox.Object{
			"ipam/prefixes/11":  {"prefix": "10.0.30.0/24", "status": map[string]any{"value": "active"}},
			"ipam/ip-ranges/77": created,
		},
		allocated: created,
	}

	claim := &netboxv1alpha1.NetBoxIPRangeClaim{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "homelab", Name: "dhcp-pool", UID: "uid-1", Generation: 1,
		},
		Spec: netboxv1alpha1.NetBoxIPRangeClaimSpec{
			NetBoxClaimSpec: netboxv1alpha1.NetBoxClaimSpec{EndpointRef: "homelab"},
			ParentPrefixRef: netboxv1alpha1.PrefixRef{Name: "home-lan"},
			Size:            64,
			Alignment:       netboxv1alpha1.RangeAlignmentPowerOfTwo,
		},
	}

	return claim, poolClaimEngine(t, "NetBoxIPRangeClaim", nb), nb
}

// TestRangeClaimAllocatesOnceAndDerivesTheEndAddress covers the one kind whose status holds
// three values for one allocation.
//
// The engine sets the start address, which is what its guard clause reads and what its
// read-after-write proved. The end address is arithmetic on top of a size NetBox has already
// confirmed, so it cannot disagree with the server -- and it is the value a human copies into a
// DHCP config.
func TestRangeClaimAllocatesOnceAndDerivesTheEndAddress(t *testing.T) {
	claim, engine, nb := rangeClaimFixture(t)

	for pass := range 50 {
		if _, err := engine.Reconcile(context.Background(), claim); err != nil {
			t.Fatalf("pass %d: %v", pass, err)
		}
	}

	if nb.posts != 1 {
		t.Errorf("%d allocations, want exactly 1", nb.posts)
	}

	if claim.Status.StartAddress != "10.0.30.64/24" {
		t.Errorf("status.startAddress = %q, want 10.0.30.64/24", claim.Status.StartAddress)
	}

	if claim.Status.EndAddress != "10.0.30.127/24" {
		t.Errorf("status.endAddress = %q, want 10.0.30.127/24 (start + size - 1, same mask)",
			claim.Status.EndAddress)
	}

	if claim.Status.Size != 64 {
		t.Errorf("status.size = %d, want 64", claim.Status.Size)
	}

	if len(nb.subs) != 1 || nb.subs[0] != "ipam/prefixes/11/place-ip-range" {
		t.Errorf("allocated through %v, want one placement against ipam/prefixes/11", nb.subs)
	}
}

// TestRangeClaimPassesThePlacementInputsAndNoSize is about the two keys that must not reach
// NetBox as columns.
//
// `size` is derived by NetBox from the two endpoints and dropped from any payload that carries
// it; `alignment` is not a NetBox concept at all. Both travel as `@`-prefixed placement inputs
// that the client consumes, which is what makes "a key NetBox would ignore" impossible to send
// by accident.
func TestRangeClaimPassesThePlacementInputsAndNoSize(t *testing.T) {
	claim, engine, nb := rangeClaimFixture(t)

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if got, ok := netbox.IntOf(nb.payload[netbox.PlacementSize]); !ok || got != 64 {
		t.Errorf("the payload carried %s=%v, want 64", netbox.PlacementSize, nb.payload[netbox.PlacementSize])
	}

	if got, _ := nb.payload[netbox.PlacementAlignment].(string); got != "PowerOfTwo" {
		t.Errorf("the payload carried %s=%v, want PowerOfTwo",
			netbox.PlacementAlignment, nb.payload[netbox.PlacementAlignment])
	}

	for _, forbidden := range []string{"size", "alignment", "start_address", "end_address"} {
		if _, sent := nb.payload[forbidden]; sent {
			t.Errorf("the payload carried %q; netbox derives it or the client adds it", forbidden)
		}
	}
}

// TestRangeClaimReportsContentionApartFromExhaustion is the condition only this kind can
// report.
//
// A contended pool has room and a full one does not, and the fixes are opposite: wait, versus
// widen the prefix. The reason has to say which.
func TestRangeClaimReportsContentionApartFromExhaustion(t *testing.T) {
	claim, engine, nb := rangeClaimFixture(t)
	nb.allocErr = &netbox.ContendedError{
		Endpoint: netbox.IPRangeEndpoint, Pool: "10.0.30.0/24", Attempts: 5,
		Body: "Defined addresses overlap with range 10.0.30.64-10.0.30.127 in VRF None",
	}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if claim.Status.StartAddress != "" || claim.Status.EndAddress != "" {
		t.Errorf("status recorded %q-%q from an allocation that did not happen",
			claim.Status.StartAddress, claim.Status.EndAddress)
	}

	ready := readyOfRangeClaim(claim)
	if ready.Reason != netboxv1alpha1.ReasonAllocationContended || ready.Status != metav1.ConditionFalse {
		t.Errorf("Ready = %s/%s, want False/AllocationContended", ready.Status, ready.Reason)
	}

	if ready.Reason == netboxv1alpha1.ReasonPoolExhausted {
		t.Error("contention was reported as exhaustion, which sends a reader looking for space that exists")
	}

	recorder, _ := engine.Events.(*fakeRecorder)
	if !hasEvent(recorder, "Warning/"+netboxv1alpha1.EventAllocationContended) {
		t.Errorf("events = %v, want %s", recorder.events, netboxv1alpha1.EventAllocationContended)
	}
}

// TestRangeClaimReclaimsByIdentity is the recovery path, on the kind that needs it most: the
// creating POST has no advisory lock behind it, so a lost response is the ordinary failure
// rather than the rare one.
func TestRangeClaimReclaimsByIdentity(t *testing.T) {
	claim, engine, nb := rangeClaimFixture(t)
	nb.list = []netbox.Object{nb.allocated}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.posts != 0 {
		t.Errorf("%d POSTs, want 0: the range carrying this identity must be reclaimed", nb.posts)
	}

	if claim.Status.StartAddress != "10.0.30.64/24" || claim.Status.Size != 64 {
		t.Errorf("status = %q/%d, want the reclaimed range", claim.Status.StartAddress, claim.Status.Size)
	}

	if got := conditionOfRangeClaim(claim, netboxv1alpha1.ConditionAllocated); got.Reason !=
		netboxv1alpha1.ReasonReclaimedByIdentity {
		t.Errorf("Allocated reason = %q, want ReclaimedByIdentity", got.Reason)
	}
}

// TestRangeClaimRefusesARangeOutsideTheParent is the guard that matters for this kind above
// the others: repointing parentPrefixRef while keeping the claim's name is the likely mistake,
// and the identity would still find the old range.
func TestRangeClaimRefusesARangeOutsideTheParent(t *testing.T) {
	claim, engine, nb := rangeClaimFixture(t)

	elsewhere := netbox.Object{}
	for key, value := range nb.allocated {
		elsewhere[key] = value
	}

	elsewhere["start_address"] = "10.0.99.64/24"
	nb.list = []netbox.Object{elsewhere}

	if _, err := engine.Reconcile(context.Background(), claim); err != nil {
		t.Fatal(err)
	}

	if nb.posts != 0 {
		t.Errorf("%d POSTs, want 0", nb.posts)
	}

	if got := readyOfRangeClaim(claim).Reason; got != netboxv1alpha1.ReasonReclaimedOutsidePool {
		t.Errorf("Ready reason = %q, want ReclaimedOutsidePool", got)
	}
}

// TestEndAddressOfIsExactArithmetic pins the one derived status value in the operator.
func TestEndAddressOfIsExactArithmetic(t *testing.T) {
	cases := []struct {
		start string
		size  int32
		want  string
	}{
		{"10.0.30.64/24", 64, "10.0.30.127/24"},
		{"10.0.30.64/24", 1, "10.0.30.64/24"},
		{"fd00:10::100/64", 4096, "fd00:10::10ff/64"},
		{"10.0.30.64/24", 0, ""},
		{"not-an-address", 8, ""},
		{"255.255.255.255/32", 2, ""},
	}

	for _, test := range cases {
		claim := &netboxv1alpha1.NetBoxIPRangeClaim{
			Spec: netboxv1alpha1.NetBoxIPRangeClaimSpec{Size: test.size},
		}
		claim.SetAllocated(test.start)

		if claim.Status.EndAddress != test.want {
			t.Errorf("SetAllocated(%q) with size %d gave end %q, want %q",
				test.start, test.size, claim.Status.EndAddress, test.want)
		}
	}
}

func conditionOfRangeClaim(claim *netboxv1alpha1.NetBoxIPRangeClaim, condType string) metav1.Condition {
	for _, condition := range claim.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

func readyOfRangeClaim(claim *netboxv1alpha1.NetBoxIPRangeClaim) metav1.Condition {
	return conditionOfRangeClaim(claim, netboxv1alpha1.ConditionReady)
}
