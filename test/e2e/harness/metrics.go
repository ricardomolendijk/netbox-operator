package harness

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
)

// Counters is one scrape of the manager's counter metrics, keyed by metric name and then by
// the label set rendered as `k=v,k=v` in sorted label order.
type Counters map[string]map[string]float64

// mutatingMethods are the HTTP methods that change NetBox. Reads are not economy-relevant
// and are not counted: a convergence that costs many GETs is fine, one that costs many
// PATCHes is churn.
var mutatingMethods = []string{http.MethodPost, http.MethodPatch, http.MethodDelete}

// Scrape reads the manager's /metrics and decodes every counter.
//
// Prometheus text format through expfmt rather than string matching: the label order in the
// exposition is not guaranteed and a grep-based reader would silently miss a series.
func (op *Operator) Scrape(ctx context.Context) (Counters, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, op.MetricsURL, nil)
	if err != nil {
		return nil, fmt.Errorf("building a request for %s: %w", op.MetricsURL, err)
	}

	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, fmt.Errorf("scraping %s: %w", op.MetricsURL, err)
	}
	defer func() { _ = response.Body.Close() }()

	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("scraping %s: unexpected status %s", op.MetricsURL, response.Status)
	}

	// NewTextParser and not a zero-value TextParser: prometheus/common v0.70 gave the parser
	// an unexported validation scheme whose zero value is invalid, and parsing with it panics
	// with "Invalid name validation scheme requested: unset" rather than returning an error.
	// UTF8Validation is what the package's own NameValidationScheme global defaults to, so
	// this asks for the behaviour the harness already had.
	parser := expfmt.NewTextParser(model.UTF8Validation)
	families, err := parser.TextToMetricFamilies(response.Body)
	if err != nil {
		return nil, fmt.Errorf("parsing the exposition from %s: %w", op.MetricsURL, err)
	}

	counters := Counters{}
	for name, family := range families {
		for _, metric := range family.GetMetric() {
			if metric.GetCounter() == nil {
				continue
			}
			if counters[name] == nil {
				counters[name] = map[string]float64{}
			}
			counters[name][labelKey(metric.GetLabel())] = metric.GetCounter().GetValue()
		}
	}
	return counters, nil
}

// labelKey renders a metric's labels as a comparable key. Sorted, because the exposition
// format guarantees no label order and an unsorted key would split one series in two.
func labelKey(labels []*dto.LabelPair) string {
	pairs := make([]string, 0, len(labels))
	for _, label := range labels {
		pairs = append(pairs, label.GetName()+"="+label.GetValue())
	}
	sort.Strings(pairs)
	return strings.Join(pairs, ",")
}

// Sum totals every series of a metric whose label set matches all of match.
func (c Counters) Sum(name string, match map[string]string) float64 {
	var total float64
	for key, value := range c[name] {
		if matchesLabels(key, match) {
			total += value
		}
	}
	return total
}

func matchesLabels(key string, match map[string]string) bool {
	pairs := map[string]bool{}
	for _, pair := range strings.Split(key, ",") {
		pairs[pair] = true
	}
	for name, value := range match {
		if !pairs[name+"="+value] {
			return false
		}
	}
	return true
}

// Mutations totals the NetBox POSTs, PATCHes and DELETEs the manager has made.
//
// The write-economy and quiescence numbers both come from here. A counter is monotonic
// across a manager's lifetime, so both assertions take a difference rather than a value --
// and a manager restart resets it, which is why the restart run cannot assert on it.
func (c Counters) Mutations() float64 {
	var total float64
	for _, method := range mutatingMethods {
		total += c.Sum("netbox_operator_api_requests_total", map[string]string{"method": method})
	}
	return total
}

// MutationBreakdown is the same figure per method, for a failure message that says which
// kind of write ran away.
func (c Counters) MutationBreakdown() string {
	parts := make([]string, 0, len(mutatingMethods))
	for _, method := range mutatingMethods {
		parts = append(parts, fmt.Sprintf("%s=%.0f", method,
			c.Sum("netbox_operator_api_requests_total", map[string]string{"method": method})))
	}
	return strings.Join(parts, " ")
}

// ReconcileErrors is the number of reconciles that ended on an error path. NBO-017 requires
// it to be zero for a whole passing run: a waiting state is not an error, and an object that
// reached Ready through one hid a bug.
func (c Counters) ReconcileErrors() float64 {
	return c.Sum("netbox_operator_reconcile_total", map[string]string{"result": "error"})
}

// Reconciles is the total reconcile count, all results. The cycle run bounds it: two objects
// in a cycle must not turn into a requeue storm.
func (c Counters) Reconciles() float64 {
	var total float64
	for _, value := range c["netbox_operator_reconcile_total"] {
		total += value
	}
	return total
}

// WaitQuiet asserts that no mutating NetBox request happens for the whole of window.
//
// The anti-hot-loop assertion at system level, and the reason an end-state-only check is not
// enough: a resolver that converges by re-PATCHing forever passes every other assertion in
// this suite.
func (op *Operator) WaitQuiet(ctx context.Context, window time.Duration) (float64, error) {
	before, err := op.Scrape(ctx)
	if err != nil {
		return 0, err
	}

	select {
	case <-ctx.Done():
		return 0, fmt.Errorf("waiting out the quiet window: %w", ctx.Err())
	case <-time.After(window):
	}

	after, err := op.Scrape(ctx)
	if err != nil {
		return 0, err
	}
	return after.Mutations() - before.Mutations(), nil
}
