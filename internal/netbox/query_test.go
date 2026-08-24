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
//
// It is also deliberately *stricter* than NetBox on one axis: a parameter whose lookup
// NetBox does not register gets a 400 rather than being ignored. Real NetBox ignores it and
// answers with the unfiltered set (#206), which is why a fake that answers whatever it is
// asked let every null pin go out misspelled for as long as it did.
type fakeNetBox struct{ objects []Object }

func (f *fakeNetBox) server(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if param, ok := unregisteredParam(r.URL.Query()); !ok {
			http.Error(w, `{"detail":"unknown filter `+param+`"}`, http.StatusBadRequest)

			return
		}
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
//
// Both null spellings are here because NetBox has both: the `null` sentinel is a *value* on
// a bare filter (a model-choice or char column) and `__empty` is a suffix (a numeric one).
// A consequence the real thing shares: there is no way to match the literal string `null`
// on a char column, because django-filter maps that value to a NULL predicate before the
// column ever sees it.
func matchesParam(obj Object, param, want string) bool {
	filter, lookup, _ := strings.Cut(param, "__")
	live, present := fieldValue(obj, filter)

	switch Lookup(lookup) {
	case emptyLookup:
		return present != (want == emptyValue)
	case LookupIExact:
		return present && strings.EqualFold(live, want)
	default:
		if want == nullSentinel {
			return !present
		}

		return present && live == want
	}
}

// unregisteredParam returns the first query parameter NetBox would not recognise, and
// whether the query is clean. The fakes 400 on one instead of ignoring it -- see
// netbox.LookupRegistered for why that is the check worth having.
func unregisteredParam(query url.Values) (string, bool) {
	for param := range query {
		if !LookupRegistered(param) {
			return param, false
		}
	}

	return "", true
}

// fieldValue resolves one filter name against a stored object: `vrf_id` reads the id out
// of the nested `vrf`, anything else is the column itself. The second result is false
// when the object holds no value there, which is what a null pin asks about.
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

// TestNullPinsAFilterRatherThanOmittingIt covers the other half of the same gap.
// ipam.IPAddress has no `meta.constraints` (docs/netbox-schema.md -> ipam.IPAddress), so
// the identical address exists once globally and once per VRF and `vrf_id` is always
// either matched or pinned null.
func TestNullPinsAFilterRatherThanOmittingIt(t *testing.T) {
	fake := &fakeNetBox{objects: []Object{
		{"id": 1, "address": "10.0.0.1/32"},
		{"id": 2, "address": "10.0.0.1/32", "vrf": Object{"id": 7, "name": "prod"}},
	}}
	client := newTestClient(t, fake.server(t), nil)
	ctx := context.Background()

	global, err := client.GetOne(ctx, "ipam/ip-addresses",
		Params{"address": "10.0.0.1/32"}.Null("vrf_id", NullColumnRef))
	if err != nil {
		t.Fatalf("GetOne with vrf_id=null: %v", err)
	}
	if global == nil {
		t.Fatal("vrf_id=null matched nothing; the global address is there")
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
		"null pin on a ref":  {Params{}.Null("vrf_id", NullColumnRef), "vrf_id=null"},
		"plain map is valid": {Params{"slug": "home"}, "slug=home"},
		"combined": {
			Params{}.Match("name", LookupIExact, "dns").Match("site_id", LookupExact, "3").
				Null("tenant_id", NullColumnRef),
			"name__ie=dns&site_id=3&tenant_id=null",
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

// TestNullPinSpellingPerColumnType pins the exact query string a null pin puts on the wire
// for each column class, because that is the whole of #206: the operator sent
// `?<filter>__isnull=true` for every one of them and NetBox 4.6.8 registers no such
// parameter, so django-filter dropped the filter and answered with the *unfiltered* set.
//
// Every expected string below traces to NetBox 4.6.8 source:
//
//   - `?parent_id=null` -- a foreign key gets FILTER_NEGATION_LOOKUP_MAP
//     (netbox/utilities/constants.py:29, selected at netbox/netbox/filtersets.py:164-170),
//     which registers `n` and nothing else, so there is no suffix to send. `null` is
//     FILTERS_NULL_CHOICE_VALUE (netbox/netbox/settings.py:771) and django-filter turns it
//     into a NULL predicate in MultipleChoiceFilter.filter
//     (django_filters/filters.py:262-264).
//   - `?rd=null` -- a char column *does* register `empty`
//     (FILTER_CHAR_BASED_LOOKUP_MAP, netbox/utilities/constants.py:15) but it maps to the
//     string-length lookup `CAST(LENGTH(col) AS BOOLEAN) IS NOT TRUE`
//     (netbox/extras/lookups.py:69-73), which also matches `”`. The sentinel is exact.
//   - `?scope_id__empty=true` -- a numeric column maps `empty` to the ORM's `isnull`
//     (FILTER_NUMERIC_BASED_LOOKUP_MAP, netbox/utilities/constants.py:26) and cannot take
//     the sentinel, because its form field casts every value with the real field class
//     (netbox/utilities/filters.py:39-46). `true` is what the resulting BooleanFilter's
//     forms.NullBooleanField accepts (django/forms/fields.py:838-852).
func TestNullPinSpellingPerColumnType(t *testing.T) {
	tests := map[string]struct {
		params Params
		want   string
	}{
		"foreign key takes the null sentinel":       {Params{}.Null("parent_id", NullColumnRef), "parent_id=null"},
		"char column takes the null sentinel too":   {Params{}.Null("rd", NullColumnChar), "rd=null"},
		"numeric column takes the empty lookup":     {Params{}.Null("scope_id", NullColumnNumeric), "scope_id__empty=true"},
		"a pin never emits the isnull ORM lookup":   {Params{}.Null("vrf_id", NullColumnRef), "vrf_id=null"},
		"an undeclared class fails in the loud way": {Params{}.Null("scope_id", NullColumn("")), "scope_id=null"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			got := encodeParams(tc.params)
			if got != tc.want {
				t.Errorf("encodeParams = %q, want %q", got, tc.want)
			}
			if strings.Contains(got, "__isnull") {
				t.Errorf("%q carries __isnull, which NetBox registers nowhere", got)
			}
		})
	}
}

// TestUnregisteredLookupIsRejected is the guard that would have caught #206 on the first
// run. NetBox itself does not reject an unknown parameter -- it ignores it and returns
// everything -- so the fakes have to, or a misspelled filter reads as a successful lookup
// that simply matched more than it should.
func TestUnregisteredLookupIsRejected(t *testing.T) {
	fake := &fakeNetBox{objects: []Object{{"id": 1, "name": "emea"}}}
	client := newTestClient(t, fake.server(t), nil)

	_, err := client.GetOne(context.Background(), "dcim/regions",
		Params{"name": "emea", "parent_id__isnull": "true"})
	if err == nil {
		t.Fatal("GetOne with parent_id__isnull succeeded; the fake answered a filter NetBox drops")
	}
}

func TestLookupRegistered(t *testing.T) {
	tests := map[string]bool{
		// No suffix at all: a plain filter, or a null sentinel riding on one.
		"name":               true,
		"parent_id":          true,
		"limit":              true,
		"assigned_object_id": true,
		// Suffixes NetBox's lookup maps name (netbox/utilities/constants.py:5, :20, :29, :33).
		"name__ie":        true,
		"name__ic":        true,
		"vid__gte":        true,
		"group_id__n":     true,
		"scope_id__empty": true,
		"tag__any":        true,
		// The ORM lookup `empty` maps to, which is not a parameter -- #206.
		"parent_id__isnull": false,
		"rd__isnull":        false,
		// Plausible near-misses.
		"name__iexact":   false,
		"vrf_id__null":   false,
		"name__contains": false,
	}
	for param, want := range tests {
		if got := LookupRegistered(param); got != want {
			t.Errorf("LookupRegistered(%q) = %v, want %v", param, got, want)
		}
	}
}
