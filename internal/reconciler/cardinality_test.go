package reconciler

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/resolver"
)

// resolvedList is a resolution that turned one to-many reference into several ids.
func resolvedList(field string, ids ...int64) resolver.Resolution {
	refs := make(resolver.FieldRefs, 0, len(ids))
	for _, id := range ids {
		refs = append(refs, resolver.Result{ID: id, ObjectType: "ipam.asn", Mode: resolver.ModeName})
	}

	return resolver.Resolution{ByField: map[string]resolver.FieldRefs{field: refs}}
}

// TestToManyRefIsWrittenAsAListOfIDs is the engine half of NBO-088: a resolved to-many
// reference reaches the payload as the list NetBox takes, not as one id and not as the
// references the user wrote.
func TestToManyRefIsWrittenAsAListOfIDs(t *testing.T) {
	tests := []struct {
		name string
		ids  []int64
		want []any
	}{
		{name: "several elements", ids: []int64{7, 5}, want: []any{float64(5), float64(7)}},
		{name: "one element", ids: []int64{5}, want: []any{float64(5)}},

		// An empty list is a value, not an omission: it says this object has no ASNs, which
		// is a thing NetBox can be told.
		{name: "no elements", ids: nil, want: []any{}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			obj := fakeObject()
			obj.Spec.ASNRefs = []fakeRef{{Name: "as64500"}}

			nb := &fakeClient{created: liveTag(7)}
			engine := engineWith(t, fakeDescriptor(), nb,
				&fakeRefs{resolution: resolvedList("asnRefs", tc.ids...)})

			if _, err := engine.Reconcile(context.Background(), obj); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			got := nb.lastPayload()["asns"]
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("payload[asns] = %#v, want %#v", got, tc.want)
			}

			resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
			if resolved.Status != metav1.ConditionTrue {
				t.Errorf("RefsResolved = %s/%s (%s), want True",
					resolved.Status, resolved.Reason, resolved.Message)
			}
		})
	}
}

// TestToManyRefIsSortedAndDeduplicated pins the shape of the list the engine sends.
//
// Sorted and deduplicated rather than in spec order, because netbox.Drift compares an M2M
// field as an order-independent set (docs/concepts/drift.md rule 3) and NetBox does not
// preserve the order it was given. Carrying the spec's order would advertise an ordering
// that nothing downstream honours; carrying duplicates would send a list NetBox collapses.
func TestToManyRefIsSortedAndDeduplicated(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ASNRefs = []fakeRef{{Name: "as64500"}, {Name: "as64501"}, {Name: "as64500"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, fakeDescriptor(), nb,
		&fakeRefs{resolution: resolvedList("asnRefs", 9, 5, 9)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	want := []any{float64(5), float64(9)}
	if got := nb.lastPayload()["asns"]; !reflect.DeepEqual(got, want) {
		t.Errorf("payload[asns] = %#v, want %#v", got, want)
	}
}

// TestToManyRefDriftsCleanly is the anti-hot-loop assertion for the new class, and the reason
// the payload holds float64 rather than int64.
//
// NetBox returns an M2M as a list of nested objects and takes it as a list of bare ids, and
// netbox.IDsOf reads the desired side through a coercion that knows float64, int and string
// but not int64. An []int64 in the payload therefore compares as the empty set: the drift
// would never be satisfied and the operator would PATCH the same list for the lifetime of the
// object.
func TestToManyRefDriftsCleanly(t *testing.T) {
	sent := netbox.Object{"name": "Managed", "slug": "managed", "asns": []any{float64(5), float64(9)}}
	live := netbox.Object{
		"name": "Managed", "slug": "managed",
		"asns": []any{
			map[string]any{"id": float64(9), "asn": float64(64501)},
			map[string]any{"id": float64(5), "asn": float64(64500)},
		},
	}

	rules := fieldRules(fakeDescriptor())

	if !rules.M2M["asns"] {
		t.Fatal("asns is not compared as an M2M set; the class did not reach the comparison")
	}

	if drift := netbox.Drift(live, sent, rules); len(drift) != 0 {
		t.Errorf("second reconcile would PATCH %v -- this is an infinite loop", drift)
	}
}

// TestPartiallyResolvedToManyRefWritesNothing is NBO-088's central rule at the engine level.
//
// The resolver reports the field as blocked rather than partly resolved, so the field is
// unresolved, and since #195 an unresolved declared reference withholds the write entirely.
// The rule NBO-088 exists for is still the one on trial: writing the elements that did resolve
// would be a full-replacement write of a shorter list -- a deletion, reported as a success --
// and the assertion on the calls is what keeps the payload check from passing vacuously
// against a request that was never made.
func TestPartiallyResolvedToManyRefWritesNothing(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ASNRefs = []fakeRef{{Name: "as64500"}, {Name: "as64501"}}

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, fakeDescriptor(), nb,
		&fakeRefs{resolution: blockedOn("asnRefs", resolver.ErrRefNotFound, 0)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if _, sent := nb.lastPayload()["asns"]; sent {
		t.Errorf("payload carries asns = %v; a partially resolvable list writes nothing",
			nb.lastPayload()["asns"])
	}

	if len(nb.calls) != 0 {
		t.Errorf("netbox calls = %v, want none: the spec declares asnRefs and they did not resolve", nb.calls)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonWaitingForRef)
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if !strings.Contains(resolved.Message, "asnRefs") {
		t.Errorf("RefsResolved message = %q, want it to name the field that did not resolve", resolved.Message)
	}
}

// resolvedAgainstUnreadyTarget is a resolution that produced an id from a target that is not
// Ready itself, which is what NBO-089 made possible: a reference needs its target to hold an
// id, not to be Ready.
func resolvedAgainstUnreadyTarget(field string, id int64, detail string) resolver.Resolution {
	return resolver.Resolution{ByField: map[string]resolver.FieldRefs{
		field: {{
			ID: id, ObjectType: "dcim.region", Mode: resolver.ModeName,
			Target:         types.NamespacedName{Namespace: "catalogue", Name: "emea"},
			TargetNotReady: detail,
		}},
	}}
}

// TestUnreadyTargetIsReportedRatherThanWaitedFor is the engine half of NBO-089.
//
// The referrer reaches Ready and writes the id, because an id is the whole claim it needed --
// and its RefsResolved message says the object behind that id is unfinished, quoting the
// target's own condition. Both halves matter: without the first, `driftMode: Report` blocks
// every referrer in the namespace indefinitely; without the second, a Ready object silently
// points at something that is not.
func TestUnreadyTargetIsReportedRatherThanWaitedFor(t *testing.T) {
	obj := fakeObject()
	obj.Spec.ParentRef = &fakeRef{Name: "emea"}

	detail := `target Ready=False, Reason=ReportPending: "the endpoint's driftMode is Report"`
	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, fakeDescriptor(), nb,
		&fakeRefs{resolution: resolvedAgainstUnreadyTarget("parentRef", 42, detail)})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.lastPayload()["parent"]; got != int64(42) {
		t.Errorf("payload[parent] = %v, want 42: the id resolved, so it is written", got)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue || ready.Reason != netboxv1alpha1.ReasonSynced {
		t.Errorf("Ready = %s/%s, want True/%s -- an unready target is not the referrer's wait",
			ready.Status, ready.Reason, netboxv1alpha1.ReasonSynced)
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionTrue || resolved.Reason != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("RefsResolved = %s/%s, want True/%s",
			resolved.Status, resolved.Reason, netboxv1alpha1.ReasonAllResolved)
	}

	for _, want := range []string{"parentRef", "resolved, target not ready", "ReportPending"} {
		if !strings.Contains(resolved.Message, want) {
			t.Errorf("RefsResolved message = %q, want it to contain %q", resolved.Message, want)
		}
	}
}

// TestEmptyToManyRefIsDeclaredAndDoesNotBlock is the corner where #169 and #195 meet.
//
// `asnRefs: []` *declares* the field -- that is exactly what field ownership exists to tell
// apart from an absent one (NBO-079), and the empty list is an instruction: this object has no
// ASNs, clear whatever NetBox holds. But there is nothing in it to resolve, so it cannot be a
// precondition, and a rule that read "declared and not in ByField blocks" would deadlock every
// object that clears a to-many field.
//
// The real resolver rather than a canned resolution, because that is where the guarantee has
// to hold: ResolveAll files an empty FieldRefs for an empty list, and applyResolved counts a
// present key as resolved regardless of its length. A canned resolution would be asserting the
// fixture.
func TestEmptyToManyRefIsDeclaredAndDoesNotBlock(t *testing.T) {
	asnGVK := schema.GroupVersionKind{Group: "netbox.kubeforge.org", Version: "v1alpha1", Kind: "NetBoxASN"}

	descriptor := fakeDescriptor()
	descriptor.Fields = withTarget(descriptor.Fields, "asnRefs", asnGVK)

	// `asnRefs: []` marshals to nothing at all -- every field of a real kind carries
	// `omitempty` -- so the managed-fields entry is the only thing that says the user wrote
	// it, exactly as NBO-079 describes. Setting the Go field to an empty slice would test the
	// *absent* case by accident.
	obj := fakeObject()
	ownedBy(obj, "flux", "asnRefs")

	nb := &fakeClient{created: liveTag(7)}
	engine := engineWith(t, descriptor, nb, &resolver.Resolver{Objects: fakeCluster{}})

	if _, err := engine.Reconcile(context.Background(), obj); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got := nb.methods(); !slices.Equal(got, []string{"GETONE", "POST"}) {
		t.Errorf("netbox calls = %v, want [GETONE POST]: an empty list needs no resolution", got)
	}

	// The instruction carried out, not the field omitted: an empty list is a value NetBox can
	// be told, and omitting it would mean "do not manage this reference".
	if got := nb.lastPayload()["asns"]; !reflect.DeepEqual(got, []any{}) {
		t.Errorf("payload[asns] = %#v, want an empty list", got)
	}

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionTrue {
		t.Errorf("Ready = %s/%s (%s), want True", ready.Status, ready.Reason, ready.Message)
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if resolved.Status != metav1.ConditionTrue {
		t.Errorf("RefsResolved = %s/%s (%s), want True", resolved.Status, resolved.Reason, resolved.Message)
	}
}
