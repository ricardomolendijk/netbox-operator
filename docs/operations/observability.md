# Observability

A controller has no end of run, so there is no final summary to read. Three surfaces
answer the three questions instead:

| Question | Surface |
|---|---|
| Is it working? | **Metrics** — `/metrics`, scraped |
| What did it change? | **Events** — `kubectl describe`, and the `changes` key in the log |
| Why is *this* object stuck? | **Conditions**, then the log filtered to that object |

This page is written for whoever builds the dashboard. Every metric below is declared in
one file, [`internal/metrics/metrics.go`](../../internal/metrics/metrics.go), so the
exported set and its cardinality can be reviewed in one read.

## Enabling the endpoint

Metrics are registered on controller-runtime's registry, so they are served by the
manager's own metrics server alongside the `controller_runtime_*`, `workqueue_*` and Go
runtime metrics. There is no second port and no second scrape target.

The manager binds nothing by default — `--metrics-bind-address` defaults to `0`:

```sh
/manager --metrics-bind-address=:8443     # HTTPS, self-signed certificate
/manager --metrics-bind-address=:8080 --metrics-secure=false
```

With `--metrics-secure` (the default) the server presents a self-signed certificate and
does **not** apply an authn/authz filter, so a scraper needs `insecureSkipVerify: true`
and the port must not be reachable outside the cluster. The shipped Deployment in
`config/manager/` passes neither flag, so a kustomize install needs the flag and the scrape
target in an overlay of its own.

The **Helm chart** passes them: `metrics.enabled` (on by default) adds
`--metrics-bind-address` and a `ClusterIP` `Service`, and `metrics.serviceMonitor.enabled`
adds a `ServiceMonitor` — gated on `monitoring.coreos.com/v1` existing, so asking for one on
a cluster without the Prometheus Operator is a skipped resource and a line in `NOTES.txt`
rather than a failed install. It sets `insecureSkipVerify` on the scrape when
`metrics.secure` is on, because of the self-signed certificate above. See
[installing](../install.md#values).

## Metrics

Nine metrics. Nothing is exported that nobody would look at — a metric with no question
behind it is a series budget spent on noise.

### `netbox_operator_reconcile_total`

| | |
|---|---|
| Type | Counter |
| Labels | `kind`, `result` |
| Cardinality | ~120 kinds × 9 results = **1080** worst case |

Exactly one increment per object reconcile, so `sum(netbox_operator_reconcile_total)` is a
count of reconciles and anything can be divided by it.

| `result` | Meaning |
|---|---|
| `created` | The NetBox object did not exist and was POSTed. |
| `updated` | Drift was PATCHed away. |
| `recreated` | Identity a PATCH cannot reach: deleted and POSTed again (`dcim.Cable`). |
| `unchanged` | NetBox already matched the spec; nothing was sent. **Should dominate.** |
| `deleted` | The CR went away and the NetBox object with it. |
| `dryrun` | Drift found on a `DryRun` endpoint, deliberately not corrected. |
| `reported` | Drift found on a `driftMode: Report` endpoint, deliberately not corrected. Separate from `dryrun` because the two are set in different fields and mean different things about intent: `DryRun` is "this whole endpoint is a rehearsal", `Report` is "this endpoint is live and drift is somebody else's to fix". |
| `waiting` | Endpoint not `Ready`, no usable natural key yet, `AdoptOnly` with nothing to adopt, or the status write lost a race with another pass of the same object — see [a cached read is not a conflict](../concepts/errors-and-retries.md#a-cached-read-is-not-a-conflict). Normal during a rollout. |
| `error` | NetBox rejected the payload, was unreachable, or holds an object this CR may not claim. |

Adoption is not a result: it is not a terminal state — the same pass goes on to update or
find no drift — and it is reported by the `Adopted` Event and `status.adopted`.

**Alert on:** `error` as a share of the total, over a window long enough to ride out a
NetBox restart.

```promql
sum(rate(netbox_operator_reconcile_total{result="error"}[15m]))
  / sum(rate(netbox_operator_reconcile_total[15m])) > 0.05
```

**Graph:** stacked `rate()` by `result`. A healthy cluster is a wall of `unchanged` with
occasional `created`/`updated`. `waiting` that never drains is a stuck graph, and a steady
band of `reported` or `dryrun` is an endpoint that is not converging by configuration — see
[gitops](gitops.md).

### `netbox_operator_reconcile_duration_seconds`

| | |
|---|---|
| Type | Histogram (11 buckets, 10ms → 60s) |
| Labels | `kind` |
| Cardinality | ~120 kinds × 14 series per histogram = **1680** worst case |

One whole reconcile: entering the engine to its exit, including the NetBox round trips and
the status write.

**Alert on:** nothing directly. Slow reconciles matter through the workqueue, and
`workqueue_queue_duration_seconds` (controller-runtime's own) is the metric that says work
is not keeping up.

**Graph:** `histogram_quantile(0.99, ...)` by `kind`. A `kind` whose p99 is seconds is
usually one doing an unfiltered list.

### `netbox_operator_drift_detected_total` and `netbox_operator_drift_corrected_total`

| | |
|---|---|
| Type | Counter (both) |
| Labels | `kind`, `field` |
| Cardinality | ~120 kinds × a few tens of columns = **a few thousand** each, worst case |

`field` is a NetBox API column name — `status`, `description`, `scope_id`. Bounded by the
NetBox schema, because it can only come from a
[Descriptor](../concepts/descriptor.md)'s field map.

Two counters rather than one with a `corrected` label, because the **gap between them is
the signal**. On a `DryRun` endpoint, and under `driftMode: Report`
([ADR-0005](../decisions/0005-gitops-coexistence.md), [gitops](gitops.md)), drift is found
and deliberately left alone. One counter would make "reporting exactly as configured" and
"failing to write" the same shape on a dashboard.

**Alert on:** a single field correcting over and over. That is either a human fighting the
operator, or a missing normalisation in
[drift detection](../concepts/drift.md) producing a diff that is never satisfied — a hot
PATCH loop against the NetBox API for as long as the object exists.

```promql
sum by (kind, field) (rate(netbox_operator_drift_corrected_total[10m])) > 0.05
```

**Graph:** `detected - corrected` by `kind`. Non-zero and flat is `Report` mode working.
Non-zero and climbing on an `Apply` endpoint means writes are failing; cross-check
`reconcile_total{result="error"}`.

### `netbox_operator_api_requests_total`

| | |
|---|---|
| Type | Counter |
| Labels | `endpoint`, `method`, `code` |
| Cardinality | ~120 paths × 4 methods × 16 codes = **7680** worst case; a few hundred in practice |

`endpoint` is the NetBox REST path — `ipam/prefixes`, `dcim/devices`, plus the literal
`status` for the health probe. It is **not** the `NetBoxEndpoint` object's name; see
[Cardinality](#cardinality) below.

`code` is a closed set: the statuses NetBox and its proxies actually return (`200`, `201`,
`204`, `400`, `401`, `403`, `404`, `409`, `429`, `500`, `502`, `503`, `504`) reported
verbatim, anything else collapsed to its class (`3xx`, `4xx`, `5xx`), and `error` for a
request that never got a response at all.

One increment per **attempt**, so a retried request counts more than once. That is
deliberate: the retry is load NetBox actually saw.

**Alert on:**

```promql
sum(rate(netbox_operator_api_requests_total{code=~"401|403"}[5m])) > 0
```

A 401 or 403 is a revoked token or a missing NetBox permission. It never fixes itself, it
fails the whole `NetBoxEndpoint` rather than one object, and every write behind that
endpoint has stopped. Page on it.

```promql
sum(rate(netbox_operator_api_requests_total{code=~"5xx|500|502|503|504|error"}[10m])) > 1
```

NetBox is unhealthy or unreachable. Page if sustained; the operator retries and objects
stay `Ready=False` with `APIError` meanwhile.

**Graph:** `rate()` by `code`, and `rate()` by `endpoint` to see which kind is generating
the traffic. `429` climbing means the endpoint's `spec.rateLimit` is above what NetBox
will take.

### `netbox_operator_api_request_duration_seconds`

| | |
|---|---|
| Type | Histogram (12 buckets, 5ms → 30s) |
| Labels | `endpoint`, `method` |
| Cardinality | ~120 paths × 4 methods × 15 series per histogram = **7200** worst case |

One NetBox round trip, from sending the request to having read the response body.
Client-side rate-limiter waiting is **excluded** — that is the operator throttling itself,
not NetBox being slow, and conflating the two hides both.

The top bucket is 30s because that is the client's default timeout, so a request that
timed out is still counted somewhere.

**Alert on:** p99 approaching the endpoint's `spec.timeout`, which is the point at which
reconciles start failing rather than being slow.

```promql
histogram_quantile(0.99,
  sum by (le, endpoint) (rate(netbox_operator_api_request_duration_seconds_bucket[5m]))) > 10
```

**Graph:** p50/p99 by `endpoint`. A single path much slower than the rest is usually an
unfiltered list.

### `netbox_operator_endpoint_reconcile_total`

| | |
|---|---|
| Type | Counter |
| Labels | `result` |
| Cardinality | **10** series, whatever the cluster does |

`NetBoxEndpoint` reconciles, labelled by the condition reason they settled on: `Ready`, or
one of `SecretMissing`, `CABundleMissing`, `TokenMissing`, `AuthError`, `ProbeFailed`,
`VersionUnsupported`, `VersionUnparseable`, `InvalidConfig`.

Kept separate from `reconcile_total` even though a `NetBoxEndpoint` is a kind: the two
result vocabularies have nothing in common, and a panel that mixes `unchanged` with
`VersionUnsupported` is legible to nobody. It is not labelled by the endpoint's namespace
or name, which is why the *reason* carries the diagnosis — `kubectl get netboxendpoints -A`
says which one.

**Alert on:** any non-`Ready` result, which the reason then names.

```promql
sum by (result) (rate(netbox_operator_endpoint_reconcile_total{result!="Ready"}[10m])) > 0
```

### `netbox_operator_client_cache_size`

| | |
|---|---|
| Type | Gauge |
| Labels | none |
| Cardinality | **1** series |

How many NetBox clients are cached — that is, how many endpoints currently have a usable,
authenticated, version-checked client. An object reconcile whose endpoint is not in here
can only wait, so this dropping is the earliest operator-side sign that writes have
stopped.

**Alert on:** the single highest-value alert in this document.

```promql
netbox_operator_client_cache_size == 0
```

Zero means nothing can be written to NetBox at all. Compare against the number of
`NetBoxEndpoint` objects (from kube-state-metrics) to catch "three of four endpoints are
up", which is invisible in the absolute number.

### `netbox_operator_ref_enqueue_total`

| | |
|---|---|
| Type | Counter |
| Labels | `targetKind`, `referrerKind` |
| Cardinality | one series per pair the schema connects — a few hundred, not ~120² |

Referrer reconciles woken by an event on the object they reference: a target that gained a
`status.id`, flipped `Ready`, started deleting or went away — and, with
`targetKind="NetBoxRefGrant"`, a grant that authorised a reference which was being denied
([references](../concepts/references.md#ordering-and-convergence)).

This is how you tell that a dependency graph is converging on events rather than on the
resync. A manifest applied in reverse order produces a burst here and then goes quiet; a
graph that is only ever converging on `resyncPeriod` produces nothing at all while objects
sit on `RefsResolved=False`.

Both labels are Kind names from a `Descriptor`, so neither is user input, and a series
exists only for a `(target, referrer)` pair that some kind's field map actually declares.

**Alert on:** nothing, on its own — a busy cluster and an idle one both look reasonable. It
is a diagnostic to read next to `netbox_operator_reconcile_total{result="waiting"}`: waiting
objects and no enqueues is a watch that is matching nothing.

### `netbox_operator_allocations_total`

| | |
|---|---|
| Type | counter |
| Labels | `kind`, `result` |
| Cardinality | 3 claim kinds x 4 results = 12 series |

Allocation attempts by claim kind and outcome. `result` is one of:

| `result` | Meaning |
|---|---|
| `allocated` | NetBox handed out an object to a claim that had none |
| `reclaimed` | an object carrying the claim's allocation identity was found and adopted, so nothing was allocated |
| `exhausted` | the pool had nothing left |
| `failed` | every other refusal: a pool the operator will not allocate out of, an identity resolving outside its pool, two objects sharing one identity, no identity store, or NetBox refusing the POST |

The **ratio** between the first two is the signal, not either count. `reclaimed` is expected in
bursts — a cluster rebuilt from Git reclaims every address it had — and expected to be near
zero otherwise. A steady `reclaimed` rate that tracks the reconcile rate means something is
failing between the POST and the status write, and each of those passes is a claim that got its
address back rather than a duplicate, which is the mechanism working and the underlying fault
worth finding.

```promql
# Reclaims outside a rebuild: the crash-recovery path is being exercised in steady state.
sum(rate(netbox_operator_allocations_total{result="reclaimed"}[30m])) > 0
```

```promql
# Somebody's pool is full. Each claim retries every 10m, so this is a low, flat rate.
sum by (kind) (rate(netbox_operator_allocations_total{result="exhausted"}[30m])) > 0
```

There is deliberately **no free-address or utilisation gauge** beside this. An IPv6 `/64` has
2^64 addresses, so the operator never asks NetBox how much of a pool is free: exhaustion is
detected only by the allocating POST's own `409`, and a number that is both expensive to obtain
and misleading to publish is worse than no number ([claims](../concepts/claims.md)).

### `netbox_operator_allocations_retained_total`

| | |
|---|---|
| Type | counter |
| Labels | `kind` |
| Cardinality | 3 series |

NetBox objects the operator stopped tracking because their claim was deleted, and did not
delete. This is the metric half of reporting them: the `AddressRetained` Event ages out of its
namespace within the hour, and "how many addresses has this cluster left behind" has to stay
answerable afterwards.

Three things increment it, and they are not equally normal
([#225](https://github.com/ricardomolendijk/netbox-operator/issues/225)):

| Cause | How normal |
|---|---|
| `spec.deletionPolicy: Retain` | a deliberate choice. `Normal`/`AddressRetained`. |
| a non-writing endpoint (`driftMode: Report`, `mode: DryRun`) | expected for that endpoint. `Warning`/`AddressRetained`. |
| a `Delete` the operator **gave up on** after 8 failed attempts | a leak nobody asked for. `Warning`/`AddressRetained`, and always worth looking at. |

Not an alert on its own — retaining on purpose is a normal thing to do — but worth graphing
next to `allocations_total{result="allocated"}`, and worth alerting on when the cluster uses the
default `Delete` everywhere, because then every increment is the third row. A cluster that
retains as fast as it allocates is leaking addresses into NetBox at that rate, and each one is
findable by its `cf_k8s_allocation_identity`.

```promql
# Retained addresses over the last day, by claim kind.
increase(netbox_operator_allocations_retained_total[1d])
```

### `netbox_operator_conflicts_total`

| | |
|---|---|
| Type | counter |
| Labels | `kind`, `reason` |
| Cardinality | ~240 series worst case, and **no series at all** on a cluster with one writer |

Reconciles that found another writer's provenance stamp on the NetBox object they were about to
write to — another cluster's (`reason="ForeignCluster"`) or another CR's in this one
(`reason="ForeignOwner"`) — and wrote to it anyway. The operator does not serialise writes
between writers and will not
([#18](https://github.com/ricardomolendijk/netbox-operator/issues/18)); this counter, the
`Conflict` condition and `status.conflict` are the whole of what it does instead
([multi-writer](multi-writer.md)).

**Alert on a window, never on an instant**, and give it a `for:` of at least one resync period.
A conflict during a rolling migration or a cluster rebuild is expected and transient, and an
alert that fires on those is an alert that gets muted.

```promql
# Somebody must look: two writers are still taking turns over the same object.
sum by (kind, reason) (increase(netbox_operator_conflicts_total[1h])) > 0
```

A counter rather than a gauge of "objects currently in conflict", deliberately: such a gauge
has to be decremented when a conflict clears, only that object's own reconcile knows it
cleared, and keeping it accurate therefore needs a series per object — a label carrying a
namespace and a name, which is exactly what the cardinality rule below forbids. It is not
labelled by the other writer's cluster id either: that value is read out of a NetBox custom
field, so its cardinality is a third party's to decide. **Who** the other writer is lives on the
object, in `status.conflict.owner` and `status.conflict.clusterID`.

### `netbox_operator_spec_ownership_untracked_total`

| | |
|---|---|
| Type | Counter |
| Labels | `kind` |
| Cardinality | ~120 series worst case, and **zero on a healthy cluster** |

Reconciles of an object whose `metadata.managedFields` says nothing about its spec, so the
operator had to read the user's intent off the Go zero value instead
([field ownership](../concepts/field-ownership.md)).

On such an object an explicitly-empty string, list, map, `false` or `0` cannot be told apart
from an absent one, so **clearing that field in Git changes nothing in NetBox and no condition
disagrees.** The operator keeps working — every non-empty field is still managed — but one
third of the API is quietly unavailable on those objects.

It should never fire. The API server has recorded field ownership on every write since
Kubernetes 1.18, so a non-zero rate means something between the API server and the engine is
erasing it: a `cache.Options.DefaultTransform` stripping `managedFields` to save memory is the
usual cause, and a restore that dropped them is the other.

**Alert on:** any of it.

```promql
sum by (kind) (rate(netbox_operator_spec_ownership_untracked_total[15m])) > 0
```

### `netbox_operator_sweep_findings`

| | |
|---|---|
| Type | Gauge |
| Labels | `kind`, `reason` (`Orphaned`, `Suspected`, `Unattributed`) |
| Cardinality | ~120 kinds × 3 reasons = **360** worst case |

NetBox objects the last completed [`NetBoxSweep`](sweeps.md) run could not match to a live
CR: the stamped objects this cluster has left behind.

A gauge and not a counter, because the question is how many are outstanding *now* and the
same orphan is found again by every run — a counter would turn one leaked address into a
rising line forever. Every scanned kind is set on every completed run, **zeros included**, so
an orphan somebody adopts or deletes by hand shows up as the series returning to zero and an
alert on it clears by itself.

A **refused** run does not touch it. Zeroing on refusal would report "no orphans" for the one
state in which the sweep could not see anything, which is the failure mode the whole feature
is shaped around.

**Alert on:** orphans existing at all. Not urgent, and not nothing.

```promql
sum by (kind) (netbox_operator_sweep_findings{reason="Orphaned"}) > 0
```

> **Limitation.** Not labelled by the sweep's namespace or name, because those are user input
> and nothing in this document is labelled by user input. The consequence is that two sweeps
> covering one kind in two namespaces write the same series and the last run wins.
> `NetBoxSweep.status.findings` is the authoritative record; one sweep per kind per cluster is
> the configuration that makes this metric mean what it says.

### `netbox_operator_sweep_runs_total`

| | |
|---|---|
| Type | Counter |
| Labels | `result` |
| Cardinality | **10** series, whatever the cluster does |

Sweep runs by the condition reason they settled on: `Complete`, or one of the refusal reasons
(`EndpointDryRun`, `DriftOff`, `ProvenanceDisabled`, `Truncated`, `Timeout`, …). The full
table is in [sweeps.md](sweeps.md#when-a-sweep-refuses-to-run).

It is the freshness half of `netbox_operator_sweep_findings`. A findings gauge sitting at zero
is either a clean cluster or a sweep that has been refused since the last time it could see
anything, and from metrics alone the only way to tell them apart is that `Complete` here has
stopped increasing.

**Alert on:** a sweep that has not completed a run in two intervals, and on repeated
refusals.

```promql
increase(netbox_operator_sweep_runs_total{result="Complete"}[2d]) == 0

sum by (result) (rate(netbox_operator_sweep_runs_total{result!="Complete"}[1h])) > 0
```

`Truncated` is the one to page on rather than to graph: it means a list paginated past the
client's page cap, so the operator saw a partial set of NetBox — and a partial set makes live
objects look absent.

### `netbox_operator_webhook_duration_seconds`

| | |
|---|---|
| Type | Histogram |
| Labels | `kind`, `operation` |
| Buckets | 0.5ms, 1ms, 2.5ms, 5ms, 10ms, 25ms, 50ms, 100ms, 250ms, 1s, 5s |
| Cardinality | ~120 x 2 x 14 = ~3360 series worst case |

One admission review by the validating webhook, end to end
([the admission webhook](admission-webhooks.md)).

Not labelled by webhook name, because there is one webhook serving the whole API group; the
interesting axis is which Kind is slow. `operation` is `CREATE` or `UPDATE`, which do not do the
same work — an update carries an old object and a create does not.

This is the instrument the whole design of the webhook hangs off. Its budget is **p99 under
10ms** for an object with ten references, and it is met by reading cached objects and never
NetBox: an admission path that started making a live read would show up here as a p99 in the
tens of milliseconds long before anybody noticed the API server's own latency had gone into
every apply. The webhook's own `timeoutSeconds` is 5, which is the top bucket, so a review the
API server gave up on is still containable.

A review that reaches the timeout is *not* an error for the applier —
`failurePolicy: Ignore` admits the object — so this metric and nothing else is how a slow
webhook becomes visible.

**Alert on:** p99 above 100ms, which is an order of magnitude over budget and a fifth of the
way to the timeout.

```promql
histogram_quantile(0.99,
  sum by (le, kind) (rate(netbox_operator_webhook_duration_seconds_bucket[5m]))) > 0.1
```

## Cardinality

A label whose value set is unbounded turns a metric into an outage: every distinct value
is a live series in the scraper for as long as it is exported, and nothing evicts it.

Nothing here is labelled by object name, namespace, UID or message. The label dimensions
are:

| Label | Bounded by | Size |
|---|---|---|
| `kind` | The registered kinds, a compile-time set | ~120 |
| `result` | Constants in `internal/metrics` and the condition reasons in `api/v1alpha1` | 8 / 10 |
| `endpoint` | `Descriptor.Endpoint` — one NetBox REST path per kind | ~120 |
| `method` | HTTP verbs the client uses | 4 |
| `code` | A closed set, unexpected statuses collapsed to their class | 16 |
| `field` | NetBox API column names, from a Descriptor's field map | tens per model |
| `reason` | The `Conflict` condition's reasons, constants in `api/v1alpha1` | 2 |
| `targetKind`, `referrerKind` | Kind names from a Descriptor, paired only where a field map declares a reference | a few hundred pairs |
| `reason` | The sweep finding reasons in `api/v1alpha1` | 3 |
| `operation` | The admission verbs the webhook registers | 2 |

Worst case, with every kind implemented and every path and status exercised, is roughly
**25 000 series**. Realistically a cluster using a handful of kinds against one NetBox
exports a few hundred.

### Why `endpoint` is a label and the endpoint's *name* is not

`endpoint` looks like the risky one, so it is worth being explicit: it is the NetBox REST
path from the Descriptor (`ipam/prefixes`), not anything a user typed. There is exactly one
path per registered kind, so it is bounded by the same ~120 as `kind` and grows only when
this repository adds a kind.

The `NetBoxEndpoint` **object's** namespace and name are the opposite: user input, with no
ceiling. Labelling by them would also be the wrong shape — a per-endpoint series is
answered better by `kube_customresource_*` over the CR's own conditions, which is where
per-object state belongs. `endpoint_reconcile_total{result}` carries the diagnosis instead,
and `kubectl get netboxendpoints -A` carries the identity.

This is enforced, not reviewed: `TestNoUnboundedLabels` gathers the registry and fails on
any label outside the allowed set, so adding one is a deliberate act with a cardinality
argument attached.

## Events

Events are what a user sees in `kubectl describe` without knowing conditions exist. They
mark **transitions**. They are not a log: an Event per resync fills the namespace with the
same line and buries whatever actually happened.

They are written to `events.k8s.io/v1`, which is what the tables below mean by an **action**
next to the reason.

### Reason, action, and note

Three fields, and they answer different questions.

- **Reason** is *how it turned out*: `Created`, `AuthError`, `PoolExhausted`. It is the same
  vocabulary as the condition reasons, deliberately, so the two cannot drift apart.
- **Action** is *what the operator was doing*: `Create`, `Probe`, `Allocate`. It is the same
  vocabulary as the `action` key on the log line beside the Event, so a reader has one set
  of verbs rather than two. Success and failure of one operation share an action — `Probe`
  covers both `Ready` and `AuthError`, and `Delete` covers every way a deletion can end.
- **Note** is the human-readable sentence, and the only one of the three that is not a
  contract. It is capped at 1024 characters by the API server; a longer one is cut and
  marked `[truncated]`, and the full detail is in the object's conditions.

`kubectl describe` shows the reason and the note. The action needs a wider read:

```console
$ kubectl get events.events.k8s.io -n homelab \
    -o custom-columns='REASON:.reason,ACTION:.action,OBJECT:.regarding.name'
REASON              ACTION        OBJECT
Ready               Probe         homelab
Created             Create        rack-a1
ChildMaterialised   Materialise   vm-web
```

**Events for the same reason, action and object are aggregated into a series** for about
six minutes, and the note is *not* part of what makes two Events the same. Two writes to one
object inside that window therefore show as one Event with a count and the **first** note.
Where the distinction matters the Event names a second object: a materialised or pruned
child goes in the Event's `related` field, so one parent materialising six children produces
six Events rather than one saying `(x6)`. For everything else the standing detail is in
`status.conditions`, which is why the Event never had to carry it.

### On a `NetBoxEndpoint`

Emitted only when the `Ready` condition's status or reason changes, so a NetBox that has
been unreachable for a week produces one Event, not one every thirty seconds. The Event
reason is the condition reason, so the two vocabularies cannot drift apart.

| Reason | Type | Action | Fires when |
|---|---|---|---|
| `Ready` | Normal | `Probe` | The endpoint becomes usable: token accepted, version in range, client cached. |
| `AuthError` | Warning | `Probe` | NetBox returned 401 or 403 for the status probe. |
| `TokenMissing` | Warning | `Probe` | The Secret exists but has no value under the referenced key. |
| `SecretMissing` | Warning | `Probe` | The token or CA-bundle Secret is not readable — often because it is not labelled `netbox.kubeforge.org/endpoint-credential=true`. |
| `CABundleMissing` | Warning | `Probe` | `spec.tlsConfig.caBundleSecretRef` points at a Secret that is not there. |
| `ProbeFailed` | Warning | `Probe` | NetBox was unreachable, timed out, or answered something unusable. |
| `VersionUnsupported` | Warning | `Probe` | NetBox is outside `>=4.2, <5.0`. |
| `VersionUnparseable` | Warning | `Probe` | `/api/status/` returned something that is not a version. |
| `InvalidConfig` | Warning | `Probe` | The spec cannot produce a client: bad URL, unusable CA bundle. |
| `Provisioned` | Normal | `Bootstrap` | The provenance tag and custom fields were created or widened in NetBox. |
| `BootstrapFailed`, `BootstrapDisabled` | Warning | `Bootstrap` | The bootstrap could not finish, so no object controller is handed this endpoint. |

An Event or a condition never quotes an upstream response body. It reports the status code,
the media type, the length, and NetBox's own `detail` string when the body carries one — the
body itself is on the manager's log at `-v=1`, because these two surfaces are readable by
whoever chose the host that produced it
([#298](https://github.com/ricardomolendijk/netbox-operator/issues/298)).

```console
$ kubectl describe netboxendpoint homelab
...
Events:
  Type     Reason      Age   From                       Message
  ----     ------      ----  ----                       -------
  Warning  AuthError   12m   netboxendpoint-controller  netbox authentication or permission failure (401): "Invalid token." (application/json, 27 bytes)
  Normal   Ready       2m    netboxendpoint-controller  netbox 4.6.8 at https://netbox.example.com accepted the token; client available
```

### On an object CR

Emitted by the engine when it writes, or when it refuses to.

| Reason | Type | Action | Fires when |
|---|---|---|---|
| `Created` | Normal | `Create` | A new NetBox object was POSTed. |
| `Adopted` | Normal | `Adopt` | An existing NetBox object matched the natural key and `spec.onConflict` permitted taking it over. Names the id and the key that matched. |
| `Updated` | Normal | `Update` | Fields were PATCHed. The message is the aligned `field: old → new` diff. |
| `Recreated` | Normal | `Recreate` | The object was deleted and POSTed again because its identity is not PATCHable. |
| `Conflict` | Warning | `Claim` | NetBox holds an object this CR cannot safely claim: more than one natural-key match, an unadoptable match, or a protected relation. |
| `Invalid` | Warning | `Write` | NetBox rejected the payload, or the spec cannot be rendered into one. |
| `DriftDetected` | Normal | `ReportDrift` | Drift was found on a `driftMode: Report` endpoint and deliberately left alone. The message is the same `field: old → new` diff, prefixed `report only: would have written …`. |
| `Conflict` | Warning | `Claim` | *(also)* The live NetBox object carries another cluster's or another CR's provenance stamp. Names the claimant and the manifest to edit. Fires once per claimant; the write goes ahead regardless ([multi-writer](multi-writer.md)). |
| `ConflictSustained` | Warning | `Claim` | The same claimant has been on the object for five consecutive reconciles — a two-writer fight rather than a flap. Fires exactly once, at the threshold. |
| `Deleted`, `Retained`, `NothingToDelete`, `DeleteBlocked`, `FinalizerSkipped`, `CascadeDeleted` | Normal, `DeleteBlocked` and `CascadeDeleted` Warning | `Delete` | The CR is going away. `CascadeDeleted` names the CRs that `netbox.kubeforge.org/cascade-delete=true` removed so a refused delete could proceed — a Warning, because it deletes objects the user did not name. Every outcome gets an Event, because once the finalizer is off there is no status left to read ([deletion](../concepts/deletion.md)). |
| `ChildMaterialised`, `ChildPruned` | Normal | `Materialise`, `Prune` | An inline entry became a child CR, or a child CR went away because its entry did. The Event names the child in `related` ([inline children](../concepts/inline-children.md)). |
| `ChildFieldReverted` | Warning | `Materialise` | A field on a materialised child that somebody else had taken ownership of, taken back. |

On a `DryRun` endpoint the write's own reason is kept with a `dry run: would have written …`
message, because the endpoint is rehearsing that write. `driftMode: Report` replaces it with
`DriftDetected` instead: "updated" and "would have updated" must not read alike in
`kubectl describe`. Normal rather than Warning in both cases — nothing has malfunctioned, and
a Warning per object per resync would make the mode unusable in the adoption week it exists
for.

Both fire **only when the reported drift is new**: the first pass that finds it, and any
later pass on which it changes. A non-writing endpoint finds the same drift on every resync
and writes nothing, so an unguarded Event would be one duplicate per object per interval
for as long as the mode is left on — and `driftMode: Report` is meant to be left on for a
week over an entire NetBox. Relying on Kubernetes to aggregate identical Events into a count
(`DriftDetected (x27)`) is not enough: aggregation still writes to etcd, still consumes the
namespace's Event retention, and the standing state belongs in a condition either way. The
`DriftDetected` condition and `drift_detected_total` are the signals that carry every pass
(see [the transition rule](../concepts/reconciliation.md#an-event-or-an-error-log-on-a-repeating-state-is-keyed-on-the-transition)).

There is deliberately **no** Event for a transient failure. A 500 or a timeout resolves on
its own, and an Event for each one is noise at cluster scale — those show up in
`api_requests_total` and in the `Ready=False/APIError` condition.

### On a claim

Every one of these is the `Allocate` action: they are the outcomes of one operation, which
is the allocation engine trying to give a claim an address, prefix or range out of a pool
([claims](../concepts/claims.md)). A claim's deletion Events are the object CR's, above.

| Reason | Type | Fires when |
|---|---|---|
| `Allocated` | Normal | A new address, prefix or range was taken out of the pool. |
| `AllocationReclaimed` | Normal | The claim's previous allocation was still there and still its own, so nothing was written. |
| `PoolExhausted` | Warning | The pool has nothing left of the requested size. |
| `PoolNotAllocatable` | Warning | The pool CR is not in a state anything can be allocated from. |
| `PoolUnexpectedStatus` | Warning | The pool is in service and is being subdivided anyway. |
| `AllocationConflict` | Warning | Two claims are asking for the same thing. |
| `AllocationContended` | Warning | NetBox refused the allocating write because somebody else got there first. |
| `ForeignAllocation` | Warning | The address this claim holds carries another claim's identity. |
| `ReclaimedOutsidePool` | Warning | The claim's stored allocation is no longer inside the pool it was taken from. |
| `AllocationLost` | Warning | A settled claim's address is no longer in NetBox at all. The claim keeps reporting it — a claim never re-allocates — so this is the only thing that says the pin is stale. |

### On a `NetBoxSweep`

| Reason | Type | Action | When |
|---|---|---|---|
| `OrphansFound` | Normal | `Sweep` | A completed run confirmed at least one orphan. Normal rather than Warning: an orphan is a fact about NetBox, not a malfunction of the operator, and it is reported so somebody can decide what to do about it |
| `SweepRefused` | Warning | `Sweep` | A run did not happen, so the findings in `status` are older than they look. The message names the refusal reason and when the findings were last true. Emitted on the transition only, so a NetBox that is down does not fill the namespace |

Neither is a substitute for `status`. Events age out — an hour by default — and a
[sweep's](sweeps.md) whole value is that "what has this cluster left behind in NetBox" is
still answerable tomorrow, so `status.findings` is the record and these two are the
notification. Never read a missing Event as a missing orphan.


## Logging

Structured, always. `logr` taken from the context, never constructed ad hoc.

### Levels

| Level | Means | Example |
|---|---|---|
| `error` | **Needs a human.** Nothing will fix it without intervention. | A revoked token; a natural key matching two objects; a list truncated at the page cap. |
| `info` | **State changed.** | An object was created; drift was corrected; an endpoint became `Ready`. |
| `debug` | Everything else. | A resync that found no drift; a retry; request and response bodies. |

The rule that matters: **a reconcile that changes nothing logs at `debug`.** One line per
object per resync at `info` is a log nobody reads, and a log nobody reads is worse than no
log. Two consequences to know about:

- An endpoint that stays `Ready` logs `endpoint ready` at `debug` on every resync, and at
  `info` only when it *becomes* ready. Same for failures: the first is `error`, the repeats
  are `debug`, and the standing state lives in the condition.
- A `DryRun` endpoint finds the same drift every resync and writes nothing, so
  `dry run: netbox was not written` is `debug`. In `Report` mode the signals that scale are
  `drift_detected_total` and the `DriftDetected` condition, not the log.

`--zap-log-level` selects the level. `debug` enables everything above:

```sh
/manager --zap-log-level=debug           # includes request and response bodies
/manager --zap-log-level=info            # default
```

### The stable key set

Written down so log queries can be written before the incident.

| Key | On | Value |
|---|---|---|
| `kind` | Every reconcile line | `NetBoxPrefix`, `NetBoxEndpoint`, … |
| `namespace`, `name` | Every reconcile line | The CR. Supplied by controller-runtime. |
| `endpoint` | Every line that made a NetBox request | The REST path: `ipam/prefixes`, `status`. |
| `action` | Every line | `build`, `adopt`, `create`, `update`, `recreate`, `recover`, `stop`, `none`, `probe`, `request`, `retry`, `list`, `map`. |
| `netboxID` | Lines about a located object | `status.id`. |
| `changes` | `create`, `update`, `recreate` | The `field: old → new` diff, the same string as the Event. |
| `reason` | `stop`, `probe` | The condition reason just written. |
| `err` | Debug lines carrying a failure | The error text. On an `error` line the error is the first argument, not a key. |
| `method`, `code`, `body` | Client lines | HTTP verb, status, and the redacted body at `debug`. |

`action` is the key worth learning: it turns "what did the operator do" into a filter.

### Reading it

The manager logs JSON. Every example below assumes
`kubectl logs -n netbox-operator-system deploy/controller-manager`.

Everything the operator changed, newest last, as a table:

```sh
kubectl logs -n netbox-operator-system deploy/controller-manager \
  | jq -r 'select(.action | IN("create","update","recreate","adopt"))
           | [.ts, .kind, .namespace + "/" + .name, .action, .changes // "-"] | @tsv'
```

```
2026-08-21T09:14:02.113Z  NetBoxPrefix  team-a/office-v4   update  description: "" → "Office wing B", status: container → active
2026-08-21T09:14:02.884Z  NetBoxVLAN    team-a/office      create  name: Office, vid: 120
```

Everything that needs a human, and nothing that does not:

```sh
kubectl logs -n netbox-operator-system deploy/controller-manager \
  | jq -r 'select(.level == "error")
           | [.ts, .kind, .namespace + "/" + .name, .reason // "-", .msg, .error] | @tsv'
```

One object's whole story, including the requests it made:

```sh
kubectl logs -n netbox-operator-system deploy/controller-manager \
  | jq -c 'select(.namespace == "team-a" and .name == "office-v4")'
```

Which objects are waiting rather than failing — the difference between a stuck graph and a
broken one:

```sh
kubectl logs -n netbox-operator-system deploy/controller-manager \
  | jq -r 'select(.action == "stop") | .reason' | sort | uniq -c | sort -rn
```

### Secrets

No secret material appears in a log at any level.

Request and response bodies are logged at `debug` through a **tested** redaction pass, not
a convention: `auth_psk`, `psk`, `preshared_key`, `password`, `token`, `secret`,
`private_key` and `api_key` are masked wherever they appear, including nested inside a
`results` array — masking only the top level would put every PSK on a list page into the
log. `custom_fields` are collapsed to their key names, because the names help debugging and
the values are arbitrary user data. The API token itself is only ever an HTTP header and is
never a payload field.

Redaction also runs only when `debug` is actually enabled, so the copy it makes is not a
per-request cost at `info`.

## Related

- [Errors and retries](../concepts/errors-and-retries.md) — which NetBox failure becomes
  which typed error, and what gets retried where.
- [Drift detection](../concepts/drift.md) — what `drift_detected_total{field=...}` is
  counting, and why a wrong normalisation shows up there first.
- [ADR-0005 — Coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md) —
  why `detected` and `corrected` are separate counters.
- [`NetBoxEndpoint` reference](../reference/netboxendpoint.md) — every condition and reason
  the endpoint Events are named after.
- [Sweeps](sweeps.md) — what `sweep_findings` is counting, why a refused run deliberately
  leaves the gauge alone, and why the authoritative record is a `status` and not a metric.
