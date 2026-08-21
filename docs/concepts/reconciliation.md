# Reconciliation

How the operator gets from "here is an object" to "NetBox agrees with it", and what it
does when it cannot.

One reconcile loop exists today: `NetBoxEndpointReconciler` in
`internal/controller/netboxendpoint_controller.go`. It is the worked example for
everything below, because it is real. The generic per-object loop is designed but not
built — see [object lifecycle](object-lifecycle.md).

## The level-triggered model

A controller is handed an object key and asked to make reality match that object. It is
never handed a change. There is no "the URL was edited", no "the Secret's third byte
flipped", no ordered event stream to replay — only `Reconcile(ctx, req)` with a namespace
and a name, and the object as it is right now.

Practically, three things follow.

**Reconcile is idempotent by necessity, not by discipline.** The same request can arrive
any number of times: a spec edit, a Secret event, the `resyncPeriod` timer, a manager
restart replaying the whole informer cache, a leader election handover. Nothing
distinguishes the first call from the thousandth, so any step that is not safe to repeat
is a bug waiting for a busy Tuesday.

**Every step must tolerate running against a half-finished object.** A reconcile can be
interrupted anywhere — the process is killed, the context is cancelled, the API server
rejects a status write on a conflict. The next reconcile starts from the top with no
memory of how far the last one got, so each step has to work out its own situation from
observable state rather than assume a predecessor completed. The endpoint loop gets this
almost for free: it reads a Secret, builds a client, probes, and writes status, and none
of those depend on a previous pass. The object loop will not get it for free, which is
why `status.id` is only written once the object provably exists (see
[Condition conventions](#condition-conventions)).

**Missing an event is survivable; a wrong steady state is not.** Because the loop is
driven by level and not by edge, a dropped watch event costs latency, not correctness —
the next resync finds the same truth. That is what makes it acceptable for the endpoint
controller to requeue on a timer rather than chase every possible trigger.

## The endpoint loop, step by step

`Reconcile` (`netboxendpoint_controller.go:47`) runs these in order. Every failure path
goes through `fail` (`:178`), which drops the cached client, sets conditions, writes
status, and returns a `RequeueAfter` — never an error.

### 1. Fetch the object

`r.Get(ctx, req.NamespacedName, endpoint)`.

A `NotFound` calls `r.Cache.Forget(req.Namespace, req.Name)` and returns success: the
endpoint is gone, so the client must go with it. This is the only place a deleted
endpoint is cleaned up, and it works precisely because the model is level-triggered — the
controller does not need a deletion event, it needs to notice the object is absent.

Any other Get error is wrapped and **returned as an error** (`:54`). That is correct: a
failure to read from the informer cache is a fault in the manager, not in NetBox.

### 2. Read the token Secret

`readToken` (`:117`) fetches the Secret named by `spec.tokenSecretRef.name` from the
endpoint's **own namespace**, and reads `spec.tokenSecretRef.key`, defaulting to `token`
(`:124`–`:127`). It returns the token *and* the Secret's `resourceVersion`, which is what the
client cache is keyed on.

| Failure | Reason | Condition set | Requeue |
|---|---|---|---|
| Secret absent | `SecretMissing` | `Authenticated=False`, `Ready=False` | 30s |
| Key absent, or its value empty | `TokenMissing` | `Authenticated=False`, `Ready=False` | 30s |

An empty value is treated the same as an absent key (`:128`–`:131`) — a Secret with
`token: ""` is a misconfiguration, not an anonymous session.

The Secret is deliberately not namespace-qualified. Reading a Secret from another
namespace is a privilege escalation dressed up as convenience, and it would force the
operator's Secret RBAC to be genuinely cluster-wide
(`api/v1alpha1/netboxendpoint_types.go:43`–`:47`).

### 3. Build the client config

`buildConfig` (`:139`) maps the spec onto `netbox.Config`: `url`, the token, `mode`,
`timeout`, and `rateLimit.qps` / `rateLimit.burst` when set. If `spec.tlsConfig` is
present it also carries `insecureSkipVerify` and, when `caBundleSecretRef` is set, reads
a second Secret for the PEM bundle under key `ca.crt` by default (`:165`–`:172`).

Every failure here — an unreadable CA bundle Secret, a bundle Secret with no such key —
is `InvalidConfig`, requeued after **2 minutes**. Note the asymmetry: a missing *token*
Secret is `SecretMissing` at 30s, a missing *CA bundle* Secret is `InvalidConfig` at 2m,
because `buildConfig`'s errors are classified by where they came from rather than by
what went wrong.

### 4. Construct the client

`netbox.New(cfg)` (`:66`). This returns an error only for configuration that cannot work
at all: an empty URL, an unparseable URL, a scheme that is not `http`/`https`, or a CA
bundle containing no usable certificates (`internal/netbox/client.go:108`–`:141`). It
performs no I/O, so it says nothing about whether NetBox is up. Failures are
`InvalidConfig`, 2 minutes.

### 5. Probe `GET /api/status/`

`nbClient.Status(ctx)` (`:71`). One request answers three questions at once — is NetBox
reachable, is the token good, and what version is it — because NetBox's status endpoint
requires an authenticated request (NetBox source: `netbox/netbox/api/views.py`,
`StatusView`). It reads the `netbox-version` key and the keys of `plugins`
(`internal/netbox/status.go:20`–`:36`).

The reason comes from `reasonFor` (`:211`), which translates the client's already-typed
error rather than re-diagnosing it:

| Client error | Reason | Condition set | Requeue |
|---|---|---|---|
| `*netbox.AuthError` (401, 403) | `AuthError` | `Authenticated=False`, `Ready=False` | 2m |
| anything else — `*TransientError`, `*NotFoundError`, `*ValidationError`, a context deadline | `ProbeFailed` | `Ready=False` only | 30s |

`ProbeFailed` is the catch-all, and it covers more than "NetBox is down". A URL pointing
at something that is not NetBox produces a 404 (`*NotFoundError`); a reverse proxy
returning an HTML error page produces a `*ValidationError` whose message is the page's
first line (`internal/netbox/do.go:122`–`:136`). Both land here.

Retries inside this one call are the client's, not the controller's: `do`
(`internal/netbox/do.go:24`) retries only `*TransientError` and `*RateLimitError`, up to
`DefaultMaxRetries` (4) with full jitter. See
[errors and retries](errors-and-retries.md) — this page does not restate it.

### 6. Parse and gate the version

`netbox.SupportedVersion(status.Version)` (`:76`) parses the string and compares it
against the compiled-in range `netbox.MinVersion` = `4.2.0`, `netbox.MaxVersion` =
`5.0.0`, half-open (`internal/netbox/version.go:18`–`:21`, `:76`–`:90`).

| Outcome | Reason | Condition set | Requeue |
|---|---|---|---|
| Unparseable version string | `VersionUnparseable` | `VersionSupported=False`, `Ready=False` | 10m |
| Parsed but outside `[4.2.0, 5.0.0)` | `VersionUnsupported` | `VersionSupported=False`, `Ready=False` | 10m |

On the unsupported path — and only there — `status.netboxVersion` and `status.plugins`
are recorded first (`:81`–`:82`) and then persisted by `fail`, so
`kubectl get netboxendpoint` shows the version that was refused. Knowing what it found is
how anyone diagnoses this. The `VersionUnparseable` path returns at `:78`, before those
assignments, so `status.netboxVersion` stays empty there and the raw string survives only
in the condition message.

The lower bound is the guard that matters. NetBox 4.2 replaced the `site` foreign key on
`Prefix`, `Cluster`, `WirelessLAN` and `VLANGroup` with a polymorphic `(scope_type,
scope_id)` pair and a read-only `_site` cache
(`docs/netbox-schema.md` → `dcim.CachedScopeMixin`, plus the preamble's rule that
`_`-prefixed columns are NetBox-maintained caches). On 4.2+ a write to `site` silently
no-ops. An operator pointed at 4.1 would therefore not fail — it would appear to work and
change nothing, which is strictly worse. Refusing to hand out a client is the only honest
response.

### 7. Cache the client

`r.Cache.put(clientKey{...}, nbClient)` (`:94`). See
[the client cache](#the-client-cache-and-secret-resourceversion).

### 8. Set conditions and requeue

Three conditions go to `True` with reason `Ready` (`:105`–`:110`): `Authenticated`
("token accepted"), `VersionSupported` ("netbox <version>"), `Ready` ("client
available"). A single log line at info level records the URL, version, mode and plugin
list (`:101`).

The return is `ctrl.Result{RequeueAfter: resyncPeriod(endpoint)}, r.writeStatus(...)`
(`:112`). `resyncPeriod` (`:225`) uses `spec.resyncPeriod` when positive and otherwise
falls back to 10 minutes — belt and braces, since the CRD already defaults the field to
`10m` (`api/v1alpha1/netboxendpoint_types.go:107`–`:110`).

## Tiered backoff, and why

`failureBackoff` (`:198`) picks the requeue delay from the reason alone:

| Reason | Delay | Reasoning |
|---|---|---|
| `VersionUnsupported`, `VersionUnparseable` | 10m | Nothing the operator does changes the answer. NetBox has to be upgraded, or the endpoint pointed elsewhere — both of which bump `metadata.generation` and trigger an immediate reconcile anyway. |
| `AuthError`, `InvalidConfig` | 2m | Needs a human to fix a token's permissions or a manifest. A Secret edit arrives on the watch, so the timer is only the floor. |
| `SecretMissing`, `TokenMissing`, `ProbeFailed`, anything else | 30s | Plausibly self-correcting: a Secret is about to be created by a sealed-secrets controller, or NetBox is mid-restart. |

Re-probing an unsupported version every 30 seconds would be pure noise. It cannot
succeed: the version is a property of the server, the operator's supported range is
compiled in, and neither moves without an event that already triggers a reconcile. What
it *would* produce is 120 log lines an hour per broken endpoint, 120 status writes an
hour against the API server, and 120 authenticated requests an hour against a NetBox
nobody is asking the operator to use — all of it drowning the log lines that do mean
something. The 10-minute tier exists so that the retry rate matches the rate at which
the answer could possibly change.

The same logic runs the other way for `ProbeFailed`. A NetBox that is restarting is back
in well under 10 minutes, and 30 seconds of staleness in the `Ready` condition is the
cost of finding out.

## Why NetBox being down is never a returned error

`Reconcile`'s doc comment states the rule (`:44`–`:46`): it never returns an error for
anything about NetBox's availability. An unreachable or misconfigured NetBox is a
condition on the object.

The reason is that a returned error is a statement about the *controller*, and
controller-runtime treats it as one. It increments
`controller_runtime_reconcile_errors_total{controller="netboxendpoint"}` and
`controller_runtime_reconcile_total{result="error"}`, logs at error level, and re-adds the
request to the workqueue rate-limited
(`sigs.k8s.io/controller-runtime@v0.22.4/pkg/internal/controller/controller.go:462`–`:474`).
If NetBox's uptime fed that path, the manager's error rate — the number every alert and
every dashboard treats as "is the operator healthy" — would become a function of somebody
else's uptime. The one alert you need to stay meaningful stops being meaningful exactly
when an outage starts.

Reporting it as a condition instead puts the fact where it belongs: on the object that
has the problem, visible to `kubectl describe`, and available to `kubectl wait
--for=condition=Ready`.

### What the two exits actually do

| Return | Effect |
|---|---|
| `ctrl.Result{}, err` | Rate-limited requeue. controller-runtime's default limiter is the max of a per-item exponential backoff from 5ms to 1000s and a global 10 qps / 100 burst token bucket (`k8s.io/client-go@v0.34.1/util/workqueue/default_rate_limiters.go:50`–`:56`). Successive failures for the same key double the delay up to ~16 minutes; a success calls `Forget` and resets it. |
| `ctrl.Result{RequeueAfter: d}, nil` | `Forget` the key, then re-add after exactly `d` (`controller.go:475`–`:483`). Counted as `result="requeue_after"`, not as an error. No backoff accumulates, so the delay is whatever the code chose and stays there. |

`RequeueAfter` is right when the code knows how long to wait and the wait is a normal
state — which is every failure this loop can see. Returning an error is right when the
code does *not* know: an API server write that lost a conflict, a cache read that failed,
a bug. Then exponential backoff is the correct guess, and inflating the error metric is
the correct signal.

One wrinkle worth knowing: an error and a `RequeueAfter` are mutually exclusive, and the
error wins. Line `:112` returns both — `RequeueAfter: resyncPeriod(endpoint)` alongside
`r.writeStatus(...)`. On the normal path `writeStatus` returns nil and the resync is
scheduled. When it returns an error (a status-update conflict, say), the `RequeueAfter` is
discarded in favour of a rate-limited requeue, and controller-runtime logs a warning
about being handed both.

## Watches, and why a Secret event matters

`SetupWithManager` (`:251`) does two things beyond the obvious `For(&NetBoxEndpoint{})`.

**A field index.** `mgr.GetFieldIndexer().IndexField` registers the index
`spec.tokenSecretRef.name` (the constant `secretRefIndex`, `:28`) over
`NetBoxEndpoint`, extracting exactly that field (`:252`–`:260`).

**A Secret watch with a map function.** `Watches(&corev1.Secret{},
handler.EnqueueRequestsFromMapFunc(r.endpointsForSecret))` (`:266`). `endpointsForSecret`
(`:279`) lists endpoints in the changed Secret's namespace with
`client.MatchingFieldsSelector{Selector: fields.OneTermEqualSelector(secretRefIndex,
obj.GetName())}` and returns one `reconcile.Request` per hit.

The index is what makes that cheap. Without it, every Secret event in the cluster would
mean listing every `NetBoxEndpoint` and filtering in Go. With it, the lookup is a map
hit in the informer's index. A list error is logged and returns nil rather than failing —
the resync will catch up.

### Why this is not optional

Rotating a token means writing a new value into the Secret. The `NetBoxEndpoint` itself
does not change: same `spec`, same `metadata.generation`, no event on its own watch. With
only the resync, the operator would keep using the old token until the next
`resyncPeriod` tick — up to 10 minutes by default, and however long the user configured
if they raised it. Ten minutes of 401s from every object controller in the namespace,
every one of them correctly reporting `AuthError` against a token that was already
replaced.

With the watch, the Secret write enqueues the endpoint immediately, `readToken` reads the
new value, and the new client is cached within one reconcile. That is the whole of
NBO-004's "credential rotation without a restart", and it is about eight lines of wiring.

The same mechanism is why the index is on the token Secret and not on the CA bundle
Secret: rotating a CA bundle is not watched, so it takes effect at the next resync. That
is a real gap, though a much rarer one.

## The client cache and Secret `resourceVersion`

`ClientCache` (`internal/controller/clientcache.go:23`) maps `clientKey` to
`*netbox.Client`. The key is `{namespace, name, generation, secretVersion}` (`:15`), and
`secretVersion` is the token Secret's `metadata.resourceVersion` as returned by
`readToken`.

Putting the Secret's version *in the key* makes invalidation structural. The alternative
is a piece of logic — "if the Secret changed, throw the old client away" — living
somewhere, in some code path, that somebody has to remember to call. Every real
cache-invalidation bug is that sentence: a new code path that forgets the call, and a
stale credential that keeps working until it does not. A key that includes the
credential's version cannot be forgotten, because there is nowhere to forget it from: a
rotated Secret is a different key, and a different key is a different entry.

`put` (`:35`) additionally deletes every other entry for the same namespace and name
before inserting. `Lookup` (`:48`) matches on namespace and name only — object
controllers ask "the client for endpoint X", not "the client for endpoint X at
generation 4 with Secret version 91827" — so without that eviction a rotation would
leave two entries for one endpoint and `Lookup` would return whichever the map iteration
reached first. The eviction is what guarantees at most one live client per endpoint.

### Why a failing endpoint must actively forget

`fail` calls `r.Cache.Forget(e.Namespace, e.Name)` as its first action (`:179`), before
it touches conditions. `Forget` (`:62`) deletes every entry for that endpoint.

Leaving the client in place would be worse than useless. Object controllers read the
cache, and a miss means "not Ready, wait" — that is the contract (`clientcache.go:46`–`:47`). A client
left behind after a 403 is a client that every object controller in the namespace will
keep writing through, generating an identical `AuthError` per object, when the endpoint
already knows the answer and has recorded it. The endpoint's job is to be the single
place that failure is diagnosed; a stale cache entry would scatter it back across every
CR in the cluster, which is exactly what typing the error was meant to prevent
(`internal/netbox/errors.go:41`–`:43`).

## Requeue versus error: the decision table

| Situation | What the code does | Why |
|---|---|---|
| Endpoint object not found | `Forget`, return success | Nothing left to reconcile. Requeuing a deleted object is a slow no-op loop. |
| Get on the endpoint fails for any other reason | return the error | The informer cache is broken. That is a controller fault and belongs in the error metric. |
| Token Secret absent | condition, requeue 30s | Ordering, most likely. A Secret arriving is not a controller failure. |
| Token key absent or empty | condition, requeue 30s | User error; a Secret edit is watched, so the timer is a floor. |
| CA bundle Secret unreadable or missing its key | condition, requeue 2m | Needs a human. |
| `netbox.New` rejects the URL or the CA bundle | condition, requeue 2m | Needs a spec edit, which bumps `generation` and reconciles at once. |
| `GET /api/status/` returns 401 or 403 | condition, requeue 2m | Someone has to fix the token or its NetBox permissions. |
| NetBox unreachable, 5xx, timeout, non-NetBox URL | condition, requeue 30s | Somebody else's outage. Must not touch the operator's error rate. |
| Version outside `[4.2.0, 5.0.0)`, or unparseable | condition, requeue 10m | Cannot self-correct. Fast retries are noise. |
| Everything succeeded | conditions `True`, requeue after `resyncPeriod` | Periodic re-probe catches a NetBox upgrade, a revoked token, an expired certificate. |
| `Status().Update` fails | return the error | A conflict means the object moved under us; exponential backoff and a fresh read is the right answer. |

The through-line: **return an error only for a failure of the operator's own
machinery.** Everything about the outside world is state, and state goes in `status`.

## Condition conventions

`setCondition` (`:232`) wraps `meta.SetStatusCondition`, which is what keeps
`lastTransitionTime` honest — it only moves when `status` actually changes, so a condition
that has been `False` for an hour says so instead of resetting on every reconcile.

**Every condition carries `ObservedGeneration`** (`:238`), and `writeStatus` (`:242`) sets
`status.observedGeneration` on every path, success and failure alike. This is what lets a
reader tell "reconciled and healthy" from "not yet looked at since the last edit". Without
it, `kubectl wait --for=condition=Ready` returns immediately on a stale `True` from
before the spec change, and any automation built on that quietly does the wrong thing.

**The three condition types** (`api/v1alpha1/netboxendpoint_types.go:8`–`:17`):

| Type | Meaning | Set to `False` by |
|---|---|---|
| `Ready` | There is a usable client. Object controllers wait on this one. | every failure |
| `Authenticated` | The token was accepted. | `AuthError`, `TokenMissing`, `SecretMissing` |
| `VersionSupported` | The server's version is in range. | `VersionUnsupported`, `VersionUnparseable` |

`fail`'s switch (`:182`–`:187`) only touches the specific condition when the reason
matches. `ProbeFailed` and `InvalidConfig` therefore set `Ready=False` and leave
`Authenticated` and `VersionSupported` at whatever the last successful probe left them —
`True`, if there was one. Read those two as "the last time we could tell, this was the
answer", not as current fact. `Ready` is the only one that is always current.

**Status is the only thing the operator writes.** `writeStatus` goes through
`r.Status().Update(ctx, e)` and there is no code path in the controller that writes an
endpoint's `spec` or `metadata`. That is not a stylistic preference; it is what makes the
operator able to share an object with Flux or Argo CD, both of which would otherwise
revert the write and be re-written, forever, at whichever resync interval is shorter. See
[ADR-0005 — coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md).

**`status.id` is only set once an object provably exists server-side.** The endpoint has
no `status.id`, but the rule is set here because it comes out of the same reasoning. The
populator this operator replaces handed out synthetic negative ids in dry-run mode so
that dependent objects had something to reference. In a controller that id would be
written to `status.id` and then treated as real forever — every later reconcile would
`GET` an id that never existed. `Client.Create` in dry-run mode therefore returns the
payload marked suppressed and invents nothing
(`internal/netbox/client.go:225`–`:236`). An empty `status.id` is the honest
representation of "not created", and it is the state the create path is designed to
recover from.

## What this page does not cover

- Which NetBox failures are retried, where, and why — [errors and retries](errors-and-retries.md).
- How a live object is compared against the desired payload — [drift detection](drift.md).
- The per-object create/adopt/update loop, which is designed and not yet built —
  [object lifecycle](object-lifecycle.md).
- Symptom-first diagnosis — [troubleshooting](../operations/troubleshooting.md).
