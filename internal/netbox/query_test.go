package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// fakeNetBox answers a list request out of a fixed set of objects, applying the lookup
// modifiers the client puts on the wire.
//
// It stores names exactly as given and treats `__ie` as the only case-insensitive
// comparison. Both halves are the point of the fake: one that lowercased on the way in
// would answer `?name=dns` with the object stored as `DNS`, and the duplicate-creation
// bug these tests exist to catch would be invisible.
type fakeNetBox struct{ objects []Object }

func (f *fakeNetBox) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		matched := make([]Object, 0, len(f.objects))
		for _, obj := range f.objects {
			if matchesQuery(obj, r.URL.Query()) {
				matched = append(matched, obj)
			}
		}
		body, err := json.Marshal(map[string]any{"count": len(matched), "results": matched})
		if err != nil {
			t.Errorf("marshalling the fake response: %v", err)

			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)

	return srv
}

func matchesQuery(obj Object, query url.Values) bool {
	for param, values := range query {
		if param == "limit" {
			continue
		}
		if !matchesParam(obj, param, values[0]) {
			return false
		}
	}

	return true
}

// matchesParam applies one query parameter, splitting the lookup modifier off the
// parameter name the way NetBox's filterset does.
func matchesParam(obj Object, param, want string) bool {
	filter, lookup, _ := strings.Cut(param, "__")
	live, present := fieldValue(obj, filter)

	switch Lookup(lookup) {
	case isNullLookup:
		wantNull := want == isNullValue

		return present != wantNull
	case LookupIExact:
		return present && strings.EqualFold(live, want)
	default:
		return present && live == want
	}
}

// fieldValue resolves one filter name against a stored object: `vrf_id` reads the id out
// of the nested `vrf`, anything else is the column itself. The second result is false
// when the object holds no value there, which is what `__isnull` asks about.
func fieldValue(obj Object, filter string) (string, bool) {
	column, isID := strings.CutSuffix(filter, "_id")
	if !isID {
		value, ok := obj[filter]

		return asString(value), ok && value != nil
	}

	nested, ok := obj[column].(Object)
	if !ok {
		return "", false
	}
	id, ok := asInt(nested["id"])

	return strconv.Itoa(id), ok
}

// TestIExactLookupFindsAnObjectStoredInAnotherCase is the regression test for the gap
// NBO-069 found on the client side. dcim.Device is unique on `Lower('name')`
// (docs/netbox-schema.md -> dcim.Device.meta.constraints), so a CR named `dns` and a
// device stored as `DNS` are one object as far as NetBox is concerned. Looked up exactly,
// the client reports nothing, the engine creates a second device, and NetBox either
// rejects the write or accepts it under a different case.
func TestIExactLookupFindsAnObjectStoredInAnotherCase(t *testing.T) {
	fake := &fakeNetBox{objects: []Object{{"id": 11, "name": "DNS"}}}
	client := newTestClient(t, fake.server(t), nil)
	ctx := context.Background()

	found, err := client.GetOne(ctx, "dcim/devices", Params{}.Match("name", LookupIExact, "dns"))
	if err != nil {
		t.Fatalf("GetOne with an __ie lookup: %v", err)
	}
	if found == nil {
		t.Fatal("__ie lookup for `dns` did not find the device stored as `DNS`; the engine would create a second one")
	}
	if id, _ := found.ID(); id != 11 {
		t.Errorf("matched id %d, want 11", id)
	}

	// The exact lookup is the failure mode, asserted against the same fake so the two
	// cannot drift apart: if this one starts matching, the fake has begun folding case
	// and the test above no longer proves anything.
	missed, err := client.GetOne(ctx, "dcim/devices", Params{}.Match("name", LookupExact, "dns"))
	if err != nil {
		t.Fatalf("GetOne with an exact lookup: %v", err)
	}
	if missed != nil {
		t.Errorf("exact lookup for `dns` matched %v; the fake is not storing names case-sensitively", missed)
	}
}

// TestIsNullPinsAFilterRatherThanOmittingIt covers the other half of the same gap.
// ipam.IPAddress has no `meta.constraints` (docs/netbox-schema.md -> ipam.IPAddress), so
// the identical address exists once globally and once per VRF and `vrf_id` is always
// either matched or pinned null.
func TestIsNullPinsAFilterRatherThanOmittingIt(t *testing.T) {
	fake := &fakeNetBox{objects: []Object{
		{"id": 1, "address": "10.0.0.1/32"},
		{"id": 2, "address": "10.0.0.1/32", "vrf": Object{"id": 7, "name": "prod"}},
	}}
	client := newTestClient(t, fake.server(t), nil)
	ctx := context.Background()

	global, err := client.GetOne(ctx, "ipam/ip-addresses",
		Params{"address": "10.0.0.1/32"}.Null("vrf_id"))
	if err != nil {
		t.Fatalf("GetOne with vrf_id__isnull: %v", err)
	}
	if global == nil {
		t.Fatal("vrf_id__isnull=true matched nothing; the global address is there")
	}
	if id, _ := global.ID(); id != 1 {
		t.Errorf("matched id %d, want 1 -- the null pin adopted the address out of VRF prod", id)
	}

	inVRF, err := client.GetOne(ctx, "ipam/ip-addresses",
		Params{"address": "10.0.0.1/32"}.Match("vrf_id", LookupExact, "7"))
	if err != nil {
		t.Fatalf("GetOne with vrf_id: %v", err)
	}
	if id, _ := inVRF.ID(); id != 2 {
		t.Errorf("matched id %d, want 2", id)
	}

	// Omitting the filter is what a missing null pin looks like from NetBox's side: both
	// addresses match, and taking either one is a guess.
	_, err = client.GetOne(ctx, "ipam/ip-addresses", Params{"address": "10.0.0.1/32"})
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("omitting vrf_id gave %v, want *AmbiguousError", err)
	}
}

func TestParamsRenderLookupModifiers(t *testing.T) {
	tests := map[string]struct {
		params Params
		want   string
	}{
		"exact":              {Params{}.Match("name", LookupExact, "dns"), "name=dns"},
		"case-insensitive":   {Params{}.Match("name", LookupIExact, "dns"), "name__ie=dns"},
		"null pin":           {Params{}.Null("vrf_id"), "vrf_id__isnull=true"},
		"plain map is valid": {Params{"slug": "home"}, "slug=home"},
		"combined": {
			Params{}.Match("name", LookupIExact, "dns").Match("site_id", LookupExact, "3").Null("tenant_id"),
			"name__ie=dns&site_id=3&tenant_id__isnull=true",
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			if got := encodeParams(tc.params); got != tc.want {
				t.Errorf("encodeParams = %q, want %q", got, tc.want)
			}
		})
	}
}
