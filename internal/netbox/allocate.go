package netbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"strconv"
	"strings"

	logf "sigs.k8s.io/controller-runtime/pkg/log"
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

	// PlaceRange carves one ipam.IPRange out of a prefix, and is **not** a NetBox URL.
	//
	// NetBox 4.6.8 has no `available-ranges` view. What it has is
	// `POST ipam/ip-ranges/{id}/available-ips/` -- an address out of a *range*, the opposite
	// operation -- so `ipam/api/urls.py` offers exactly three allocation paths and none of
	// them places a range. A range is therefore placed by arithmetic here and committed with
	// an ordinary `POST ipam/ip-ranges/`, which is why this constant sits beside the two real
	// sub-paths rather than pretending to be one: a claim kind still names which allocation
	// mechanism it relies on, and this one names the mechanism that has no lock.
	//
	// Its safety argument is different, not absent. `ipam.IPRange.clean()` rejects a range
	// that overlaps another range in the same VRF (`ipam/models/ip.py`), and every API write
	// runs it -- NetBox's ValidatedModelSerializer calls `full_clean()` before saving
	// (`netbox/api/serializers/base.py`). So the server is still the arbiter of a collision;
	// it just says so with a 400 after the fact instead of a lock before it.
	PlaceRange = "place-ip-range"
)

// IPRangeEndpoint is where a placed range is created.
//
// Named here rather than passed in, because Allocate is handed the *pool's* endpoint --
// `ipam/prefixes` -- and the allocated model is a different one. A claim descriptor declares
// the same path in its Endpoint (the read-after-write and the identity search use it), and a
// test asserts the two agree.
const IPRangeEndpoint = "ipam/ip-ranges"

// The placement inputs a range claim passes through the allocating payload.
//
// Prefixed with `@` so that they cannot collide with a NetBox field name and so that one
// arriving at NetBox is obvious rather than silently ignored -- which is what would happen to
// a key called `size`: `ipam.IPRange.size` is `editable=False` and derived in `save()`, so
// NetBox drops it from the write serializer without complaint. Both are removed from the body
// before it is sent, and placeRange refuses to send a body that still holds one.
const (
	// PlacementSize is how many consecutive addresses to reserve.
	PlacementSize = "@size"

	// PlacementAlignment is the Alignment to honour. Absent means AlignAny.
	PlacementAlignment = "@alignment"
)

// placementPrefix is what marks a key as a placement input rather than a NetBox field.
const placementPrefix = "@"

// maxPlacementAttempts is how many times one reconcile recomputes a placement that NetBox
// rejected as overlapping.
//
// A constant rather than a spec field: it is a property of how contended a NetBox is, which
// nobody writing a manifest knows, and every value anyone would set it to is between three and
// ten. Five attempts with jittered backoff clears the realistic case -- a burst of claims
// applied by one `kubectl apply` -- and anything past that is a pool under sustained
// contention, which is a different report (ContendedError) rather than a longer loop.
const maxPlacementAttempts = 5

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

	// The one sub-path that is not a NetBox URL, and the guard clause is the whole of the
	// dispatch: everything below is the locked path, which is every other claim kind.
	if sub == PlaceRange {
		return c.placeRange(ctx, endpoint, id, payload)
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

// placement is one range claim's request, read out of the allocating payload.
type placement struct {
	size  uint64
	align Alignment

	// body is the payload with the placement inputs removed: what actually gets POSTed, once
	// the two addresses are added to it.
	body Object
}

// placementRequest reads the placement inputs out of payload and returns the body without
// them.
//
// It refuses a payload that still carries an `@`-prefixed key afterwards. That check is not
// paranoia about this function: it is what makes adding a third placement input a compile-time
// and test-time failure rather than a key silently sent to NetBox, which for `@size` NetBox
// would accept and ignore.
func placementRequest(payload Object) (placement, error) {
	size, ok := asInt(payload[PlacementSize])
	if !ok || size <= 0 {
		return placement{}, fmt.Errorf(
			"placing an ip-range needs %s in the payload and it holds %v", PlacementSize, payload[PlacementSize])
	}

	body := make(Object, len(payload))

	for key, value := range payload {
		if strings.HasPrefix(key, placementPrefix) {
			continue
		}

		body[key] = value
	}

	align := Alignment(asString(payload[PlacementAlignment]))
	if align == "" {
		align = AlignAny
	}

	if align != AlignAny && align != AlignPowerOfTwo {
		return placement{}, fmt.Errorf("%s is %q, which is not an alignment", PlacementAlignment, align)
	}

	return placement{size: uint64(size), align: align, body: body}, nil
}

// placeRange carves one ipam.IPRange out of a prefix and creates it.
//
// The whole of the unlocked path. It reads the parent, computes a placement from the ranges
// that already exist in the parent's VRF, and commits it -- then, if NetBox says the placement
// overlaps something, recomputes from fresh state and tries again. Contention is therefore
// resolved by the server rather than by a client-side mutex, which is the only kind of answer
// that also holds when two clusters allocate out of one NetBox.
//
// Like Allocate itself it does not retry a transport failure or a 5xx: a POST that committed
// and lost its response would be repeated, and the second range is as real as the first. Only
// an overlap rejection is retried, and an overlap rejection is proof that nothing was created.
func (c *Client) placeRange(ctx context.Context, poolEndpoint string, poolID int, payload Object) (Object, error) {
	request, err := placementRequest(payload)
	if err != nil {
		return nil, err
	}

	parent, err := c.GetByID(ctx, poolEndpoint, poolID)
	if err != nil {
		return nil, fmt.Errorf("reading the parent prefix netbox %s/%d: %w", poolEndpoint, poolID, err)
	}

	if parent == nil {
		return nil, &NotFoundError{Endpoint: poolEndpoint, ID: poolID}
	}

	cidr, err := netip.ParsePrefix(asString(parent["prefix"]))
	if err != nil {
		return nil, fmt.Errorf("netbox %s/%d has prefix %q, which is not a network: %w",
			poolEndpoint, poolID, asString(parent["prefix"]), err)
	}

	// The parent's VRF, and it is load-bearing twice over. NetBox's overlap check is
	// `filter(vrf=self.vrf)`, so a range created in the global table is checked against the
	// global table's ranges and not against the VRF the parent prefix lives in -- placing it
	// against one set and having it validated against another is how two teams get the same
	// block. Unlike `available-prefixes`, which injects the parent's vrf itself
	// (`AvailablePrefixesView.prep_object_data`), a plain POST inherits nothing.
	vrf, inVRF := asInt(idOf(parent["vrf"]))

	for attempt := 1; ; attempt++ {
		placed, err := c.nextRange(ctx, cidr, vrf, inVRF, request)
		if err != nil {
			return nil, err
		}

		created, err := c.createRange(ctx, request, placed, cidr.Bits(), vrf, inVRF)
		if err == nil {
			return created, nil
		}

		if !overlapping(err) {
			return nil, err
		}

		logf.FromContext(ctx).V(1).Info("an ip-range placement was rejected as overlapping",
			"action", "allocate", "pool", cidr.String(), "size", request.size,
			"attempt", attempt, "err", err.Error())

		if attempt >= maxPlacementAttempts {
			return nil, &ContendedError{
				Endpoint: IPRangeEndpoint, Pool: cidr.String(),
				Attempts: attempt, Body: err.Error(),
			}
		}

		if err := sleep(ctx, c.jitter(attempt)); err != nil {
			return nil, err
		}
	}
}

// nextRange computes where the next range of the requested size goes.
func (c *Client) nextRange(
	ctx context.Context, cidr netip.Prefix, vrf int, inVRF bool, request placement,
) (Interval, error) {
	occupied, err := c.occupiedRanges(ctx, vrf, inVRF)
	if err != nil {
		return Interval{}, err
	}

	placed, ok := FirstGap(cidr, occupied, request.size, request.align)
	if ok {
		return placed, nil
	}

	// The same type the locked endpoints' 409 becomes, so exhaustion is one condition reason
	// and one requeue tier however it was discovered.
	return Interval{}, &ExhaustedError{
		Endpoint: IPRangeEndpoint,
		Body: fmt.Sprintf("%s has no run of %d consecutive addresses free of an existing ip-range"+
			" (alignment %s), so no range was created", cidr, request.size, request.align),
	}
}

// occupiedRanges are the ip-ranges that could occupy space in the parent, as intervals.
//
// Filtered by VRF and by nothing else, which is deliberate on both counts:
//
//   - **Not by `?parent=`.** NetBox's IPRangeFilterSet.parent matches only ranges whose start
//     *and* end are inside the prefix (`search_by_parent`, `net_host_contained` on both), so a
//     range straddling the boundary is invisible to it. A placement computed from a list that
//     cannot see it overlaps it on every attempt and reports contention forever.
//   - **Not by `?vrf_id__isnull=true`** for the global table. `vrf_id` is a
//     ModelMultipleChoiceFilter, and NetBox generates only the `__n` negation for those
//     (FILTER_NEGATION_LOOKUP_MAP, `netbox/netbox/filtersets.py`); django-filter ignores an
//     unregistered parameter, so the pin would silently match every VRF. The global table is
//     therefore selected here, from the rows.
//
// The result is bounded by how many ip-ranges exist, never by how many addresses they hold,
// which is what makes an IPv6 /64 parent as cheap as a /24.
func (c *Client) occupiedRanges(ctx context.Context, vrf int, inVRF bool) ([]Interval, error) {
	params := Params{}
	if inVRF {
		params.Match("vrf_id", LookupExact, strconv.Itoa(vrf))
	}

	rows, err := c.List(ctx, IPRangeEndpoint, params)
	if err != nil {
		return nil, fmt.Errorf("listing the existing ip-ranges: %w", err)
	}

	occupied := make([]Interval, 0, len(rows))

	for _, row := range rows {
		if _, rowInVRF := asInt(idOf(row["vrf"])); rowInVRF != inVRF {
			continue
		}

		lo, okLo := hostOf(asString(row["start_address"]))
		hi, okHi := hostOf(asString(row["end_address"]))

		if !okLo || !okHi {
			// Loud rather than skipped. A row whose endpoints cannot be read is a row whose
			// space cannot be avoided, and treating it as free is how a placement lands on
			// top of it.
			id, _ := row.ID()

			return nil, fmt.Errorf("netbox %s/%d has start_address=%q end_address=%q,"+
				" which are not addresses, so the free space could not be computed",
				IPRangeEndpoint, id, row["start_address"], row["end_address"])
		}

		occupied = append(occupied, Interval{Lo: lo, Hi: hi})
	}

	return occupied, nil
}

// createRange commits one placement.
//
// `size` is never sent. `ipam.IPRange.size` is `editable=False` and computed in `save()` as
// `end - start + 1`, so it is not in the write serializer at all: sending it would be silently
// dropped, and a payload whose value NetBox ignores is a payload that lies to whoever reads it.
// The response is checked against the request instead, which is the same fact asserted where it
// is true.
func (c *Client) createRange(
	ctx context.Context, request placement, placed Interval, bits, vrf int, inVRF bool,
) (Object, error) {
	body := make(Object, len(request.body)+3)
	for key, value := range request.body {
		body[key] = value
	}

	// With the parent's mask, because IPRange.clean() requires the two endpoints to carry the
	// same prefix length and NetBox's own UI writes the containing prefix's.
	body["start_address"] = fmt.Sprintf("%s/%d", placed.Lo, bits)
	body["end_address"] = fmt.Sprintf("%s/%d", placed.Hi, bits)

	if inVRF {
		body["vrf"] = vrf
	}

	created, err := c.attempt(ctx, http.MethodPost,
		c.endpointURL(IPRangeEndpoint), IPRangeEndpoint+"/"+PlaceRange, body)
	if err != nil {
		return nil, err
	}

	if size, ok := asInt(created["size"]); !ok || uint64(size) != request.size {
		return nil, fmt.Errorf("netbox created %s with size %v, not the %d requested",
			IPRangeEndpoint, created["size"], request.size)
	}

	return created, nil
}

// overlapping reports whether err is NetBox refusing a range because it overlaps another.
//
// Matching on wording, and confined to this one function for the reason isProtected gives:
// `IPRange.clean()` raises a plain ValidationError, so a 400 is all the API can say and the
// only thing distinguishing "this placement collided" from "this payload is wrong" is the
// sentence. Getting it wrong in the safe direction -- failing to recognise an overlap -- reports
// Invalid instead of retrying, which is visible; the other direction retries a payload that
// cannot succeed until maxPlacementAttempts runs out, which is also visible. Neither creates
// anything.
func overlapping(err error) bool {
	var invalid *ValidationError
	if !errors.As(err, &invalid) {
		return false
	}

	// On raw rather than Body: Body is a summary now (#298), and a summary that dropped
	// the sentence would silently stop recognising contention and report Invalid instead.
	return strings.Contains(strings.ToLower(invalid.raw), "overlap")
}

// hostOf reads the host part of a NetBox address column, which carries a mask
// (`10.0.30.128/24`) but is one address.
func hostOf(value string) (netip.Addr, bool) {
	if prefix, err := netip.ParsePrefix(value); err == nil {
		return prefix.Addr().Unmap(), true
	}

	addr, err := netip.ParseAddr(value)
	if err != nil {
		return netip.Addr{}, false
	}

	return addr.Unmap(), true
}

// idOf reads a foreign key out of either shape NetBox uses: the bare id a payload writes, or
// the nested object a read returns.
func idOf(value any) any {
	if nested, ok := value.(map[string]any); ok {
		return nested["id"]
	}

	return value
}
