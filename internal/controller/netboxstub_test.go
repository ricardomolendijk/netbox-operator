package controller

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/provenance"
)

// stubWrite is one mutating request the stub received.
type stubWrite struct {
	Method string
	// Endpoint is set only for a provenance write, where two endpoints share one slice and
	// a test has to tell a tag POST from a custom-field POST.
	Endpoint string
	ID       int64
	Payload  netbox.Object
}

// netboxStubServer is a NetBox for one endpoint, generic over the kind.
//
// It exists because the alternative -- a hand-written stub per kind -- makes the real cost
// of adding a kind about 250 lines of test scaffolding rather than the three small files
// CONTRIBUTING.md advertises. The engine is generic; its tests should be too, or the
// extensibility claim is only true of the production code.
//
// `key` is the natural-key field the kind is looked up by, which is all the stub needs to
// know about the kind: every other column it stores as it was sent, and reads back in the
// shape NetBox would.
type netboxStubServer struct {
	t        *testing.T
	endpoint string
	key      string
	// url is the stub's own base URL, kept because every object carries a self `url` and
	// that column is where status.url comes from.
	url string

	// refKeys are the extra `<column>_id` filters this stub honours; see stubKind.refKeys.
	refKeys []string

	mu      sync.Mutex
	objects map[int64]netbox.Object
	writes  []stubWrite
	nextID  int64

	// extrasWrites is kept apart from writes so that recorded() still means "what the
	// engine did to the kind under test". A provenance bootstrap runs once per endpoint and
	// would otherwise show up in every write count in this package.
	extrasWrites []stubWrite

	// createStatus, when set, is returned for a POST instead of creating. For asserting
	// what the engine does with a 400 or a 409.
	createStatus int

	// extras holds the provenance definitions -- extras.Tag and extras.CustomField -- when
	// a test has switched them on with withProvenance. Nil otherwise, and that is what
	// keeps this stub honest for every other test: an endpoint with no spec.managedBy must
	// not touch these paths at all, so a request to one 404s rather than being quietly
	// served.
	extras map[string][]netbox.Object

	// extrasStatus, when set, is returned for a POST to an extras path. A NetBox token
	// without extras.add_customfield is the case it stands for.
	extrasStatus int

	// protected is the set of object ids whose DELETE is refused with the 409 Django
	// raises for a protected foreign key. The stub models no foreign keys of its own, so a
	// test that needs "netbox refuses this delete while a dependent exists" stages it --
	// exactly as `cascade` stages `on_delete=CASCADE`.
	//
	// A set of ids rather than a flag, because the case worth testing is a *chain*: one
	// object refusing while another does not is what makes a teardown ordered, and a flag
	// could only make every delete refuse at once.
	protected map[int64]bool
}

// provenanceEndpoints are the two paths the provenance bootstrap talks to.
var provenanceEndpoints = []string{"extras/tags", "extras/custom-fields"}

// stubKind is everything the stub needs to know about a kind: the endpoint it serves and
// the natural-key field objects are looked up by. Each kind declares its own next to that
// kind's tests.
//
// One value rather than two parameters because unparam is right that `key` is the same
// string at every call site -- both kinds registered so far are keyed by `slug` -- and that
// is a fact about those two kinds rather than about the stub, which is parameterised for the
// first kind whose identity is not a slug.
type stubKind struct {
	endpoint string
	key      string

	// refKeys are extra `<column>_id` filters the stub matches exactly, against the
	// un-suffixed column the payload actually writes -- `?interface_a_id=9` against
	// `interface_a: 9`, which is how NetBox spells the same pair on the way in and on the
	// way out.
	//
	// Opt-in and empty for every kind whose identity is one field, so the default behaviour
	// of stubMatches does not move. It exists for wireless.WirelessLink, the first kind
	// whose natural key is *two* references and therefore the first for which matching one
	// field is matching the wrong row.
	refKeys []string
}

// newNetBoxStub returns a running stub and its URL.
func newNetBoxStub(t *testing.T, kind stubKind) (*netboxStubServer, string) {
	t.Helper()

	s := &netboxStubServer{
		t: t, endpoint: kind.endpoint, key: kind.key, refKeys: kind.refKeys,
		objects: map[int64]netbox.Object{}, nextID: 100,
	}

	srv := httptest.NewServer(http.HandlerFunc(s.route))
	t.Cleanup(srv.Close)
	s.url = srv.URL

	return s, srv.URL
}

// route dispatches on the path. Only two shapes exist: the collection and one object.
func (s *netboxStubServer) route(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/api/status/") {
		writeStubJSON(w, http.StatusOK, netbox.Object{
			"netbox-version": "4.6.8", "plugins": map[string]any{},
		})

		return
	}

	if s.routeExtras(w, r) {
		return
	}

	tail := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/"+s.endpoint), "/")
	if tail == "" {
		s.collection(w, r)

		return
	}

	id, err := strconv.ParseInt(tail, 10, 64)
	if err != nil {
		writeStubJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}
	s.object(w, r, id)
}

// withProvenance switches on the extras endpoints the provenance bootstrap needs.
func (s *netboxStubServer) withProvenance() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.extras = map[string][]netbox.Object{}
}

// routeExtras serves the provenance definitions, and reports whether it handled the
// request.
//
// A store that is switched off handles nothing, so the path falls through to the kind's own
// routing and 404s -- which is how a test proves an endpoint with no spec.managedBy never
// asks. It also never claims the kind's own endpoint: when the kind under test *is*
// extras.Tag, its store already is the tag store, and two stores fighting over one path
// would make a test pass or fail on which check ran first.
func (s *netboxStubServer) routeExtras(w http.ResponseWriter, r *http.Request) bool {
	s.mu.Lock()
	enabled := s.extras != nil
	s.mu.Unlock()

	if !enabled {
		return false
	}

	for _, endpoint := range provenanceEndpoints {
		prefix := "/api/" + endpoint + "/"
		if endpoint == s.endpoint || !strings.HasPrefix(r.URL.Path, prefix) {
			continue
		}

		s.extrasRequest(w, r, endpoint, strings.Trim(strings.TrimPrefix(r.URL.Path, prefix), "/"))

		return true
	}

	return false
}

func (s *netboxStubServer) extrasRequest(w http.ResponseWriter, r *http.Request, endpoint, tail string) {
	if r.Method == http.MethodGet {
		s.extrasList(w, r, endpoint)

		return
	}

	if r.Method == http.MethodPost {
		s.extrasCreate(w, r, endpoint)

		return
	}

	s.extrasPatch(w, r, endpoint, tail)
}

func (s *netboxStubServer) extrasList(w http.ResponseWriter, r *http.Request, endpoint string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := r.URL.Query()
	results := []netbox.Object{}

	for _, obj := range s.extras[endpoint] {
		if extrasMatches(obj, query) {
			results = append(results, obj)
		}
	}

	writeStubJSON(w, http.StatusOK, netbox.Object{"count": len(results), "results": results, "next": nil})
}

func (s *netboxStubServer) extrasCreate(w http.ResponseWriter, r *http.Request, endpoint string) {
	payload, ok := decodeStub(w, r)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.extrasWrites = append(s.extrasWrites, stubWrite{
		Method: http.MethodPost, Endpoint: endpoint, Payload: payload,
	})

	if s.extrasStatus != 0 {
		writeStubJSON(w, s.extrasStatus, netbox.Object{"detail": "You do not have permission."})

		return
	}

	s.nextID++

	stored := netbox.Object{"id": float64(s.nextID)}
	for key, value := range payload {
		stored[key] = value
	}
	s.extras[endpoint] = append(s.extras[endpoint], stored)

	writeStubJSON(w, http.StatusCreated, stored)
}

func (s *netboxStubServer) extrasPatch(w http.ResponseWriter, r *http.Request, endpoint, tail string) {
	id, err := strconv.ParseInt(tail, 10, 64)
	if err != nil {
		writeStubJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}

	payload, ok := decodeStub(w, r)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.extrasWrites = append(s.extrasWrites, stubWrite{
		Method: http.MethodPatch, Endpoint: endpoint, ID: id, Payload: payload,
	})

	for _, obj := range s.extras[endpoint] {
		if stored, ok := obj.ID(); ok && int64(stored) == id {
			for key, value := range payload {
				obj[key] = value
			}
			writeStubJSON(w, http.StatusOK, obj)

			return
		}
	}

	writeStubJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})
}

// extrasMatches applies the exact-match filters the bootstrap sends: `slug` for a tag and
// `name` for a custom field.
func extrasMatches(obj netbox.Object, query url.Values) bool {
	for name, values := range query {
		if name == "limit" || name == "offset" {
			continue
		}
		if fmt.Sprint(obj[name]) != values[0] {
			return false
		}
	}

	return true
}

// seedExtras puts a definition into NetBox without the operator having created it, for the
// case where a NetBox admin made them by hand.
func (s *netboxStubServer) seedExtras(endpoint string, obj netbox.Object) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++

	stored := netbox.Object{"id": float64(s.nextID)}
	for key, value := range obj {
		stored[key] = value
	}
	s.extras[endpoint] = append(s.extras[endpoint], stored)
}

func (s *netboxStubServer) collection(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		s.list(w, r)

		return
	}
	s.create(w, r)
}

func (s *netboxStubServer) list(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	query := r.URL.Query()
	// Stricter than NetBox on purpose. NetBox ignores a query parameter it does not
	// recognise and answers with the *unfiltered* set (#206), so a stub that answers
	// whatever it is asked cannot tell a filter the server would honour from one it would
	// drop -- which is how every null pin went out as the non-existent `__isnull` through
	// every test in this package. 400 is what makes the next misspelling visible.
	for name := range query {
		if !netbox.LookupRegistered(name) {
			writeStubJSON(w, http.StatusBadRequest, netbox.Object{
				"detail": "unknown filter " + name,
			})

			return
		}
	}

	results := []netbox.Object{}
	// Ordered by id rather than by map iteration, so a query matching more than one object
	// answers the same way on every run and an ambiguity test cannot pass by luck.
	for _, id := range slices.Sorted(maps.Keys(s.objects)) {
		if s.stubMatches(s.objects[id], query) {
			results = append(results, s.objects[id])
		}
	}
	writeStubJSON(w, http.StatusOK, netbox.Object{
		"count": len(results), "results": results, "next": nil,
	})
}

func (s *netboxStubServer) create(w http.ResponseWriter, r *http.Request) {
	payload, ok := decodeStub(w, r)
	if !ok {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	s.writes = append(s.writes, stubWrite{Method: http.MethodPost, Payload: payload})

	if s.createStatus != 0 {
		writeStubJSON(w, s.createStatus, netbox.Object{s.key: []any{"already exists"}})

		return
	}

	_, created := s.store(payload)
	writeStubJSON(w, http.StatusCreated, created)
}

// stubTimestamp is what every object's created and last_updated read back as. Fixed rather
// than the wall clock, because no operator behaviour may depend on their value and a stable
// one keeps a failing test's diff readable.
const stubTimestamp = "2026-08-21T00:00:00Z"

// store puts obj into NetBox under a fresh id and returns both. The caller holds the lock.
//
// It adds the four read-only columns every ChangeLoggedModel serialises rather than storing
// only what it was sent, because two things are unobservable against a NetBox that omits
// them: status.url is copied straight out of a write response, and a descriptor that wrongly
// tried to manage `display`, `created` or `last_updated` would PATCH the same value on every
// resync forever (docs/netbox-schema.md, preamble).
func (s *netboxStubServer) store(obj netbox.Object) (int64, netbox.Object) {
	s.nextID++

	stored := netbox.Object{"id": float64(s.nextID)}
	for k, v := range obj {
		stored[k] = v
	}

	stored["url"] = fmt.Sprintf("%s/api/%s/%d/", s.url, s.endpoint, s.nextID)
	stored["created"], stored["last_updated"] = stubTimestamp, stubTimestamp
	// `display` is the model's __str__, which is `name` on every kind that has one and the
	// natural key on a kind that does not.
	stored["display"] = stored[s.key]
	if name, ok := stored["name"]; ok {
		stored["display"] = name
	}

	// NetBox returns a choice column as {"value","label"} and a decimal as a padded string.
	// Reproducing that here is the point of an integration stub: a stub that echoes the
	// request cannot catch a normalisation bug.
	s.objects[s.nextID] = netboxShape(stored)

	return s.nextID, s.objects[s.nextID]
}

func (s *netboxStubServer) object(w http.ResponseWriter, r *http.Request, id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, exists := s.objects[id]
	if !exists {
		writeStubJSON(w, http.StatusNotFound, netbox.Object{"detail": "Not found."})

		return
	}

	switch r.Method {
	case http.MethodGet:
		writeStubJSON(w, http.StatusOK, obj)
	case http.MethodDelete:
		s.writes = append(s.writes, stubWrite{Method: http.MethodDelete, ID: id})

		if s.protected[id] {
			// NetBox's own wording, and a 409, which is what internal/netbox classifies as
			// a *ProtectedError (errors.go, classify).
			writeStubJSON(w, http.StatusConflict, netbox.Object{
				"detail": fmt.Sprintf(
					"Unable to delete object %d. There are dependent objects: protected foreign key", id),
			})

			return
		}

		delete(s.objects, id)
		w.WriteHeader(http.StatusNoContent)
	default:
		s.mu.Unlock()
		payload, ok := decodeStub(w, r)
		s.mu.Lock()
		if !ok {
			return
		}
		s.writes = append(s.writes, stubWrite{Method: http.MethodPatch, ID: id, Payload: payload})
		for k, v := range payload {
			obj[k] = stubPatchValue(k, obj[k], v)
		}
		s.objects[id] = netboxShape(obj)
		writeStubJSON(w, http.StatusOK, s.objects[id])
	}
}

// stubPatchValue is what one column becomes when a PATCH names it. Replacement, except for
// the one container NetBox merges instead.
//
// CustomFieldsDataField.to_internal_value starts from what is stored and overlays the
// submitted keys -- `data = {**self.parent.instance.custom_field_data, **data}`
// (extras/api/customfields.py) -- which is the entire reason the operator is allowed to send
// a partial container and compare only the keys it sets (customFieldsEqual,
// internal/netbox/drift.go). A stub that replaced it would delete an unmanaged custom field
// on the operator's first write and so hide the bug the merge exists to prevent.
//
// A null in the submitted map is a value like any other here: it overwrites, and the read
// gives it back as null, which is how a removal settles (#196).
func stubPatchValue(field string, stored, sent any) any {
	if field != provenance.CustomFieldsField {
		return sent
	}

	submitted, ok := sent.(map[string]any)
	if !ok {
		return sent
	}

	merged := map[string]any{}
	if existing, ok := stored.(map[string]any); ok {
		maps.Copy(merged, existing)
	}
	maps.Copy(merged, submitted)

	return merged
}

// netboxShape rewrites a stored object into the shape NetBox returns on read: choice
// columns become {"value","label"} and the two decimal columns become padded strings.
//
// Which columns those are is hardcoded rather than read off the kind's Descriptor, and that
// is the decision worth defending: a stub that derived NetBox's read shape from the same
// descriptor the production code writes against would agree with it by construction, and a
// test that cannot disagree cannot fail. `status` is the only choice column on the kinds
// that use this stub and latitude/longitude the only decimals; extend the list when a kind
// needs more, and the extension is the documentation.
//
// `object_types` is deliberately not in it. NetBox serialises extras.Tag.object_types as the
// same `app_label.model` strings it accepts on write (docs/netbox-schema.md -> extras.Tag,
// a ManyToManyField onto contenttypes.ContentType), so it reads back as it was written and
// there is no normalisation to reproduce.
//
// `tags` very much is. It is written as bare ids and read back as nested objects, and the
// differ relies on that asymmetry (internal/netbox, sameIDSet reads the live side with
// nestedIDs). A stub that echoed the ids made every provenance-stamped object report drift on
// its own tag list and PATCH it on every resync -- the hot loop docs/concepts/drift.md opens
// by warning about, invisible until a kind was tested with spec.managedBy on and a resync
// short enough to observe (NBO-025).
func netboxShape(obj netbox.Object) netbox.Object {
	out := netbox.Object{}
	for k, v := range obj {
		out[k] = v
	}

	if ids := netbox.IDsOf(out["tags"]); len(ids) > 0 {
		nested := make([]any, 0, len(ids))
		for _, id := range ids {
			nested = append(nested, map[string]any{
				"id": float64(id), "display": fmt.Sprintf("tag-%d", id),
			})
		}
		out["tags"] = nested
	}

	if value, ok := out["status"].(string); ok {
		out["status"] = map[string]any{"value": value, "label": strings.ToTitle(value[:1]) + value[1:]}
	}
	for _, field := range []string{"latitude", "longitude"} {
		switch n := out[field].(type) {
		case float64:
			out[field] = fmt.Sprintf("%.6f", n)
		case string:
			if parsed, err := strconv.ParseFloat(n, 64); err == nil {
				out[field] = fmt.Sprintf("%.6f", parsed)
			}
		}
	}

	return out
}

// stubMatches applies the filters the operator sends: exact equality on the natural key,
// the provenance tag by slug, and a custom field by value.
//
// The last two are here for NetBoxSweep, and they have to be real rather than ignored: a
// sweep that forgot `?tag=` or `?cf_k8s_cluster=` would still pass every test against a stub
// that answered every query with everything -- and those two filters are the entire reason a
// sweep cannot report a hand-made object or another cluster's healthy ones. A filter the
// stub does not recognise is ignored, as before.
func (s *netboxStubServer) stubMatches(obj netbox.Object, query url.Values) bool {
	for name, values := range query {
		switch {
		case name == "limit" || name == "offset":
		case name == "tag":
			if !s.hasTag(obj, values[0]) {
				return false
			}
		case strings.HasPrefix(name, "cf_"):
			if stubCustomField(obj, strings.TrimPrefix(name, "cf_")) != values[0] {
				return false
			}
		case name == s.key:
			if fmt.Sprint(obj[name]) != values[0] {
				return false
			}
		case slices.Contains(s.refKeys, name):
			if fmt.Sprint(obj[strings.TrimSuffix(name, "_id")]) != values[0] {
				return false
			}
		}
	}

	return true
}

// hasTag reports whether obj carries the tag with this slug.
//
// The id-to-slug direction comes from the stub's own extras.Tag store, because that is the
// direction the wire uses: the engine writes `tags` as a list of ids, and only the tag store
// knows which slug an id is. An object seeded with nested `{"slug": ...}` entries -- which is
// the shape NetBox reads back -- matches too, so a test can seed a tagged object without
// looking an id up first. The caller holds the lock.
func (s *netboxStubServer) hasTag(obj netbox.Object, slug string) bool {
	for _, tag := range asStubList(obj[provenance.TagsField]) {
		if fmt.Sprint(tag["slug"]) == slug {
			return true
		}
	}

	for _, id := range netbox.IDsOf(obj[provenance.TagsField]) {
		for _, defined := range s.extras["extras/tags"] {
			stored, ok := defined.ID()
			if ok && stored == id && fmt.Sprint(defined["slug"]) == slug {
				return true
			}
		}
	}

	return false
}

// asStubList reads a `tags` value in the nested shape NetBox returns it in, and yields
// nothing for the list-of-ids shape the engine writes.
func asStubList(value any) []netbox.Object {
	entries, ok := value.([]any)
	if !ok {
		return nil
	}

	out := make([]netbox.Object, 0, len(entries))
	for _, entry := range entries {
		if nested, ok := entry.(map[string]any); ok {
			out = append(out, nested)
		}
	}

	return out
}

// stubCustomField reads one custom field off an object, as a string. An absent container or
// key reads as empty, which is what NetBox's `?cf_x=` matches nothing against.
func stubCustomField(obj netbox.Object, name string) string {
	fields, ok := obj[provenance.CustomFieldsField].(map[string]any)
	if !ok {
		return ""
	}

	value, ok := fields[name].(string)
	if !ok {
		return ""
	}

	return value
}

// managedTagID is the NetBox id of the provenance tag definition, for a test that has to
// stamp an object the operator did not create -- which is how an orphan is staged.
//
// No slug parameter: the only tag the operator ever writes is the one its own bootstrap
// created, and reading its id from the stub's store rather than from a literal is what keeps
// the seeded stamp and the engine's stamp the same tag.
func (s *netboxStubServer) managedTagID() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, defined := range s.extras["extras/tags"] {
		if fmt.Sprint(defined["slug"]) == provenance.DefaultTag {
			if id, ok := defined.ID(); ok {
				return id
			}
		}
	}

	s.t.Fatalf("stub has no extras/tags named %q", provenance.DefaultTag)

	return 0
}

// seed puts an object into NetBox without the operator having created it, for adoption
// tests. Returns its id.
func (s *netboxStubServer) seed(obj netbox.Object) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	id, _ := s.store(obj)

	return id
}

// protect makes the stub refuse a DELETE of these ids with the 409 Django's ProtectedError
// surfaces as, and release stops refusing -- which is what a dependent going away does in a
// real NetBox.
func (s *netboxStubServer) protect(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.protected == nil {
		s.protected = map[int64]bool{}
	}

	for _, id := range ids {
		s.protected[id] = true
	}
}

// release stops refusing the delete of these ids.
func (s *netboxStubServer) release(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		delete(s.protected, id)
	}
}

// deletes counts the DELETEs the stub has seen for one id, refused ones included. It is how
// a test tells "retried once" from "retried in a hot loop".
func (s *netboxStubServer) deletes(id int64) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0

	for _, write := range s.writes {
		if write.Method == http.MethodDelete && write.ID == id {
			count++
		}
	}

	return count
}

// cascade removes rows server-side without the operator having asked, which is what
// NetBox's `on_delete=CASCADE` does to a nested group's descendants when their parent is
// deleted. The stub does not model foreign keys, so a test that needs the cascade stages it.
//
// Distinct from a DELETE arriving at the stub: nothing is recorded in writes, because the
// point is that the operator did not do this.
func (s *netboxStubServer) cascade(ids ...int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	for _, id := range ids {
		if _, ok := s.objects[id]; !ok {
			s.t.Fatalf("stub has no object %d to cascade away", id)
		}

		delete(s.objects, id)
	}
}

// setField changes a value server-side, as a human editing the NetBox UI would. This is how
// drift is created in a test.
func (s *netboxStubServer) setField(id int64, name string, value any) {
	s.mu.Lock()
	defer s.mu.Unlock()

	obj, ok := s.objects[id]
	if !ok {
		s.t.Fatalf("stub has no object %d to edit", id)
	}
	obj[name] = value
	s.objects[id] = netboxShape(obj)
}

// get returns a copy of one stored object, or nil once it is gone.
//
// A copy, because a test polls it from its own goroutine while the engine is still writing
// the original: handing out the stored map is a concurrent map read and write, which is a
// runtime fatal error rather than a flake.
func (s *netboxStubServer) get(id int64) netbox.Object {
	s.mu.Lock()
	defer s.mu.Unlock()

	return maps.Clone(s.objects[id])
}

// countByKey is how a test asserts the engine did not create a duplicate.
func (s *netboxStubServer) countByKey(value string) int {
	s.mu.Lock()
	defer s.mu.Unlock()

	count := 0
	for _, obj := range s.objects {
		if fmt.Sprint(obj[s.key]) == value {
			count++
		}
	}

	return count
}

// recordedExtras is what the provenance bootstrap wrote, which is how "created once, and
// re-running changes nothing" is asserted from the outside.
func (s *netboxStubServer) recordedExtras() []stubWrite {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]stubWrite(nil), s.extrasWrites...)
}

func (s *netboxStubServer) recorded() []stubWrite {
	s.mu.Lock()
	defer s.mu.Unlock()

	return append([]stubWrite(nil), s.writes...)
}

func writeStubJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func decodeStub(w http.ResponseWriter, r *http.Request) (netbox.Object, bool) {
	var payload netbox.Object
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		writeStubJSON(w, http.StatusBadRequest, netbox.Object{"detail": "malformed body"})

		return nil, false
	}

	return payload, true
}

// TestStubRejectsAnUnregisteredFilter is the guard on the guard. The stub is what every test
// in this package asserts against, so a stub that answers a filter NetBox would drop makes
// the drop invisible -- which is exactly how every kind shipped with `?<filter>__isnull=true`
// pinned on a candidate NetBox never applied (#206). NetBox itself will not tell you: it
// ignores an unregistered parameter and returns the unfiltered set.
func TestStubRejectsAnUnregisteredFilter(t *testing.T) {
	stub, url := newNetBoxStub(t, regionKind)
	stub.seed(netbox.Object{"name": "emea", "slug": "emea"})

	for _, query := range []string{"slug=emea&parent_id__isnull=true", "slug=emea&slug__iexact=emea"} {
		resp, err := http.Get(url + "/api/" + regionKind.endpoint + "/?" + query) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("GET ?%s: %v", query, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("GET ?%s = %d, want 400 -- the stub answered a filter NetBox drops", query, resp.StatusCode)
		}
	}

	// The spellings NetBox does register still work, or the check is just an outage.
	for _, query := range []string{"slug=emea&parent_id=null", "name__ie=EMEA", "slug__ic=eme"} {
		resp, err := http.Get(url + "/api/" + regionKind.endpoint + "/?" + query) //nolint:noctx // test client
		if err != nil {
			t.Fatalf("GET ?%s: %v", query, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET ?%s = %d, want 200", query, resp.StatusCode)
		}
	}
}
