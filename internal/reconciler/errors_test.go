package reconciler

import (
	"fmt"
	"strings"
	"testing"
	"time"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// TestClassify is the error table from docs/concepts/errors-and-retries.md, asserted rather
// than described. Every entry classifies by type: NetBox's wording changes between
// releases, so a message match would be a silent regression on upgrade.
func TestClassify(t *testing.T) {
	const resync = 5 * time.Minute

	tests := []struct {
		name        string
		err         error
		wantReason  string
		wantRequeue time.Duration
		wantEvent   string
		wantSevere  bool
	}{
		{
			name:        "an endpoint that is not ready",
			err:         fmt.Errorf("%w: homelab", errEndpointNotReady),
			wantReason:  netboxv1alpha1.ReasonWaitingForEndpoint,
			wantRequeue: endpointRetry,
		},
		{
			name:        "no usable natural key",
			err:         errNoCandidate,
			wantReason:  netboxv1alpha1.ReasonWaitingForKey,
			wantRequeue: resync,
		},
		{
			name:        "adopt-only with nothing to adopt",
			err:         errAdoptOnly,
			wantReason:  netboxv1alpha1.ReasonAdoptOnly,
			wantRequeue: resync,
		},
		{
			name: "several objects match one natural key",
			err: &netbox.AmbiguousError{
				Endpoint: "extras/tags", Params: netbox.Params{"slug": "managed"},
				Matched: 2, IDs: []int{4, 9},
			},
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantRequeue: resync,
			wantEvent:   netboxv1alpha1.EventConflict,
			wantSevere:  true,
		},
		{
			name:        "an object this CR may not take over",
			err:         &refusedAdoption{id: 9},
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantRequeue: resync,
			wantEvent:   netboxv1alpha1.EventConflict,
			wantSevere:  true,
		},
		{
			name:        "a 409 or a protected relation",
			err:         &netbox.ProtectedError{Status: 409},
			wantReason:  netboxv1alpha1.ReasonConflict,
			wantRequeue: resync,
			wantEvent:   netboxv1alpha1.EventConflict,
			wantSevere:  true,
		},
		{
			name:        "a 400",
			err:         &netbox.ValidationError{Status: 400},
			wantReason:  netboxv1alpha1.ReasonInvalid,
			wantRequeue: resync,
			wantEvent:   netboxv1alpha1.EventInvalid,
			wantSevere:  true,
		},
		{
			name:        "a descriptor that cannot render the spec",
			err:         fmt.Errorf("%w: comments", errUnmappedField),
			wantReason:  netboxv1alpha1.ReasonInvalid,
			wantRequeue: resync,
			wantEvent:   netboxv1alpha1.EventInvalid,
			wantSevere:  true,
		},
		{
			name:        "a 404 on a write",
			err:         &netbox.NotFoundError{Endpoint: "extras/tags", ID: 7},
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: vanishedRetry,
		},
		{
			name:        "a 401 or 403 waits for the endpoint to be fixed",
			err:         &netbox.AuthError{Status: 403},
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: authRetry,
			wantSevere:  true,
		},
		{
			name:        "a 429 honours retry-after",
			err:         &netbox.RateLimitError{RetryAfter: 42 * time.Second},
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: 42 * time.Second,
		},
		{
			name:        "a 429 without a retry-after header",
			err:         &netbox.RateLimitError{},
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: rateLimitRetry,
		},
		{
			name:        "a 5xx or a transport failure",
			err:         &netbox.TransientError{Status: 503},
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: transientRetry,
		},
		{
			// Its own reason rather than the unclassified default: "the lookup paginated past
			// the cap" looks nothing like a NetBox outage from the outside, and the fix is a
			// filter or the cap rather than waiting (NBO-090).
			name:        "a lookup that paginated past the page cap",
			err:         &netbox.TruncatedError{Endpoint: "dcim/sites", MaxPages: 1000, Collected: 50000},
			wantReason:  netboxv1alpha1.ReasonTruncated,
			wantRequeue: truncatedRetry,
			wantEvent:   netboxv1alpha1.EventInvalid,
			wantSevere:  true,
		},
		{
			name:        "anything the table does not cover is worth being loud about",
			err:         errStatusWrite,
			wantReason:  netboxv1alpha1.ReasonAPIError,
			wantRequeue: resync,
			wantSevere:  true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classify(tc.err, resync)

			if got.reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", got.reason, tc.wantReason)
			}

			if got.requeue != tc.wantRequeue {
				t.Errorf("requeue = %s, want %s", got.requeue, tc.wantRequeue)
			}

			if got.event != tc.wantEvent {
				t.Errorf("event = %q, want %q", got.event, tc.wantEvent)
			}

			if got.severe != tc.wantSevere {
				t.Errorf("severe = %v, want %v", got.severe, tc.wantSevere)
			}
		})
	}
}

// TestClassifyTruncatedMessage is the reporting half of NBO-077. That ticket asserted the
// engine writes nothing on a truncated lookup; nothing asserted what it tells the user, and
// a condition saying only "results would be incomplete" leaves the reader without either of
// the two fixes -- which are different fixes, and neither of them is "wait".
func TestClassifyTruncatedMessage(t *testing.T) {
	err := &netbox.TruncatedError{Endpoint: "dcim/sites", MaxPages: 1000, Collected: 50000}

	message := classify(err, 5*time.Minute).message(err)

	// The cap and how far it got, so the reader can tell "the filter did not apply" from
	// "this endpoint really is that big" without reproducing the query by hand.
	for _, want := range []string{"dcim/sites", "1000", "50000"} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, want it to name %q", message, want)
		}
	}

	// Both fixes, because the message is the only place a user meets this state.
	for _, want := range []string{"narrowed", "MaxPages", "nothing was written"} {
		if !strings.Contains(message, want) {
			t.Errorf("message = %q, want it to say %q", message, want)
		}
	}
}

func TestRenderChanges(t *testing.T) {
	tests := []struct {
		name    string
		changes []netbox.Change
		want    string
	}{
		{
			name:    "a scalar",
			changes: []netbox.Change{{Field: "color", Old: "ff0000", New: "9e9e9e"}},
			want:    "color: ff0000 → 9e9e9e",
		},
		{
			// A foreign key reads back as a whole nested object; printing it raw buries the
			// one interesting field in a JSON dump, inside a truncated Event message.
			name: "a foreign key shows the id it points at",
			changes: []netbox.Change{
				{Field: "tenant", Old: map[string]any{"id": float64(3), "name": "acme"}, New: float64(4)},
			},
			want: "tenant: 3 → 4",
		},
		{
			name: "a choice field shows its value",
			changes: []netbox.Change{
				{Field: "status", Old: map[string]any{"value": "planned", "label": "Planned"}, New: "active"},
			},
			want: "status: planned → active",
		},
		{
			name:    "an unset field says so",
			changes: []netbox.Change{{Field: "description", Old: nil, New: "managed by k8s"}},
			want:    "description: unset → managed by k8s",
		},
		{
			name: "several changes read in one line",
			changes: []netbox.Change{
				{Field: "color", Old: "ff0000", New: "9e9e9e"},
				{Field: "weight", Old: float64(1000), New: float64(500)},
			},
			want: "color: ff0000 → 9e9e9e, weight: 1000 → 500",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := renderChanges(tc.changes); got != tc.want {
				t.Fatalf("renderChanges() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestJitter checks the spread stays inside a tenth either way. Objects applied together
// otherwise resync in lockstep for the rest of their lives.
func TestJitter(t *testing.T) {
	const base = 10 * time.Minute

	for range 100 {
		got := Jitter(base)

		if got < base-base/10 || got > base+base/10 {
			t.Fatalf("Jitter(%s) = %s, want within 10%%", base, got)
		}
	}

	if got := Jitter(0); got != 0 {
		t.Errorf("Jitter(0) = %s, want 0: no requeue means no requeue", got)
	}
}
