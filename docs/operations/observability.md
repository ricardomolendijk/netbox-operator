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
| `waiting` | Endpoint not `Ready`, no usable natural key yet, or `AdoptOnly` with nothing to adopt. Normal during a rollout. |
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
| `targetKind`, `referrerKind` | Kind names from a Descriptor, paired only where a field map declares a reference | a few hundred pairs |

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

### On a `NetBoxEndpoint`

Emitted only when the `Ready` condition's status or reason changes, so a NetBox that has
been unreachable for a week produces one Event, not one every thirty seconds. The Event
reason is the condition reason, so the two vocabularies cannot drift apart.

| Reason | Type | Fires when |
|---|---|---|
| `Ready` | Normal | The endpoint becomes usable: token accepted, version in range, client cached. |
| `AuthError` | Warning | NetBox returned 401 or 403 for the status probe. |
| `TokenMissing` | Warning | The Secret exists but has no value under the referenced key. |
| `SecretMissing` | Warning | The token or CA-bundle Secret is not readable — often because it is not labelled `netbox.kubeforge.org/endpoint-credential=true`. |
| `CABundleMissing` | Warning | `spec.tlsConfig.caBundleSecretRef` points at a Secret that is not there. |
| `ProbeFailed` | Warning | NetBox was unreachable, timed out, or answered something unusable. |
| `VersionUnsupported` | Warning | NetBox is outside `>=4.2, <5.0`. |
| `VersionUnparseable` | Warning | `/api/status/` returned something that is not a version. |
| `InvalidConfig` | Warning | The spec cannot produce a client: bad URL, unusable CA bundle. |

```console
$ kubectl describe netboxendpoint homelab
...
Events:
  Type     Reason      Age   From                       Message
  ----     ------      ----  ----                       -------
  Warning  AuthError   12m   netboxendpoint-controller  netbox authentication or permission failure (401): {"detail":"Invalid token."}
  Normal   Ready       2m    netboxendpoint-controller  netbox 4.6.8 at https://netbox.example.com accepted the token; client available
```

### On an object CR

Emitted by the engine when it writes, or when it refuses to.

| Reason | Type | Fires when |
|---|---|---|
| `Created` | Normal | A new NetBox object was POSTed. |
| `Adopted` | Normal | An existing NetBox object matched the natural key and `spec.onConflict` permitted taking it over. Names the id and the key that matched. |
| `Updated` | Normal | Fields were PATCHed. The message is the aligned `field: old → new` diff. |
| `Recreated` | Normal | The object was deleted and POSTed again because its identity is not PATCHable. |
| `Conflict` | Warning | NetBox holds an object this CR cannot safely claim: more than one natural-key match, an unadoptable match, or a protected relation. |
| `Invalid` | Warning | NetBox rejected the payload, or the spec cannot be rendered into one. |
| `DriftDetected` | Normal | Drift was found on a `driftMode: Report` endpoint and deliberately left alone. The message is the same `field: old → new` diff, prefixed `report only: would have written …`. |

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
