package resolver

import (
	"errors"
	"testing"
	"time"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// TestClassify is the error table, row by row: every cause maps to exactly one condition
// reason and one requeue policy.
//
// It is a test rather than a comment because the table is the product. A cause with the wrong
// requeue is either an object that waits ten minutes for something an event would have
// delivered, or one that hammers NetBox for something only a human can fix.
func TestClassify(t *testing.T) {
	tests := []struct {
		name        string
		err         error
		wantReason  string
		wantRequeue time.Duration
	}{
		{
			// No timer at all: the target's own reconcile is what changes this, and NBO-013's
			// watch re-enqueues the referrer the moment it does.
			name:       "not ready waits for an event",
			err:        blocked(ErrRefNotReady, ModeName),
			wantReason: netboxv1alpha1.ReasonRefNotReady,
		},
		{
			// A CR that does not exist yet is a creation event this operator receives.
			name:       "a missing CR waits for an event",
			err:        blocked(ErrRefNotFound, ModeName),
			wantReason: netboxv1alpha1.ReasonRefNotFound,
		},
		{
			// NetBox sends nothing when an object appears, so a timer is the only thing that
			// will ever notice.
			name:        "a missing netbox object gets a timer",
			err:         blocked(ErrRefNotFound, ModeSlug),
			wantReason:  netboxv1alpha1.ReasonRefNotFound,
			wantRequeue: netboxRetry,
		},
		{
			name:        "a missing lookup target gets a timer",
			err:         blocked(ErrRefNotFound, ModeLookup),
			wantReason:  netboxv1alpha1.ReasonRefNotFound,
			wantRequeue: netboxRetry,
		},
		{
			name:        "a missing raw id gets a timer",
			err:         blocked(ErrRefNotFound, ModeID),
			wantReason:  netboxv1alpha1.ReasonRefNotFound,
			wantRequeue: netboxRetry,
		},
		{
			name:        "ambiguity needs a human",
			err:         blocked(ErrRefAmbiguous, ModeSlug),
			wantReason:  netboxv1alpha1.ReasonRefAmbiguous,
			wantRequeue: humanRetry,
		},
		{
			name:       "a denied reference waits for its grant",
			err:        blocked(ErrRefDenied, ModeName),
			wantReason: netboxv1alpha1.ReasonRefDenied,
		},
		{
			name:       "a cycle waits for a spec change",
			err:        blocked(ErrRefCycle, ModeName),
			wantReason: netboxv1alpha1.ReasonRefCycle,
		},
		{
			// Its own reason rather than RefCycle: a 40-deep hierarchy told "you have a cycle"
			// sends its author looking for one that is not there. No timer either -- the graph
			// is the size it is until somebody edits it.
			name:       "a graph too deep to walk waits for a spec change",
			err:        blocked(ErrRefDepthExceeded, ModeName),
			wantReason: netboxv1alpha1.ReasonRefDepthExceeded,
		},
		{
			name:        "an unavailable kind waits for an upgrade",
			err:         blocked(ErrRefKindUnavailable, ModeName),
			wantReason:  netboxv1alpha1.ReasonRefKindUnavailable,
			wantRequeue: humanRetry,
		},
		{
			name:       "a malformed reference is invalid",
			err:        blocked(ErrRefMalformed, ""),
			wantReason: netboxv1alpha1.ReasonInvalid,
		},
		{
			// Not a resolution failure at all. Reporting it as one would send whoever reads the
			// condition to a field that is perfectly fine.
			name:       "anything else is invalid",
			err:        errors.New("something else entirely"),
			wantReason: netboxv1alpha1.ReasonInvalid,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.err)

			if got.Reason != tc.wantReason {
				t.Errorf("Classify(%v).Reason = %q, want %q", tc.err, got.Reason, tc.wantReason)
			}

			if got.Requeue != tc.wantRequeue {
				t.Errorf("Classify(%v).Requeue = %s, want %s", tc.err, got.Requeue, tc.wantRequeue)
			}
		})
	}
}

// blocked builds a resolution failure of one cause and mode, which is all Classify reads.
func blocked(cause error, mode Mode) *Error {
	return &Error{Cause: cause, Field: "regionRef", Mode: mode, TargetGVK: regionGVK}
}

// TestErrorRenders checks the sentence a user actually sees. It goes into the condition
// verbatim, so it has to name the field they wrote and the object it pointed at -- in the
// vocabulary of the mode they used, since a slug has no namespace and a name has no filter.
func TestErrorRenders(t *testing.T) {
	tests := []struct {
		name string
		err  *Error
		want string
	}{
		{
			name: "name mode names the CR",
			err: &Error{
				Cause: ErrRefNotReady, Field: "regionRef", Mode: ModeName, TargetGVK: regionGVK,
				Ref: objectRef("emea"), Target: namespacedName("catalogue", "emea"),
				Detail: `target Ready=False, Reason=Invalid: "slug must be unique"`,
			},
			want: `regionRef -> netboxregion/catalogue/emea: not ready ` +
				`(target Ready=False, Reason=Invalid: "slug must be unique")`,
		},
		{
			name: "slug mode names the slug",
			err: &Error{
				Cause: ErrRefAmbiguous, Field: "regionRef", Mode: ModeSlug, TargetGVK: regionGVK,
				Ref: slugRef("emea"), Detail: "2 netbox dcim/regions match map[slug:emea]: id 12 (EMEA), id 19 (Emea)",
			},
			want: "regionRef -> netboxregion slug=emea: ambiguous " +
				"(2 netbox dcim/regions match map[slug:emea]: id 12 (EMEA), id 19 (Emea))",
		},
		{
			name: "lookup mode names the filter",
			err: &Error{
				Cause: ErrRefNotFound, Field: "vlanRef", Mode: ModeLookup, TargetGVK: regionGVK,
				Ref: lookupRef(map[string]string{"vid": "20", "site": "home"}),
			},
			want: "vlanRef -> netboxregion lookup=map[site:home vid:20]: not found",
		},
		{
			name: "id mode names the id",
			err: &Error{
				Cause: ErrRefNotFound, Field: "regionRef", Mode: ModeID, TargetGVK: regionGVK,
				Ref: idRef(12),
			},
			want: "regionRef -> netboxregion id=12: not found",
		},
		{
			// A cycle renders the ring rather than the fact of one: the path, in order, from
			// the object reporting it back to itself. "A cycle was detected" would leave a
			// user to find the ring by hand, which is the whole of the work.
			name: "a cycle names the ring it found",
			err: &Error{
				Cause: ErrRefCycle, Field: "parentRef", Mode: ModeName, TargetGVK: regionGVK,
				Ref: objectRef("b"), Target: namespacedName("team-a", "b"),
				Path: RefPath{
					{GVK: regionGVK, Key: namespacedName("team-a", "a")},
					{GVK: regionGVK, Key: namespacedName("team-a", "b")},
					{GVK: regionGVK, Key: namespacedName("team-a", "a")},
				},
				Detail: "netboxregion/team-a/a -> netboxregion/team-a/b -> netboxregion/team-a/a",
			},
			want: "parentRef -> netboxregion/team-a/b: reference cycle " +
				"(netboxregion/team-a/a -> netboxregion/team-a/b -> netboxregion/team-a/a)",
		},
		{
			// A descriptor that declares a reference without saying what it points at. The
			// message has to survive it, because this is the state a wrong descriptor is in.
			name: "no target kind still renders",
			err: &Error{
				Cause: ErrRefKindUnavailable, Field: "regionRef",
				Detail: "the descriptor declares no target kind for regionRef",
			},
			want: "regionRef -> <no target kind>: target kind unavailable " +
				"(the descriptor declares no target kind for regionRef)",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.err.Error(); got != tc.want {
				t.Errorf("Error() =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

// TestResolutionReports checks what a caller reads off a Resolution: one reason, every
// message, and the soonest requeue.
func TestResolutionReports(t *testing.T) {
	resolution := Resolution{Blocked: []Blocker{
		{
			Field: "regionRef", Reason: netboxv1alpha1.ReasonRefAmbiguous, Requeue: humanRetry,
			Err: &Error{Cause: ErrRefAmbiguous, Field: "regionRef", Mode: ModeSlug, TargetGVK: regionGVK,
				Ref: slugRef("emea")},
		},
		{
			Field: "tenantRef", Reason: netboxv1alpha1.ReasonRefNotFound, Requeue: netboxRetry,
			Err: &Error{Cause: ErrRefNotFound, Field: "tenantRef", Mode: ModeID, TargetGVK: tenantGVK,
				Ref: idRef(9)},
		},
	}}

	// The first blocker's, because a reason is a single value tooling keys on.
	if got := resolution.Reason(); got != netboxv1alpha1.ReasonRefAmbiguous {
		t.Errorf("Reason() = %q, want %q", got, netboxv1alpha1.ReasonRefAmbiguous)
	}

	want := "regionRef -> netboxregion slug=emea: ambiguous; tenantRef -> netboxtenant id=9: not found"
	if got := resolution.Message(); got != want {
		t.Errorf("Message() =\n%q\nwant\n%q", got, want)
	}

	// The soonest of the two: holding the missing tenant for ten minutes because a slug is
	// ambiguous would delay the one that can still resolve on its own.
	if got := resolution.Requeue(); got != netboxRetry {
		t.Errorf("Requeue() = %s, want %s", got, netboxRetry)
	}

	// Nothing blocked is not a state that needs a reason, and it must not borrow one.
	if got := (Resolution{}).Reason(); got != netboxv1alpha1.ReasonAllResolved {
		t.Errorf("Reason() on an empty resolution = %q, want %q", got, netboxv1alpha1.ReasonAllResolved)
	}

	if got := (Resolution{}).Requeue(); got != 0 {
		t.Errorf("Requeue() on an empty resolution = %s, want 0", got)
	}
}
