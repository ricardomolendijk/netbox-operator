package reconciler

import (
	"context"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"

	"github.com/ricardomolendijk/netbox-operator/internal/metrics"
	"github.com/ricardomolendijk/netbox-operator/internal/netbox"
	"github.com/ricardomolendijk/netbox-operator/internal/registry"
)

// The collectors are process-global, so every assertion here is a delta around one
// reconcile. An absolute value would couple this test to the order the package's tests
// run in, which is exactly the kind of assertion that fails once, mysteriously, in CI.

// results is every value the `result` label can take, so a case can assert that the one
// it expects moved and that none of the others did. A reconcile that increments two
// buckets makes sum(reconcile_total) stop being a count of reconciles.
var results = []string{
	metrics.ResultCreated,
	metrics.ResultUpdated,
	metrics.ResultRecreated,
	metrics.ResultUnchanged,
	metrics.ResultDeleted,
	metrics.ResultDryRun,
	metrics.ResultWaiting,
	metrics.ResultError,
}

// adoptedObject is a CR that has already recorded its NetBox id, so the engine locates by
// id and goes straight to the compare-and-patch path every resync takes.
func adoptedObject() *fakeKind {
	obj := fakeObject()
	obj.Status.ID = 7

	return obj
}

// driftedTag is liveTag with one field a human has changed underneath the operator.
func driftedTag(id int) netbox.Object {
	live := liveTag(id)
	live["color"] = "ff0000"

	return live
}

func TestReconcileMetrics(t *testing.T) {
	tests := []struct {
		name       string
		descriptor registry.Descriptor
		object     func() *fakeKind
		client     func(t *testing.T) *fakeClient
		notReady   bool

		wantResult string
		// wantDrift and wantCorrected are the field names expected to have been counted
		// as detected and as written. Separate, because that gap is the whole point of
		// two counters (docs/decisions/0005-gitops-coexistence.md).
		wantDrift     []string
		wantCorrected []string
	}{
		{
			name:   "a create counts as created and is not drift",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{created: liveTag(7)}
			},
			wantResult: metrics.ResultCreated,
		},
		{
			name:   "a resync that finds nothing to do counts as unchanged",
			object: adoptedObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: liveTag(7)}
			},
			wantResult: metrics.ResultUnchanged,
		},
		{
			name:   "a corrected field is counted as both detected and corrected",
			object: adoptedObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(7), patched: liveTag(7)}
			},
			wantResult:    metrics.ResultUpdated,
			wantDrift:     []string{"color"},
			wantCorrected: []string{"color"},
		},
		{
			name:       "a dry run detects drift and corrects none of it",
			descriptor: fakeDescriptor(),
			object:     adoptedObject,
			client: func(t *testing.T) *fakeClient {
				return &fakeClient{get: driftedTag(7), dryRun: dryRunClient(t)}
			},
			wantResult: metrics.ResultDryRun,
			wantDrift:  []string{"color"},
		},
		{
			name:       "a recreate counts as recreated",
			descriptor: recreateDescriptor(),
			object: func() *fakeKind {
				obj := adoptedObject()
				obj.Spec.Slug = "renamed"

				return obj
			},
			client: func(*testing.T) *fakeClient {
				return &fakeClient{get: liveTag(7), created: liveTag(8)}
			},
			wantResult:    metrics.ResultRecreated,
			wantDrift:     []string{"slug"},
			wantCorrected: []string{"slug"},
		},
		{
			name:       "an endpoint that is not ready is waiting, not failing",
			object:     fakeObject,
			notReady:   true,
			client:     func(*testing.T) *fakeClient { return &fakeClient{} },
			wantResult: metrics.ResultWaiting,
		},
		{
			name:   "a rejected payload counts as an error",
			object: fakeObject,
			client: func(*testing.T) *fakeClient {
				return &fakeClient{createErr: &netbox.ValidationError{Status: 400, Body: "slug is not unique"}}
			},
			wantResult: metrics.ResultError,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			descriptor := tc.descriptor
			if descriptor.GVK.Kind == "" {
				descriptor = fakeDescriptor()
			}
			kind := descriptor.GVK.Kind

			reconciles := watch(t, metrics.ReconcileTotal, labelSets(kind, results)...)
			detected := watch(t, metrics.DriftDetected, labelSets(kind, fieldNames(descriptor))...)
			corrected := watch(t, metrics.DriftCorrected, labelSets(kind, fieldNames(descriptor))...)
			durations := histogramCount(t, kind)

			engine := &Engine{
				Finalizers:  &fakeFinalizers{},
				Descriptors: fakeDescriptors{descriptor: descriptor, registered: true},
				Endpoints: fakeEndpoints{
					endpoint: Endpoint{Client: tc.client(t), Resync: testResync},
					ready:    !tc.notReady,
				},
				Status: &fakeStatus{},
				Scheme: fakeScheme(t),
			}

			if _, err := engine.Reconcile(context.Background(), tc.object()); err != nil {
				t.Fatalf("Reconcile() = %v", err)
			}

			for _, result := range results {
				want := 0.0
				if result == tc.wantResult {
					want = 1.0
				}
				if got := reconciles.delta(kind, result); got != want {
					t.Errorf("reconcile_total{kind=%q,result=%q} moved by %v, want %v",
						kind, result, got, want)
				}
			}

			assertFields(t, "drift_detected_total", detected, kind, descriptor, tc.wantDrift)
			assertFields(t, "drift_corrected_total", corrected, kind, descriptor, tc.wantCorrected)

			if got := histogramCount(t, kind) - durations; got != 1 {
				t.Errorf("reconcile_duration_seconds{kind=%q} observed %d times, want 1", kind, got)
			}
		})
	}
}

// assertFields checks that exactly the named fields were counted, and that no other field
// the descriptor knows about was.
func assertFields(t *testing.T, metric string, counted *counters, kind string,
	descriptor registry.Descriptor, want []string,
) {
	t.Helper()

	wanted := map[string]bool{}
	for _, field := range want {
		wanted[field] = true
	}

	for _, field := range fieldNames(descriptor) {
		expect := 0.0
		if wanted[field] {
			expect = 1.0
		}
		if got := counted.delta(kind, field); got != expect {
			t.Errorf("%s{kind=%q,field=%q} moved by %v, want %v", metric, kind, field, got, expect)
		}
	}
}

// fieldNames is every API column the descriptor can produce a Change for, which is the
// bounded set the `field` label draws from.
func fieldNames(d registry.Descriptor) []string {
	names := make([]string, 0, len(d.Fields))
	for _, field := range d.Fields {
		names = append(names, field.API)
	}

	return names
}

func labelSets(first string, rest []string) [][]string {
	sets := make([][]string, 0, len(rest))
	for _, value := range rest {
		sets = append(sets, []string{first, value})
	}

	return sets
}

// counters holds the value of a set of series before an action, so the test can assert on
// what moved rather than on what the whole package has accumulated.
type counters struct {
	t      *testing.T
	vec    *prometheus.CounterVec
	before map[string]float64
}

func watch(t *testing.T, vec *prometheus.CounterVec, labelSets ...[]string) *counters {
	t.Helper()

	c := &counters{t: t, vec: vec, before: map[string]float64{}}
	for _, labels := range labelSets {
		// WithLabelValues creates the child at zero if it does not exist, which is what
		// makes "moved by 0" assertable for a series nothing has touched yet.
		c.before[seriesKey(labels)] = testutil.ToFloat64(vec.WithLabelValues(labels...))
	}

	return c
}

// delta is how far one series has moved since watch() was called.
func (c *counters) delta(labels ...string) float64 {
	c.t.Helper()

	return testutil.ToFloat64(c.vec.WithLabelValues(labels...)) - c.before[seriesKey(labels)]
}

func seriesKey(labels []string) string {
	key := ""
	for _, label := range labels {
		key += label + "\x00"
	}

	return key
}

// histogramCount is how many observations one histogram series holds. testutil has no
// equivalent of ToFloat64 for histograms, and asserting the duration is recorded at all
// is worth the six lines.
func histogramCount(t *testing.T, kind string) uint64 {
	t.Helper()

	observer, ok := metrics.ReconcileDuration.WithLabelValues(kind).(prometheus.Metric)
	if !ok {
		t.Fatalf("the reconcile duration histogram is not a prometheus.Metric")
	}

	var out dto.Metric
	if err := observer.Write(&out); err != nil {
		t.Fatalf("reading the reconcile duration histogram: %v", err)
	}

	return out.GetHistogram().GetSampleCount()
}
