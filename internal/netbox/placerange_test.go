package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// rangeStub is a NetBox that answers the three requests a range placement makes: the parent
// prefix, the existing ranges, and the create.
type rangeStub struct {
	mu sync.Mutex

	// parent is the object returned for the parent prefix.
	parent Object

	// existing are the rows `GET ipam/ip-ranges/` answers with. Replaced by the create
	// handler when overlapUntil is in play, which is what makes a recomputation see fresh
	// state.
	existing []Object

	// overlapUntil rejects the first n creates as overlapping, which is what NetBox does to
	// the loser of a race.
	overlapUntil int

	// creates are the bodies POSTed to ipam/ip-ranges, in order.
	creates []Object

	// paths is every path requested with its query, in order, so a test can assert what was
	// asked and what was not.
	paths []string
}

func (s *rangeStub) server(t *testing.T) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()

		path := r.Method + " " + r.URL.Path
		if r.URL.RawQuery != "" {
			path += "?" + r.URL.RawQuery
		}

		s.paths = append(s.paths, path)

		switch {
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/ipam/prefixes/"):
			respond(t, w, http.StatusOK, s.parent)
		case r.Method == http.MethodGet && r.URL.Path == "/api/ipam/ip-ranges/":
			respond(t, w, http.StatusOK, Object{"results": asAny(s.existing)})
		case r.Method == http.MethodPost && r.URL.Path == "/api/ipam/ip-ranges/":
			s.create(t, w, r)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// create is NetBox's own behaviour, narrowed to what matters here: it derives `size` from the
// two endpoints, and it refuses an overlap with a 400 whose body names one.
func (s *rangeStub) create(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()

	body := Object{}
	raw, _ := io.ReadAll(r.Body)

	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("the create body is not JSON: %v", err)
	}

	s.creates = append(s.creates, body)

	if len(s.creates) <= s.overlapUntil {
		// And the space really is taken from now on, so a recomputation moves.
		s.existing = append(s.existing, Object{
			"id": float64(900 + len(s.creates)), "start_address": body["start_address"],
			"end_address": body["end_address"],
		})

		respond(t, w, http.StatusBadRequest, Object{
			"non_field_errors": []any{"Defined addresses overlap with range 10.0.30.0-10.0.30.9 in VRF None"},
		})

		return
	}

	start, _ := hostOf(asString(body["start_address"]))
	end, _ := hostOf(asString(body["end_address"]))

	respond(t, w, http.StatusCreated, Object{
		"id": float64(77), "start_address": body["start_address"], "end_address": body["end_address"],
		"size": float64(Interval{Lo: start, Hi: end}.Size()),
	})
}

func respond(t *testing.T, w http.ResponseWriter, status int, body Object) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	if err := json.NewEncoder(w).Encode(body); err != nil {
		t.Fatalf("encoding the stub response: %v", err)
	}
}

func asAny(objects []Object) []any {
	out := make([]any, 0, len(objects))
	for _, obj := range objects {
		out = append(out, map[string]any(obj))
	}

	return out
}

// placeOne runs one placement against a stub and returns what it created.
func placeOne(t *testing.T, stub *rangeStub, size int, align Alignment) (Object, error) {
	t.Helper()

	srv := stub.server(t)
	defer srv.Close()

	client := newTestClient(t, srv, func(cfg *Config) { cfg.BaseDelay = time.Millisecond })

	payload := Object{PlacementSize: float64(size), "custom_fields": map[string]any{"k8s_allocation_identity": "abc"}}
	if align != "" {
		payload[PlacementAlignment] = string(align)
	}

	return client.Allocate(context.Background(), "ipam/prefixes", 11, PlaceRange, payload)
}

// TestPlaceRangeCreatesTheFirstGap is the happy path, and the assertions are about the body.
//
// `size` is never sent: ipam.IPRange.size is editable=False and derived in save(), so a `size`
// in the payload is dropped in silence -- and a payload NetBox ignores is a payload that lies
// to whoever reads it back. The two endpoints carry the parent's mask, which IPRange.clean()
// requires to match.
func TestPlaceRangeCreatesTheFirstGap(t *testing.T) {
	stub := &rangeStub{
		parent: Object{"id": float64(11), "prefix": "10.0.30.0/24"},
		existing: []Object{
			{"id": float64(1), "start_address": "10.0.30.0/24", "end_address": "10.0.30.12/24"},
		},
	}

	created, err := placeOne(t, stub, 64, AlignPowerOfTwo)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if got := asString(created["start_address"]); got != "10.0.30.64/24" {
		t.Errorf("created start_address = %q, want 10.0.30.64/24 (aligned to the block)", got)
	}

	if got := asString(created["end_address"]); got != "10.0.30.127/24" {
		t.Errorf("created end_address = %q, want 10.0.30.127/24", got)
	}

	body := stub.creates[0]

	for _, forbidden := range []string{"size", PlacementSize, PlacementAlignment} {
		if _, sent := body[forbidden]; sent {
			t.Errorf("the create body carried %q, which netbox derives or does not know", forbidden)
		}
	}

	if _, stamped := body["custom_fields"]; !stamped {
		t.Error("the create body dropped custom_fields; the allocation identity rides on this call")
	}
}

// TestPlaceRangeNeverEnumeratesAddresses is the property that makes an IPv6 parent affordable.
//
// The occupied set is the other ranges, never the addresses inside them. A /64 has 2^64
// addresses and 0 or 1 ranges, and only one of those two numbers can be asked about -- so
// asking `available-ips` or `ipam/ip-addresses` anything at all is the bug, not a slow path.
func TestPlaceRangeNeverEnumeratesAddresses(t *testing.T) {
	stub := &rangeStub{parent: Object{"id": float64(11), "prefix": "fd00:10::/64"}}

	created, err := placeOne(t, stub, 4096, AlignAny)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if got := asString(created["start_address"]); got != "fd00:10::/64" {
		t.Errorf("created start_address = %q, want fd00:10::/64", got)
	}

	for _, path := range stub.paths {
		if strings.Contains(path, "available-ips") || strings.Contains(path, "ip-addresses") {
			t.Errorf("a range placement asked %q; placement is arithmetic, never enumeration", path)
		}
	}
}

// TestPlaceRangeRecomputesAfterAnOverlap is the concurrency argument, exercised.
//
// NetBox rejects the first attempt because somebody else took the gap between the read and the
// write. That rejection is proof nothing was created, which is the only reason retrying it is
// safe -- and the retry recomputes from fresh state rather than resending the same body.
func TestPlaceRangeRecomputesAfterAnOverlap(t *testing.T) {
	stub := &rangeStub{
		parent:       Object{"id": float64(11), "prefix": "10.0.30.0/24"},
		overlapUntil: 2,
	}

	created, err := placeOne(t, stub, 8, AlignAny)
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if len(stub.creates) != 3 {
		t.Fatalf("%d creates, want 3: two rejected and one accepted", len(stub.creates))
	}

	first, third := asString(stub.creates[0]["start_address"]), asString(stub.creates[2]["start_address"])
	if first == third {
		t.Errorf("the third attempt resent %q; a rejected placement must be recomputed", first)
	}

	if got := asString(created["start_address"]); got != "10.0.30.16/24" {
		t.Errorf("created start_address = %q, want 10.0.30.16/24 after two losses", got)
	}
}

// TestPlaceRangeReportsContentionApartFromExhaustion is the distinction the whole kind rests
// on.
//
// Exhausted means the pool is full and only widening it helps. Contended means the space is
// there and somebody else keeps getting there first. Reporting one as the other sends a human
// looking for space that exists.
func TestPlaceRangeReportsContentionApartFromExhaustion(t *testing.T) {
	stub := &rangeStub{
		parent:       Object{"id": float64(11), "prefix": "10.0.30.0/24"},
		overlapUntil: maxPlacementAttempts,
	}

	_, err := placeOne(t, stub, 8, AlignAny)

	var contended *ContendedError
	if !errors.As(err, &contended) {
		t.Fatalf("err = %T (%v), want *ContendedError", err, err)
	}

	if contended.Attempts != maxPlacementAttempts {
		t.Errorf("reported %d attempts, want %d", contended.Attempts, maxPlacementAttempts)
	}

	if len(stub.creates) != maxPlacementAttempts {
		t.Errorf("%d creates, want %d: one per attempt and no more",
			len(stub.creates), maxPlacementAttempts)
	}

	var exhausted *ExhaustedError
	if errors.As(err, &exhausted) {
		t.Error("contention was reported as exhaustion; the two have different fixes")
	}
}

// TestPlaceRangeExhaustsWithoutCreating is the other half: no gap means no POST at all.
func TestPlaceRangeExhaustsWithoutCreating(t *testing.T) {
	stub := &rangeStub{
		parent: Object{"id": float64(11), "prefix": "10.0.30.0/24"},
		existing: []Object{
			{"id": float64(1), "start_address": "10.0.30.0/24", "end_address": "10.0.30.255/24"},
		},
	}

	_, err := placeOne(t, stub, 8, AlignAny)

	var exhausted *ExhaustedError
	if !errors.As(err, &exhausted) {
		t.Fatalf("err = %T (%v), want *ExhaustedError", err, err)
	}

	if len(stub.creates) != 0 {
		t.Errorf("%d creates against a full parent, want 0", len(stub.creates))
	}
}

// TestPlaceRangeSendsTheParentsVRF is load-bearing rather than tidy.
//
// A plain POST inherits nothing -- unlike available-prefixes, which injects the parent's vrf
// itself -- and NetBox's overlap check is `filter(vrf=self.vrf)`. A range placed against the
// ranges of one VRF and validated against another is how two teams get the same block.
func TestPlaceRangeSendsTheParentsVRF(t *testing.T) {
	stub := &rangeStub{
		parent: Object{
			"id": float64(11), "prefix": "10.0.30.0/24",
			"vrf": map[string]any{"id": float64(4), "name": "vrf-home"},
		},
	}

	if _, err := placeOne(t, stub, 8, AlignAny); err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if got, _ := asInt(stub.creates[0]["vrf"]); got != 4 {
		t.Errorf("the create body carried vrf=%v, want the parent's 4", stub.creates[0]["vrf"])
	}

	// And the occupancy query was scoped to the same VRF, because the same block may
	// legitimately be a range in every VRF and treating another VRF's range as an obstacle
	// refuses space that is free.
	var listed string

	for _, path := range stub.paths {
		if strings.HasPrefix(path, "GET /api/ipam/ip-ranges/") {
			listed = path
		}
	}

	if !strings.Contains(listed, "vrf_id=4") {
		t.Errorf("the occupancy query was %q, want vrf_id=4 in it", listed)
	}

	// And no `parent=` filter: NetBox's IPRangeFilterSet.parent needs *both* endpoints inside
	// the prefix, so a range straddling the boundary is invisible to it -- and a placement
	// computed from a list that cannot see it overlaps forever.
	if strings.Contains(listed, "parent=") {
		t.Errorf("the occupancy query was %q; a `parent=` filter cannot see a straddling range", listed)
	}
}

// TestPlaceRangeRefusesAPayloadWithoutASize is the guard on the mechanism itself: the
// placement inputs are passed through the payload, so a descriptor that forgot to declare them
// must fail loudly rather than place a zero-length range.
func TestPlaceRangeRefusesAPayloadWithoutASize(t *testing.T) {
	stub := &rangeStub{parent: Object{"id": float64(11), "prefix": "10.0.30.0/24"}}
	srv := stub.server(t)

	defer srv.Close()

	client := newTestClient(t, srv, nil)

	if _, err := client.Allocate(
		context.Background(), "ipam/prefixes", 11, PlaceRange, Object{}); err == nil {
		t.Fatal("a payload with no @size was accepted")
	}

	if len(stub.paths) != 0 {
		t.Errorf("made %v before noticing there was no size", stub.paths)
	}
}
