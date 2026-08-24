package resolver

import (
	"context"
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestReferenceRequiresAnIDRatherThanReadiness is NBO-089's decision, stated as a table over
// the target states a referrer can meet.
//
// The discriminator is the target's Ready **reason**, not its status. `Ready=False` is not one
// state: `driftMode: Report` makes it the steady state of every object at an endpoint by
// design (NBO-065), so requiring readiness meant a Report namespace blocked every object
// pointing into it for as long as the adoption ran -- and an adoption is exactly when a
// catalogue namespace holds the objects everything points at.
//
// An id is only recorded once the object provably exists in NetBox, which is the whole claim a
// referrer needs. What refuses is the small set of reasons where the id is for an object the
// target no longer describes.
func TestReferenceRequiresAnIDRatherThanReadiness(t *testing.T) {
	tests := []struct {
		name   string
		reason string

		wantCause error
		wantNote  bool
	}{
		{
			// The ticket's case. Report detects drift, reports it and never corrects it, so
			// Ready=False is the steady state rather than a transient.
			name: "a target in Report mode resolves", reason: netboxv1alpha1.ReasonReportPending,
			wantNote: true,
		},
		{
			// The identical shape, on a different field of the endpoint. Nobody filed it,
			// which is the argument for keying on the reason rather than enumerating modes.
			name: "a target at a DryRun endpoint resolves", reason: netboxv1alpha1.ReasonDryRunPending,
			wantNote: true,
		},
		{
			// A target whose own reference is unresolved has an id and a missing column. Its
			// id is right, and blocking here would cascade one unresolved leaf up a whole
			// chain -- the opposite of "a graph applied in any order converges".
			name: "a target waiting on its own reference resolves", reason: netboxv1alpha1.ReasonWaitingForRef,
			wantNote: true,
		},
		{
			name: "a target with a deferred field pending resolves", reason: netboxv1alpha1.ReasonDeferredFieldPending,
			wantNote: true,
		},
		{
			// NetBox being unreachable says nothing about which object the id names.
			name: "a target whose writes are failing resolves", reason: netboxv1alpha1.ReasonAPIError,
			wantNote: true,
		},
		{
			// NetBox holds an object matching this CR's natural key that it may not claim, so
			// an id it still carries came from a key it no longer has.
			name: "a target in Conflict is refused", reason: netboxv1alpha1.ReasonConflict,
			wantCause: ErrRefTargetFailed,
		},
		{
			name: "a target whose adoption matched nothing is refused", reason: netboxv1alpha1.ReasonAdoptOnly,
			wantCause: ErrRefTargetFailed,
		},
		{
			// NetBox rejected the payload, so the object's fields are not what the CR says.
			name: "a target NetBox rejected is refused", reason: netboxv1alpha1.ReasonInvalid,
			wantCause: ErrRefTargetFailed,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeReader{objects: []target{{
				gvk: regionGVK, namespace: "team-a", name: "emea", id: 12,
				ready: metav1.ConditionFalse, reason: tc.reason, message: "netbox differs from the spec",
			}}}
			resolver := &Resolver{Objects: reader, Kinds: kinds()}

			got, err := resolver.Resolve(context.Background(), Request{
				NetBox:   &fakeNetBox{},
				Referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
				Field:    regionField(),
				Ref:      netboxv1alpha1.ObjectRef{Name: "emea"},
			})

			if tc.wantCause != nil {
				assertRefused(t, err, tc.wantCause, tc.reason)

				return
			}

			if err != nil {
				t.Fatalf("Resolve() = %v, want the id: an id is the claim a referrer needs", err)
			}

			if got.ID != 12 {
				t.Errorf("Resolve().ID = %d, want 12", got.ID)
			}

			if tc.wantNote && got.TargetNotReady == "" {
				t.Error("TargetNotReady is empty; a referrer that proceeds still has to say the target is unfinished")
			}

			if tc.wantNote && !strings.Contains(got.TargetNotReady, tc.reason) {
				t.Errorf("TargetNotReady = %q, want it to quote the target's reason %q", got.TargetNotReady, tc.reason)
			}
		})
	}
}

// assertRefused checks the refusal carries the classification, the reason and the target's own
// words -- so a reader is sent at the target rather than at the manifest in front of them.
func assertRefused(t *testing.T, err error, wantCause error, wantReason string) {
	t.Helper()

	if !errors.Is(err, wantCause) {
		t.Fatalf("Resolve() = %v, want %v", err, wantCause)
	}

	if got := Classify(err).Reason; got != netboxv1alpha1.ReasonRefTargetFailed {
		t.Errorf("Classify().Reason = %q, want %q", got, netboxv1alpha1.ReasonRefTargetFailed)
	}

	// No timer: only an edit to the target changes the answer, and an edit is an event.
	if got := Classify(err).Requeue; got != 0 {
		t.Errorf("Classify().Requeue = %v, want none: a broken target is fixed by a person", got)
	}

	if !strings.Contains(err.Error(), wantReason) {
		t.Errorf("error = %q, want it to quote the target's reason %q", err.Error(), wantReason)
	}
}

// TestNoIDIsStillNotReady is the other half of the decision: `ErrRefNotReady` keeps its real
// meaning, which is a target with no id at all.
//
// That is the case which genuinely must wait -- there is nothing to point at yet -- and it is
// the case the ticket's recommendation was careful to preserve. A target that has not
// reconciled once reports no Ready condition and no id, and both shapes have to wait.
func TestNoIDIsStillNotReady(t *testing.T) {
	tests := map[string]target{
		"no id and no conditions": {gvk: regionGVK, namespace: "team-a", name: "emea"},
		"no id and Ready=False": {
			gvk: regionGVK, namespace: "team-a", name: "emea",
			ready: metav1.ConditionFalse, reason: netboxv1alpha1.ReasonReportPending,
		},
		// A Conflict with no id is the ordinary shape of a Conflict: the engine refused to
		// claim anything, so there is nothing to refuse a referrer *over* -- it is a wait.
		"no id and a Conflict": {
			gvk: regionGVK, namespace: "team-a", name: "emea",
			ready: metav1.ConditionFalse, reason: netboxv1alpha1.ReasonConflict,
		},
	}

	for name, object := range tests {
		t.Run(name, func(t *testing.T) {
			resolver := &Resolver{Objects: &fakeReader{objects: []target{object}}, Kinds: kinds()}

			_, err := resolver.Resolve(context.Background(), Request{
				NetBox:   &fakeNetBox{},
				Referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
				Field:    regionField(),
				Ref:      netboxv1alpha1.ObjectRef{Name: "emea"},
			})

			if !errors.Is(err, ErrRefNotReady) {
				t.Fatalf("Resolve() = %v, want %v", err, ErrRefNotReady)
			}

			if got := Classify(err).Reason; got != netboxv1alpha1.ReasonRefNotReady {
				t.Errorf("Classify().Reason = %q, want %q", got, netboxv1alpha1.ReasonRefNotReady)
			}
		})
	}
}

// TestReadyTargetCarriesNoNote pins the negative: a target that is Ready leaves nothing on the
// referrer's condition, so the note means something when it is there.
func TestReadyTargetCarriesNoNote(t *testing.T) {
	resolver := &Resolver{Objects: &fakeReader{objects: []target{readyTarget()}}, Kinds: kinds()}

	got, err := resolver.Resolve(context.Background(), Request{
		NetBox:   &fakeNetBox{},
		Referrer: types.NamespacedName{Namespace: "team-a", Name: "ams"},
		Field:    regionField(),
		Ref:      netboxv1alpha1.ObjectRef{Name: "emea"},
	})
	if err != nil {
		t.Fatalf("Resolve() = %v", err)
	}

	if got.TargetNotReady != "" {
		t.Errorf("TargetNotReady = %q, want empty for a Ready target", got.TargetNotReady)
	}
}

// TestToManyRefReportsEachUnreadyTarget is where the two tickets meet: a to-many reference
// resolves against several targets, and each one that is unfinished is reported on its own
// element rather than collapsed into one note about the field.
func TestToManyRefReportsEachUnreadyTarget(t *testing.T) {
	reporting := routeTarget("rt-65000-1", 5)
	reporting.ready, reporting.reason = metav1.ConditionFalse, netboxv1alpha1.ReasonReportPending

	reader := &fakeReader{objects: []target{reporting, routeTarget("rt-65000-2", 7)}}
	resolver := &Resolver{Objects: reader, Kinds: kinds()}

	obj := referrer("vrf-a", map[string]any{"importTargets": refList("rt-65000-1", "rt-65000-2")})

	resolution, err := resolver.ResolveAll(context.Background(), &fakeNetBox{}, obj, siteDescriptor())
	if err != nil {
		t.Fatalf("ResolveAll() = %v", err)
	}

	refs, ok := resolution.ByField["importTargets"]
	if !ok {
		t.Fatalf("importTargets did not resolve; blocked = %+v", resolution.Blocked)
	}

	notes := 0
	for _, ref := range refs {
		if ref.TargetNotReady != "" {
			notes++
		}
	}

	if notes != 1 {
		t.Errorf("%d elements report an unready target, want exactly the one that is", notes)
	}
}
