package netbox

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
)

// do performs one request, retrying only failures that a retry can fix.
//
// Retries are confined to *TransientError and *RateLimitError. A 400 or a 409 will fail
// identically every time, and retrying them here would hide the failure from the engine,
// which is the component that knows whether to back off, fail the object, or fail the
// endpoint.
func (c *Client) do(ctx context.Context, method, target, endpoint string, payload Object) (Object, error) {
	var lastErr error
	for attempt := 0; attempt <= c.maxRetries; attempt++ {
		if attempt > 0 {
			if err := sleep(ctx, c.retryDelay(attempt-1, lastErr)); err != nil {
				return nil, err
			}
		}
		obj, err := c.attempt(ctx, method, target, endpoint, payload)
		if err == nil {
			return obj, nil
		}
		if !Retryable(err) {
			return nil, err
		}
		lastErr = err
		logf.FromContext(ctx).V(1).Info("retrying netbox request",
			"method", method, "endpoint", endpoint, "action", "retry",
			"attempt", attempt+1, "err", err.Error())
	}
	return nil, fmt.Errorf("netbox request to %s gave up after %d attempts: %w", endpoint, c.maxRetries+1, lastErr)
}

// retryDelay honours a 429's Retry-After and otherwise uses jittered exponential backoff.
func (c *Client) retryDelay(attempt int, err error) time.Duration {
	var limited *RateLimitError
	if errors.As(err, &limited) {
		if limited.RetryAfter > 0 {
			return limited.RetryAfter
		}
		return DefaultRateLimitDelay
	}
	return c.jitter(attempt)
}

// attempt performs exactly one HTTP round trip.
func (c *Client) attempt(ctx context.Context, method, target, endpoint string, payload Object) (Object, error) {
	if c.limiter != nil {
		if err := c.limiter.Wait(ctx); err != nil {
			return nil, fmt.Errorf("waiting for the client rate limiter: %w", err)
		}
	}

	req, err := c.newRequest(ctx, method, target, payload)
	if err != nil {
		return nil, err
	}

	// Enabled() is checked rather than relying on the logger to drop the line: redact
	// copies the whole body, and doing that for every request and every 250-object list
	// page when nobody is listening is real work for nothing.
	log := logf.FromContext(ctx).WithValues("method", method, "endpoint", endpoint, "action", "request")
	debug := log.V(1)

	if payload != nil && debug.Enabled() {
		debug.Info("netbox request", "body", redact(payload))
	}

	// Timed from here rather than from the top of the function, so the metric measures
	// NetBox's latency and not the operator throttling itself at the rate limiter above.
	started := time.Now()

	resp, err := c.http.Do(req)
	if err != nil {
		metrics.ObserveRequest(endpoint, method, 0, time.Since(started))
		// A cancelled or timed-out context is the caller's decision, not a NetBox
		// failure: surfacing it as transient would make the engine retry a reconcile
		// that has already been abandoned.
		if ctxErr := ctx.Err(); ctxErr != nil {
			return nil, fmt.Errorf("netbox request to %s: %w", endpoint, ctxErr)
		}
		return nil, &TransientError{Err: fmt.Errorf("requesting %s: %w", endpoint, err)}
	}
	defer func() { _ = resp.Body.Close() }()

	body, readErr := io.ReadAll(resp.Body)
	// Recorded once the body is read, because a response whose body is still arriving has
	// not cost the caller its full latency yet.
	metrics.ObserveRequest(endpoint, method, resp.StatusCode, time.Since(started))
	if readErr != nil {
		return nil, &TransientError{Err: fmt.Errorf("reading response from %s: %w", endpoint, readErr)}
	}

	if resp.StatusCode >= http.StatusBadRequest {
		return nil, classify(endpoint, resp.StatusCode, resp.Header, body)
	}

	decoded, err := decode(endpoint, body)
	if err != nil {
		return nil, err
	}
	if debug.Enabled() {
		debug.Info("netbox response", "code", resp.StatusCode, "body", redact(decoded))
	}

	return decoded, nil
}

func (c *Client) newRequest(ctx context.Context, method, target string, payload Object) (*http.Request, error) {
	var reader io.Reader
	if payload != nil {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encoding request body for %s: %w", target, err)
		}
		reader = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		return nil, fmt.Errorf("building %s request for %s: %w", method, target, err)
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// decode parses a successful response. An empty body is normal for 204 on DELETE.
func decode(endpoint string, body []byte) (Object, error) {
	if len(bytes.TrimSpace(body)) == 0 {
		return nil, nil
	}
	var obj Object
	if err := json.Unmarshal(body, &obj); err != nil {
		// A body we cannot parse is not transient: the same request will produce the
		// same unparseable body. Most often it is an HTML error page from a proxy in
		// front of NetBox, so the first line is the useful part.
		return nil, &ValidationError{
			Body: fmt.Sprintf("unparseable response from %s: %s", endpoint, firstLine(body)),
		}
	}
	return obj, nil
}

func firstLine(body []byte) string {
	text := strings.TrimSpace(string(body))
	if idx := strings.IndexByte(text, '\n'); idx >= 0 {
		text = text[:idx]
	}
	const max = 200
	if len(text) > max {
		return text[:max] + "…"
	}
	return text
}

// secretFields are payload keys whose values must never reach a log, at any level.
// NetBox stores pre-shared keys and passwords in plain fields on several models.
var secretFields = map[string]struct{}{
	"auth_psk": {}, "psk": {}, "preshared_key": {}, "password": {}, "auth_key": {},
	"token": {}, "secret": {}, "private_key": {}, "api_key": {},
}

// redact copies payload with secret values masked and custom fields collapsed to their
// key names. Custom fields are collapsed rather than masked because their names are
// useful for debugging and their values are arbitrary user data. This is a tested
// function rather than a convention because "remember not to log that" does not hold.
//
// It descends into nested objects and lists. That is not decoration: a list response
// arrives as {"results": [...]}, so masking only the top level would put every
// `auth_psk` on the page straight into the log.
func redact(payload Object) Object {
	out := make(Object, len(payload))
	for key, value := range payload {
		out[key] = redactValue(key, value)
	}
	return out
}

// redactValue masks one value, recursing through the containers it may be inside. Depth
// is bounded by the JSON itself, exactly as in parseFieldErrors.
func redactValue(key string, value any) any {
	if isSecretField(key) {
		return "[redacted]"
	}
	if key == customFieldsKey {
		return redactCustomFields(value)
	}

	switch typed := value.(type) {
	case map[string]any:
		return map[string]any(redact(typed))
	case []any:
		items := make([]any, 0, len(typed))
		for _, item := range typed {
			// The parent's key is carried down so that a list *of* secrets is masked by
			// its own name; a list of objects is masked by each object's own keys.
			items = append(items, redactValue(key, item))
		}
		return items
	default:
		return value
	}
}

func isSecretField(key string) bool {
	_, ok := secretFields[strings.ToLower(key)]
	return ok
}

func redactCustomFields(value any) any {
	fields, ok := value.(map[string]any)
	if !ok {
		return "[redacted]"
	}
	names := make([]string, 0, len(fields))
	for name := range fields {
		names = append(names, name)
	}
	sort.Strings(names)
	return fmt.Sprintf("[%d custom fields redacted: %s]", len(names), strings.Join(names, ", "))
}
