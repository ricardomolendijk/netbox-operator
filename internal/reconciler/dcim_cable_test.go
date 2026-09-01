package reconciler

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// dcim.Cable through the engine, over the **real** Descriptor rather than a fake one.
//
// Three things about this kind cannot be checked any other way, and each of them fails
// silently if it is wrong.
//
// The terminations are a **to-many** polymorphic pair -- the first in the catalogue -- so they
// reach the payload as one field carrying a list of `{object_type, object_id}` objects rather
// than as two columns (registry.GenericFKList). A pair written the to-one way would be two
// keys NetBox does not know on dcim.Cable, and NetBox drops an unknown key rather than
// rejecting it: 201, and a cable connected to nothing.
//
// The identity is four **filterset** names, not columns, because `dcim.Cable` has no
// `meta.constraints` at all. django-filter answers an unrecognised parameter with the
// *unfiltered* set (#206), so a misspelling here adopts the first cable in NetBox.
//
// And the update is **destructive**. A comparison that saw drift where there is none would
// delete and re-create a cable on every resync, which is why the order-independence of the
// termination lists is asserted on the request bytes and not on a condition.

var (
	cableGVK       = netboxv1alpha1.GroupVersion.WithKind("NetBoxCable")
	cableBundleGVK = netboxv1alpha1.CableBundleRef{}.TargetGVK()
	interfaceGVK   = netboxv1alpha1.InterfaceRef{}.TargetGVK()
)

// cableEngine is the engine wired to the shipped Descriptor and to a cluster holding whatever
// interfaces the caller supplies.
func cableEngine(t *testing.T, nb NetBoxClient, targets ...*unstructured.Unstructured) *Engine {
	t.Helper()

	reg := registry.New()

	for _, gvk := range []schema.GroupVersionKind{
		cableGVK, cableBundleGVK, interfaceGVK, netboxv1alpha1.TenantRef{}.TargetGVK(),
	} {
		d, ok := registry.Get(gvk)
		if !ok {
			t.Fatalf("no descriptor for %s; this test needs the shipped one", gvk)
		}

		if err := reg.Add(d); err != nil {
			t.Fatalf("registering %s: %v", gvk, err)
		}
	}

	scheme := runtime.NewScheme()
	scheme.AddKnownTypeWithName(cableGVK, &netboxv1alpha1.NetBoxCable{})

	cable, _ := registry.Get(cableGVK)

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: cable, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        &resolver.Resolver{Objects: fakeCluster{objects: targets}, Kinds: reg},
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      scheme,
	}
}

// cableBetween is one CR terminating on the named interfaces, one end each.
func cableBetween(a, b []netboxv1alpha1.CableTerminationTarget) *netboxv1alpha1.NetBoxCable {
	return &netboxv1alpha1.NetBoxCable{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "sw1-sw2", Generation: 1},
		Spec: netboxv1alpha1.NetBoxCableSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			ATerminations:    a,
			BTerminations:    b,
			Status:           netboxv1alpha1.LinkStatusConnected,
			Type:             "cat6",
			Label:            "patch-14",
		},
	}
}

// onInterface is one termination, spelled the way a manifest spells it.
func onInterface(name string) netboxv1alpha1.CableTerminationTarget {
	return netboxv1alpha1.CableTerminationTarget{
		InterfaceRef: &netboxv1alpha1.InterfaceRef{Name: name},
	}
}

// fourInterfaces are the CRs the cases below point at: ids chosen so that sorting by id and
// sorting by manifest order are different answers.
func fourInterfaces() []*unstructured.Unstructured {
	return []*unstructured.Unstructured{
		readyTarget(interfaceGVK, "team-a", "sw1-eth0", 41),
		readyTarget(interfaceGVK, "team-a", "sw1-eth1", 17),
		readyTarget(interfaceGVK, "team-a", "sw2-eth0", 42),
		readyTarget(interfaceGVK, "team-a", "sw2-eth1", 18),
	}
}

// TestCableTerminationsReachThePayloadAsTwoListsOfPairs is the acceptance criterion of the
// to-many pair, asserted on the request body.
//
// The `dcim.interface` string comes from NetBoxInterface's own Descriptor.ObjectType through
// the real resolver, not from anything written on the union member, which is the whole
// spelling rule (docs/concepts/generic-refs.md). The keys inside each element are
// GenericObjectSerializer's `object_type` / `object_id`
// (netbox/netbox/api/serializers/generic.py:15) and *not* the model's `termination_type` /
// `termination_id`.
func TestCableTerminationsReachThePayloadAsTwoListsOfPairs(t *testing.T) {
	nb := &fakeClient{created: netbox.Object{"id": float64(7)}}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")},
	)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	payload := nb.lastPayload()

	want := map[string][]any{
		"a_terminations": {map[string]any{"object_type": "dcim.interface", "object_id": int64(41)}},
		"b_terminations": {map[string]any{"object_type": "dcim.interface", "object_id": int64(42)}},
	}

	for field, elements := range want {
		if got := payload[field]; !reflect.DeepEqual(got, elements) {
			t.Errorf("payload[%s] = %#v, want %#v", field, got, elements)
		}
	}

	// The names that must never appear. The four `termination_*` filters are filterset
	// parameters and not columns of dcim.Cable; `termination_type` / `termination_id` are
	// columns of a *different* model whose whole serializer is read-only
	// (netbox/dcim/api/serializers_/cables.py:71); `_abs_length` is a cache NetBox maintains;
	// and `connector` / `positions` are not writable anywhere in the 4.6.8 REST API. NetBox
	// drops a key it does not know rather than rejecting it, so any of these would return 201
	// and set nothing.
	for _, forbidden := range []string{
		"termination_a_type", "termination_a_id", "termination_b_type", "termination_b_id",
		"termination_type", "termination_id", "termination", "cable_end",
		"connector", "positions", "_abs_length",
	} {
		if _, present := payload[forbidden]; present {
			t.Errorf("payload carries %q, which NetBox would silently ignore: %v", forbidden, payload)
		}
	}

	if got := conditionOfCable(obj, netboxv1alpha1.ConditionReady); got.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s, want True", got.Status, got.Reason)
	}
}

// TestCableLooksItselfUpByAllFourTerminationFilters is the other half, and the half a guessed
// filterset name would break silently.
//
// Each of the four is declared on CableFilterSet (netbox/dcim/filtersets.py:2637). What makes
// them identity rather than a search is `unique(termination_type, termination_id)` on
// dcim.CableTermination: an object is terminated by at most one cable, globally.
func TestCableLooksItselfUpByAllFourTerminationFilters(t *testing.T) {
	nb := &fakeClient{created: netbox.Object{"id": float64(7)}}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")},
	)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	lookup := lastLookup(nb)
	if lookup == nil {
		t.Fatal("the engine never looked the cable up; it would duplicate on every fresh cluster")
	}

	want := netbox.Params{
		registry.CableTerminationATypeField: "dcim.interface",
		registry.CableTerminationAIDField:   "41",
		registry.CableTerminationBTypeField: "dcim.interface",
		registry.CableTerminationBIDField:   "42",
	}
	if got, wanted := sortedPairs(lookup), sortedPairs(want); !slices.Equal(got, wanted) {
		t.Errorf("lookup = %v, want %v", got, wanted)
	}

	// `label` is deliberately absent: NetBox lets any number of cables share one, so a
	// candidate on it would adopt somebody else's cable.
	if _, present := lookup["label"]; present {
		t.Error("the lookup filters on label, which is not unique on dcim.Cable")
	}
}

// TestCableTerminationOrderIsNotData is the acceptance criterion "reordering entries within
// aTerminations produces zero API writes", and it has to hold in two places at once.
//
// The *payload* has to be identical, or the last-applied hash changes and the object churns.
// And the *lookup* has to be identical, or a reordered manifest looks like a different cable
// and the engine creates a second one. Both come from applyGenericFKList sorting by
// `(object type, id)` before rendering, which is why the interface ids above are deliberately
// not in manifest order.
func TestCableTerminationOrderIsNotData(t *testing.T) {
	forward := cablePayloadAndLookup(t, []netboxv1alpha1.CableTerminationTarget{
		onInterface("sw1-eth0"), onInterface("sw1-eth1"),
	})
	reversed := cablePayloadAndLookup(t, []netboxv1alpha1.CableTerminationTarget{
		onInterface("sw1-eth1"), onInterface("sw1-eth0"),
	})

	if !reflect.DeepEqual(forward.payload["a_terminations"], reversed.payload["a_terminations"]) {
		t.Errorf("reordering aTerminations changed the payload:\n  %#v\n  %#v",
			forward.payload["a_terminations"], reversed.payload["a_terminations"])
	}

	if got, want := sortedPairs(reversed.lookup), sortedPairs(forward.lookup); !slices.Equal(got, want) {
		t.Errorf("reordering aTerminations changed the lookup:\n  %v\n  %v", got, want)
	}

	// The representative element is the lowest id, not the first written, so the answer does
	// not depend on which spelling of the same cable a user reached for.
	if got := forward.lookup[registry.CableTerminationAIDField]; got != "17" {
		t.Errorf("%s = %q, want 17 (the lowest id on the A end)",
			registry.CableTerminationAIDField, got)
	}
}

// TestCableTerminationSetDoesNotDriftAgainstNetBoxOrder is the same fact from the read side,
// and the one that decides whether a cable survives a resync.
//
// NetBox returns the elements in CableTermination row order and adds
// GenericObjectSerializer's read-only `object` expansion to each. Compared as an ordered list
// of whole objects, every cable would drift on every resync -- and on this kind drift means
// DELETE-then-POST, not PATCH.
func TestCableTerminationSetDoesNotDriftAgainstNetBoxOrder(t *testing.T) {
	live := liveCable()
	// NetBox's order, which is not the operator's, plus the key the operator never sends.
	live["a_terminations"] = []any{
		map[string]any{
			"object_type": "dcim.interface", "object_id": float64(41),
			"object": map[string]any{"id": float64(41), "name": "eth0"},
		},
		map[string]any{
			"object_type": "dcim.interface", "object_id": float64(17),
			"object": map[string]any{"id": float64(17), "name": "eth1"},
		},
	}

	nb := &fakeClient{get: live, list: []netbox.Object{live}}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth1"), onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")},
	)
	obj.Status.ID = 7

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); slices.Contains(got, "DELETE") || slices.Contains(got, "POST") {
		t.Errorf("the engine re-created a cable that had not changed: %v", got)
	}

	if got := conditionOfCable(obj, netboxv1alpha1.ConditionSynced); got.Reason != netboxv1alpha1.ReasonNoDrift {
		t.Errorf("Synced = %s/%s, want %s", got.Status, got.Reason, netboxv1alpha1.ReasonNoDrift)
	}
}

// TestCableChangingATerminationRecreates is the destructive path, and the order it happens in.
//
// DELETE before POST, and not the other way round: `unique(termination_type, termination_id)`
// keeps the wanted endpoint occupied by the old cable until it is gone, so the replacement
// cannot be created first (docs/netbox-schema.md -> dcim.CableTermination, meta.constraints).
func TestCableChangingATerminationRecreates(t *testing.T) {
	live := liveCable()

	nb := &fakeClient{get: live, list: []netbox.Object{live}, created: netbox.Object{"id": float64(9)}}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		// Was sw2-eth0 (42) in liveCable; now the other port on the same switch.
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth1")},
	)
	obj.Status.ID = 7

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	writes := make([]string, 0, len(nb.methods()))

	for _, method := range nb.methods() {
		if method == "DELETE" || method == "POST" || method == "PATCH" {
			writes = append(writes, method)
		}
	}

	if want := []string{"DELETE", "POST"}; !slices.Equal(writes, want) {
		t.Errorf("writes = %v, want %v", writes, want)
	}

	if obj.Status.ID != 9 {
		t.Errorf("status.id = %d, want 9 (the replacement's, taken from the create response)",
			obj.Status.ID)
	}
}

// TestCableLabelChangeIsAPatch is the other side of RecreateOn: a strategy without the field
// list would make every edit destructive, and a cable is not a thing to unplug to relabel.
func TestCableLabelChangeIsAPatch(t *testing.T) {
	live := liveCable()
	live["label"] = "patch-13"

	nb := &fakeClient{get: live, list: []netbox.Object{live}, patched: liveCable()}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")},
	)
	obj.Status.ID = 7

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); slices.Contains(got, "DELETE") {
		t.Errorf("relabelling the cable deleted it: %v", got)
	}

	if got := nb.lastPayload(); got["label"] != "patch-14" {
		t.Errorf("PATCH body = %v, want label patch-14", got)
	}
}

// TestCableFieldClassification is the table NBO-049's test plan asks for: field by field,
// which bucket each writable dcim.Cable field falls in.
//
// Driven off the shipped Descriptor rather than off a copy of it, so it is an assertion about
// what the engine will do and not about what this file says. `patch` is the default and
// `recreate` the exception -- a field that quietly became identity-bearing would show up here
// as a bucket change.
func TestCableFieldClassification(t *testing.T) {
	cable, ok := registry.Get(cableGVK)
	if !ok {
		t.Fatal("no descriptor for NetBoxCable")
	}

	if cable.UpdateStrategy != registry.UpdateRecreate {
		t.Fatalf("UpdateStrategy = %q, want %q", cable.UpdateStrategy, registry.UpdateRecreate)
	}

	for _, tc := range []struct {
		api    string
		bucket string
	}{
		{api: "a_terminations", bucket: "recreate"},
		{api: "b_terminations", bucket: "recreate"},
		{api: "type", bucket: "patch"},
		{api: "status", bucket: "patch"},
		{api: "profile", bucket: "patch"},
		{api: "tenant", bucket: "patch"},
		{api: "bundle", bucket: "patch"},
		{api: "label", bucket: "patch"},
		{api: "color", bucket: "patch"},
		{api: "length", bucket: "patch"},
		{api: "length_unit", bucket: "patch"},
		{api: "description", bucket: "patch"},
		{api: "comments", bucket: "patch"},
		// Not writable at all, in either bucket: a cache NetBox maintains from the two fields
		// above it.
		{api: "_abs_length", bucket: "readOnly"},
	} {
		got := "patch"

		switch {
		case slices.Contains(cable.ReadOnly, tc.api):
			got = "readOnly"
		case slices.Contains(cable.RecreateOn, tc.api):
			got = "recreate"
		}

		if got != tc.bucket {
			t.Errorf("%s is %s, want %s", tc.api, got, tc.bucket)
		}
	}
}

// TestCableTerminationsCascadeNowhere is the ownership answer, recorded rather than derived.
//
// `dcim.CabledObjectModel.cable` is `on_delete=SET_NULL` (docs/netbox-schema.md), so deleting
// an interface clears that interface's own cached column and leaves the cable standing. An
// owner reference on the termination union would garbage-collect the CR while NetBox still held
// the row, so no member cascades and the kind has no containment parent -- and "unstated" would
// be a third answer, which is why every member says `false`.
func TestCableTerminationsCascadeNowhere(t *testing.T) {
	cable, ok := registry.Get(cableGVK)
	if !ok {
		t.Fatal("no descriptor for NetBoxCable")
	}

	if cable.ContainmentRef != "" {
		t.Errorf("ContainmentRef = %q, want empty: nothing about a cable cascades", cable.ContainmentRef)
	}

	if len(cable.GenericFKs) != 2 {
		t.Fatalf("NetBoxCable declares %d polymorphic pairs, want 2", len(cable.GenericFKs))
	}

	for _, pair := range cable.GenericFKs {
		if !pair.ToMany() {
			t.Errorf("%s is a to-one pair; a cable end may carry several terminations", pair.Spec)
		}

		for _, member := range pair.Members {
			if member.CascadeOnDelete == nil {
				t.Errorf("%s.%s leaves CascadeOnDelete unstated", pair.Spec, member.Spec)

				continue
			}

			if *member.CascadeOnDelete {
				t.Errorf("%s.%s cascades; dcim.CabledObjectModel.cable is SET_NULL",
					pair.Spec, member.Spec)
			}
		}
	}
}

// TestCableWaitsRatherThanCreatingWhenATerminationDoesNotResolve is the precondition rule on
// the one kind where writing without it would be unrecoverable.
//
// A cable created with one end resolved is a half-cable, and correcting it means DELETE and
// POST rather than PATCH -- so a partially resolved list writes nothing at all, which is
// resolveGeneric's all-or-nothing rule.
func TestCableWaitsRatherThanCreatingWhenATerminationDoesNotResolve(t *testing.T) {
	nb := &fakeClient{}
	// Only the A end exists as a CR.
	engine := cableEngine(t, nb, readyTarget(interfaceGVK, "team-a", "sw1-eth0", 41))

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")},
	)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if payload := nb.lastPayload(); payload != nil {
		t.Errorf("the engine wrote %v with one end unresolved", payload)
	}

	if got := conditionOfCable(obj, netboxv1alpha1.ConditionRefsResolved); got.Status != metav1.ConditionFalse {
		t.Errorf("RefsResolved = %s/%s, want False", got.Status, got.Reason)
	}
}

// TestCableMemberWithNoRegisteredKindWaits is the RefKindUnavailable outcome for the eight
// members whose Kinds have not landed. It is asserted rather than worked around: a stub that
// accepted `rearPortRef` and dropped it would report Ready while NetBox held no cable.
func TestCableMemberWithNoRegisteredKindWaits(t *testing.T) {
	nb := &fakeClient{}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{{
			RearPortRef: &netboxv1alpha1.RearPortRef{Name: "panel-1-rear-14"},
		}},
	)

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if payload := nb.lastPayload(); payload != nil {
		t.Errorf("the engine wrote %v with an unresolvable termination", payload)
	}

	refs := conditionOfCable(obj, netboxv1alpha1.ConditionRefsResolved)
	if refs.Reason != netboxv1alpha1.ReasonRefKindUnavailable {
		t.Errorf("RefsResolved = %s/%s, want %s",
			refs.Status, refs.Reason, netboxv1alpha1.ReasonRefKindUnavailable)
	}

	// The message names the indexed member path, because a cable end may carry several
	// terminations and "bTerminations" alone would not say which.
	if want := "bTerminations[0].rearPortRef"; !strings.Contains(refs.Message, want) {
		t.Errorf("RefsResolved message = %q, want it to name %s", refs.Message, want)
	}
}

// TestCableMetricsRecordARecreate keeps the observability contract honest: a recreate is its
// own result, not an update wearing one's clothes (docs/operations/observability.md).
func TestCableMetricsRecordARecreate(t *testing.T) {
	if metrics.ResultRecreated == metrics.ResultUpdated {
		t.Fatal("a recreate and an update share a result label; the two are not the same event")
	}
}

// liveCable is the NetBox object matching cableBetween(sw1-eth0, sw2-eth0) exactly, in the
// shape NetBox returns it: choice fields as {value,label}, terminations as
// GenericObjectSerializer objects with the read-only `object` expansion.
func liveCable() netbox.Object {
	return netbox.Object{
		"id":      float64(7),
		"url":     "https://netbox.invalid/api/dcim/cables/7/",
		"display": "#7",
		"type":    map[string]any{"value": "cat6", "label": "CAT6"},
		"status":  map[string]any{"value": "connected", "label": "Connected"},
		"label":   "patch-14",
		"a_terminations": []any{map[string]any{
			"object_type": "dcim.interface", "object_id": float64(41),
			"object": map[string]any{"id": float64(41), "name": "eth0"},
		}},
		"b_terminations": []any{map[string]any{
			"object_type": "dcim.interface", "object_id": float64(42),
			"object": map[string]any{"id": float64(42), "name": "eth0"},
		}},
		"created": "2026-08-01T00:00:00Z",
	}
}

// cableSnapshot is what one reconcile of a cable sent and asked for.
type cableSnapshot struct {
	payload netbox.Object
	lookup  netbox.Params
}

// cablePayloadAndLookup reconciles one cable whose A end is the given list and hands back both
// halves, so a reordering case compares them together rather than twice.
func cablePayloadAndLookup(t *testing.T, a []netboxv1alpha1.CableTerminationTarget) cableSnapshot {
	t.Helper()

	nb := &fakeClient{created: netbox.Object{"id": float64(7)}}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(a, []netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	return cableSnapshot{payload: nb.lastPayload(), lookup: lastLookup(nb)}
}

// lastLookup is the natural-key query the engine issued, or nil if it issued none.
func lastLookup(nb *fakeClient) netbox.Params {
	var params netbox.Params

	for _, c := range nb.calls {
		if c.method == "GETONE" {
			params = c.params
		}
	}

	return params
}

func conditionOfCable(obj *netboxv1alpha1.NetBoxCable, condType string) metav1.Condition {
	for _, condition := range obj.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}

// TestCableRetainRefusesARecreate is the one case where two spec fields contradict each other.
//
// `deletionPolicy: Retain` means "never destroy this NetBox object" and a recreate destroys it,
// along with every `dcim.CablePath` traversing it. The operator refuses instead of picking one,
// and refuses in this direction because a recreate is unrecoverable while a refusal is one edit
// away from either outcome.
//
// Zero writes is the assertion that matters. A `Retain` cable whose terminations were edited
// must still be in NetBox afterwards.
func TestCableRetainRefusesARecreate(t *testing.T) {
	live := liveCable()

	nb := &fakeClient{get: live, list: []netbox.Object{live}, created: netbox.Object{"id": float64(9)}}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth1")},
	)
	obj.Status.ID = 7
	obj.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	for _, method := range nb.methods() {
		if method == "DELETE" || method == "POST" || method == "PATCH" {
			t.Errorf("the engine wrote (%v) against deletionPolicy: Retain", nb.methods())

			break
		}
	}

	if obj.Status.ID != 7 {
		t.Errorf("status.id = %d, want 7 unchanged: nothing was replaced", obj.Status.ID)
	}

	ready := conditionOfCable(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonInvalid {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonInvalid)
	}

	// The message names the field that changed, because reverting it is half the fix.
	if want := "b_terminations"; !strings.Contains(ready.Message, want) {
		t.Errorf("Ready message = %q, want it to name %s", ready.Message, want)
	}
}

// TestCableRetainStillPatches keeps the refusal narrow: `Retain` blocks the destructive path
// and nothing else. Relabelling a retained cable is an ordinary PATCH.
func TestCableRetainStillPatches(t *testing.T) {
	live := liveCable()
	live["label"] = "patch-13"

	nb := &fakeClient{get: live, list: []netbox.Object{live}, patched: liveCable()}
	engine := cableEngine(t, nb, fourInterfaces()...)

	obj := cableBetween(
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw1-eth0")},
		[]netboxv1alpha1.CableTerminationTarget{onInterface("sw2-eth0")},
	)
	obj.Status.ID = 7
	obj.Spec.DeletionPolicy = netboxv1alpha1.DeletionRetain

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.lastPayload(); got["label"] != "patch-14" {
		t.Errorf("PATCH body = %v, want label patch-14", got)
	}
}
