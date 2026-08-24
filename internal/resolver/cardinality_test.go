package resolver

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestResolveToManyReference is NBO-088's acceptance case, at the four cardinalities a list
// has: none, one, several, and several of which one does not resolve.
//
// The last row is the one the ticket is really about. A partially resolvable list must
// contribute *nothing*: NetBox's M2M write replaces the whole list, so writing the three
// that resolved is not a smaller version of the right answer, it is a deletion of the two
// that did not -- reported as a successful write.
func TestResolveToManyReference(t *testing.T) {
	targets := []target{
		routeTarget("rt-65000-1", 5),
		routeTarget("rt-65000-2", 7),
		routeTarget("rt-65000-3", 9),
	}

	tests := []struct {
		name string
		spec []any

		wantIDs     []int64
		wantBlocked []string
	}{
		{name: "an empty list resolves to no ids", spec: []any{}, wantIDs: []int64{}},
		{name: "one element", spec: refList("rt-65000-1"), wantIDs: []int64{5}},
		{
			name: "several elements", spec: refList("rt-65000-1", "rt-65000-2", "rt-65000-3"),
			wantIDs: []int64{5, 7, 9},
		},
		{
			// Sorted, not spec order. NetBox does not preserve M2M order and netbox.Drift
			// compares these as a set, so a resolver that carried the spec's order would be
			// advertising an ordering the comparison then ignores.
			name: "the ids are sorted rather than in spec order",
			spec: refList("rt-65000-3", "rt-65000-1", "rt-65000-2"), wantIDs: []int64{5, 7, 9},
		},
		{
			// Two references to one object are one member of a set, which is what NetBox
			// stores either way.
			name: "a repeated element is one id",
			spec: refList("rt-65000-1", "rt-65000-1"), wantIDs: []int64{5},
		},
		{
			name: "one unresolvable among several resolves nothing",
			spec: refList("rt-65000-1", "rt-nonexistent", "rt-65000-3"),
			// Only the element that failed is a blocker. The two that resolved are not
			// reported as problems -- they are simply not written, because the field is not.
			wantBlocked: []string{"rt-nonexistent"},
		},
		{
			name:        "several unresolvable elements are each named",
			spec:        refList("rt-nope-1", "rt-65000-2", "rt-nope-2"),
			wantBlocked: []string{"rt-nope-1", "rt-nope-2"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{objects: targets}
			resolver := &Resolver{Objects: reader, Kinds: kinds()}

			obj := referrer("vrf-a", map[string]any{"importTargets": tc.spec})

			resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor())
			if err != nil {
				t.Fatalf("ResolveAll() = %v, want no error: an unresolved element is a state", err)
			}

			refs, resolved := resolution.ByField["importTargets"]

			if len(tc.wantBlocked) > 0 {
				assertNothingWritten(t, resolution, resolved, tc.wantBlocked)

				return
			}

			if !resolved {
				t.Fatalf("importTargets did not resolve; blocked = %+v", resolution.Blocked)
			}

			if len(resolution.Blocked) != 0 {
				t.Errorf("Blocked = %+v, want none", resolution.Blocked)
			}

			if got := refs.IDs(); !reflect.DeepEqual(got, tc.wantIDs) {
				t.Errorf("IDs() = %v, want %v", got, tc.wantIDs)
			}
		})
	}
}

// assertNothingWritten is the partial-list rule: the field is absent from ByField, and every
// element that failed is named in the message a human reads.
func assertNothingWritten(t *testing.T, resolution Resolution, resolved bool, wantNamed []string) {
	t.Helper()

	if resolved {
		t.Fatal("a partially resolvable list resolved; three of five ids is a deletion, not a success")
	}

	if len(resolution.Blocked) != len(wantNamed) {
		t.Fatalf("Blocked = %+v, want one blocker per unresolved element (%d)", resolution.Blocked, len(wantNamed))
	}

	message := resolution.Message()
	for _, name := range wantNamed {
		if !strings.Contains(message, name) {
			t.Errorf("Message() = %q, want it to name the unresolved element %q", message, name)
		}
	}

	for _, blocker := range resolution.Blocked {
		if blocker.Field != "importTargets" {
			t.Errorf("Blocked field = %q, want the spec field the user wrote", blocker.Field)
		}

		if blocker.Reason != netboxv1alpha1.ReasonRefNotFound {
			t.Errorf("Blocked reason = %q, want %q", blocker.Reason, netboxv1alpha1.ReasonRefNotFound)
		}
	}
}

// TestResolveToManyIsIndependentOfTheToOneFields checks that one field's cardinality does not
// leak into another's: a resolved list and a resolved scalar coexist on one object, keyed by
// the spec names the user wrote.
func TestResolveToManyIsIndependentOfTheToOneFields(t *testing.T) {
	reader := &fakeReader{objects: []target{readyTarget(), routeTarget("rt-65000-1", 5)}}
	resolver := &Resolver{Objects: reader, Kinds: kinds()}

	obj := referrer("vrf-a", map[string]any{
		"regionRef":     map[string]any{"name": "emea"},
		"importTargets": refList("rt-65000-1"),
	})

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	if got := resolution.ByField["regionRef"].IDs(); !reflect.DeepEqual(got, []int64{12}) {
		t.Errorf("regionRef IDs() = %v, want [12]", got)
	}

	if got := resolution.ByField["importTargets"].IDs(); !reflect.DeepEqual(got, []int64{5}) {
		t.Errorf("importTargets IDs() = %v, want [5]", got)
	}
}

// TestResolveRefusesAShapeItsClassDoesNotDeclare covers the two ways a descriptor and a CRD
// can disagree about a field's cardinality.
//
// Both are refused rather than coerced. Coercing is what the previous behaviour amounted to:
// a list under a field with no cardinality was skipped, so every to-many reference in the
// catalogue was declared, dropped and reported NotImplemented (NBO-088). Silently taking the
// first element of a list, or wrapping one object in a list, would be the same class of
// mistake with a worse failure mode -- a wrong id written and reported as success.
func TestResolveRefusesAShapeItsClassDoesNotDeclare(t *testing.T) {
	tests := map[string]map[string]any{
		"a list under a to-one field": {"regionRef": refList("emea", "apac")},
		"an object under a to-many field": {
			"importTargets": map[string]any{"name": "rt-65000-1"},
		},
	}

	for name, spec := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &Resolver{Objects: &fakeReader{}, Kinds: kinds()}

			_, err := resolver.ResolveAll(context.Background(), &fakeNetBox{},
				referrer("vrf-a", spec), siteDescriptor())
			if err == nil {
				t.Fatal("ResolveAll() = nil, want an error: the descriptor and the spec disagree")
			}

			// An error rather than a Blocker: a Blocker says the manifest is waiting for
			// something, and nothing about this object is going to change by waiting.
			var refErr *Error
			if errors.As(err, &refErr) {
				t.Errorf("ResolveAll() = %v, want a plain error rather than a reference blocker", err)
			}
		})
	}
}
