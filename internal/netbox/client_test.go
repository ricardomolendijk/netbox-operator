package netbox

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns a client pointed at srv with retries and backoff turned down, so
// a table test does not spend seconds sleeping.
func newTestClient(t *testing.T, srv *httptest.Server, mutate func(*Config)) *Client {
	t.Helper()
	cfg := Config{URL: srv.URL, Token: "test-token", MaxRetries: Retries(0), BaseDelay: time.Millisecond}
	if mutate != nil {
		mutate(&cfg)
	}
	client, err := New(cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return client
}

// TestRetriesZeroMeansZero guards the reason Config.MaxRetries is a pointer: with a
// plain int, asking to fail fast silently produced DefaultMaxRetries attempts.
func TestRetriesZeroMeansZero(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := New(Config{URL: srv.URL, MaxRetries: Retries(0)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.GetByID(context.Background(), "dcim/sites", 1); err == nil {
		t.Fatal("expected an error")
	}
	if got := requests.Load(); got != 1 {
		t.Errorf("made %d requests with MaxRetries=0, want 1", got)
	}
}

func TestRetriesNilMeansDefault(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	client, err := New(Config{URL: srv.URL, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := client.GetByID(context.Background(), "dcim/sites", 1); err == nil {
		t.Fatal("expected an error")
	}
	if got, want := requests.Load(), int64(DefaultMaxRetries+1); got != want {
		t.Errorf("made %d requests, want %d", got, want)
	}
}

func TestNewRejectsUnusableConfig(t *testing.T) {
	tests := map[string]Config{
		"empty url":     {},
		"bad scheme":    {URL: "ftp://netbox.example"},
		"unparseable":   {URL: "http://[::1"},
		"bad ca bundle": {URL: "https://netbox.example", CABundle: []byte("not a certificate")},
	}
	for name, cfg := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("expected an error, got nil")
			}
		})
	}
}

func TestNewNormalisesBaseURL(t *testing.T) {
	tests := map[string]string{
		"https://netbox.example":          "https://netbox.example/api",
		"https://netbox.example/":         "https://netbox.example/api",
		"https://netbox.example/api":      "https://netbox.example/api",
		"https://netbox.example/api/":     "https://netbox.example/api",
		"https://netbox.example:8443/api": "https://netbox.example:8443/api",
	}
	for input, want := range tests {
		t.Run(input, func(t *testing.T) {
			client, err := New(Config{URL: input})
			if err != nil {
				t.Fatalf("New(%q): %v", input, err)
			}
			if client.base != want {
				t.Errorf("base = %q, want %q", client.base, want)
			}
		})
	}
}

// TestClassification covers every row of the table in the spec. The point is that the
// engine can branch on error type, so each case asserts errors.As finds the right one.
func TestClassification(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		body    string
		header  http.Header
		assert  func(*testing.T, error)
		retryOK bool
	}{
		{
			name:   "400 preserves field errors",
			status: http.StatusBadRequest,
			body:   `{"slug":["This field must be unique."],"vid":["Invalid value."]}`,
			assert: func(t *testing.T, err error) {
				var target *ValidationError
				if !errors.As(err, &target) {
					t.Fatalf("want *ValidationError, got %T", err)
				}
				if got := target.Fields["slug"]; len(got) != 1 || got[0] != "This field must be unique." {
					t.Errorf("Fields[slug] = %v", got)
				}
				if len(target.Fields["vid"]) != 1 {
					t.Errorf("Fields[vid] = %v", target.Fields["vid"])
				}
			},
		},
		{
			name:   "400 detail becomes a non-field error",
			status: http.StatusBadRequest,
			body:   `{"detail":"A VLAN with this VID already exists in this group."}`,
			assert: func(t *testing.T, err error) {
				var target *ValidationError
				if !errors.As(err, &target) {
					t.Fatalf("want *ValidationError, got %T", err)
				}
				if len(target.Fields[nonFieldKey]) != 1 {
					t.Errorf("Fields[%s] = %v", nonFieldKey, target.Fields[nonFieldKey])
				}
			},
		},
		{
			name:   "400 nested serializer error is flattened",
			status: http.StatusBadRequest,
			body:   `{"scope":{"scope_type":["Invalid content type."]}}`,
			assert: func(t *testing.T, err error) {
				var target *ValidationError
				if !errors.As(err, &target) {
					t.Fatalf("want *ValidationError, got %T", err)
				}
				if len(target.Fields["scope.scope_type"]) != 1 {
					t.Errorf("flattened fields = %v", target.Fields)
				}
			},
		},
		{
			name:   "401 is an auth error",
			status: http.StatusUnauthorized,
			body:   `{"detail":"Invalid token."}`,
			assert: func(t *testing.T, err error) {
				var target *AuthError
				if !errors.As(err, &target) {
					t.Fatalf("want *AuthError, got %T", err)
				}
			},
		},
		{
			name:   "403 is an auth error, not validation",
			status: http.StatusForbidden,
			body:   `{"detail":"You do not have permission to perform this action."}`,
			assert: func(t *testing.T, err error) {
				var target *AuthError
				if !errors.As(err, &target) {
					t.Fatalf("want *AuthError, got %T", err)
				}
			},
		},
		{
			name:   "404 is not found",
			status: http.StatusNotFound,
			body:   `{"detail":"Not found."}`,
			assert: func(t *testing.T, err error) {
				var target *NotFoundError
				if !errors.As(err, &target) {
					t.Fatalf("want *NotFoundError, got %T", err)
				}
			},
		},
		{
			name:   "409 is protected",
			status: http.StatusConflict,
			body:   `{"detail":"Unable to delete object."}`,
			assert: func(t *testing.T, err error) {
				var target *ProtectedError
				if !errors.As(err, &target) {
					t.Fatalf("want *ProtectedError, got %T", err)
				}
			},
		},
		{
			name:   "400 naming a protected foreign key is protected, not validation",
			status: http.StatusBadRequest,
			body:   `{"detail":"Cannot delete some instances of model 'Tenant' because they are referenced through a protected foreign key."}`,
			assert: func(t *testing.T, err error) {
				var target *ProtectedError
				if !errors.As(err, &target) {
					t.Fatalf("want *ProtectedError, got %T", err)
				}
			},
		},
		{
			name:   "429 is a rate limit and carries Retry-After",
			status: http.StatusTooManyRequests,
			body:   `{"detail":"Request was throttled."}`,
			header: http.Header{"Retry-After": []string{"5"}},
			assert: func(t *testing.T, err error) {
				var target *RateLimitError
				if !errors.As(err, &target) {
					t.Fatalf("want *RateLimitError, got %T", err)
				}
				if target.RetryAfter != 5*time.Second {
					t.Errorf("RetryAfter = %v, want 5s", target.RetryAfter)
				}
			},
			retryOK: true,
		},
		{
			name:   "500 is transient",
			status: http.StatusInternalServerError,
			body:   `<html>server error</html>`,
			assert: func(t *testing.T, err error) {
				var target *TransientError
				if !errors.As(err, &target) {
					t.Fatalf("want *TransientError, got %T", err)
				}
			},
			retryOK: true,
		},
		{
			name:   "503 is transient",
			status: http.StatusServiceUnavailable,
			body:   ``,
			assert: func(t *testing.T, err error) {
				var target *TransientError
				if !errors.As(err, &target) {
					t.Fatalf("want *TransientError, got %T", err)
				}
			},
			retryOK: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				for key, values := range tc.header {
					w.Header()[key] = values
				}
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv, nil).GetByID(context.Background(), "dcim/sites", 1)
			if err == nil {
				t.Fatal("expected an error, got nil")
			}
			tc.assert(t, err)
			if got := Retryable(err); got != tc.retryOK {
				t.Errorf("Retryable = %v, want %v", got, tc.retryOK)
			}
		})
	}
}

func TestGetByIDAttachesTheIDToNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, nil).GetByID(context.Background(), "ipam/prefixes", 42)
	var notFound *NotFoundError
	if !errors.As(err, &notFound) {
		t.Fatalf("want *NotFoundError, got %T", err)
	}
	if notFound.ID != 42 {
		t.Errorf("ID = %d, want 42 -- the engine needs it to clear status.id", notFound.ID)
	}
}

// TestGetOneAmbiguityNamesEveryMatch is the whole of NBO-074 on the client side. ipam.Prefix
// has no meta.constraints, so two matches is a state NetBox permits and taking the first
// would silently adopt an unrelated object -- but refusing is only half an answer. The
// caller's next step is to look at those two objects, so the error has to say which two, and
// a count leaves them to reproduce the query by hand.
func TestGetOneAmbiguityNamesEveryMatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":2,"results":[` +
			`{"id":11,"display":"10.0.0.0/24"},{"id":12,"display":"10.0.0.0/24 (VRF prod)"}]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, nil).GetOne(context.Background(), "ipam/prefixes",
		map[string]string{"prefix": "10.0.0.0/24"})
	var ambiguous *AmbiguousError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("want *AmbiguousError, got %T (%v)", err, err)
	}

	if ambiguous.Matched != 2 {
		t.Errorf("Matched = %d, want 2", ambiguous.Matched)
	}

	if !reflect.DeepEqual(ambiguous.IDs, []int{11, 12}) {
		t.Errorf("IDs = %v, want [11 12]", ambiguous.IDs)
	}

	// Positional with IDs, because "10.0.0.0/24 (VRF prod)" is what a human recognises and
	// the id alone is what sends them looking it up.
	if !reflect.DeepEqual(ambiguous.Display, []string{"10.0.0.0/24", "10.0.0.0/24 (VRF prod)"}) {
		t.Errorf("Display = %v, want the display of each match", ambiguous.Display)
	}

	// Asserted on the rendered message and not only on the fields: this string is the
	// Conflict condition's message verbatim, so it is the one the operator reads.
	for _, want := range []string{"id 11 (10.0.0.0/24)", "id 12 (10.0.0.0/24 (VRF prod))"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Error() = %q, want it to contain %q", err, want)
		}
	}
}

// TestGetOneAmbiguityWithoutIDsStillSaysSo covers the response that can be reported no
// further: several matches, none of which carried an id. Saying "matched 2 objects" and
// stopping there would read like a message with the ids omitted by mistake.
func TestGetOneAmbiguityWithoutIDsStillSaysSo(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":2,"results":[{"name":"a"},{"name":"b"}]}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, nil).GetOne(context.Background(), "ipam/prefixes",
		map[string]string{"prefix": "10.0.0.0/24"})
	if err == nil {
		t.Fatal("two matches with no ids reported no error; the caller would treat that as no match")
	}

	if !strings.Contains(err.Error(), "none of which carried a netbox id") {
		t.Errorf("Error() = %q, want it to say the matches carried no id", err)
	}
}

func TestGetOneNoMatchIsNotAnError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":0,"results":[]}`))
	}))
	defer srv.Close()

	obj, err := newTestClient(t, srv, nil).GetOne(context.Background(), "dcim/sites",
		map[string]string{"slug": "absent"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if obj != nil {
		t.Errorf("obj = %v, want nil", obj)
	}
}

func TestListFollowsPagination(t *testing.T) {
	var srv *httptest.Server
	page := 0
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		page++
		if page < 3 {
			_, _ = fmt.Fprintf(w, `{"results":[{"id":%d}],"next":"%s/api/dcim/sites/?page=%d"}`, page, srv.URL, page+1)
			return
		}
		_, _ = fmt.Fprintf(w, `{"results":[{"id":%d}],"next":null}`, page)
	}))
	defer srv.Close()

	objs, err := newTestClient(t, srv, nil).List(context.Background(), "dcim/sites", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 3 {
		t.Errorf("got %d objects, want 3", len(objs))
	}
}

func TestListRefusesToReturnTruncatedResults(t *testing.T) {
	// A NetBox that always reports a next page must not be able to exhaust memory, so the
	// cap stays -- but it must not hand back a partial answer either. A caller cannot tell
	// partial from complete, and acting on absence means creating a duplicate.
	var srv *httptest.Server
	var requests atomic.Int64
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		_, _ = fmt.Fprintf(w, `{"results":[{"id":1}],"next":"%s/api/dcim/sites/?page=2"}`, srv.URL)
	}))
	defer srv.Close()

	client := newTestClient(t, srv, func(c *Config) { c.MaxPages = 3 })
	objs, err := client.List(context.Background(), "dcim/sites", nil)

	var truncated *TruncatedError
	if !errors.As(err, &truncated) {
		t.Fatalf("err = %v, want *TruncatedError", err)
	}
	if objs != nil {
		t.Errorf("got %d objects alongside the error; partial results must not escape", len(objs))
	}
	if truncated.MaxPages != 3 {
		t.Errorf("MaxPages = %d, want 3", truncated.MaxPages)
	}
	if truncated.Collected != 3 {
		t.Errorf("Collected = %d, want 3 -- the diagnosis needs to say how far it got", truncated.Collected)
	}
	if got := requests.Load(); got != 3 {
		t.Errorf("made %d requests, want 3 (the cap)", got)
	}
	// Not retryable: the same request truncates in the same place. Retrying it would burn
	// the API budget and never converge.
	if Retryable(err) {
		t.Error("a truncated list was reported as retryable")
	}
}

func TestRetryOnlyRetriesWhatARetryCanFix(t *testing.T) {
	tests := map[string]struct {
		status   int
		wantReqs int64
	}{
		"500 is retried":     {http.StatusInternalServerError, 3},
		"400 is not retried": {http.StatusBadRequest, 1},
		"409 is not retried": {http.StatusConflict, 1},
		"403 is not retried": {http.StatusForbidden, 1},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			client := newTestClient(t, srv, func(c *Config) { c.MaxRetries = Retries(2) })
			_, err := client.GetByID(context.Background(), "dcim/sites", 1)
			if err == nil {
				t.Fatal("expected an error")
			}
			if got := requests.Load(); got != tc.wantReqs {
				t.Errorf("made %d requests, want %d", got, tc.wantReqs)
			}
		})
	}
}

func TestRetryHonoursRetryAfter(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv, func(c *Config) { c.MaxRetries = Retries(1) })
	start := time.Now()
	if _, err := client.GetByID(context.Background(), "dcim/sites", 1); err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	// Asserting a lower bound only: the upper bound is the test runner's business.
	if elapsed := time.Since(start); elapsed < time.Second {
		t.Errorf("retried after %v, want at least the 1s Retry-After", elapsed)
	}
}

func TestContextCancellationAbortsInFlightRequest(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	defer close(release)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	_, err := newTestClient(t, srv, nil).GetByID(ctx, "dcim/sites", 1)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
	// A cancelled reconcile must not look like a NetBox failure, or the engine requeues
	// work that was deliberately abandoned.
	var transient *TransientError
	if errors.As(err, &transient) {
		t.Error("cancellation was reported as a transient NetBox error")
	}
}

func TestDryRunIssuesNoMutatingRequests(t *testing.T) {
	var mutations atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations.Add(1)
		}
		_, _ = w.Write([]byte(`{"id":7,"name":"live"}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv, func(c *Config) { c.Mode = ModeDryRun })
	ctx := context.Background()

	created, err := client.Create(ctx, "dcim/sites", Object{"name": "home", "slug": "home"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if !Suppressed(created) {
		t.Error("created object is not marked suppressed")
	}
	if _, ok := created.ID(); ok {
		t.Error("a DryRun create invented an id; nothing exists server-side to have one")
	}
	if created["name"] != "home" {
		t.Errorf("payload not returned: %v", created)
	}

	patched, err := client.Patch(ctx, "dcim/sites", 7, Object{"name": "away"})
	if err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if !Suppressed(patched) {
		t.Error("patched object is not marked suppressed")
	}

	// A suppressed delete has to be tellable from a completed one, or a caller reports a
	// deletion that never happened -- which for the finalizer means dropping itself and
	// leaving the object behind in NetBox.
	deleted, err := client.Delete(ctx, "dcim/sites", 7)
	if err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if !Suppressed(deleted) {
		t.Error("a DryRun delete is indistinguishable from a completed one")
	}
	// The marker names its target, so a caller can render "would delete dcim/sites/7".
	if id, ok := deleted.ID(); !ok || id != 7 {
		t.Errorf("suppressed delete id = %d (found %t), want 7", id, ok)
	}
	if deleted["endpoint"] != "dcim/sites" {
		t.Errorf("suppressed delete endpoint = %v, want dcim/sites", deleted["endpoint"])
	}

	// Reads must still hit the live API, so drift is reported against real state.
	if _, err := client.GetByID(ctx, "dcim/sites", 7); err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if got := mutations.Load(); got != 0 {
		t.Errorf("DryRun client issued %d mutating requests, want 0", got)
	}
}

func TestApplyModeSendsMutations(t *testing.T) {
	var methods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		methods = append(methods, r.Method)
		_, _ = w.Write([]byte(`{"id":7}`))
	}))
	defer srv.Close()

	client := newTestClient(t, srv, nil)
	ctx := context.Background()
	if _, err := client.Create(ctx, "dcim/sites", Object{"name": "home"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := client.Patch(ctx, "dcim/sites", 7, Object{"name": "away"}); err != nil {
		t.Fatalf("Patch: %v", err)
	}
	if _, err := client.Delete(ctx, "dcim/sites", 7); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	want := []string{http.MethodPost, http.MethodPatch, http.MethodDelete}
	if len(methods) != len(want) {
		t.Fatalf("methods = %v, want %v", methods, want)
	}
	for i := range want {
		if methods[i] != want[i] {
			t.Errorf("methods[%d] = %s, want %s", i, methods[i], want[i])
		}
	}
}

func TestDeleteTolerated204(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	deleted, err := newTestClient(t, srv, nil).Delete(context.Background(), "dcim/sites", 1)
	if err != nil {
		t.Fatalf("Delete on 204: %v", err)
	}
	// A 204 has no body, and a real delete must not look suppressed.
	if deleted != nil {
		t.Errorf("Delete on 204 returned %v, want nil", deleted)
	}
}

func TestUnparseableBodyIsNotTransient(t *testing.T) {
	// An HTML error page from a proxy in front of NetBox will be identical on retry.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, nil).GetByID(context.Background(), "dcim/sites", 1)
	if Retryable(err) {
		t.Errorf("unparseable body reported as retryable: %v", err)
	}
}

func TestRequestCarriesTokenAndAccept(t *testing.T) {
	var auth, accept string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth, accept = r.Header.Get("Authorization"), r.Header.Get("Accept")
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	if _, err := newTestClient(t, srv, nil).GetByID(context.Background(), "dcim/sites", 1); err != nil {
		t.Fatalf("GetByID: %v", err)
	}
	if auth != "Token test-token" {
		t.Errorf("Authorization = %q", auth)
	}
	if accept != "application/json" {
		t.Errorf("Accept = %q", accept)
	}
}

func TestEncodeParamsIsStable(t *testing.T) {
	params := map[string]string{"slug": "home", "brief": "1", "limit": "250"}
	first := encodeParams(params)
	for range 20 {
		if got := encodeParams(params); got != first {
			t.Fatalf("encodeParams not stable: %q then %q", first, got)
		}
	}
	if want := "brief=1&limit=250&slug=home"; first != want {
		t.Errorf("encodeParams = %q, want %q", first, want)
	}
}

// TestListNeverFollowsPaginationToAnotherOrigin is the regression test for the token this
// client would otherwise hand to whoever answers a list.
//
// Every request carries `Authorization: Token ...`, and `next` is a value out of a response
// body. Following it verbatim means one crafted page exfiltrates the endpoint's credential --
// and Go's own cross-origin credential stripping does not apply, because this is a request the
// client builds rather than a redirect it follows.
func TestListNeverFollowsPaginationToAnotherOrigin(t *testing.T) {
	var attackerHits atomic.Int64
	var sawToken atomic.Bool
	attacker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attackerHits.Add(1)
		if r.Header.Get("Authorization") != "" {
			sawToken.Store(true)
		}
		_, _ = fmt.Fprint(w, `{"results":[],"next":null}`)
	}))
	defer attacker.Close()

	var pages atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if pages.Add(1) == 1 {
			_, _ = fmt.Fprintf(w, `{"results":[{"id":1}],"next":"%s/api/dcim/sites/?page=2"}`, attacker.URL)
			return
		}
		_, _ = fmt.Fprint(w, `{"results":[{"id":2}],"next":null}`)
	}))
	defer srv.Close()

	objs, err := newTestClient(t, srv, nil).List(context.Background(), "dcim/sites", nil)
	if err != nil {
		t.Fatalf("List: %v", err)
	}

	if attackerHits.Load() != 0 {
		t.Errorf("the client made %d request(s) to the other origin, want 0", attackerHits.Load())
	}
	if sawToken.Load() {
		t.Error("the api token was sent to an origin netbox named in a response body")
	}
	// The query the server chose is still honoured, on the origin we asked: the second page
	// was fetched from srv, so pagination is followed rather than abandoned.
	if len(objs) != 2 {
		t.Errorf("got %d objects, want 2 -- pagination must still follow, just not off-origin", len(objs))
	}
}

// TestNextPageKeepsTheServersQuery holds the half of the fix that is not about security: the
// rewrite must preserve whatever paging scheme NetBox chose, since limit/offset and a cursor
// both live entirely in the query.
func TestNextPageKeepsTheServersQuery(t *testing.T) {
	for _, tc := range []struct {
		name    string
		current string
		next    string
		want    string
	}{
		{
			name:    "same origin passes through",
			current: "https://netbox.example/api/dcim/sites/?limit=250",
			next:    "https://netbox.example/api/dcim/sites/?limit=250&offset=250",
			want:    "https://netbox.example/api/dcim/sites/?limit=250&offset=250",
		},
		{
			name:    "a proxy-rewritten host keeps the query and loses the host",
			current: "https://netbox.internal/api/dcim/sites/?limit=250",
			next:    "https://netbox.public/api/dcim/sites/?limit=250&offset=250",
			want:    "https://netbox.internal/api/dcim/sites/?limit=250&offset=250",
		},
		{
			name:    "a cursor survives the rewrite",
			current: "https://netbox.example/api/dcim/sites/?limit=250",
			next:    "https://elsewhere.example/whatever/?cursor=cD0yMDI0",
			want:    "https://netbox.example/api/dcim/sites/?cursor=cD0yMDI0",
		},
		{
			name:    "a relative next is resolved against the current page",
			current: "https://netbox.example/api/dcim/sites/?limit=250",
			next:    "?limit=250&offset=500",
			want:    "https://netbox.example/api/dcim/sites/?limit=250&offset=500",
		},
		{
			name:    "no next page ends the loop",
			current: "https://netbox.example/api/dcim/sites/?limit=250",
			next:    "",
			want:    "",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := nextPage(context.Background(), tc.current, tc.next)
			if err != nil {
				t.Fatalf("nextPage: %v", err)
			}
			if got != tc.want {
				t.Errorf("nextPage = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestRedactURLMasksCredentials covers the strings that reach a log line, an Event and a
// condition. The unparseable case is the one that matters most: it is the error message about
// a url, so it is the message most likely to be carrying one nobody sanitised.
func TestRedactURLMasksCredentials(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"https://netbox.example/api", "https://netbox.example/api"},
		{"https://user:s3cr3t@netbox.example/api", "https://user:xxxxx@netbox.example/api"},
		{"https://user:s3cr3t@netbox.example:8443/api?x=1", "https://user:xxxxx@netbox.example:8443/api?x=1"},
		{"https://user:s3cr3t@netbox.example/api\x7f", "https://xxxxx:xxxxx@netbox.example/api\x7f"},
	} {
		if got := RedactURL(tc.in); got != tc.want {
			t.Errorf("RedactURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
		if strings.Contains(RedactURL(tc.in), "s3cr3t") {
			t.Errorf("RedactURL(%q) leaked the password", tc.in)
		}
	}
}

// TestUpstreamBodyIsNotReflected is #298's exfiltration channel. classify's errors are
// written into a NetBoxEndpoint's Ready condition and a Warning Event by the endpoint
// controller, so whatever a body says is readable by whoever chose the host that said it.
func TestUpstreamBodyIsNotReflected(t *testing.T) {
	const marker = "eyJzZWNyZXQiOiJub3QteW91cnMifQ"

	for name, tc := range map[string]struct {
		status      int
		contentType string
		body        string
	}{
		"401":              {http.StatusUnauthorized, "application/json", `{"Token":"` + marker + `"}`},
		"403":              {http.StatusForbidden, "application/json", `{"Token":"` + marker + `"}`},
		"400":              {http.StatusBadRequest, "text/plain", marker},
		"409":              {http.StatusConflict, "text/plain", marker},
		"418":              {http.StatusTeapot, "text/plain", marker},
		"200 but not json": {http.StatusOK, "text/html", "<html><body>" + marker + "</body></html>"},
	} {
		t.Run(name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			_, err := newTestClient(t, srv, nil).Status(context.Background())
			if err == nil {
				t.Fatal("expected an error")
			}
			if strings.Contains(err.Error(), marker) {
				t.Errorf("the upstream body reached the error string, which is the condition"+
					" message and the Event: %v", err)
			}
			// Suppressing is not the goal; a message that says nothing costs the
			// diagnosability these errors exist for. The shape has to survive.
			if !strings.Contains(err.Error(), "bytes") {
				t.Errorf("error = %v, want it to report the body's length", err)
			}
		})
	}
}

// TestNetBoxsOwnErrorSentenceSurvives is the other half: the summary is shaped so that a
// real NetBox still says the thing an operator acts on. DRF puts it under `detail`, and a
// host that is not a DRF application does not produce that key.
func TestNetBoxsOwnErrorSentenceSurvives(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail":"Invalid token."}`))
	}))
	defer srv.Close()

	_, err := newTestClient(t, srv, nil).Status(context.Background())
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "Invalid token.") {
		t.Errorf("error = %v, want netbox's own detail to survive the summary", err)
	}
	if !strings.Contains(err.Error(), "application/json") {
		t.Errorf("error = %v, want the media type", err)
	}
}

// TestFieldErrorsAreOrderedAndBounded covers the other thing a 400 puts in a condition.
// Sorted because an unsorted map join rewrites the condition on every reconcile, and
// bounded because the names and the messages are the server's, not NetBox's by definition.
func TestFieldErrorsAreOrderedAndBounded(t *testing.T) {
	fields := map[string][]string{"slug": {"must be unique"}, "name": {"required"}}
	err := &ValidationError{Status: 400, Fields: fields}
	for range 20 {
		if got, want := err.Error(),
			"netbox rejected the payload (400): name: required, slug: must be unique"; got != want {
			t.Fatalf("Error() = %q, want %q", got, want)
		}
	}

	long := map[string][]string{}
	for i := range 40 {
		long[fmt.Sprintf("field%02d", i)] = []string{strings.Repeat("x", 200)}
	}
	if got := len((&ValidationError{Status: 400, Fields: long}).Error()); got > 700 {
		t.Errorf("a 40-field 400 rendered %d characters; the budget is 512 plus the prefix", got)
	}
}
