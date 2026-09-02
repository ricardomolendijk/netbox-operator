package netbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"mime"
	"net/http"
	"slices"
	"strings"
	"time"
)

// The client classifies every failure into one of these types so the engine can choose a
// requeue strategy without parsing strings. Errors are matched with errors.As, never by
// comparing messages -- NetBox's wording changes between releases.
//
// Deliberately absent: a generic "APIError". An unclassified failure would get whatever
// backoff the engine defaults to, which is how a permanent error ends up retried forever.

// ValidationError is a 400: NetBox rejected the payload. Retrying an unchanged payload
// cannot succeed, so the engine backs off hard and waits for the spec to change.
type ValidationError struct {
	Status int
	// Fields maps a field name to its errors, as DRF returns them. Non-field errors are
	// under the key "__all__". Preserved because "which field" is the only useful part
	// of a 400 for whoever has to fix the manifest. Rendered within a fixed character
	// budget for the reason summariseBody gives.
	Fields map[string][]string
	// Body is a summary of what the server said, not the body itself: see summariseBody.
	Body string

	// raw is the body verbatim, for the two in-package predicates that have to match on
	// NetBox's wording (overlapping, and isProtected's caller). Unexported so that it
	// cannot be rendered into a condition by accident, which is the whole point of Body
	// being a summary.
	raw string
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return fmt.Sprintf("netbox rejected the payload (%d): %s", e.Status, e.Body)
	}
	return fmt.Sprintf("netbox rejected the payload (%d): %s", e.Status, summariseFields(e.Fields))
}

// summariseFields renders Fields in a stable order and within a fixed budget.
//
// Sorted because the message is written into a condition: Go randomises map iteration, so
// an unsorted join makes a two-field 400 produce a different message every reconcile, and
// every reconcile a real status write.
//
// Budgeted because the field names and messages come from the server, and on a 400 the
// server is whatever host spec.url named (#298). Trimming to a few hundred characters
// keeps "which field" -- the reason this map is parsed at all -- without turning a
// condition into a general-purpose readout of someone else's response.
func summariseFields(fields map[string][]string) string {
	const budget = 512

	parts := make([]string, 0, len(fields))
	spent := 0
	for _, field := range slices.Sorted(maps.Keys(fields)) {
		if spent >= budget {
			parts = append(parts, fmt.Sprintf("and %d more", len(fields)-len(parts)))
			break
		}
		part := clipText(fmt.Sprintf("%s: %s", field, strings.Join(fields[field], "; ")), budget-spent)
		spent += len(part)
		parts = append(parts, part)
	}
	return strings.Join(parts, ", ")
}

// AuthError is a 401 or 403: the token is missing, wrong, or lacks permission. This
// fails the NetBoxEndpoint rather than the individual object, because otherwise one bad
// token scatters identical failures across every CR in the cluster.
type AuthError struct {
	Status int
	// Body is a summary of what the server said, not the body itself: see summariseBody.
	Body string
}

func (e *AuthError) Error() string {
	return fmt.Sprintf("netbox authentication or permission failure (%d): %s", e.Status, e.Body)
}

// NotFoundError is a 404. On a GET by id it means the object was deleted server-side and
// the CR's status.id must be cleared.
type NotFoundError struct {
	Endpoint string
	ID       int
}

func (e *NotFoundError) Error() string {
	if e.ID == 0 {
		return fmt.Sprintf("netbox object not found at %s", e.Endpoint)
	}
	return fmt.Sprintf("netbox object %s/%d not found", e.Endpoint, e.ID)
}

// ProtectedError is a deletion blocked by a protected foreign key. Django raises
// ProtectedError, which NetBox surfaces as a 409 (and in some paths a 400 whose body
// names the protection). Not a fast-retry error: something else must be deleted first.
type ProtectedError struct {
	Status int
	// Body is a summary of what the server said, not the body itself: see summariseBody.
	Body string
}

func (e *ProtectedError) Error() string {
	return fmt.Sprintf("netbox refused the delete, object is referenced (%d): %s", e.Status, e.Body)
}

// RateLimitError is a 429. RetryAfter carries the server's Retry-After header when it
// sent one; the engine requeues after that rather than guessing.
type RateLimitError struct {
	RetryAfter time.Duration
	// Body is a summary of what the server said, not the body itself: see summariseBody.
	Body string
}

func (e *RateLimitError) Error() string {
	return fmt.Sprintf("netbox rate limited the request, retry after %s", e.RetryAfter)
}

// TransientError is a 5xx or a transport failure: worth retrying with backoff.
type TransientError struct {
	Status int
	Err    error
}

func (e *TransientError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("netbox request failed transiently: %v", e.Err)
	}
	return fmt.Sprintf("netbox returned a transient error (%d)", e.Status)
}

func (e *TransientError) Unwrap() error { return e.Err }

// AmbiguousError is more than one match for a lookup that must identify one object.
//
// This is an error rather than "take the first result" on purpose. Several NetBox models
// have no database uniqueness to lean on -- ipam.Prefix and ipam.IPAddress have no
// meta.constraints at all, and ipam.VRF.name is not unique -- so picking the first match
// silently adopts an unrelated object. In the populator that reparents every prefix and
// address keyed on that VRF. See docs/netbox-schema.md.
//
// It names every match rather than counting them. The reader's next question is always
// "which ones?", and a count leaves them to reproduce the query by hand -- while the ids
// and NetBox's own display strings are already in the response body the lookup would
// otherwise throw away.
type AmbiguousError struct {
	Endpoint string
	Params   Params

	// Matched is how many objects the lookup matched. Kept alongside IDs rather than
	// derived from it, because a match whose body carried no id is still a match and must
	// not quietly reduce the total.
	Matched int

	// IDs is the NetBox primary key of every match that carried one, in the order NetBox
	// returned them.
	IDs []int

	// Display is NetBox's `display` for each of IDs, at the same index -- empty where the
	// endpoint sent none. Carried because "10.0.0.0/24" is what a human recognises and
	// "11" is not, and the two together are enough to go and look.
	Display []string

	// Objects are the matches themselves, as NetBox returned them.
	//
	// Carried for the caller that has to *decide between* them rather than only report
	// them: an ipam.IPAddress with spec.allowDuplicate identifies its own object by the
	// provenance stamp on it (NBO-025), and the stamp is a custom field on the body. The
	// alternative was a second request for data this response already contained.
	Objects []Object
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("ambiguous lookup on %s: %v matched %d netbox objects, %s; refusing to guess which one was meant",
		e.Endpoint, e.Params, e.Matched, e.Matches())
}

// Matches renders the matched objects as `id 11 (10.0.0.0/24), id 12 (10.0.0.0/24)`.
//
// Exported so that a caller which already names the endpoint and the query -- a reference,
// whose own rendering carries both -- can report what it hit without repeating them.
func (e *AmbiguousError) Matches() string {
	if len(e.IDs) == 0 {
		return "none of which carried a netbox id"
	}

	parts := make([]string, 0, len(e.IDs)+1)
	for i, id := range e.IDs {
		part := fmt.Sprintf("id %d", id)
		if i < len(e.Display) && e.Display[i] != "" {
			part += " (" + e.Display[i] + ")"
		}
		parts = append(parts, part)
	}

	if len(e.IDs) < e.Matched {
		parts = append(parts, "and others that carried no netbox id")
	}

	return strings.Join(parts, ", ")
}

// ambiguous builds the error for a lookup that matched more than one object, reading the id
// and the display of each out of the matches so that no caller has to ask NetBox a second
// time to find out what it hit.
func ambiguous(endpoint string, params Params, matches []Object) *AmbiguousError {
	err := &AmbiguousError{
		Endpoint: endpoint, Params: params, Matched: len(matches), Objects: matches,
	}

	for _, obj := range matches {
		id, ok := obj.ID()
		if !ok {
			continue
		}

		err.IDs = append(err.IDs, id)
		err.Display = append(err.Display, asString(obj["display"]))
	}

	return err
}

// TruncatedError is a list that hit the page cap before NetBox stopped offering pages.
//
// It is deliberately not retryable: the same request will truncate at the same place. It
// means either the filter did not apply -- a natural-key lookup expects a handful of
// results, so paginating past the cap says the query was wrong -- or the endpoint genuinely
// holds more objects than MaxPages allows, and somebody has to decide which.
//
// Returning this instead of partial results is the whole point. A caller that cannot tell
// truncation from completeness will act on absence, and acting on absence means creating a
// duplicate, or in a prune, deleting something real.
type TruncatedError struct {
	Endpoint string
	MaxPages int
	// Collected is how many objects had been read when the cap was hit, for diagnosis
	// only. It is not returned to the caller as data.
	Collected int
}

func (e *TruncatedError) Error() string {
	return fmt.Sprintf(
		"list of %s truncated at the %d-page cap after %d objects; results would be incomplete",
		e.Endpoint, e.MaxPages, e.Collected)
}

// ContendedError is a placement that lost the race, repeatedly.
//
// Its own type beside ExhaustedError, and the distinction is the whole reason it exists:
// exhausted means the pool is full and only widening it or freeing something helps, contended
// means the space is there and somebody else took this attempt's candidate. Reporting one as
// the other sends a human looking for space that exists, or waiting for space that does not.
//
// It is only ever produced by the unlocked placement path (see PlaceRange). The advisory-locked
// endpoints cannot contend: NetBox serialises them, so a loser there is told the pool is
// exhausted or is handed a different object.
type ContendedError struct {
	// Endpoint is the REST path the range would have been created at.
	Endpoint string

	// Pool is the parent prefix the placement was computed inside.
	Pool string

	// Attempts is how many placements were computed and rejected.
	Attempts int

	// Body is what NetBox said about the last one, verbatim.
	Body string
}

func (e *ContendedError) Error() string {
	return fmt.Sprintf(
		"%d placements in %s were each rejected as overlapping a range created between the read and"+
			" the write; nothing was created: %s", e.Attempts, e.Pool, e.Body)
}

// Retryable reports whether err is worth retrying without any change to the request.
// Only transient failures and rate limits qualify; a 400 or a 409 will fail identically
// every time, and retrying them inside the client hides the failure from the engine.
func Retryable(err error) bool {
	var transient *TransientError
	var limited *RateLimitError
	return errors.As(err, &transient) || errors.As(err, &limited)
}

// classify turns an HTTP response into one of the typed errors above. body is the
// already-read response body.
func classify(endpoint string, status int, header http.Header, body []byte) error {
	text := strings.TrimSpace(string(body))
	summary := summariseBody(header.Get("Content-Type"), body)
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return &AuthError{Status: status, Body: summary}
	case status == http.StatusNotFound:
		return &NotFoundError{Endpoint: endpoint}
	case status == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: retryAfter(header), Body: summary}
	case status == http.StatusConflict, isProtected(text):
		return &ProtectedError{Status: status, Body: summary}
	case status == http.StatusBadRequest:
		return &ValidationError{Status: status, Fields: parseFieldErrors(body), Body: summary, raw: text}
	case status >= 500:
		return &TransientError{Status: status}
	default:
		// Any other 4xx is a client-side problem that a retry will not fix. Treated as
		// validation so the engine backs off rather than hammering.
		return &ValidationError{Status: status, Body: summary, raw: text}
	}
}

// summariseBody is everything a response body is allowed to contribute to an error string.
//
// Every error above is rendered into a NetBoxEndpoint's `Ready` condition message and into
// a Warning Event by internal/controller/netboxendpoint_controller.go's fail(). Both are
// readable by whoever can read the CR -- which is whoever wrote `spec.url`. So the body
// being summarised here is a body from a host of the CR author's choosing, fetched with the
// operator's network position and the operator's token, and putting it back verbatim turns
// a misconfigured field into a read primitive (#298).
//
// What survives is what diagnoses a *real* NetBox and nothing else:
//
//   - the media type and the length, which is what distinguishes "NetBox answered" from
//     "an HTML error page from the proxy in front of it" -- the single most common cause of
//     a baffling endpoint failure, and the one firstLine() used to exist for;
//   - DRF's `detail` string, when the body parses as JSON and carries one. That is NetBox's
//     own error shape ("Invalid token.", "You do not have permission to perform this
//     action."), it is the sentence an operator actually acts on, and a host that is not a
//     DRF application does not produce it.
//
// The verbatim body is not lost: attempt() logs it, redacted, at debug -- an operator-only
// surface, unlike a condition on a namespaced object.
func summariseBody(contentType string, body []byte) string {
	shape := fmt.Sprintf("%s, %d bytes", mediaType(contentType), len(body))
	if len(strings.TrimSpace(string(body))) == 0 {
		return "empty body (" + shape + ")"
	}
	if detail := netboxDetail(body); detail != "" {
		return fmt.Sprintf("%q (%s)", clipText(detail, maxDetail), shape)
	}
	return "body withheld, it is not netbox's error shape (" + shape +
		"); run the manager with -v=1 to log it"
}

// maxDetail bounds the one piece of server-chosen prose that reaches a condition.
const maxDetail = 200

// netboxDetail returns DRF's `detail`, the key NetBox puts a single error sentence under.
// Empty for a body that is not a JSON object, or does not carry one, or carries something
// other than a string there.
func netboxDetail(body []byte) string {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return ""
	}
	detail, _ := decoded["detail"].(string)
	return strings.TrimSpace(detail)
}

// mediaType is the Content-Type without its parameters, and without trusting its length:
// the header is as server-chosen as the body is.
func mediaType(contentType string) string {
	parsed, _, err := mime.ParseMediaType(contentType)
	if err != nil || parsed == "" {
		return "unknown content type"
	}
	return clipText(parsed, 64)
}

// clip bounds a server-chosen string, marking that it was cut. Cut on runes rather than
// bytes: a body is arbitrary input, and slicing one mid-rune writes U+FFFD into a
// condition.
func clipText(text string, max int) string {
	if len(text) <= max {
		return text
	}
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return string(runes[:max]) + "…"
}

// isProtected detects a Django ProtectedError that arrived with a non-409 status.
// Matching on wording is unavoidable here -- DRF flattens the exception into a detail
// string -- so it is confined to this one function and kept broad.
func isProtected(body string) bool {
	lower := strings.ToLower(body)
	return strings.Contains(lower, "protected foreign key") ||
		strings.Contains(lower, "protected objects") ||
		strings.Contains(lower, "cannot delete some instances")
}

// retryAfter reads the Retry-After header, which is either a count of seconds or an
// HTTP date. Returns 0 when absent or unparseable, letting the caller pick a default.
func retryAfter(header http.Header) time.Duration {
	value := strings.TrimSpace(header.Get("Retry-After"))
	if value == "" {
		return 0
	}
	if seconds, err := time.ParseDuration(value + "s"); err == nil {
		return seconds
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0
	}
	if until := time.Until(when); until > 0 {
		return until
	}
	return 0
}

// nonFieldKey collects errors that DRF reports without naming a field.
const nonFieldKey = "__all__"

// parseFieldErrors pulls DRF's field-level messages out of a 400 body.
//
// DRF is not consistent about the shape: a serializer error is {"field": ["msg"]}, a
// model clean() surfaces as {"detail": "msg"} or {"non_field_errors": ["msg"]}, and a
// nested serializer (scope, assigned_object) nests one level deeper. All four are
// flattened here, nested keys joined with a dot, so the caller gets one flat map.
// An unparseable body yields nil, and ValidationError.Body reports its shape instead.
func parseFieldErrors(body []byte) map[string][]string {
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil
	}
	fields := make(map[string][]string, len(decoded))
	for key, value := range decoded {
		name := key
		if key == "detail" || key == "non_field_errors" {
			name = nonFieldKey
		}
		flattenErrors(fields, name, value)
	}
	if len(fields) == 0 {
		return nil
	}
	return fields
}

// flattenErrors appends every string it can reach under value to fields[name],
// descending into nested maps and lists. Depth is bounded by the JSON itself.
func flattenErrors(fields map[string][]string, name string, value any) {
	switch typed := value.(type) {
	case string:
		fields[name] = append(fields[name], typed)
	case []any:
		for _, item := range typed {
			flattenErrors(fields, name, item)
		}
	case map[string]any:
		for key, nested := range typed {
			flattenErrors(fields, name+"."+key, nested)
		}
	}
}
