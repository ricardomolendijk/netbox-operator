package netbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

// TestAllocateDoesNotRetry is the most important assertion in this file, and it is about a
// *missing* feature.
//
// Every other write here goes through do(), which retries a 5xx. An allocating POST must not:
// it is not idempotent, so a POST that committed and lost its response, retried, allocates a
// second address -- silently, one per attempt, until the prefix is full. The engine's identity
// search is what recovers a lost response, and a client-side retry would defeat it by
// creating the very duplicate the search then refuses to resolve.
func TestAllocateDoesNotRetry(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	// Retries left at the default, so this proves the call opts out rather than that the
	// config turned them off.
	client, err := New(Config{URL: srv.URL, Token: "t"})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if _, err := client.Allocate(
		context.Background(), "ipam/prefixes", 11, AvailableIPs, Object{}); err == nil {
		t.Fatal("expected the 500 to be returned")
	}

	if got := requests.Load(); got != 1 {
		t.Errorf("made %d requests, want exactly 1: an allocating POST is not idempotent and"+
			" must never be retried inside the client", got)
	}
}

// TestAllocateClassifies covers every status the allocation view answers with.
//
// The 409 is the one that earns this function: everywhere else in the client a 409 is a
// protected relation, which means "delete something else first". On an allocation POST it
// means "this pool is empty", which is a different state with a different fix -- and the
// claim reconciler waits on it rather than failing.
func TestAllocateClassifies(t *testing.T) {
	cases := map[string]struct {
		status int
		body   string
		assert func(*testing.T, error)
	}{
		"409 is exhaustion": {
			status: http.StatusConflict,
			body:   `{"detail": "Insufficient resources are available to satisfy the request"}`,
			assert: func(t *testing.T, err error) {
				t.Helper()

				var exhausted *ExhaustedError
				if !errors.As(err, &exhausted) {
					t.Fatalf("err = %T (%v), want *ExhaustedError", err, err)
				}

				if exhausted.ID != 11 || exhausted.Endpoint != "ipam/prefixes/available-ips" {
					t.Errorf("error names %s/%d, want ipam/prefixes/available-ips/11",
						exhausted.Endpoint, exhausted.ID)
				}

				// A caller has to be able to quote NetBox's own words in the condition.
				if !errors.As(err, &exhausted) || exhausted.Body == "" {
					t.Error("the error dropped netbox's body")
				}
			},
		},
		"400 is still validation": {
			status: http.StatusBadRequest,
			body:   `{"dns_name": ["Enter a valid hostname."]}`,
			assert: func(t *testing.T, err error) {
				t.Helper()

				var invalid *ValidationError
				if !errors.As(err, &invalid) {
					t.Fatalf("err = %T (%v), want *ValidationError", err, err)
				}

				if len(invalid.Fields["dns_name"]) == 0 {
					t.Errorf("field errors = %v, want dns_name preserved", invalid.Fields)
				}
			},
		},
		"403 is still auth": {
			status: http.StatusForbidden,
			body:   `{"detail": "You do not have permission"}`,
			assert: func(t *testing.T, err error) {
				t.Helper()

				var denied *AuthError
				if !errors.As(err, &denied) {
					t.Errorf("err = %T (%v), want *AuthError", err, err)
				}
			},
		},
		"404 is still not found": {
			status: http.StatusNotFound,
			body:   `{"detail": "Not found."}`,
			assert: func(t *testing.T, err error) {
				t.Helper()

				var missing *NotFoundError
				if !errors.As(err, &missing) {
					t.Errorf("err = %T (%v), want *NotFoundError", err, err)
				}
			},
		},
		"500 is still transient": {
			status: http.StatusInternalServerError,
			assert: func(t *testing.T, err error) {
				t.Helper()

				var transient *TransientError
				if !errors.As(err, &transient) {
					t.Errorf("err = %T (%v), want *TransientError", err, err)
				}
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			client := newTestClient(t, srv, nil)

			_, err := client.Allocate(
				context.Background(), "ipam/prefixes", 11, AvailableIPs, Object{})
			if err == nil {
				t.Fatal("expected an error")
			}

			tc.assert(t, err)
		})
	}
}

// TestAllocatePostsASingleObjectToTheSubPath pins the request shape.
//
// A single object rather than a one-element list, because NetBox mirrors the shape it was
// given: one object in, one object out. That makes "NetBox returned more objects than were
// asked for" unrepresentable rather than a failure mode to handle.
func TestAllocatePostsASingleObjectToTheSubPath(t *testing.T) {
	var path, body string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)

		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id": 412, "address": "10.0.20.37/24"}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv, nil)

	allocated, err := client.Allocate(context.Background(), "ipam/prefixes", 11, AvailableIPs,
		Object{"dns_name": "dns.home.arpa"})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if want := "/api/ipam/prefixes/11/available-ips/"; path != want {
		t.Errorf("posted to %q, want %q", path, want)
	}

	var decoded any
	if err := json.Unmarshal([]byte(body), &decoded); err != nil {
		t.Fatalf("body %q is not json: %v", body, err)
	}

	if _, isList := decoded.([]any); isList {
		t.Errorf("body %q is a list; a single object is what makes a multi-object answer"+
			" unrepresentable", body)
	}

	if id, ok := allocated.ID(); !ok || id != 412 {
		t.Errorf("allocated = %v, want the object netbox created", allocated)
	}
}

// TestAllocateInDryRunSendsNothing keeps a rehearsing endpoint from allocating.
func TestAllocateInDryRunSendsNothing(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	client := newTestClient(t, srv, func(cfg *Config) { cfg.Mode = ModeDryRun })

	allocated, err := client.Allocate(
		context.Background(), "ipam/prefixes", 11, AvailableIPs, Object{"dns_name": "dns"})
	if err != nil {
		t.Fatalf("Allocate: %v", err)
	}

	if !Suppressed(allocated) {
		t.Errorf("allocated = %v, want a suppressed object", allocated)
	}

	if got := requests.Load(); got != 0 {
		t.Errorf("made %d requests in DryRun, want 0", got)
	}
}

// TestURLIsNormalised is load-bearing for the allocation identity: three spellings of one
// NetBox must not be three identities, or the same manifest would reclaim nothing after
// somebody tidied a trailing slash out of the NetBoxEndpoint.
func TestURLIsNormalised(t *testing.T) {
	for _, configured := range []string{
		"https://netbox.example", "https://netbox.example/", "https://netbox.example/api",
		"https://netbox.example/api/",
	} {
		client, err := New(Config{URL: configured, Token: "t"})
		if err != nil {
			t.Fatalf("New(%q): %v", configured, err)
		}

		if got := client.URL(); got != "https://netbox.example" {
			t.Errorf("URL() = %q for %q, want https://netbox.example", got, configured)
		}
	}
}

// TestChoiceOfReadsBothShapes covers the two ways NetBox serialises a choice column, which is
// what the pool admissibility guard compares against.
func TestChoiceOfReadsBothShapes(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"nested", map[string]any{"value": "container", "label": "Container"}, "container"},
		{"flattened", "container", "container"},
		{"absent", nil, ""},
		{"empty nesting", map[string]any{}, ""},
		// A choice column is always a string in NetBox, so a number is not one and reads as
		// absent rather than being coerced -- which is what every caller wants, since a
		// coerced value would compare equal to a descriptor's declared value by accident.
		{"not a choice at all", float64(3), ""},
	}

	for _, tc := range cases {
		if got := ChoiceOf(tc.value); got != tc.want {
			t.Errorf("ChoiceOf(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// TestCustomFieldRoundTrip covers the two helpers the allocation identity is written and read
// with, including the merge behaviour that keeps a payload from clobbering the provenance
// stamp it was built on top of.
func TestCustomFieldRoundTrip(t *testing.T) {
	payload := Object{}

	SetCustomField(payload, "k8s_uid", "uid-1")
	SetCustomField(payload, "k8s_allocation_identity", "9f2c41b7ae05d813")
	SetCustomField(payload, "", "dropped")

	if got := CustomFieldOf(payload, "k8s_uid"); got != "uid-1" {
		t.Errorf("k8s_uid = %q, want uid-1: the second write clobbered the first", got)
	}

	if got := CustomFieldOf(payload, "k8s_allocation_identity"); got != "9f2c41b7ae05d813" {
		t.Errorf("k8s_allocation_identity = %q, want the identity", got)
	}

	fields, ok := payload["custom_fields"].(map[string]any)
	if !ok {
		t.Fatalf("custom_fields = %T, want map[string]any -- a map[string]string never"+
			" compares equal in Changes and PATCHes forever", payload["custom_fields"])
	}

	if len(fields) != 2 {
		t.Errorf("custom_fields = %v, want two entries and no empty name", fields)
	}

	if got := CustomFieldOf(Object{}, "k8s_uid"); got != "" {
		t.Errorf("CustomFieldOf on an object with no container = %q, want empty", got)
	}
}
