package netbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

// The advisory-locked allocation sub-paths, appended to one pool object's URL.
//
// NetBox takes a `select_for_update` advisory lock for the whole of one of these requests
// (`ipam/api/views.py:121-129`, `:352-427`), so the POST is a single atomic call: two
// controller workers -- or two clusters -- cannot be handed the same address. That is why
// this operator takes no client-side lock and does not serialise per pool. A client-side
// lock would be both unnecessary here and wrong across two clusters.
//
// Spelled as constants because which one a claim kind uses is data on its
// registry.ClaimDescriptor rather than a branch anywhere.
const (
	// AvailableIPs allocates one ipam.IPAddress out of a prefix or an ip-range.
	AvailableIPs = "available-ips"

	// AvailablePrefixes allocates one child ipam.Prefix out of a container.
	AvailablePrefixes = "available-prefixes"
)

// ExhaustedError is a pool with nothing left to hand out.
//
// Its own type rather than the *ProtectedError a 409 classifies to everywhere else,
// because on an allocation POST a 409 means something entirely different: not "delete
// something else first" but "there is no free object in here". The two are fixed
// differently and only one of them is about this claim's pool.
//
// It is deliberately not a *ValidationError either. The request is perfectly valid and
// will succeed unchanged the moment somebody widens the prefix or frees an address, which
// is why the claim waits rather than failing terminally
// (docs/decisions/0004-claims-first-allocation.md, exhaustion).
type ExhaustedError struct {
	// Endpoint is the pool's REST path with the sub-path attached, e.g.
	// `ipam/prefixes/available-ips`.
	Endpoint string

	// ID is the pool object's NetBox primary key.
	ID int

	// Body is what NetBox said, verbatim.
	Body string
}

func (e *ExhaustedError) Error() string {
	return fmt.Sprintf("netbox has no free object left in %s/%d: %s", e.Endpoint, e.ID, e.Body)
}

// URL is the NetBox this client talks to, normalised: no trailing slash, and no `/api`
// suffix whether or not the configured URL carried one.
//
// It exists for one caller, and the caller is the reason it is derived from the client
// rather than read from the NetBoxEndpoint CR: it is the first component of a claim's
// allocation identity (docs/decisions/0005-gitops-coexistence.md section 3), and an
// identity computed from a field that can be momentarily unreadable would allocate a
// *second* address the one time the read failed. Taken from the client, it cannot be empty
// and cannot disagree with the NetBox that is about to be POSTed to.
//
// Normalisation is load-bearing for the same reason: `https://nb/`, `https://nb` and
// `https://nb/api` are one NetBox, and three spellings of it must not be three identities.
func (c *Client) URL() string { return strings.TrimSuffix(c.base, "/api") }

// Allocate POSTs payload to one pool's advisory-locked available-* sub-path and returns the
// object NetBox created.
//
// The body is a single object rather than a one-element list, and the choice is not
// cosmetic: NetBox mirrors the shape it was given -- a list in, a list of results out; one
// object in, one object out (`AvailableIPsView.post`) -- so the single-object form makes
// "NetBox returned more objects than were asked for" unrepresentable instead of a failure
// mode to handle. It also means an allocation is an ordinary Object-in, Object-out request
// like every other call here.
//
// NetBox injects `address` (and the parent's `vrf`) into the body and otherwise honours the
// full write serializer, so `custom_fields` and `tags` ride along on the atomic call. There
// is therefore no window in which an allocated address exists without the identity that
// says whose it is -- which is the whole reason the identity is written here rather than by
// a follow-up PATCH.
//
// **It does not retry.** Every other write in this package goes through do(), which retries
// a 5xx or a transport failure; this one goes through a single attempt, because an
// allocating POST is not idempotent. A POST that committed and lost its response, retried,
// allocates a *second* address -- silently, one per attempt, until the prefix is full. So a
// transient failure here is returned to the caller, whose next pass finds whatever actually
// landed by searching for its own identity. That search is the recovery mechanism; a
// client-side retry would defeat it.
//
// A DryRun client sends nothing and returns the payload marked suppressed, exactly as
// Create does.
func (c *Client) Allocate(
	ctx context.Context, endpoint string, id int, sub string, payload Object,
) (Object, error) {
	if c.DryRun() {
		return suppress(payload), nil
	}

	// The metric and log label carries the sub-path and not the pool's id: one series per
	// prefix would make netbox_operator_api_requests_total unbounded in the size of the
	// IPAM.
	label := endpoint + "/" + sub

	allocated, err := c.attempt(ctx, http.MethodPost, c.objectURL(endpoint, id)+sub+"/", label, payload)
	if err != nil {
		return nil, exhausted(label, id, err)
	}

	return allocated, nil
}

// exhausted re-classifies the one status code that means something different on an
// allocation POST than it does anywhere else.
//
// Keyed on the status carried by the typed error rather than on the body's wording: a 400
// whose text happens to mention a protected relation classifies to the same Go type and is
// not exhaustion.
func exhausted(endpoint string, id int, err error) error {
	var protected *ProtectedError
	if !errors.As(err, &protected) || protected.Status != http.StatusConflict {
		return err
	}

	return &ExhaustedError{Endpoint: endpoint, ID: id, Body: protected.Body}
}
