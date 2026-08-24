package reconciler

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
// `rd__isnull=true` rather than the filter merely being left out: candidates are tried in
// order and the engine falls through when one matches nothing, so a name-only candidate would
// be reached by a VRF that *does* declare an rd whose object does not exist yet. It would find
// an unrelated VRF of that name, adopt it, and PATCH its own rd onto it.
func TestVRFWithoutAnRDIsLookedUpByNameAndANullRD(t *testing.T) {
	obj := vrfObject(nil)
	obj.Spec.RD = ""

	nb := &fakeClient{created: liveVRF()}
	engine := vrfEngine(t, nb, &fakeRefs{})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := netbox.Params{"name": "Donkerslootstraat (RTM)", "rd__isnull": "true"}
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

// vrfCondition returns one condition off a NetBoxVRF, or the zero value when it was never set.
func vrfCondition(obj *netboxv1alpha1.NetBoxVRF, condType string) metav1.Condition {
	for _, condition := range obj.Status.Conditions {
		if condition.Type == condType {
			return condition
		}
	}

	return metav1.Condition{}
}
