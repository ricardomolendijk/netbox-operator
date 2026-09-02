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
		// Redacted, because a url that does not parse is still a url somebody may have put a
		// password in, and this error is rendered into a condition on the NetBoxEndpoint.
		return nil, fmt.Errorf("parsing netbox url %q: %w", RedactURL(cfg.URL), err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, fmt.Errorf("netbox url %q must be http or https", RedactURL(cfg.URL))
	}
	if err := checkBaseURL(parsed, cfg.URL); err != nil {
		return nil, err
	}

	transport, err := buildTransport(cfg)
	if err != nil {
		return nil, err
	}

	// Safe as string surgery only because checkBaseURL has just established that
	// parsed.String() is scheme, authority and path and nothing else. With a query on the
	// end, "/api" and every path after it lands inside a parameter value instead.
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

// checkBaseURL refuses the url shapes that are not a NetBox base url.
//
// This is the reconcile-time half of the rule; the other half is the CEL on
// NetBoxEndpointSpec.URL, which rejects the same three shapes at apply time. Both exist
// because the validating webhook's failurePolicy is Ignore and CEL is skipped for any
// client that builds a Config without going through the API server at all -- the manager's
// own flags, and every test. docs/operations/admission-webhooks.md: every denial has a
// reconcile-time backstop, and the backstop is the authority.
//
// Refusing rather than stripping. Stripping would make the endpoint work while quietly
// meaning something other than what was written, and the whole complaint in #298 is a url
// that means something other than it looks like it means.
//
// raw is passed separately so the message quotes what the operator wrote, not the
// normalised form -- and redacted, because a url with a password in it is exactly one of
// the shapes being rejected here, and this error is rendered into a condition.
func checkBaseURL(parsed *url.URL, raw string) error {
	switch {
	case parsed.Host == "":
		return fmt.Errorf("netbox url %q must name a host", RedactURL(raw))
	case parsed.User != nil:
		// Not "the password leaks": that is true but secondary. A NetBox token goes in the
		// Authorization header from a Secret, so userinfo here is either a credential in a
		// world-readable spec field or a mistake.
		return fmt.Errorf("netbox url %q must not carry userinfo; put the credential in the"+
			" secret named by tokenSecretRef", RedactURL(raw))
	case parsed.RawQuery != "" || parsed.ForceQuery:
		// The one with teeth. Every request path is appended to this url, so a query on the
		// end swallows "/api/status/" as a parameter value and the path that is actually
		// requested is whatever was written before the "?" (#298).
		return fmt.Errorf("netbox url %q must not carry a query string: the rest of the api"+
			" path is appended to it, so a query would absorb that suffix and choose the"+
			" request path itself", RedactURL(raw))
	case parsed.Fragment != "":
		return fmt.Errorf("netbox url %q must not carry a fragment: a fragment is never sent"+
			" to a server", RedactURL(raw))
	}
	return nil
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

		next, err := nextPage(ctx, target, asString(body["next"]))
		if err != nil {
			return nil, err
		}
		target = next
	}
	return all, nil
}

// nextPage is the URL of the next page, rebuilt onto the one we asked for.
//
// Only the query survives the round trip. `next` is a value out of the response body, and
// every request this client makes carries the endpoint's API token in an Authorization
// header -- so following it verbatim means a NetBox that answers
// `{"next": "https://elsewhere/"}` is handed the token, and a client with
// insecureSkipVerify set hands it to whoever is in the middle instead. Go's own redirect
// handling strips credentials across origins; this is not a redirect but a request the
// client builds itself, so that protection never applies and the check has to be here.
//
// Rebuilt rather than refused, because the two ways a legitimate `next` differs from the
// request are both benign: NetBox renders it from the request's absolute URI, so a reverse
// proxy that rewrites Host or forwards a different scheme produces a URL that is correct
// for a browser and wrong for this client. Pagination lives entirely in the query
// (`limit`/`offset`, or a cursor), so keeping the query and discarding the origin follows
// the server's paging exactly while making the destination unforgeable. The mismatch is
// still worth saying out loud once per list, because the other thing that produces one is
// the attack.
//
// An empty `next` is the last page and returns "", which is what ends the loop.
func nextPage(ctx context.Context, current, next string) (string, error) {
	if next == "" {
		return "", nil
	}

	parsed, err := url.Parse(next)
	if err != nil {
		// Not transient: the same list will produce the same unparseable value.
		return "", &ValidationError{
			Body: fmt.Sprintf("netbox returned an unparseable next-page url: %s", RedactURL(next)),
		}
	}

	base, err := url.Parse(current)
	if err != nil {
		return "", fmt.Errorf("parsing the current page url: %w", err)
	}

	if parsed.IsAbs() && (parsed.Scheme != base.Scheme || parsed.Host != base.Host) {
		logf.FromContext(ctx).Info("netbox paginated to a different origin; keeping the query and "+
			"discarding the host, because following it would send this endpoint's token there",
			"action", "list", "expected", base.Scheme+"://"+base.Host,
			"got", parsed.Scheme+"://"+parsed.Host)
	}

	// The query the server chose, on the URL we know is the endpoint's.
	paged := *base
	paged.RawQuery = parsed.RawQuery

	return paged.String(), nil
}

// RedactURL renders a URL with any embedded credentials masked, for a log line, an Event or
// a condition message.
//
// url.URL.Redacted does this for a URL that parses; the string form is here because the one
// place a URL most needs redacting is the error saying it could not be parsed. Everything
// between `//` and the last `@` of the authority is the userinfo, and that is what goes.
func RedactURL(raw string) string {
	if parsed, err := url.Parse(raw); err == nil {
		return parsed.Redacted()
	}

	start := strings.Index(raw, "//")
	if start < 0 {
		return raw
	}
	authority := raw[start+2:]
	end := strings.IndexAny(authority, "/?#")
	if end < 0 {
		end = len(authority)
	}
	at := strings.LastIndex(authority[:end], "@")
	if at < 0 {
		return raw
	}

	return raw[:start+2] + "xxxxx:xxxxx" + authority[at:]
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
