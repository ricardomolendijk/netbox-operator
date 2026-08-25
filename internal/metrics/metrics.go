// Package metrics is the operator's entire Prometheus surface.
//
// Every metric lives in this one file so the set can be reviewed -- and its cardinality
// argued about -- at a glance. Registration goes through controller-runtime's registry
// rather than the default Prometheus one, so these appear on the manager's existing
// /metrics endpoint alongside the controller-runtime and Go runtime metrics, with no
// second HTTP server and no second scrape target.
//
// # Cardinality
//
// A label whose value set is unbounded turns a metric into an outage: every distinct
// value is a live time series in the scraper for as long as it is exported. Nothing here
// is labelled by object name or namespace. `method`, `code` and `result` are closed sets
// written down in code; the three dimensions that could conceivably grow are:
//
//   - kind -- one of the ~120 registered NetBox kinds (docs/concepts/descriptor.md).
//     Bounded by the code, not by the cluster.
//   - endpoint -- a NetBox REST path from registry.Descriptor.Endpoint, e.g.
//     `ipam/prefixes`, plus the literal `status` for the health probe. Bounded by the
//     same ~120 descriptors, one path each. Notably *not* the NetBoxEndpoint object's
//     name, which is user input.
//   - field -- a NetBox API column name from the descriptor's field map. NetBox models
//     carry a few tens of writable columns, so this is bounded by the schema.
//
// The per-metric comments state the worst case that follows.
package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	ctrlmetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
)

// Reconcile results, the value set of the `result` label on ReconcileTotal.
//
// Exactly one is recorded per reconcile, so sum(reconcile_total) is a count of
// reconciles. Adoption is deliberately absent: adopting is not a terminal state -- the
// same pass goes on to update or find no drift -- and it is reported by the `Adopted`
// Event and `status.adopted` instead.
const (
	// ResultCreated is a NetBox object that did not exist and was POSTed.
	ResultCreated = "created"

	// ResultUpdated is drift that was PATCHed away.
	ResultUpdated = "updated"

	// ResultRecreated is an object whose identity a PATCH cannot reach, deleted and
	// POSTed again (dcim.Cable is the case).
	ResultRecreated = "recreated"

	// ResultUnchanged is the steady state: NetBox already matched the spec and nothing
	// was sent. This is the bucket that should dominate a healthy cluster.
	ResultUnchanged = "unchanged"

	// ResultDeleted is an object removed from NetBox because its CR went away.
	ResultDeleted = "deleted"

	// ResultDryRun is drift found on a DryRun endpoint, deliberately not corrected.
	ResultDryRun = "dryrun"

	// ResultReported is drift found on a `driftMode: Report` endpoint, deliberately not
	// corrected.
	//
	// Its own bucket rather than folded into dryrun: the two are configured in different
	// fields and mean different things about intent -- DryRun is "this whole endpoint is
	// a rehearsal", Report is "this endpoint is live but drift is somebody else's to
	// fix" -- and a dashboard that cannot tell them apart cannot answer which one to
	// switch off (docs/decisions/0005-gitops-coexistence.md).
	ResultReported = "reported"

	// ResultWaiting is a reconcile that could not proceed and is not a failure: the
	// endpoint is not Ready yet, no natural key is usable yet, or onConflict is
	// AdoptOnly and there is nothing to adopt. Normal during a rollout.
	ResultWaiting = "waiting"

	// ResultError is a reconcile that failed: NetBox rejected the payload, was
	// unreachable, or holds an object this CR cannot claim.
	ResultError = "error"
)

// requestBuckets are latency buckets for one NetBox HTTP round trip. Weighted towards
// the fast end -- a healthy NetBox answers a filtered list in tens of milliseconds -- and
// extended to 30s because that is the client's default timeout, so the top bucket has to
// be able to contain a request that timed out.
var requestBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30}

// reconcileBuckets are latency buckets for one whole reconcile. Coarser than
// requestBuckets and starting later, because a reconcile is several round trips plus a
// status write and sub-10ms resolution would only measure the fake-client tests.
var reconcileBuckets = []float64{0.01, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10, 30, 60}

var factory = promauto.With(ctrlmetrics.Registry)

// ReconcileTotal counts object reconciles by kind and outcome.
//
// Cardinality: kind (~120) x result (9) = ~1080 series worst case. Bounded by the code:
// both label sets are compile-time constants.
var ReconcileTotal = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_reconcile_total",
	Help: "Object reconciles by kind and outcome.",
}, []string{"kind", "result"})

// ReconcileDuration measures how long one reconcile took, end to end.
//
// Cardinality: kind (~120) x (11 buckets + +Inf + sum + count) = ~1680 series worst case.
var ReconcileDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "netbox_operator_reconcile_duration_seconds",
	Help:    "Duration of one object reconcile, from entering the engine to its exit.",
	Buckets: reconcileBuckets,
}, []string{"kind"})

// DriftDetected counts fields found to differ between NetBox and the spec.
//
// Labelled by field because that is the single most useful diagnostic the operator has:
// one field spiking here is either a human fighting the operator, or a missing
// normalisation in internal/netbox/drift.go producing a diff that is never satisfied --
// a hot PATCH loop for as long as the object exists.
//
// Cardinality: kind (~120) x field (a few tens per model) = a few thousand series worst
// case. Bounded by the NetBox schema, since a field name can only come from a
// descriptor's field map.
var DriftDetected = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_drift_detected_total",
	Help: "Fields found to differ between NetBox and the desired spec.",
}, []string{"kind", "field"})

// DriftCorrected counts fields actually written back to NetBox.
//
// Separate from DriftDetected rather than a label on it, because the gap between the two
// is the whole signal in `driftMode: Report` and on a DryRun endpoint: drift is found
// and deliberately left alone (docs/decisions/0005-gitops-coexistence.md). One counter
// with a `corrected` label would make "reporting as configured" and "failing to write"
// the same shape on a dashboard.
//
// Cardinality: identical to DriftDetected.
var DriftCorrected = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_drift_corrected_total",
	Help: "Fields written back to NetBox to correct drift.",
}, []string{"kind", "field"})

// Conflicts counts reconciles that found another writer's provenance stamp on the NetBox
// object they were about to write to, and wrote to it anyway (NBO-047).
//
// A counter and not a gauge, which is the one place this deviates from what NBO-047 sketched.
// A gauge of "objects currently in conflict" has to be decremented when a conflict clears,
// and the only thing that knows a given object's conflict cleared is that object's own
// reconcile -- so keeping it accurate needs a series per object, which is a label carrying a
// namespace and a name and is exactly what TestNoUnboundedLabels forbids. A counter needs no
// such bookkeeping: `increase(netbox_operator_conflicts_total[1h]) > 0` is the "somebody must
// look" alert, it goes flat by itself when the overlap is fixed, and the per-object standing
// state is on the object as `status.conflict` where it can carry names.
//
// Deliberately *not* labelled by the other writer's cluster id either. That value is read out
// of a NetBox custom field, so it is user input by way of a third party: a garbage or hostile
// value there would mint a series per value. Who the other writer is lives in the condition,
// the Event and `status.conflict`; "how many objects does cluster X claim on this NetBox" is a
// question NetBox itself answers, with `?cf_k8s_cluster=X`
// (docs/operations/multi-writer.md).
//
// Cardinality: kind (~120) x reason (2, the Conflict condition's reasons, closed in code) =
// ~240 series worst case, and no series at all on a cluster with one writer.
var Conflicts = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_conflicts_total",
	Help: "Reconciles that found another cluster's or another CR's stamp on the NetBox object.",
}, []string{"kind", "reason"})

// APIRequests counts NetBox HTTP round trips by REST path, method and response class.
//
// One increment per attempt, so a retried request counts more than once -- that is
// deliberate: the retry is load NetBox actually saw.
//
// Cardinality: endpoint (~120) x method (4) x code (16, see StatusLabel) = 7680 series
// worst case, and in practice a few hundred, since one endpoint sees two or three
// methods and two or three status codes.
var APIRequests = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_api_requests_total",
	Help: "NetBox API requests by REST path, HTTP method and response status.",
}, []string{"endpoint", "method", "code"})

// APIRequestDuration measures one NetBox round trip, from sending the request to having
// read the response body. Client-side rate-limiter waiting is excluded, because that is
// the operator throttling itself and not NetBox being slow.
//
// Cardinality: endpoint (~120) x method (4) x (12 buckets + +Inf + sum + count) = 7200
// series worst case.
var APIRequestDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "netbox_operator_api_request_duration_seconds",
	Help:    "Latency of one NetBox API round trip.",
	Buckets: requestBuckets,
}, []string{"endpoint", "method"})

// EndpointReconcileTotal counts NetBoxEndpoint reconciles by their outcome, which is the
// condition reason the reconcile settled on: `Ready`, or one of the failure reasons in
// api/v1alpha1 (AuthError, TokenMissing, VersionUnsupported, ...).
//
// Not folded into ReconcileTotal, even though a NetBoxEndpoint is a kind: the result
// vocabularies have nothing in common, and a dashboard panel that mixes `unchanged` with
// `VersionUnsupported` is legible to nobody. Not labelled by endpoint namespace or name,
// which is why the reason carries the diagnosis instead.
//
// Cardinality: result (10) = 10 series, whatever the cluster does.
var EndpointReconcileTotal = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_endpoint_reconcile_total",
	Help: "NetBoxEndpoint reconciles by the condition reason they settled on.",
}, []string{"result"})

// SpecOwnershipUntracked counts reconciles of an object whose `metadata.managedFields`
// says nothing about its spec, so the engine had to read the user's intent off the Go zero
// value instead (NBO-079).
//
// It is the observability half of that fallback. On such an object an explicitly-empty
// string, bool or plain number cannot be told apart from an absent one, so clearing the
// field in Git changes nothing in NetBox and no condition disagrees -- exactly the silent
// failure the tri-state work exists to remove. Nonzero here means some client is writing
// these objects in a way that erases field ownership: a cache transform stripping
// managedFields, or an object restored from a backup that dropped them.
//
// Cardinality: kind (~120) = ~120 series worst case, and zero series on a healthy cluster,
// because the API server has tracked field ownership on every write since 1.18.
var SpecOwnershipUntracked = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_spec_ownership_untracked_total",
	Help: "Reconciles with no spec field ownership to read, falling back to non-zero fields only.",
}, []string{"kind"})

// Allocation results, for AllocationsTotal. Deliberately not the Result* set above: an
// allocation has outcomes a declarative reconcile does not have, and the two questions
// ("did this pass do anything" and "did this claim get an address") are answered by
// different labels on purpose.
const (
	// AllocationAllocated is an object NetBox handed out to a claim that had none.
	AllocationAllocated = "allocated"

	// AllocationReclaimed is an object found by allocation identity and adopted, so no
	// second one was allocated.
	//
	// Its own bucket rather than folded into allocated, because the ratio between the two
	// is the health signal: reclaims on a steady cluster mean claims are being re-created,
	// and a reclaim rate that tracks the reconcile rate means something is failing between
	// the POST and the status write.
	AllocationReclaimed = "reclaimed"

	// AllocationExhausted is a pool with nothing left to hand out.
	AllocationExhausted = "exhausted"

	// AllocationFailed is every other refusal: a pool the operator will not allocate out
	// of, an identity that resolved outside the pool, two objects sharing one identity, an
	// endpoint with nowhere to store an identity, or NetBox refusing the POST.
	AllocationFailed = "failed"
)

// AllocationsTotal counts allocation attempts by claim kind and outcome.
//
// There is deliberately **no free-address or utilisation gauge** beside it. An IPv6 /64 has
// 2^64 addresses, so a count is both impossible to obtain and misleading to publish, and
// the operator never asks NetBox how much of a pool is free -- exhaustion is detected only
// by the allocating POST's own 409 (docs/concepts/claims.md).
//
// Cardinality: kind (3 claim kinds) x result (4) = 12 series.
var AllocationsTotal = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_allocations_total",
	Help: "Allocation attempts by claim kind and outcome.",
}, []string{"kind", "result"})

// AllocationsRetained counts NetBox objects the operator stopped tracking because their
// claim was deleted, and did not delete.
//
// The metric half of the garbage-collection reporting path: the Event naming the object ages
// out of its namespace within the hour, and this does not, so "how many addresses has this
// cluster left behind" stays answerable
// (https://github.com/ricardomolendijk/netbox-operator/issues/182).
//
// Three things increment it and they are not equally normal, which is why the Event beside it
// carries the reason: spec.deletionPolicy Retain (a choice), a non-writing endpoint, and a
// Delete the operator gave up on after a bounded retry (a leak). Since #225 made Delete the
// default, an increment on a cluster that never writes Retain is the third one -- so a rate
// that is not zero there is worth alerting on rather than graphing.
var AllocationsRetained = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_allocations_retained_total",
	Help: "NetBox objects left behind, and reported, when their claim was deleted.",
}, []string{"kind"})

// ClientCacheSize is how many NetBox clients are cached, which is how many endpoints
// currently have a usable, authenticated, version-checked client. Object reconciles for
// an endpoint that is not in here can only wait, so this dropping is the earliest
// operator-side signal that writes have stopped.
//
// Cardinality: 1 series.
var ClientCacheSize = factory.NewGauge(prometheus.GaugeOpts{
	Name: "netbox_operator_client_cache_size",
	Help: "NetBox clients currently cached, one per endpoint with a usable client.",
})

// knownStatus is the set of HTTP statuses NetBox and the proxies in front of it actually
// return, and therefore the only ones reported verbatim.
var knownStatus = map[int]bool{
	200: true, 201: true, 204: true,
	400: true, 401: true, 403: true, 404: true, 409: true, 429: true,
	500: true, 502: true, 503: true, 504: true,
}

// StatusLabel renders an HTTP status for the `code` label.
//
// It is a closed set rather than the raw code, which is the cardinality guard on
// APIRequests: a misconfigured proxy in front of NetBox can emit any status it likes,
// and `code` multiplies against ~120 endpoints and 4 methods. A status NetBox is known to
// return is reported exactly, because 403-versus-404 is the entire diagnostic value of
// this metric; anything else collapses to its class. A code of 0 means the request never
// got a response at all.
func StatusLabel(code int) string {
	switch {
	case code == 0:
		return "error"
	case knownStatus[code]:
		return strconv.Itoa(code)
	case code >= 100 && code < 600:
		return strconv.Itoa(code/100) + "xx"
	default:
		return "error"
	}
}

// ObserveRequest records one NetBox round trip. A code of 0 means the request failed
// before a response arrived.
func ObserveRequest(endpoint, method string, code int, took time.Duration) {
	APIRequests.WithLabelValues(endpoint, method, StatusLabel(code)).Inc()
	APIRequestDuration.WithLabelValues(endpoint, method).Observe(took.Seconds())
}

// ObserveReconcile records the outcome and duration of one object reconcile.
func ObserveReconcile(kind, result string, took time.Duration) {
	ReconcileTotal.WithLabelValues(kind, result).Inc()
	ReconcileDuration.WithLabelValues(kind).Observe(took.Seconds())
}

// RefEnqueueTotal counts referrer reconcile requests produced by an event on a reference
// target: a target that gained a `status.id`, flipped `Ready`, started deleting or went
// away, and -- with `targetKind` set to `NetBoxRefGrant` -- a grant that authorised a
// reference which was denied a moment ago.
//
// It is the metric that says the watches are doing their job. A dependency graph applied
// in reverse order converges through these enqueues rather than through the resync, so a
// zero here while objects sit on `RefNotReady` means the watch, the field index or the
// predicate is wrong -- and the resync will hide it by eventually converging anyway.
//
// Cardinality: targetKind x referrerKind, and the product is not the bound. A series
// exists only for a pair the descriptor graph actually connects, which is one edge of
// NetBox's foreign-key graph: a few hundred pairs across the full catalogue, not ~120^2.
// Both halves are Kind names from a Descriptor, so neither is user input.
var RefEnqueueTotal = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_ref_enqueue_total",
	Help: "Referrer reconciles enqueued by an event on a reference target.",
}, []string{"targetKind", "referrerKind"})

// SweepFindings is how many NetBox objects the last NetBoxSweep run could not match to a
// live CR, by kind and by why.
//
// A gauge rather than a counter, because the question is "how many are outstanding right
// now", not "how many have ever been seen": the same orphan is found again by every run,
// and a counter would turn one leaked address into a rising line forever. Every scanned
// kind is set on every completed run, zeros included, so an orphan that gets adopted or
// deleted by hand is visible as the series returning to zero.
//
// A **refused** run does not touch it. Zeroing on refusal would report "no orphans" for the
// one state where the sweep could not see anything, which is the failure mode this whole
// feature is shaped around; the freshness signal is SweepRuns and the sweep's own
// `Ready` condition instead.
//
// Cardinality: kind (~120) x reason (3) = ~360 series worst case, both label sets bounded
// by the code. Deliberately **not** labelled by the sweep's namespace or name, which is
// user input -- with the consequence that two sweeps covering one kind in two namespaces
// write the same series and the last run wins. That is a real limitation and the reason
// `status.findings` and not this metric is the authoritative record; one sweep per kind per
// cluster is the configuration that makes the metric mean what it says
// (docs/operations/sweeps.md).
var SweepFindings = factory.NewGaugeVec(prometheus.GaugeOpts{
	Name: "netbox_operator_sweep_findings",
	Help: "NetBox objects the last completed sweep could not match to a live CR, by kind and reason.",
}, []string{"kind", "reason"})

// SweepRuns counts NetBoxSweep runs by the condition reason they settled on: `Complete`,
// or one of the refusal reasons in api/v1alpha1 (Truncated, EndpointDryRun, DriftOff, ...).
//
// It is the freshness half of SweepFindings. A findings gauge sitting at zero is either a
// clean cluster or a sweep that has been refused since the last time it could see anything,
// and the only way to tell from metrics alone is that `Complete` here has stopped
// increasing.
//
// Cardinality: result (10) = 10 series, whatever the cluster does.
var SweepRuns = factory.NewCounterVec(prometheus.CounterOpts{
	Name: "netbox_operator_sweep_runs_total",
	Help: "NetBoxSweep runs by the condition reason they settled on.",
}, []string{"result"})

// webhookBuckets are latency buckets for one admission review.
//
// An order of magnitude finer than requestBuckets at the fast end, because the budget being
// measured is an order of magnitude smaller: NBO-044 asks for p99 under 10ms for an object
// with ten references, so buckets that start at 5ms could not tell a pass from a failure. The
// top bucket is the webhook configuration's own `timeoutSeconds: 5`, so a review the API
// server gave up on is still containable.
var webhookBuckets = []float64{0.0005, 0.001, 0.0025, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 1, 5}

// WebhookDuration measures one admission review, end to end.
//
// Labelled by kind and operation and *not* by webhook name: there is one webhook serving the
// whole API group (internal/webhook/admission.Path), so a `webhook` label would be a constant
// and the interesting axis is which Kind is slow. Operation matters because an UPDATE carries
// an old object and a CREATE does not, so the two do not do the same work.
//
// Cardinality: kind (~120) x operation (2) x (11 buckets + +Inf + sum + count) = ~3360 series
// worst case. Both labels come from the AdmissionRequest's own TypeMeta and verb, neither of
// which is free-form user input.
var WebhookDuration = factory.NewHistogramVec(prometheus.HistogramOpts{
	Name:    "netbox_operator_webhook_duration_seconds",
	Help:    "Duration of one admission review, by kind and operation.",
	Buckets: webhookBuckets,
}, []string{"kind", "operation"})

// ObserveWebhook records one admission review.
func ObserveWebhook(kind, operation string, took time.Duration) {
	WebhookDuration.WithLabelValues(kind, operation).Observe(took.Seconds())
}
