package netbox

import (
	"context"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/funcr"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	logf "sigs.k8s.io/controller-runtime/pkg/log"

	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
)

// TestRedact is the reason request and response bodies may be logged at all. CONTRIBUTING
// requires redaction to be a tested function rather than a habit, and the cases below are
// the shapes NetBox actually returns.
func TestRedact(t *testing.T) {
	tests := []struct {
		name    string
		payload Object
		want    Object
	}{
		{
			name:    "a pre-shared key is masked",
			payload: Object{"ssid": "corp", "auth_psk": "s3cret"},
			want:    Object{"ssid": "corp", "auth_psk": "[redacted]"},
		},
		{
			name:    "every secret field name is masked, whatever its case",
			payload: Object{"Token": "t", "PASSWORD": "p", "private_key": "k"},
			want:    Object{"Token": "[redacted]", "PASSWORD": "[redacted]", "private_key": "[redacted]"},
		},
		{
			name:    "custom fields are collapsed to their names, not masked wholesale",
			payload: Object{"custom_fields": map[string]any{"owner": "alice", "cost_centre": "42"}},
			want:    Object{"custom_fields": "[2 custom fields redacted: cost_centre, owner]"},
		},
		{
			name:    "custom fields that are not a map are masked rather than trusted",
			payload: Object{"custom_fields": "surprise"},
			want:    Object{"custom_fields": "[redacted]"},
		},
		{
			// The one that matters for response logging: a list arrives as
			// {"results": [...]}, so masking only the top level would put every PSK on
			// the page into the log.
			name: "secrets nested inside a list response are masked",
			payload: Object{"count": float64(2), "results": []any{
				map[string]any{"id": float64(1), "auth_psk": "one"},
				map[string]any{"id": float64(2), "auth_psk": "two"},
			}},
			want: Object{"count": float64(2), "results": []any{
				map[string]any{"id": float64(1), "auth_psk": "[redacted]"},
				map[string]any{"id": float64(2), "auth_psk": "[redacted]"},
			}},
		},
		{
			name:    "a secret nested inside an object is masked",
			payload: Object{"tunnel": map[string]any{"name": "vpn", "preshared_key": "shh"}},
			want:    Object{"tunnel": map[string]any{"name": "vpn", "preshared_key": "[redacted]"}},
		},
		{
			name:    "a list of secrets is masked by its own key",
			payload: Object{"psk": []any{"a", "b"}},
			want:    Object{"psk": "[redacted]"},
		},
		{
			name:    "everything else survives, because a redacted log is useless",
			payload: Object{"name": "Managed", "vid": float64(4094), "enabled": true, "site": nil},
			want:    Object{"name": "Managed", "vid": float64(4094), "enabled": true, "site": nil},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redact(tc.payload); !reflect.DeepEqual(Object(got), tc.want) {
				t.Errorf("redact() = %#v, want %#v", got, tc.want)
			}
		})
	}
}

// TestRedactDoesNotMutateThePayload guards the bug that would turn debug logging into
// data loss: redact runs on the very Object the caller is about to send, so masking in
// place would PATCH "[redacted]" into NetBox.
func TestRedactDoesNotMutateThePayload(t *testing.T) {
	payload := Object{"auth_psk": "s3cret", "nested": map[string]any{"password": "hunter2"}}

	redact(payload)

	if payload["auth_psk"] != "s3cret" {
		t.Errorf("redact() overwrote the caller's payload: auth_psk = %v", payload["auth_psk"])
	}

	nested, ok := payload["nested"].(map[string]any)
	if !ok || nested["password"] != "hunter2" {
		t.Errorf("redact() overwrote a nested value in the caller's payload: %v", payload["nested"])
	}
}

// TestSecretsNeverReachTheLog is the end-to-end version of the acceptance criterion: a
// real request and a real response at debug verbosity, asserting that no secret material
// appears on any line.
func TestSecretsNeverReachTheLog(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"count":1,"results":[{"id":1,"auth_psk":"response-psk"}]}`))
	}))
	defer srv.Close()

	var logged strings.Builder
	ctx := logf.IntoContext(context.Background(), debugLogger(&logged))

	client := newTestClient(t, srv, nil)
	_, err := client.Create(ctx, "wireless/wireless-lans",
		Object{"ssid": "corp", "auth_psk": "request-psk", "custom_fields": map[string]any{"owner": "alice"}})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	out := logged.String()
	for _, secret := range []string{"request-psk", "response-psk", "alice", "test-token"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q reached the log:\n%s", secret, out)
		}
	}

	// Both bodies have to have been logged at all, or the assertions above pass because
	// nothing was written.
	for _, want := range []string{"netbox request", "netbox response", "[redacted]"} {
		if !strings.Contains(out, want) {
			t.Errorf("%q is missing, so this test proves nothing:\n%s", want, out)
		}
	}
}

// debugLogger is a logr that appends every line to out, with V(1) enabled -- the level at
// which bodies are logged at all.
func debugLogger(out *strings.Builder) logr.Logger {
	return funcr.New(func(prefix, args string) {
		out.WriteString(prefix + " " + args + "\n")
	}, funcr.Options{Verbosity: 1})
}

// metricsEndpoint is the REST path the metric assertions below use. A NetBox endpoint
// name, not an object name: that is the whole cardinality argument for the label.
const metricsEndpoint = "extras/tags"

// TestRequestMetrics asserts the counter moves per attempt and carries the status.
func TestRequestMetrics(t *testing.T) {
	var requests atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// First a 500, which the client retries, then a 200: two attempts, and the metric
		// counts what NetBox actually saw rather than what the caller asked for.
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(`{"id":1}`))
	}))
	defer srv.Close()

	before500 := requestCount(t, "500")
	before200 := requestCount(t, "200")
	beforeDuration := durationCount(t)

	client := newTestClient(t, srv, func(cfg *Config) { cfg.MaxRetries = Retries(1) })
	if _, err := client.GetByID(context.Background(), metricsEndpoint, 1); err != nil {
		t.Fatalf("GetByID: %v", err)
	}

	if got := requestCount(t, "500") - before500; got != 1 {
		t.Errorf("api_requests_total{code=\"500\"} moved by %v, want 1", got)
	}
	if got := requestCount(t, "200") - before200; got != 1 {
		t.Errorf("api_requests_total{code=\"200\"} moved by %v, want 1", got)
	}
	if got := durationCount(t) - beforeDuration; got != 2 {
		t.Errorf("api_request_duration_seconds observed %d times, want 2 (one per attempt)", got)
	}
}

// TestRequestMetricsRecordATransportFailure covers the path where there is no status code
// at all, which is the one an alert on `code="error"` fires on.
func TestRequestMetricsRecordATransportFailure(t *testing.T) {
	client, err := New(Config{URL: "http://127.0.0.1:1", Token: "t", MaxRetries: Retries(0)})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	before := requestCount(t, "error")

	if _, err := client.GetByID(context.Background(), metricsEndpoint, 1); err == nil {
		t.Fatal("GetByID against a closed port returned no error")
	}

	if got := requestCount(t, "error") - before; got != 1 {
		t.Errorf("api_requests_total{code=\"error\"} moved by %v, want 1", got)
	}
}

// requestCount reads one series of api_requests_total. Endpoint and method are fixed
// because every case below is a GET of the same REST path; the interesting dimension is
// the status code.
func requestCount(t *testing.T, code string) float64 {
	t.Helper()

	return testutil.ToFloat64(
		metrics.APIRequests.WithLabelValues(metricsEndpoint, http.MethodGet, code))
}

func durationCount(t *testing.T) uint64 {
	t.Helper()

	observer, ok := metrics.APIRequestDuration.WithLabelValues(metricsEndpoint, http.MethodGet).(interface {
		Write(*dto.Metric) error
	})
	if !ok {
		t.Fatal("the api request duration histogram cannot be read")
	}

	var out dto.Metric
	if err := observer.Write(&out); err != nil {
		t.Fatalf("reading the histogram: %v", err)
	}

	return out.GetHistogram().GetSampleCount()
}
