package reconciler

import (
	"context"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
// The resolver reports the field as blocked rather than partly resolved, so the engine leaves
// the column out of the payload entirely and Ready reports the wait. Writing the elements that
// did resolve would be a full-replacement write of a shorter list -- a deletion, reported as a
// success.
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

	ready := conditionOf(obj, netboxv1alpha1.ConditionReady)
	if ready.Status != metav1.ConditionFalse || ready.Reason != netboxv1alpha1.ReasonWaitingForRef {
		t.Errorf("Ready = %s/%s, want False/%s", ready.Status, ready.Reason, netboxv1alpha1.ReasonWaitingForRef)
	}

	resolved := conditionOf(obj, netboxv1alpha1.ConditionRefsResolved)
	if !strings.Contains(resolved.Message, "asnRefs") {
		t.Errorf("RefsResolved message = %q, want it to name the field that did not resolve", resolved.Message)
	}
}
