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

	// truncatedRetry is the wait after a lookup paginated past the client's page cap.
	//
	// The same tier as a version mismatch (internal/controller, failureBackoff), for the
	// same reason: a truncated list is not retryable at all -- the same request truncates in
	// the same place -- so the only thing that clears it is a human narrowing the filter or
	// raising MaxPages. Deliberately not the endpoint's resync, which a cluster is free to
	// set to seconds: that would poll a query that cannot succeed.
	truncatedRetry = 10 * time.Minute
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

	// errRecreateRetained is a change to an identity-bearing field on an
	// `UpdateStrategy: Recreate` kind whose deletion policy is Retain.
	//
	// Retain means "never destroy this NetBox object" and a recreate destroys it, so the two
	// instructions contradict each other and the operator refuses rather than picking one.
	// Refusing in this direction specifically: a silent recreate would delete a
	// dcim.Cable a user asked to be kept, and every dcim.CablePath through it, to satisfy an
	// edit -- and there is no undo for that, while there is an obvious undo for a refusal.
	//
	// Classified as Invalid rather than as a wait: no event and no timer clears it, only an
	// edit to the spec, which is either flipping the policy or reverting the field.
	errRecreateRetained = errors.New(
		"spec.deletionPolicy is Retain and this change can only be applied by deleting and re-creating the object")

	// errUnmappedField is a spec field the descriptor's field map does not declare. A
	// descriptor bug, and the reason the field map is explicit.
	errUnmappedField = errors.New("spec field is not in the descriptor's field map")

	// errNoCustomFields is spec.customFields set on a kind whose NetBox model carries no
	// `custom_fields` column -- extras.Tag is one. Its own sentinel rather than
	// errUnmappedField: the field is in no descriptor's field map by design, so "not in the
	// field map" would send the reader looking for a mapping bug that is not there.
	errNoCustomFields = errors.New("this kind's NetBox model has no custom_fields column")

	// errUnfilterable is a natural-key filter whose spec field holds no value a query can
	// carry.
	errUnfilterable = errors.New("natural-key filter has no value")

	// errNoObjectID is a NetBox response with no id in it, where the id is the whole point:
	// a lookup that matched, or a write that is supposed to have created something. No
	// amount of retrying will add one.
	errNoObjectID = errors.New("netbox returned an object with no id")

	// errReservedByProvenance is a CR naming a NetBox object the provenance bootstrap owns
	// on this endpoint: the `k8s-managed` tag, or one of the four custom fields
	// spec.managedBy configures. Nothing is written for it, ever
	// (registry.Descriptor.ReservedKeySpec, provenance.Config.Reserved).
	errReservedByProvenance = errors.New("this netbox object is written by the operator's own provenance bootstrap")

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

	// remedy is what to do about this state, appended to the error's own words. Empty for
	// every failure whose error already ends in an instruction.
	//
	// It lives here rather than in the client because it is not something the client knows:
	// the client reports what NetBox did, and which fix applies is a property of how the
	// engine used the call. A truncated natural-key lookup is a filter that did not apply;
	// the same truncation under a future prune would be a different sentence.
	remedy string
}

// message is what the condition and the Event carry: the error's own words, and the remedy
// when the table has one to add.
func (o outcome) message(err error) string {
	if o.remedy == "" {
		return err.Error()
	}

	return err.Error() + "; " + o.remedy
}

// classify maps a failure onto what to record and when to come back.
//
// It classifies by error type and never by message: the client already decided what kind
// of failure it saw (NBO-002), and NetBox's wording changes between releases. resync is
// the endpoint's own interval, used for everything that will not improve on its own --
// coming back sooner would just repeat the same refusal.
//
// Every error type internal/netbox exports has an arm below -- ValidationError, AuthError,
// NotFoundError, ProtectedError, RateLimitError, TransientError, AmbiguousError and
// TruncatedError. The unclassified default is for a failure that is none of them, and it is
// generic on purpose, which is also what made it a good place for a missing arm to hide
// (NBO-090). A new client error type belongs here in the same change that adds it.
func classify(err error, resync time.Duration) outcome {
	if contended, ok := classifyContended(err); ok {
		return contended
	}

	if waiting, ok := classifyWait(err, resync); ok {
		return waiting
	}

	if invalid, ok := classifyInvalid(err, resync); ok {
		return invalid
	}

	return classifyAPI(err, resync)
}

// classifyContended covers the one failure that is neither the object's fault nor NetBox's:
// another writer took the space between this pass's read and its write.
//
// Its own arm rather than a case in classifyInvalid, because it is the only entry in this table
// that resolves *without* anything a human controls changing -- the space is there, and the next
// pass may well get it. It is nonetheless loud and slow: a pool contended past
// maxPlacementAttempts is a pool where somebody should look at how many claims are competing,
// and a fast retry would add this operator to the contention it is reporting.
//
// Reached only from the allocation engine, and only from the unlocked placement path
// (netbox.PlaceRange). The advisory-locked endpoints cannot produce it.
func classifyContended(err error) (outcome, bool) {
	var contended *netbox.ContendedError
	if !errors.As(err, &contended) {
		return outcome{}, false
	}

	return outcome{
		reason: netboxv1alpha1.ReasonAllocationContended, requeue: truncatedRetry,
		event: netboxv1alpha1.EventAllocationContended, severe: true, result: metrics.ResultError,
		remedy: "the space is there and another writer took it first, so this is not an exhausted" +
			" pool; it retries on its own, and a pool that keeps reporting this has more claims" +
			" competing for it than it has room for",
	}, true
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
	var truncated *netbox.TruncatedError
	var refused *refusedAdoption
	var unclaimable *unclaimableDuplicate

	conflict := outcome{
		reason: netboxv1alpha1.ReasonConflict, requeue: resync,
		event: netboxv1alpha1.EventConflict, severe: true, result: metrics.ResultError,
	}
	invalid := outcome{
		reason: netboxv1alpha1.ReasonInvalid, requeue: resync,
		event: netboxv1alpha1.EventInvalid, severe: true, result: metrics.ResultError,
	}

	switch {
	// A duplicate-bearing kind's own refusals (NBO-025) are the same category as an
	// ambiguity: netbox holds objects this CR cannot safely claim, the matches are named, and
	// only a change in netbox or in the spec clears it.
	case errors.As(err, &ambiguous), errors.As(err, &refused), errors.As(err, &unclaimable):
		return conflict, true
	// A lookup that paginated past the page cap. Its own reason rather than Invalid or
	// APIError: the engine wrote nothing because it could not tell whether the object exists
	// (NBO-077), and neither "NetBox rejected the payload" nor "NetBox is failing" sends the
	// reader anywhere near the filter or the cap.
	case errors.As(err, &truncated):
		return outcome{
			reason: netboxv1alpha1.ReasonTruncated, requeue: truncatedRetry,
			// The same Event as a 400, because it is the same category of state -- one a
			// human has to clear -- and the condition reason carries which.
			event: netboxv1alpha1.EventInvalid, severe: true, result: metrics.ResultError,
			remedy: fmt.Sprintf(
				"nothing was written; either the filter did not apply and the lookup has to be narrowed,"+
					" or %s holds more objects than the %d-page cap allows and MaxPages has to be raised",
				truncated.Endpoint, truncated.MaxPages),
		}, true
	// A 409, or a body naming a protected relation: something else in NetBox has to change
	// first, and the same request will fail identically until it does.
	case errors.As(err, &protected):
		return conflict, true
	// Two declarations for one column -- an explicit spec.primaryIP4Ref beside an inline
	// address marked primary, or two of the latter (NBO-033). A Conflict rather than an
	// Invalid, because the payload NetBox would have been sent is not malformed: the spec
	// holds two answers to one question and the operator refuses to pick, which is the same
	// shape as two NetBox rows matching one natural key.
	case errors.Is(err, netboxv1alpha1.ErrDerivedRefConflict):
		return conflict, true
	case errors.As(err, &validation):
		return invalid, true
	// Reserved by the operator's own bootstrap. Its own reason rather than Invalid or
	// Conflict: the spec is well-formed and there is nothing to adopt -- the name is taken by
	// this operator, for this endpoint, and only a change to spec.managedBy or to the CR's
	// own name frees it. The endpoint's resync is the right interval because that is when
	// spec.managedBy would be re-read.
	case errors.Is(err, errReservedByProvenance):
		return outcome{
			reason: netboxv1alpha1.ReasonReservedByOperator, requeue: resync,
			event: netboxv1alpha1.EventInvalid, severe: true, result: metrics.ResultError,
		}, true
	case errors.Is(err, errUnmappedField), errors.Is(err, errNoCustomFields),
		errors.Is(err, errUnfilterable), errors.Is(err, errNoObjectID),
		errors.Is(err, errDuplicateNeedsProvenance),
		errors.Is(err, errDuplicateOnGeneratedChild):
		return invalid, true
	// Two instructions in the spec that contradict each other. Invalid rather than Conflict:
	// nothing about NetBox's contents is wrong, and the fix is in the manifest.
	case errors.Is(err, errRecreateRetained):
		return outcome{
			reason: netboxv1alpha1.ReasonInvalid, requeue: resync,
			event: netboxv1alpha1.EventInvalid, severe: true, result: metrics.ResultError,
			remedy: "nothing was written; either set spec.deletionPolicy: Delete to allow the" +
				" object to be replaced, or revert the field named above",
		}, true
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
