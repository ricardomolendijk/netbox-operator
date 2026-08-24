// Package netbox is the only place in the operator that builds an HTTP request.
//
// The client never panics, never exits, and classifies every failure into a type from
// errors.go so the engine can pick a requeue strategy. That is the substantive difference
// from the netbox-populator client it is ported from, which called logger.Fatal on any
// API error -- acceptable in a one-shot CLI, fatal in a controller, where one 500 from
// NetBox would take down the manager and every other object with it.
package netbox

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"golang.org/x/time/rate"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
)

// Default client tuning. Every one of these is overridable through Config, which
// NBO-004 populates from NetBoxEndpoint.spec.
const (
	DefaultTimeout    = 30 * time.Second
	DefaultPageSize   = 250
	DefaultMaxPages   = 1000
	DefaultMaxRetries = 4
	DefaultBaseDelay  = 500 * time.Millisecond
	DefaultMaxDelay   = 30 * time.Second
	// DefaultRateLimitDelay is used when a 429 arrives without a Retry-After header.
	DefaultRateLimitDelay = 5 * time.Second
)

// Mode selects whether the client is allowed to change anything.
type Mode string

const (
	// ModeApply permits every method.
	ModeApply Mode = "Apply"
	// ModeDryRun suppresses POST, PATCH and DELETE. Reads still hit the live API, so
	// drift is reported against real state.
	ModeDryRun Mode = "DryRun"
)

// Config describes one NetBox endpoint. Zero values fall back to the Default* constants,
// so a caller only sets what it cares about.
type Config struct {
	// URL is the NetBox base URL, with or without a trailing /api.
	URL string
	// Token is the NetBox API token. Never logged, at any level.
	Token string
	// Mode defaults to ModeApply.
	Mode Mode

	Timeout time.Duration

	// InsecureSkipVerify disables TLS verification. CABundle is preferred.
	InsecureSkipVerify bool
	// CABundle is a PEM bundle of additional roots.
	CABundle []byte

	// QPS and Burst configure client-side rate limiting. Zero means unlimited.
	QPS   float64
	Burst int

	// PageSize is the per-request limit for list calls.
	PageSize int
	// MaxPages caps how many pages a single list will follow, so a runaway list cannot
	// exhaust the manager's memory. Exceeding it logs a warning and returns what it has.
	MaxPages int

	// MaxRetries is a pointer so that 0 means "do not retry" rather than "unset".
	// With a plain int, a caller asking to fail fast would silently get DefaultMaxRetries
	// -- and client-side retries are not always wanted, since the engine's requeue is
	// already a retry. nil means DefaultMaxRetries.
	MaxRetries *int
	BaseDelay  time.Duration
	MaxDelay   time.Duration

	// Transport overrides the HTTP transport. Tests use this; production leaves it nil.
	Transport http.RoundTripper
}

// Client talks to one NetBox instance. Safe for concurrent use.
type Client struct {
	base    string
	token   string
	mode    Mode
	http    *http.Client
	limiter *rate.Limiter

	pageSize   int
	maxPages   int
	maxRetries int
	baseDelay  time.Duration
	maxDelay   time.Duration
}

// New builds a client for cfg. It returns an error only for configuration that cannot
// work at all -- an empty URL, an unparseable URL, or an unusable CA bundle -- never for
// anything about NetBox's availability, which is the endpoint controller's problem.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.URL) == "" {
		return nil, errors.New("netbox url is required")
	}
	parsed, err := url.Parse(strings.TrimRight(cfg.URL, "/"))
	if err != nil {
		return nil, fmt.Errorf("parsing netbox url %q: %w", cfg.URL, err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("netbox url %q must be http or https", cfg.URL)
	}

	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}

	base := strings.TrimSuffix(parsed.String(), "/api") + "/api"
	client := &Client{
		base:       base,
		token:      cfg.Token,
		mode:       orDefault(cfg.Mode, ModeApply),
		http:       &http.Client{Transport: transport, Timeout: orDefaultDuration(cfg.Timeout, DefaultTimeout)},
		pageSize:   orDefaultInt(cfg.PageSize, DefaultPageSize),
		maxPages:   orDefaultInt(cfg.MaxPages, DefaultMaxPages),
		maxRetries: retriesOrDefault(cfg.MaxRetries),
		baseDelay:  orDefaultDuration(cfg.BaseDelay, DefaultBaseDelay),
		maxDelay:   orDefaultDuration(cfg.MaxDelay, DefaultMaxDelay),
	}
	if cfg.QPS > 0 {
		client.limiter = rate.NewLimiter(rate.Limit(cfg.QPS), orDefaultInt(cfg.Burst, int(cfg.QPS)))
	}
	return client, nil
}

func buildTransport(cfg Config) (http.RoundTripper, error) {
	if cfg.Transport != nil {
		return cfg.Transport, nil
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, InsecureSkipVerify: cfg.InsecureSkipVerify} //nolint:gosec // opt-in, and reported on the endpoint's status
	if len(cfg.CABundle) > 0 {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(cfg.CABundle) {
			return nil, errors.New("ca bundle contains no usable certificates")
		}
		tlsConfig.RootCAs = pool
	}
	return &http.Transport{TLSClientConfig: tlsConfig}, nil
}

// CloseIdleConnections releases keep-alive connections this client is holding. Callers
// that replace a client -- on a token rotation, say -- must call it, or every replacement
// leaves an idle connection pool behind for the lifetime of the process.
func (c *Client) CloseIdleConnections() {
	c.http.CloseIdleConnections()
}

// Mode reports whether this client is allowed to mutate NetBox.
func (c *Client) Mode() Mode { return c.mode }

// DryRun reports whether mutations are suppressed.
func (c *Client) DryRun() bool { return c.mode == ModeDryRun }

// GetOne returns the single object matching params, or nil when nothing matches.
// More than one match is an *AmbiguousError naming every one of them, never a silent choice.
//
// Built on List rather than on a request of its own, so that a lookup which must identify
// one object is the same request either way and "several matches is ambiguous" is decided
// in exactly one place. Both callers that need the matched set -- the engine's natural-key
// lookup and the resolver's -- read it off the error rather than counting for themselves,
// which is what made this wrapper unusable by them before (NBO-074).
func (c *Client) GetOne(ctx context.Context, endpoint string, params Params) (Object, error) {
	matches, err := c.List(ctx, endpoint, params)
	if err != nil {
		return nil, err
	}

	return One(endpoint, params, matches)
}

// One reduces the matches for a lookup to the one object it has to identify: nil for none,
// the object for one, and an *AmbiguousError naming every match for more.
//
// Exported so that a fake NetBox classifies ambiguity with this code rather than with a
// copy of the rule. A fake that decides for itself when a lookup is ambiguous is a fake
// that can disagree with the client about the one thing those tests are about.
func One(endpoint string, params Params, matches []Object) (Object, error) {
	switch len(matches) {
	case 0:
		return nil, nil
	case 1:
		return matches[0], nil
	default:
		return nil, ambiguous(endpoint, params, matches)
	}
}

// List returns every object matching params, following pagination.
//
// Hitting the page cap is an error, not a truncated result. The cap itself is not
// negotiable -- a NetBox that always reports a next page must not be able to exhaust the
// manager's memory -- but returning partial data is: a caller cannot distinguish it from a
// complete answer, and the engine's natural-key lookup then finds no match in the pages it
// received and creates an object that already exists. A safety limit that silently
// duplicates data is worse than no limit, so the limit stays and the silence goes.
func (c *Client) List(ctx context.Context, endpoint string, params Params) ([]Object, error) {
	query := Params{"limit": fmt.Sprint(c.pageSize)}
	for key, value := range params {
		query[key] = value
	}
	target := c.endpointURL(endpoint) + "?" + encodeParams(query)

	var all []Object
	for page := 0; target != ""; page++ {
		if page >= c.maxPages {
			// Needs a human to raise MaxPages or narrow the filter, which is what `error`
			// means (CONTRIBUTING.md, "Logging").
			err := &TruncatedError{Endpoint: endpoint, MaxPages: c.maxPages, Collected: len(all)}
			logf.FromContext(ctx).Error(err, "netbox list truncated at the page cap",
				"endpoint", endpoint, "action", "list", "maxPages", c.maxPages, "collected", len(all))

			return nil, err
		}
		body, err := c.do(ctx, http.MethodGet, target, endpoint, nil)
		if err != nil {
			return nil, err
		}
		all = append(all, asList(body["results"])...)
		target = asString(body["next"])
	}
	return all, nil
}

// GetByID fetches one object by its NetBox id. A missing object is a *NotFoundError
// carrying the id, which is how the engine learns to clear status.id.
func (c *Client) GetByID(ctx context.Context, endpoint string, id int) (Object, error) {
	obj, err := c.do(ctx, http.MethodGet, c.objectURL(endpoint, id), endpoint, nil)
	if err != nil {
		var notFound *NotFoundError
		if errors.As(err, &notFound) {
			notFound.ID = id
		}
		return nil, err
	}
	return obj, nil
}

// Create posts a new object.
//
// A DryRun client sends nothing and returns the payload marked suppressed. It does not
// invent an id: the populator handed out synthetic negative ids so dependent objects
// could reference something, but in a controller an id that does not exist server-side
// would be written to status.id and then treated as real forever.
func (c *Client) Create(ctx context.Context, endpoint string, payload Object) (Object, error) {
	if c.DryRun() {
		return suppress(payload), nil
	}
	return c.do(ctx, http.MethodPost, c.endpointURL(endpoint), endpoint, payload)
}

// Patch updates only the given fields on an existing object.
func (c *Client) Patch(ctx context.Context, endpoint string, id int, payload Object) (Object, error) {
	if c.DryRun() {
		return suppress(payload), nil
	}
	return c.do(ctx, http.MethodPatch, c.objectURL(endpoint, id), endpoint, payload)
}

// Delete removes an object by id. A protected foreign key surfaces as *ProtectedError.
//
// A DryRun client sends nothing and returns a suppressed Object naming what it would have
// removed -- {"endpoint": endpoint, "id": id} -- so a caller can report "would delete
// ipam/prefixes/11" without consulting Mode, exactly as it recognises a suppressed Create
// or Patch. A real delete returns a nil Object, because NetBox answers 204 with no body.
func (c *Client) Delete(ctx context.Context, endpoint string, id int) (Object, error) {
	if c.DryRun() {
		return suppress(Object{"endpoint": endpoint, "id": id}), nil
	}
	return c.do(ctx, http.MethodDelete, c.objectURL(endpoint, id), endpoint, nil)
}

// suppress returns a copy of payload flagged as never-sent, so a caller cannot mistake
// it for a live object.
func suppress(payload Object) Object {
	out := make(Object, len(payload)+1)
	for key, value := range payload {
		out[key] = value
	}
	out[dryRunMarker] = true
	return out
}

func (c *Client) endpointURL(endpoint string) string {
	return fmt.Sprintf("%s/%s/", c.base, strings.Trim(endpoint, "/"))
}

func (c *Client) objectURL(endpoint string, id int) string {
	return fmt.Sprintf("%s%d/", c.endpointURL(endpoint), id)
}

// encodeParams renders params in sorted key order, so a URL is stable across calls and
// therefore usable as a cache key and readable in a log line.
func encodeParams(params Params) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	values := url.Values{}
	for _, key := range keys {
		values.Set(key, params[key])
	}
	return values.Encode()
}

func orDefault(mode, fallback Mode) Mode {
	if mode == "" {
		return fallback
	}
	return mode
}

// Retries is a convenience for setting Config.MaxRetries, including to zero.
func Retries(n int) *int { return &n }

// retriesOrDefault honours an explicit zero, unlike orDefaultInt.
func retriesOrDefault(value *int) int {
	if value == nil {
		return DefaultMaxRetries
	}
	if *value < 0 {
		return 0
	}
	return *value
}

func orDefaultInt(value, fallback int) int {
	if value <= 0 {
		return fallback
	}
	return value
}

func orDefaultDuration(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// jitter returns a randomised backoff for attempt n, capped at maxDelay. Full jitter
// (uniform in [0, backoff]) rather than equal jitter, because several controllers
// retrying the same failing endpoint in lockstep is the thing to avoid.
func (c *Client) jitter(attempt int) time.Duration {
	backoff := c.baseDelay << attempt
	if backoff > c.maxDelay || backoff <= 0 {
		backoff = c.maxDelay
	}
	return time.Duration(rand.Int64N(int64(backoff)) + 1) //nolint:gosec // jitter, not a secret
}

// sleep waits for d, or returns early if the context is done. Returns the context's
// error so a cancelled reconcile stops retrying immediately.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting to retry: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}
