package metrics

import (
	"os"
	"slices"
	"strings"
	"testing"

	dto "github.com/prometheus/client_model/go"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

func TestStatusLabel(t *testing.T) {
	tests := []struct {
		name string
		code int
		want string
	}{
		{name: "a status netbox returns is reported exactly", code: 404, want: "404"},
		{name: "created is reported exactly", code: 201, want: "201"},
		{name: "a rate limit is reported exactly", code: 429, want: "429"},
		{name: "no response at all", code: 0, want: "error"},
		// The cardinality guard: a proxy in front of NetBox can return anything, and
		// `code` multiplies against ~120 endpoints and four methods.
		{name: "an unexpected status collapses to its class", code: 418, want: "4xx"},
		{name: "an unexpected redirect collapses to its class", code: 301, want: "3xx"},
		{name: "an unexpected server error collapses to its class", code: 599, want: "5xx"},
		{name: "a nonsense status is not a label", code: 9000, want: "error"},
		{name: "a negative status is not a label", code: -1, want: "error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := StatusLabel(tc.code); got != tc.want {
				t.Errorf("StatusLabel(%d) = %q, want %q", tc.code, got, tc.want)
			}
		})
	}
}

// TestMetricsAreOnTheControllerRuntimeRegistry is the wiring assertion: a metric declared
// against the default Prometheus registry compiles and increments perfectly happily, and
// never appears on the manager's /metrics endpoint.
func TestMetricsAreOnTheControllerRuntimeRegistry(t *testing.T) {
	want := []string{
		"netbox_operator_api_request_duration_seconds",
		"netbox_operator_api_requests_total",
		"netbox_operator_client_cache_size",
		"netbox_operator_conflicts_total",
		"netbox_operator_drift_corrected_total",
		"netbox_operator_drift_detected_total",
		"netbox_operator_endpoint_reconcile_total",
		"netbox_operator_ref_enqueue_total",
		"netbox_operator_reconcile_duration_seconds",
		"netbox_operator_reconcile_total",
	}

	got := gathered(t)
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("%s is not registered on controller-runtime's registry", name)
		}
	}
}

// TestNoUnboundedLabels is the cardinality guard, enforced rather than reviewed.
//
// A label whose values come from user input has no ceiling: one series per object name is
// how a metric takes a Prometheus down. Every label this operator exports has to be
// bounded by the code (kind, method, result) or by NetBox's schema (endpoint, field,
// code).
func TestNoUnboundedLabels(t *testing.T) {
	forbidden := []string{"name", "namespace", "object", "url", "uid", "id", "message"}
	// targetKind and referrerKind are Kind names off a Descriptor, exactly like `kind`: the
	// pair is one edge of NetBox's foreign-key graph, and neither half is user input.
	// reason is a Conflict condition's reason: two values, both constants in api/v1alpha1.
	// Notably not the other writer's cluster id, which is a NetBox custom field's contents and
	// so unbounded -- see metrics.Conflicts.
	allowed := []string{"kind", "result", "endpoint", "method", "code", "field",
		"targetKind", "referrerKind", "reason"}

	for name, family := range gathered(t) {
		for _, label := range labelsOf(family) {
			if slices.Contains(forbidden, label) {
				t.Errorf("%s is labelled by %q, which is unbounded user input", name, label)
			}
			if !slices.Contains(allowed, label) {
				t.Errorf("%s is labelled by %q; add it to the allowed set in this test "+
					"and state its cardinality in metrics.go first", name, label)
			}
		}
	}
}

// gathered returns this package's metric families, keyed by name. Reading the registry
// rather than scraping the text endpoint: the text format is a rendering, and asserting
// on it tests the renderer.
func gathered(t *testing.T) map[string]*dto.MetricFamily {
	t.Helper()

	families, err := ctrlmetrics.Registry.Gather()
	if err != nil {
		t.Fatalf("gathering the registry: %v", err)
	}

	out := map[string]*dto.MetricFamily{}
	for _, family := range families {
		if name := family.GetName(); strings.HasPrefix(name, "netbox_operator_") {
			out[name] = family
		}
	}
	return out
}

// labelsOf returns every label name used by any series in a family. Gather() reports no
// series at all for a Vec that has never been incremented, so the metrics under test are
// touched once first -- see TestMain.
func labelsOf(family *dto.MetricFamily) []string {
	var names []string
	for _, metric := range family.GetMetric() {
		for _, pair := range metric.GetLabel() {
			if !slices.Contains(names, pair.GetName()) {
				names = append(names, pair.GetName())
			}
		}
	}
	return names
}

// TestMain touches every labelled metric once, because an untouched CounterVec reports no
// series and the two registry tests above would then pass by looking at nothing.
func TestMain(m *testing.M) {
	ObserveReconcile("NetBoxFake", ResultUnchanged, 0)
	ObserveRequest("extras/tags", "GET", 200, 0)
	DriftDetected.WithLabelValues("NetBoxFake", "name").Inc()
	DriftCorrected.WithLabelValues("NetBoxFake", "name").Inc()
	EndpointReconcileTotal.WithLabelValues("Ready").Inc()
	RefEnqueueTotal.WithLabelValues("NetBoxFake", "NetBoxFake").Inc()
	Conflicts.WithLabelValues("NetBoxFake", "ForeignCluster").Inc()
	ClientCacheSize.Set(0)

	os.Exit(m.Run())
}
