package resolver

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	netboxv1alpha1 "github.com/ricardomolendijk/netbox-operator/api/v1alpha1"
)

// Why a reference did not resolve. Sentinels for errors.Is, all carried by the one concrete
// *Error type below for errors.As -- so a caller branches on the classification without
// matching a string, and still recovers the field, the mode and the target it came from.
//
// The set is closed on purpose. An unclassified resolution failure would get whatever
// requeue the caller defaults to, which is how a permanent condition ends up retried
// forever, and each of these maps to exactly one condition reason and one requeue policy
// (see Classify).
var (
	// ErrRefNotFound is nothing to point at: no CR of that name, no NetBox object matching
	// that slug or lookup, or a raw id NetBox does not hold.
	ErrRefNotFound = errors.New("not found")

	// ErrRefNotReady is a target that exists and has no NetBox id yet.
	//
	// A state, not a failure -- the single most important distinction in this package. The
	// populator's ordering problem is solved by treating "not yet" as normal and letting an
	// event wake the referrer up, so nothing here backs off and nothing here is logged as an
	// error.
	ErrRefNotReady = errors.New("not ready")

	// ErrRefAmbiguous is a slug or lookup matching more than one NetBox object. Real rather
	// than theoretical: `slug` is only globally unique on some models -- ipam.VLANGroup is
	// unique on (scope_type, scope_id, slug) and tenancy.Tenant on (group, slug)
	// (docs/netbox-schema.md) -- so a bare slug can legitimately match several rows.
	ErrRefAmbiguous = errors.New("ambiguous")

	// ErrRefDenied is a cross-namespace reference with no NetBoxRefGrant permitting it.
	//
	// Declared here and produced by NBO-014, which lands the grant: the classification, the
	// condition reason and the requeue policy belong with the rest of the table rather than
	// arriving with the feature, so that adding the check is a call site and not a new
	// vocabulary.
	ErrRefDenied = errors.New("denied")

	// ErrRefCycle is a reference that cannot resolve because it depends on itself.
	//
	// Only the one-hop case is detected here -- a reference to the referring object -- which
	// is what a single resolution can see. Detection over a graph is NBO-016, and it composes
	// over the Resolution this package returns rather than replacing anything in it.
	ErrRefCycle = errors.New("reference cycle")

	// ErrRefKindUnavailable is a target Kind this operator cannot resolve against: no
	// Descriptor registered for it, or its CRD is not installed.
	//
	// Distinct from ErrRefNotFound because the manifest is correct and the fix is an
	// operator upgrade rather than a spec change. It is the common case through M2-M9, where
	// a kind may declare `tenantRef` before NetBoxTenant exists.
	ErrRefKindUnavailable = errors.New("target kind unavailable")

	// ErrRefMalformed is a reference that is present and sets none of the four modes.
	// ObjectRef's CEL rules make it unreachable through the API server; it exists so that an
	// object stored before a rule, or a Request built by hand, is refused rather than
	// silently resolving to nothing and having the field dropped from the payload.
	//
	// Note what it is not: an *absent* reference. A field the spec does not set is not
	// resolved, not blocked and not reported -- spec omission means "do not manage". The two
	// are deliberately distinct states rather than one, because server-side apply makes them
	// distinguishable at the API level too (#121), and "clear this foreign key" will be an
	// explicit instruction rather than an empty reference read as one.
	ErrRefMalformed = errors.New("no mode set")
)

// Requeue delays for the states that do not clear themselves.
//
// Both are longer than any client retry, because neither is a transport problem: they are
// the intervals at which the whole resolution is attempted again.
const (
	// netboxRetry is the wait for a NetBox object that does not exist yet. There is nothing
	// in Kubernetes to watch for it -- no CR will ever be created -- so this is the only
	// thing that will notice it appearing.
	netboxRetry = time.Minute

	// humanRetry is the wait for a state only a person can clear: an ambiguous slug to
	// disambiguate, a CRD to install. Coming back sooner would repeat the same refusal.
	humanRetry = 10 * time.Minute
)

// Error is one reference that did not resolve.
//
// It renders as `regionRef -> netboxregion/catalogue/emea: not ready (target Ready=False,
// Reason=Invalid: "vid must be unique")`, and that string goes into the condition verbatim:
// the field the user wrote, the object it pointed at, and -- when the target is the reason --
// the target's own words rather than a paraphrase of them.
type Error struct {
	// Cause is one of the sentinels above, so errors.Is classifies and errors.As inspects.
	Cause error

	// Field is the CR spec field name.
	Field string

	// Ref is the reference as written.
	Ref netboxv1alpha1.ObjectRef

	// Mode is which shape it was written in. Load-bearing for the requeue policy: a missing
	// CR is re-enqueued by its own creation, a missing NetBox object by nothing at all.
	Mode Mode

	// TargetGVK is the Kind it points at.
	TargetGVK schema.GroupVersionKind

	// Target is the CR it named, for `name` mode.
	Target types.NamespacedName

	// Detail is what to say beyond the classification.
	Detail string
}

// Error renders the reference, its target and why it did not resolve.
func (e *Error) Error() string {
	rendered := fmt.Sprintf("%s -> %s: %v", e.Field, e.target(), e.Cause)
	if e.Detail == "" {
		return rendered
	}

	return rendered + " (" + e.Detail + ")"
}

// Unwrap returns the sentinel, so errors.Is classifies this error by cause.
func (e *Error) Unwrap() error { return e.Cause }

// target renders what the reference pointed at, in the vocabulary of the mode it used: a
// namespaced name for `name`, and the NetBox query for the three modes that have no CR.
func (e *Error) target() string {
	kind := strings.ToLower(e.TargetGVK.Kind)
	if kind == "" {
		kind = "<no target kind>"
	}

	switch e.Mode {
	case ModeName:
		return kind + "/" + e.Target.Namespace + "/" + e.Ref.Name
	case ModeSlug:
		return kind + " slug=" + e.Ref.Slug
	case ModeLookup:
		return fmt.Sprintf("%s lookup=%v", kind, e.Ref.Lookup)
	case ModeID:
		return fmt.Sprintf("%s id=%d", kind, orZero(e.Ref.ID))
	}

	return kind
}

// Outcome is what one unresolved reference means for the object: the reason to report on
// RefsResolved, and when to decide again.
type Outcome struct {
	// Reason is the RefsResolved condition reason. Ready always reports WaitingForRef
	// alongside it -- one reason for "a reference is missing", and this one for which.
	Reason string

	// Requeue is when to come back, and zero for the states where coming back on a timer
	// adds nothing: an event -- a CR appearing, a grant being written, in NBO-013 a watch
	// firing -- is what clears them. A caller with a resync of its own uses that instead.
	Requeue time.Duration
}

// Classify maps a resolution failure onto the condition reason and the requeue policy.
//
// One table, so the mapping from cause to what a user sees exists exactly once and the docs
// have something to be checked against. It classifies by type and never by message, like
// the engine's own outcome table.
func Classify(err error) Outcome {
	var refErr *Error
	if !errors.As(err, &refErr) {
		// Not a resolution failure at all: the caller is holding an error this table does
		// not cover, and reporting it as a reference problem would send the reader to the
		// wrong field.
		return Outcome{Reason: netboxv1alpha1.ReasonInvalid}
	}

	switch {
	case errors.Is(err, ErrRefNotReady):
		return Outcome{Reason: netboxv1alpha1.ReasonRefNotReady}
	case errors.Is(err, ErrRefNotFound):
		return Outcome{Reason: netboxv1alpha1.ReasonRefNotFound, Requeue: notFoundRetry(refErr.Mode)}
	case errors.Is(err, ErrRefAmbiguous):
		return Outcome{Reason: netboxv1alpha1.ReasonRefAmbiguous, Requeue: humanRetry}
	case errors.Is(err, ErrRefDenied):
		return Outcome{Reason: netboxv1alpha1.ReasonRefDenied}
	case errors.Is(err, ErrRefCycle):
		return Outcome{Reason: netboxv1alpha1.ReasonRefCycle}
	case errors.Is(err, ErrRefKindUnavailable):
		return Outcome{Reason: netboxv1alpha1.ReasonRefKindUnavailable, Requeue: humanRetry}
	default:
		return Outcome{Reason: netboxv1alpha1.ReasonInvalid}
	}
}

// notFoundRetry is how long to wait for something that does not exist yet, which depends
// entirely on whether Kubernetes will tell us when it does.
//
// A named CR that is missing gets no timer: its creation is an event this operator receives.
// A slug, a lookup or a raw id that matches nothing in NetBox gets one, because NetBox sends
// nothing and a resync is the only thing that will ever notice.
func notFoundRetry(mode Mode) time.Duration {
	if mode == ModeName {
		return 0
	}

	return netboxRetry
}

// blockerFor turns a resolution failure into the Blocker a caller reports.
func blockerFor(err *Error) Blocker {
	outcome := Classify(err)

	return Blocker{Field: err.Field, Reason: outcome.Reason, Requeue: outcome.Requeue, Err: err}
}

// blocked builds the typed error for a reference this request could not resolve.
func (req Request) blocked(cause error, detail string) *Error {
	return &Error{
		Cause: cause, Field: req.Field.Spec, Ref: req.Ref, Mode: modeOf(req.Ref),
		TargetGVK: req.Field.Target, Detail: detail,
	}
}

// blockedTarget is blocked for a `name` reference, naming the CR it looked for.
func (req Request) blockedTarget(cause error, key types.NamespacedName, detail string) *Error {
	err := req.blocked(cause, detail)
	err.Target = key

	return err
}

// notReady is the wait: the target is there and its NetBox id is not.
func (req Request) notReady(key types.NamespacedName, detail string) *Error {
	return req.blockedTarget(ErrRefNotReady, key, detail)
}

// unavailableDetail says which half of "unavailable" this is: a descriptor that names no
// target Kind, or a target Kind nothing registered. The two have different fixes -- a
// descriptor edit and an operator upgrade -- so they must not read alike.
func (req Request) unavailableDetail() string {
	if req.Field.Target.Empty() {
		return fmt.Sprintf("the descriptor declares no target kind for %s", req.Field.Spec)
	}

	return fmt.Sprintf("no descriptor is registered for %s", req.Field.Target)
}

// orZero renders an optional id, so a malformed reference still prints.
func orZero(id *int64) int64 {
	if id == nil {
		return 0
	}

	return *id
}
