package reconciler

import (
	"errors"
	"fmt"
	"time"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// Requeue delays for the states that are not a resync.
//
// Each is spaced by how likely coming back sooner is to help. Nothing here is a client
// retry -- the client already retries what a retry can fix (NBO-002) -- so these are the
// intervals at which the whole decision is made again.
const (
	// endpointRetry is the wait for a NetBoxEndpoint to become Ready. Short, because the
	// endpoint controller re-probes on its own schedule and there is no watch from an
	// object to its endpoint yet.
	endpointRetry = 30 * time.Second

	// transientRetry is the wait after a 5xx or a transport failure.
	transientRetry = 30 * time.Second

	// rateLimitRetry is used when a 429 arrived without a Retry-After header.
	rateLimitRetry = 5 * time.Second

	// authRetry is the wait after a 401 or 403. Long, because the fix is a token or a
	// permission and the endpoint controller is the component that reports it
	// (docs/concepts/errors-and-retries.md).
	authRetry = 2 * time.Minute

	// vanishedRetry is the wait after a write found the object gone. Immediate in
	// practice: the next pass re-creates or re-adopts it.
	vanishedRetry = time.Second
)

// Engine-level reasons a reconcile stops. They are sentinels rather than messages so the
// outcome table classifies by type, exactly as it does for the client's errors.
var (
	// errEndpointNotReady is a spec.endpointRef with no usable client behind it.
	errEndpointNotReady = errors.New("the netbox endpoint has no ready client")

	// errNoCandidate is no usable natural-key candidate: identity cannot be established,
	// so the engine waits rather than writing anything.
	errNoCandidate = errors.New("no natural-key candidate is usable yet")

	// errAdoptOnly is onConflict: AdoptOnly with nothing to adopt.
	errAdoptOnly = errors.New("onConflict is AdoptOnly and nothing matched")

	// errUnmappedField is a spec field the descriptor's field map does not declare. A
	// descriptor bug, and the reason the field map is explicit.
	errUnmappedField = errors.New("spec field is not in the descriptor's field map")

	// errUnfilterable is a natural-key filter whose spec field holds no value a query can
	// carry.
	errUnfilterable = errors.New("natural-key filter has no value")

	// errNoObjectID is a NetBox response with no id in it, where the id is the whole point:
	// a lookup that matched, or a write that is supposed to have created something. No
	// amount of retrying will add one.
	errNoObjectID = errors.New("netbox returned an object with no id")

	// errNotConfigured is a wiring mistake: a collaborator the engine needs was not
	// supplied. It is an error rather than a panic because a nil dereference halfway
	// through a reconcile tells whoever is paged nothing about what is missing.
	errNotConfigured = errors.New("the engine is missing a collaborator")
)

// refusedAdoption is a live NetBox object this CR is not permitted to take over.
type refusedAdoption struct {
	id int
}

func (e *refusedAdoption) Error() string {
	return fmt.Sprintf("netbox object %d already matches this object's natural key and was not created by it; "+
		"set spec.onConflict to Adopt or AdoptOnly to take it over", e.id)
}

// outcome is what a blocked reconcile records, and when it tries again.
type outcome struct {
	// reason is the condition reason for Ready=False.
	reason string

	// requeue is how long to wait before deciding again.
	requeue time.Duration

	// event is the Event reason to emit, if any. Only for states a human has to resolve:
	// an Event for a transient failure is noise at scale.
	//
	// This is the by-category half of that argument; stop()'s transition guard is the
	// by-transition half. Both are kept, because neither subsumes the other and dropping
	// either brings back a flood with a different shape. This one answers "is this state
	// ever worth telling a human about" -- a wait is not, and every object in the cluster
	// passes through one, so without it a single `kubectl apply` of a large manifest emits
	// an Event per object that nobody can act on and that resolves itself. The guard
	// answers "has what we would tell them changed since we last told them", which is the
	// only question that can be asked once a state does deserve an Event. Folding them
	// together would mean either eventing waits once each, or never eventing a permanent
	// failure at all.
	event string

	// severe marks a state that needs a human, and so is logged at error rather than
	// debug.
	severe bool

	// result is the metrics.Result* bucket this failure counts as. Set here rather than
	// derived from severe, because "waiting for the endpoint" and "NetBox is down" are
	// both non-severe and only one of them is an error on a dashboard.
	result string
}

// classify maps a failure onto what to record and when to come back.
//
// It classifies by error type and never by message: the client already decided what kind
// of failure it saw (NBO-002), and NetBox's wording changes between releases. resync is
// the endpoint's own interval, used for everything that will not improve on its own --
// coming back sooner would just repeat the same refusal.
func classify(err error, resync time.Duration) outcome {
	if waiting, ok := classifyWait(err, resync); ok {
		return waiting
	}

	if invalid, ok := classifyInvalid(err, resync); ok {
		return invalid
	}

	return classifyAPI(err, resync)
}

// classifyWait covers the states that are not failures at all: something the engine is
// waiting for. None of them is logged at error, and none emits an Event, because they are
// normal and would otherwise drown the log at cluster scale.
func classifyWait(err error, resync time.Duration) (outcome, bool) {
	waiting := func(reason string, requeue time.Duration) outcome {
		return outcome{reason: reason, requeue: requeue, result: metrics.ResultWaiting}
	}

	switch {
	case errors.Is(err, errEndpointNotReady):
		return waiting(netboxv1alpha1.ReasonWaitingForEndpoint, endpointRetry), true
	case errors.Is(err, errNoCandidate):
		return waiting(netboxv1alpha1.ReasonWaitingForKey, resync), true
	case errors.Is(err, errAdoptOnly):
		return waiting(netboxv1alpha1.ReasonAdoptOnly, resync), true
	default:
		return outcome{}, false
	}
}

// classifyInvalid covers what cannot succeed until something a human controls changes: the
// spec, the descriptor, or NetBox's own contents.
func classifyInvalid(err error, resync time.Duration) (outcome, bool) {
	var validation *netbox.ValidationError
	var protected *netbox.ProtectedError
	var ambiguous *netbox.AmbiguousError
	var refused *refusedAdoption

	conflict := outcome{
		reason: netboxv1alpha1.ReasonConflict, requeue: resync,
		event: netboxv1alpha1.EventConflict, severe: true, result: metrics.ResultError,
	}
	invalid := outcome{
		reason: netboxv1alpha1.ReasonInvalid, requeue: resync,
		event: netboxv1alpha1.EventInvalid, severe: true, result: metrics.ResultError,
	}

	switch {
	case errors.As(err, &ambiguous), errors.As(err, &refused):
		return conflict, true
	// A 409, or a body naming a protected relation: something else in NetBox has to change
	// first, and the same request will fail identically until it does.
	case errors.As(err, &protected):
		return conflict, true
	case errors.As(err, &validation):
		return invalid, true
	case errors.Is(err, errUnmappedField), errors.Is(err, errUnfilterable), errors.Is(err, errNoObjectID):
		return invalid, true
	default:
		return outcome{}, false
	}
}

// classifyAPI covers everything about NetBox's availability. None of it is the object's
// fault, so none of it fails the object permanently.
func classifyAPI(err error, resync time.Duration) outcome {
	var auth *netbox.AuthError
	var limited *netbox.RateLimitError
	var transient *netbox.TransientError
	var notFound *netbox.NotFoundError

	api := func(requeue time.Duration, severe bool) outcome {
		return outcome{
			reason: netboxv1alpha1.ReasonAPIError, requeue: requeue,
			severe: severe, result: metrics.ResultError,
		}
	}

	switch {
	// A read or write that 404s after the object was located: it went away underneath us.
	case errors.As(err, &notFound):
		return api(vanishedRetry, false)
	// One bad token otherwise scatters identical failures across every CR in the cluster,
	// so the endpoint is where this gets fixed and this object just waits for it.
	case errors.As(err, &auth):
		return api(authRetry, true)
	case errors.As(err, &limited):
		return api(retryAfter(limited), false)
	case errors.As(err, &transient):
		return api(transientRetry, false)
	default:
		// An unclassified failure is the one case worth being loud about: it means the
		// client saw something the error table does not cover.
		return api(resync, true)
	}
}

func retryAfter(err *netbox.RateLimitError) time.Duration {
	if err.RetryAfter > 0 {
		return err.RetryAfter
	}

	return rateLimitRetry
}
