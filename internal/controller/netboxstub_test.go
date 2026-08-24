package controller

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
)

// stubWrite is one mutating request the stub received.
type stubWrite struct {
	Method  string
	ID      int64
	Payload netbox.Object
}

// netboxStubServer is a NetBox for one endpoint, generic over the kind.
//
// It exists because the alternative -- a hand-written stub per kind -- makes the real cost
// of adding a kind about 250 lines of test scaffolding rather than the three small files
// CONTRIBUTING.md advertises. The engine is generic; its tests should be too, or the
// extensibility claim is only true of the production code.
//
// `key` is the natural-key field the kind is looked up by, which is all the stub needs to
// know about the kind: everything else it stores and returns verbatim.
type netboxStubServer struct {
	t        *testing.T
	endpoint string
	key      string

	mu      sync.Mutex
	objects map[int64]netbox.Object
	writes  []stubWrite
	nextID  int64

	// createStatus, when set, is returned for a POST instead of creating. For asserting
	// what the engine does with a 400 or a 409.
	createStatus int
}

// newNetBoxStub returns a running stub and its URL. `key` names the natural-key field, for
// example "slug".
//
// unparam is correct that `endpoint` has one caller today: NetBoxTag predates this stub and
// still carries its own 250-line copy. Migrating it is a separate change -- CONTRIBUTING.md
// forbids refactoring another kind's tests inside a feature PR -- and until then the
// parameter is generic by design rather than by use.
//
//nolint:unparam // generic by design; NetBoxTag migrates onto it separately
func newNetBoxStub(t *testing.T, endpoint, key string) (*netboxStubServer, string) {
	t.Helper()

	s := &netboxStubServer{
		t: t, endpoint: endpoint, key: key,
		objects: map[int64]netbox.Object{}, nextID: 100,
	}

	srv := httptest.NewServer(http.HandlerFunc(s.route))
	t.Cleanup(srv.Close)

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
	results := []netbox.Object{}
	for _, obj := range s.objects {
		if stubMatches(obj, query, s.key) {
			results = append(results, obj)
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

	s.nextID++
	obj := netbox.Object{"id": float64(s.nextID)}
	for k, v := range payload {
		obj[k] = v
	}
	// NetBox returns a choice column as {"value","label"} and a decimal as a padded string.
	// Reproducing that here is the point of an integration stub: a stub that echoes the
	// request cannot catch a normalisation bug.
	s.objects[s.nextID] = netboxShape(obj)
	writeStubJSON(w, http.StatusCreated, s.objects[s.nextID])
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
			obj[k] = v
		}
		s.objects[id] = netboxShape(obj)
		writeStubJSON(w, http.StatusOK, s.objects[id])
	}
}

// netboxShape rewrites a stored object into the shape NetBox returns on read: choice
// columns become {"value","label"} and the two decimal columns become padded strings.
//
// Hardcoding which fields those are keeps the stub honest without a schema: `status` is the
// only choice column on the kinds that use this stub, and latitude/longitude the only
// decimals. Extend it when a kind needs more, and the extension is the documentation.
func netboxShape(obj netbox.Object) netbox.Object {
	out := netbox.Object{}
	for k, v := range obj {
		out[k] = v
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

// stubMatches applies the filters the engine sends. Only exact equality on the natural key
// is needed, plus the limit the client always adds.
func stubMatches(obj netbox.Object, query url.Values, key string) bool {
	for name, values := range query {
		if name == "limit" || name == "offset" {
			continue
		}
		if name != key {
			continue
		}
		if fmt.Sprint(obj[name]) != values[0] {
			return false
		}
	}

	return true
}

// seed puts an object into NetBox without the operator having created it, for adoption
// tests. Returns its id.
func (s *netboxStubServer) seed(obj netbox.Object) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.nextID++
	stored := netbox.Object{"id": float64(s.nextID)}
	for k, v := range obj {
		stored[k] = v
	}
	s.objects[s.nextID] = netboxShape(stored)

	return s.nextID
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

func (s *netboxStubServer) get(id int64) netbox.Object {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.objects[id]
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
