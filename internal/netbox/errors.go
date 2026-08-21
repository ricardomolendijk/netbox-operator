package netbox

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
	// of a 400 for whoever has to fix the manifest.
	Fields map[string][]string
	Body   string
}

func (e *ValidationError) Error() string {
	if len(e.Fields) == 0 {
		return fmt.Sprintf("netbox rejected the payload (%d): %s", e.Status, e.Body)
	}
	parts := make([]string, 0, len(e.Fields))
	for field, msgs := range e.Fields {
		parts = append(parts, fmt.Sprintf("%s: %s", field, strings.Join(msgs, "; ")))
	}
	return fmt.Sprintf("netbox rejected the payload (%d): %s", e.Status, strings.Join(parts, ", "))
}

// AuthError is a 401 or 403: the token is missing, wrong, or lacks permission. This
// fails the NetBoxEndpoint rather than the individual object, because otherwise one bad
// token scatters identical failures across every CR in the cluster.
type AuthError struct {
	Status int
	Body   string
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
	Body   string
}

func (e *ProtectedError) Error() string {
	return fmt.Sprintf("netbox refused the delete, object is referenced (%d): %s", e.Status, e.Body)
}

// RateLimitError is a 429. RetryAfter carries the server's Retry-After header when it
// sent one; the engine requeues after that rather than guessing.
type RateLimitError struct {
	RetryAfter time.Duration
	Body       string
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
type AmbiguousError struct {
	Endpoint string
	Params   Params
	Matched  int
}

func (e *AmbiguousError) Error() string {
	return fmt.Sprintf("ambiguous lookup on %s matched %d objects: %v", e.Endpoint, e.Matched, e.Params)
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
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return &AuthError{Status: status, Body: text}
	case status == http.StatusNotFound:
		return &NotFoundError{Endpoint: endpoint}
	case status == http.StatusTooManyRequests:
		return &RateLimitError{RetryAfter: retryAfter(header), Body: text}
	case status == http.StatusConflict, isProtected(text):
		return &ProtectedError{Status: status, Body: text}
	case status == http.StatusBadRequest:
		return &ValidationError{Status: status, Fields: parseFieldErrors(body), Body: text}
	case status >= 500:
		return &TransientError{Status: status}
	default:
		// Any other 4xx is a client-side problem that a retry will not fix. Treated as
		// validation so the engine backs off rather than hammering.
		return &ValidationError{Status: status, Body: text}
	}
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
// An unparseable body yields nil and the raw text survives in ValidationError.Body.
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
