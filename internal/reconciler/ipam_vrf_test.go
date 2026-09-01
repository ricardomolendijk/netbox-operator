package reconciler

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// The to-many payload cases of NBO-022, asserted against the real ipam.VRF descriptor and a
// real NetBoxVRF rather than against the fake kind.
//
// The fake kind already proves the engine writes a to-many reference as a sorted id list
// (cardinality_test.go). What these add is that ipam.VRF's own two declarations reach that
// code: the descriptor is the one internal/registry/ipam_vrf.go registers, the object is the
// CRD a user applies, and the assertion is on the JSON body the client would put on the wire.

// vrfEngine assembles an engine around the registered ipam.VRF descriptor.
//
// The registered one rather than a fixture, because "adding a Kind needs no engine change" is
// only tested if the Kind under test is the shipped one. A fixture that happened to differ
// would pass while NetBoxVRF was broken.
func vrfEngine(t *testing.T, nb NetBoxClient, refs RefResolver) *Engine {
	t.Helper()

	d, ok := registry.Get(netboxv1alpha1.GroupVersion.WithKind("NetBoxVRF"))
	if !ok {
		t.Fatal("NetBoxVRF is not registered")
	}

	scheme := runtime.NewScheme()
	if err := netboxv1alpha1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() = %v", err)
	}

	return &Engine{
		Descriptors: fakeDescriptors{descriptor: d, registered: true},
		Endpoints:   fakeEndpoints{endpoint: Endpoint{Client: nb, Resync: testResync}, ready: true},
		Refs:        refs,
		Status:      &fakeStatus{},
		LiveStatus:  &fakeLiveStatus{},
		Finalizers:  &fakeFinalizers{},
		Scheme:      scheme,
	}
}

// vrfObject is the per-house VRF from inventory.yaml, with importTargets written.
//
// `claimed` names the spec fields somebody other than the operator owns, which is how an
// explicitly-empty list survives `omitempty`: the JSON encoding of `importTargets: []` is
// nothing at all, and the engine puts it back from metadata.managedFields
// (docs/concepts/field-ownership.md). Without the entry, `[]` and absent are the same object.
func vrfObject(targets []netboxv1alpha1.RouteTargetRef, claimed ...string) *netboxv1alpha1.NetBoxVRF {
	vrf := &netboxv1alpha1.NetBoxVRF{
		ObjectMeta: metav1.ObjectMeta{Namespace: "team-a", Name: "donkerslootstraat", Generation: 1},
		Spec: netboxv1alpha1.NetBoxVRFSpec{
			NetBoxObjectSpec: netboxv1alpha1.NetBoxObjectSpec{EndpointRef: "homelab"},
			Name:             "Donkerslootstraat (RTM)",
			RD:               "65000:10",
			ImportTargets:    targets,
		},
	}

	if len(claimed) > 0 {
		fields := make(map[string]any, len(claimed))
		for _, name := range claimed {
			fields["f:"+name] = map[string]any{}
		}

		raw, _ := json.Marshal(map[string]any{"f:spec": fields})
		vrf.ManagedFields = []metav1.ManagedFieldsEntry{{
			Manager: "kubectl", Operation: metav1.ManagedFieldsOperationApply,
			FieldsType: "FieldsV1", FieldsV1: &metav1.FieldsV1{Raw: raw},
		}}
	}

	return vrf
}

// refsTo is one route target reference per name, which is all the payload cases need: the ids
// come from the canned resolution, and what is under test is what the engine does with them.
func refsTo(names ...string) []netboxv1alpha1.RouteTargetRef {
	out := make([]netboxv1alpha1.RouteTargetRef, 0, len(names))
	for _, name := range names {
		out = append(out, netboxv1alpha1.RouteTargetRef{Name: name})
	}

	return out
}

// liveVRF is what the fake NetBox answers a create with. One id, because no case here
// distinguishes two -- what these tests read back is the payload, not the object.
func liveVRF() netbox.Object {
	return netbox.Object{"id": float64(11), "name": "Donkerslootstraat (RTM)", "rd": "65000:10"}
}

// sentBody is the request body the client would put on the wire: the recorded payload, run
// through the same encoder netbox.Client uses.
//
// Asserting on the encoded body rather than only on the Go value is the point. netbox.IDsOf
// coerces a desired id through asInt, which knows float64, int and string and *not* int64, so
// an []int64 payload compares as the empty set and the operator PATCHes the same list forever
// (cardinality_test.go, TestToManyRefDriftsCleanly). float64 survives the round trip;
// anything that does not shows up here.
func sentBody(t *testing.T, nb *fakeClient) map[string]any {
	t.Helper()

	payload := nb.lastPayload()
	if payload == nil {
		return nil
	}

	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encoding the payload = %v", err)
	}

	var body map[string]any
	if err := json.Unmarshal(encoded, &body); err != nil {
		t.Fatalf("decoding the payload = %v", err)
	}

	return body
}

// TestVRFImportTargetsReachThePayloadAsSortedIDs walks every cardinality ipam.VRF's
// `import_targets` has, and asserts each on the encoded request body.
func TestVRFImportTargetsReachThePayloadAsSortedIDs(t *testing.T) {
	tests := map[string]struct {
		spec    []netboxv1alpha1.RouteTargetRef
		claimed []string
		ids     []int64

		want   []any
		absent bool
	}{
		// Absent means "do not manage": NetBox keeps whatever route targets it has, and the
		// column never reaches the payload. A defaulted empty list here would strip the route
		// targets off the first VRF the operator ever touched.
		"an absent field is not written": {absent: true},

		// Explicitly empty is an instruction rather than an omission, and it is the only way
		// to remove the last route target -- NetBox replaces an M2M wholesale on PATCH and has
		// no remove verb.
		"an explicit empty list clears the relation": {
			spec: []netboxv1alpha1.RouteTargetRef{}, claimed: []string{"importTargets"},
			want: []any{},
		},

		"one element": {spec: refsTo("rt-a"), ids: []int64{5}, want: []any{float64(5)}},

		"several elements": {
			spec: refsTo("rt-a", "rt-b", "rt-c"), ids: []int64{5, 7, 9},
			want: []any{float64(5), float64(7), float64(9)},
		},

		// Sorted, not in spec order. NetBox does not preserve M2M order and the comparison is
		// order-independent, so carrying the spec's order would advertise an ordering nothing
		// downstream honours -- and would make the lastAppliedHash short-circuit miss.
		"the ids are sorted rather than in spec order": {
			spec: refsTo("rt-c", "rt-a", "rt-b"), ids: []int64{9, 5, 7},
			want: []any{float64(5), float64(7), float64(9)},
		},

		// Two references to one route target are one member of a set, which is what NetBox
		// stores either way.
		"a repeated element is one id": {
			spec: refsTo("rt-a", "rt-a"), ids: []int64{5, 5},
			want: []any{float64(5)},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			obj := vrfObject(tc.spec, tc.claimed...)
			nb := &fakeClient{created: liveVRF()}
			engine := vrfEngine(t, nb, &fakeRefs{
				resolution: resolvedList("importTargets", tc.ids...),
			})

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			body := sentBody(t, nb)

			got, sent := body["import_targets"]
			if tc.absent {
				if sent {
					t.Fatalf("body carries import_targets = %#v; an absent field is not managed", got)
				}

				return
			}

			if !sent {
				t.Fatalf("body has no import_targets; body = %#v", body)
			}

			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("body[import_targets] = %#v, want %#v", got, tc.want)
			}

			// The Go value the client was handed, not just its encoding: []int64 would encode
			// to the same JSON and then compare as the empty set on the next pass.
			if raw := nb.lastPayload()["import_targets"]; !isFloat64List(raw) {
				t.Errorf("payload[import_targets] = %#v, want []any of float64", raw)
			}
		})
	}
}

// isFloat64List reports whether a payload value is the shape netbox.IDsOf can read.
func isFloat64List(value any) bool {
	list, ok := value.([]any)
	if !ok {
		return false
	}

	for _, element := range list {
		if _, ok := element.(float64); !ok {
			return false
		}
	}

	return true
}

// TestVRFImportAndExportTargetsAreIndependentKeys is the acceptance case for one route target
// referenced from both relations: two payload keys, each resolved on its own.
func TestVRFImportAndExportTargetsAreIndependentKeys(t *testing.T) {
	obj := vrfObject(refsTo("rt-a"))
	obj.Spec.ExportTargets = refsTo("rt-a")

	nb := &fakeClient{created: liveVRF()}
	engine := vrfEngine(t, nb, &fakeRefs{resolution: resolver.Resolution{
		ByField: map[string]resolver.FieldRefs{
			"importTargets": {{ID: 5, ObjectType: "ipam.routetarget", Mode: resolver.ModeName}},
			"exportTargets": {{ID: 5, ObjectType: "ipam.routetarget", Mode: resolver.ModeName}},
		},
	}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	body := sentBody(t, nb)
	for _, key := range []string{"import_targets", "export_targets"} {
		if got := body[key]; !reflect.DeepEqual(got, []any{float64(5)}) {
			t.Errorf("body[%s] = %#v, want [5]", key, got)
		}
	}
}

// TestVRFPartiallyResolvedImportTargetsWriteNothing is the all-or-nothing rule on a real Kind.
//
// One unresolvable element of several withholds the whole field: a full-replacement write of
// the elements that did resolve is a deletion of the ones that did not, reported as a success.
// The rule is structural rather than policy -- Resolution.ByField has no representation for
// three of five -- and this is the assertion that the structure reaches the wire.
func TestVRFPartiallyResolvedImportTargetsWriteNothing(t *testing.T) {
	obj := vrfObject(refsTo("rt-a", "rt-missing", "rt-c"))

	nb := &fakeClient{created: liveVRF()}
	engine := vrfEngine(t, nb,
		&fakeRefs{resolution: blockedOn("importTargets", resolver.ErrRefNotFound, 0)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got, sent := sentBody(t, nb)["import_targets"]; sent {
		t.Errorf("body carries import_targets = %#v; a partially resolvable list writes nothing", got)
	}

	ready := vrfCondition(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready = %s/%s, want False/%s",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonWaitingForRef)
	}

	resolved := vrfCondition(obj, netboxv1alpha1.ConditionRefsResolved)
	if !strings.Contains(resolved.Message, "importTargets") {
		t.Errorf("RefsResolved message = %q, want it to name the field that did not resolve",
			resolved.Message)
	}
}

// TestVRFWithAnRDIsLookedUpByRDAlone is the natural-key half: `rd` is column-unique, so the
// lookup carries that filter and no name filter at all.
//
// The `name` filter is the one that must not appear. It is not unique on ipam.VRF, so a lookup
// carrying it can match a VRF somebody else owns -- and adopting that one reparents every
// prefix and address keyed on it.
func TestVRFWithAnRDIsLookedUpByRDAlone(t *testing.T) {
	obj := vrfObject(nil)

	nb := &fakeClient{created: liveVRF()}
	engine := vrfEngine(t, nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Params{"rd": "65000:10"}
	if got := nb.calls[0].params; !reflect.DeepEqual(got, want) {
		t.Errorf("lookup params = %v, want %v", got, want)
	}
}

// TestVRFWithoutAnRDIsLookedUpByNameAndANullRD is the fallback candidate, pin included.
//
// `?rd=null` rather than the filter merely being left out: candidates are tried in order and
// the engine falls through when one matches nothing, so a name-only candidate would be reached
// by a VRF that *does* declare an rd whose object does not exist yet. It would find an
// unrelated VRF of that name, adopt it, and PATCH its own rd onto it.
//
// The sentinel and not `?rd__empty=true`, even though `rd` is a CharField and NetBox does
// register `__empty` on one. That lookup is a string-length test, so it matches the empty string as well
// as NULL (netbox/extras/lookups.py:69-73), and `rd` is `blank=True null=True` with no
// normalisation on save -- so an empty string is a reachable, different value
// (docs/concepts/lookups.md#how-a-null-pin-is-spelled-and-why-it-depends-on-the-column).
func TestVRFWithoutAnRDIsLookedUpByNameAndANullRD(t *testing.T) {
	obj := vrfObject(nil)
	obj.Spec.RD = ""

	nb := &fakeClient{created: liveVRF()}
	engine := vrfEngine(t, nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Params{"name": "Donkerslootstraat (RTM)", "rd": "null"}
	if got := nb.calls[0].params; !reflect.DeepEqual(got, want) {
		t.Errorf("lookup params = %v, want %v", got, want)
	}
}

// TestVRFEnforceUniqueIsOnlyWrittenWhenSet is the *bool rule, which is the reason the field is
// a pointer: enforce_unique defaults to true in NetBox, so a plain bool would make "omitted"
// and "false" the same value and the operator would turn NetBox's default off on every VRF it
// adopted.
func TestVRFEnforceUniqueIsOnlyWrittenWhenSet(t *testing.T) {
	tests := map[string]struct {
		value  *bool
		want   any
		absent bool
	}{
		"omitted is not written": {absent: true},
		"false is written as false": {
			value: new(bool), want: false,
		},
		"true is written as true": {
			value: func() *bool { yes := true; return &yes }(), want: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			obj := vrfObject(nil)
			obj.Spec.EnforceUnique = tc.value

			nb := &fakeClient{created: liveVRF()}
			engine := vrfEngine(t, nb, &fakeRefs{})

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			got, sent := sentBody(t, nb)["enforce_unique"]
			if tc.absent {
				if sent {
					t.Fatalf("body carries enforce_unique = %#v; an omitted pointer is not managed", got)
				}

				return
			}

			if got != tc.want {
				t.Errorf("body[enforce_unique] = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// The update cases. Everything above reconciles a VRF that does not exist yet, which is where
// an M2M payload is easiest to get right: the whole list is new, so nothing has to be
// compared. What NBO-020's test plan actually asks for happens on the second apply -- a
// relation that already holds two route targets, and a spec that now holds one, none, or the
// same two in a different order.
//
// `status.id` set is what puts the engine on that path: it reads the live object by id and
// diffs against it, rather than looking a VRF up by its natural key.

// adoptedVRFID is the NetBox id of the VRF the update cases reconcile against.
const adoptedVRFID = 11

// liveVRFWith is what NetBox answers a GET with: an M2M read back as a list of *nested
// objects*, which is the shape that makes this worth asserting at all. NetBox takes bare ids
// on write and returns `{"id": 5, "name": "..."}` on read, so a comparison that did not
// normalise would find drift on every pass and PATCH the same list forever.
func liveVRFWith(ids ...int64) netbox.Object {
	targets := make([]any, 0, len(ids))
	for _, id := range ids {
		targets = append(targets, map[string]any{"id": float64(id), "name": fmt.Sprintf("65000:%d", id)})
	}

	live := liveVRF()
	live["id"] = float64(adoptedVRFID)
	live["import_targets"] = targets

	return live
}

// adoptedVRF is vrfObject already written to NetBox once.
func adoptedVRF(targets []netboxv1alpha1.RouteTargetRef, claimed ...string) *netboxv1alpha1.NetBoxVRF {
	vrf := vrfObject(targets, claimed...)
	vrf.Status.ID = adoptedVRFID

	return vrf
}

// TestVRFImportTargetsAreReplacedWholesale is NBO-020's named regression test, both halves of
// it: take one route target off a relation that holds two, then clear the relation entirely.
//
// It is the only test in this file that watches the *live* value change, and it is the one that
// proves the empty state does something rather than merely reaching the payload. NetBox has no
// remove verb -- a PATCH replaces the whole relation -- so removing the last element is
// expressible only as `[]`, and `[]` is expressible only because the engine reads
// `metadata.managedFields` rather than the Go value (docs/concepts/field-ownership.md).
//
// The absent row is here for the same reason: it is the state the other two have to be
// distinguishable from, and asserting it beside them is what makes "distinguishable" a claim
// about one code path rather than three.
func TestVRFImportTargetsAreReplacedWholesale(t *testing.T) {
	tests := map[string]struct {
		spec    []netboxv1alpha1.RouteTargetRef
		claimed []string
		ids     []int64

		wantMethods []string
		wantPatched any
	}{
		// One element removed: the shorter list is the whole instruction, and it is one PATCH
		// rather than a delete followed by a write.
		"removing one target sends the remaining one": {
			spec: refsTo("rt-a"), ids: []int64{5},
			wantMethods: []string{"GET", "PATCH"}, wantPatched: []any{float64(5)},
		},

		// The last one off. `importTargets: []` marshals to nothing at all, so the claim in
		// managedFields is the only evidence the user wrote it -- and without that evidence
		// this row would be the absent row and the route targets would stay.
		"an explicit empty list clears the relation": {
			spec: []netboxv1alpha1.RouteTargetRef{}, claimed: []string{"importTargets"},
			wantMethods: []string{"GET", "PATCH"}, wantPatched: []any{},
		},

		// Absent: NetBox keeps both. Not one write, because nothing else on this VRF drifts
		// either -- which is also the assertion that the column was left out of the *desired*
		// object rather than merely out of the diff.
		"an absent field leaves the relation alone": {
			wantMethods: []string{"GET"},
		},

		// Reordered, and therefore unchanged. The ids are sent sorted and compared as a set,
		// so the two spellings of one relation produce one payload.
		"reordering the list writes nothing": {
			spec: refsTo("rt-b", "rt-a"), ids: []int64{7, 5},
			wantMethods: []string{"GET"},
		},

		// The same set with a duplicate in it is still the same set.
		"repeating an element writes nothing": {
			spec: refsTo("rt-a", "rt-b", "rt-a"), ids: []int64{5, 7, 5},
			wantMethods: []string{"GET"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			obj := adoptedVRF(tc.spec, tc.claimed...)

			nb := &fakeClient{get: liveVRFWith(5, 7), patched: liveVRFWith(5, 7)}
			engine := vrfEngine(t, nb, &fakeRefs{
				resolution: resolvedList("importTargets", tc.ids...),
			})

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			if got := nb.methods(); !slices.Equal(got, tc.wantMethods) {
				t.Fatalf("netbox calls = %v, want %v", got, tc.wantMethods)
			}

			if tc.wantPatched == nil {
				return
			}

			if got := sentBody(t, nb)["import_targets"]; !reflect.DeepEqual(got, tc.wantPatched) {
				t.Errorf("patch[import_targets] = %#v, want %#v", got, tc.wantPatched)
			}
		})
	}
}

// TestVRFClearedImportTargetsSettle is the anti-hot-loop half of the row above.
//
// The clear has to drift exactly once. A second pass over a VRF whose relation is now empty
// must find nothing to do, or the operator PATCHes `import_targets: []` at every resync for
// the lifetime of the object -- the failure docs/concepts/drift.md opens by warning about, and
// the one an empty *list* is most exposed to, since `[]` and NetBox's answer of `[]` go through
// two different normalisations to meet.
func TestVRFClearedImportTargetsSettle(t *testing.T) {
	obj := adoptedVRF([]netboxv1alpha1.RouteTargetRef{}, "importTargets")

	cleared := liveVRFWith()
	nb := &fakeClient{get: cleared, patched: cleared}
	engine := vrfEngine(t, nb, &fakeRefs{resolution: resolvedList("importTargets")})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); !slices.Equal(got, []string{"GET"}) {
		t.Errorf("netbox calls = %v, want just the read: an already-empty relation is not drift", got)
	}
}

// TestVRFUnresolvedImportTargetIsNamedByItsIndex runs the real resolver against the shipped
// ipam.VRF descriptor, which is the only way the indexed path is tested end to end: the fake
// resolvers in this package build their own Blockers, so they would be asserting the fixture.
//
// One of three route targets is missing. The whole field is withheld, nothing is written, no
// error is returned -- and the condition names `importTargets[1]`, which is the promise
// docs/reference/netboxvrf.md makes twice and the acceptance criterion NBO-020 states.
func TestVRFUnresolvedImportTargetIsNamedByItsIndex(t *testing.T) {
	routeTargetGVK := netboxv1alpha1.RouteTargetRef{}.TargetGVK()

	obj := vrfObject(refsTo("rt-a", "rt-missing", "rt-c"))

	nb := &fakeClient{created: liveVRF()}
	engine := vrfEngine(t, nb, &resolver.Resolver{Objects: fakeCluster{objects: []*unstructured.Unstructured{
		readyTarget(routeTargetGVK, "team-a", "rt-a", 5),
		readyTarget(routeTargetGVK, "team-a", "rt-c", 9),
	}}})

	// No returned error: a reference waiting for its target is a state, and an error return
	// would put controller-runtime's backoff on top of a wait an event clears.
	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v, want no error: an unresolved element is a state", err)
	}

	if len(nb.calls) != 0 {
		t.Errorf("netbox calls = %v, want none: a partially resolvable list writes nothing", nb.calls)
	}

	resolved := vrfCondition(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionFalse || resolved.Reason != netboxv1alpha1.ReasonRefNotFound {
		t.Errorf("RefsResolved = %s/%s, want False/%s",
			resolved.Status, resolved.Reason, netboxv1alpha1.ReasonRefNotFound)
	}

	if !strings.Contains(resolved.Message, "importTargets[1]") {
		t.Errorf("RefsResolved message = %q, want it to name importTargets[1]: the element, "+
			"not the relation its two siblings share", resolved.Message)
	}

	// And nothing invents a position for the elements that resolved.
	for _, unwanted := range []string{"importTargets[0]", "importTargets[2]"} {
		if strings.Contains(resolved.Message, unwanted) {
			t.Errorf("RefsResolved message = %q, want no mention of %s: it resolved",
				resolved.Message, unwanted)
		}
	}
}

// vrfCondition returns one condition off a NetBoxVRF, or the zero value when it was never set.
func vrfCondition(obj *netboxv1alpha1.NetBoxVRF, condType string) metav1.Condition {
	for _, condition := range obj.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}
