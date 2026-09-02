# Reconciliation

How the operator gets from "here is an object" to "NetBox agrees with it", and what it
does when it cannot.

`NetBoxEndpointReconciler` in `internal/controller/netboxendpoint_controller.go` is the
worked example for everything below: it is the smallest loop in the repository and every rule
on this page is visible in it. The per-object loop that drives the other 61 kinds follows the
same model through the shared engine — [the reconcile engine](engine.md) is the page for that
one, and [object lifecycle](object-lifecycle.md) is the register of what is still designed
rather than built.

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

`Reconcile` in `internal/controller/netboxendpoint_controller.go` runs these in order.
Every failure path goes through `fail`, which drops the cached client, sets conditions,
writes status, and returns a `RequeueAfter` — never an error.

### 1. Fetch the object

`r.Get(ctx, req.NamespacedName, endpoint)`.

A `NotFound` calls `r.Cache.Forget(req.Namespace, req.Name)` and returns success: the
endpoint is gone, so the client must go with it. This is the only place a deleted
endpoint is cleaned up, and it works precisely because the model is level-triggered — the
controller does not need a deletion event, it needs to notice the object is absent.

Any other Get error is wrapped and **returned as an error**. That is correct: a
failure to read from the informer cache is a fault in the manager, not in NetBox.

### 2. Read the token Secret

`readToken` fetches the Secret named by `spec.tokenSecretRef.name` from the endpoint's
**own namespace**, and reads `spec.tokenSecretRef.key`, defaulting to `token` through the
shared helper `orDefaultKey`. It returns the token *and* the Secret's `resourceVersion`,
which is what the client cache is keyed on.

| Failure | Reason | Condition set | Requeue |
|---|---|---|---|
| Token Secret absent, or invisible to the operator | `SecretMissing` | `Authenticated=False`, `VersionSupported=Unknown`, `Ready=False` | 30s |
| Key absent, or its value empty | `TokenMissing` | `Authenticated=False`, `VersionSupported=Unknown`, `Ready=False` | 30s |

An empty value is treated the same as an absent key — a Secret with
`token: ""` is a misconfiguration, not an anonymous session.

"Invisible to the operator" is a real case, not a hedge: the manager reads Secrets through
a label-scoped informer cache, so a Secret that `kubectl get secret` shows plainly is
absent as far as the operator is concerned unless it carries
`netbox.kubeforge.org/endpoint-credential=true`. The condition message covers both causes,
because distinguishing them would need an uncached read of the very Secret the scoping
exists to avoid reading. See [RBAC](../operations/rbac.md).

The Secret is deliberately not namespace-qualified. Reading a Secret from another
namespace is a privilege escalation dressed up as convenience, and it would force the
operator's Secret RBAC to be genuinely cluster-wide
(`api/v1alpha1/netboxendpoint_types.go:43`–`:47`).

### 3. Build the client config

`buildConfig` maps the spec onto `netbox.Config`: `url`, the token, `mode`,
`timeout`, and `rateLimit.qps` / `rateLimit.burst` when set. If `spec.tlsConfig` is
present it also carries `insecureSkipVerify` and, when `caBundleSecretRef` is set, reads
a second Secret for the PEM bundle, defaulting to key `ca.crt` through that same
`orDefaultKey` helper.

Failures are classified by `reasonForConfig` rather than lumped together, and the CA
bundle gets its **own** reason:

| Failure | Reason | `Authenticated` | Requeue |
|---|---|---|---|
| CA bundle Secret absent | `CABundleMissing` | `Unknown` | 30s |
| Bundle present, key absent, or no usable certificate in the PEM | `InvalidConfig` | `Unknown` | 2m |

`CABundleMissing` is distinct from the token Secret's `SecretMissing` on purpose. By the
time the CA bundle is read the token has already been read successfully, so reporting
`Authenticated=False` would send the reader to the wrong Secret — the message says
`reading ca bundle secret …` while the condition claimed the token failed. `Unknown` is the
honest value: this reconcile never asked NetBox whether the token was good.

### 4. Construct the client

`netbox.New(cfg)`. This returns an error only for configuration that cannot work
at all: an empty URL, an unparseable URL, a scheme that is not `http`/`https`, or a CA
bundle containing no usable certificates (`internal/netbox/client.go:108`–`:141`). It
performs no I/O, so it says nothing about whether NetBox is up. Failures are
`InvalidConfig`, 2 minutes.

### 5. Probe `GET /api/status/`

`nbClient.Status(ctx)`. One request answers three questions at once — is NetBox
reachable, is the token good, and what version is it — because NetBox's status endpoint
requires an authenticated request (NetBox source: `netbox/netbox/api/views.py`,
`StatusView`). It reads the `netbox-version` key and the keys of `plugins`
(`internal/netbox/status.go:20`–`:36`).

The reason comes from `reasonFor`, which translates the client's already-typed
error rather than re-diagnosing it:

| Client error | Reason | Condition set | Requeue |
|---|---|---|---|
| `*netbox.AuthError` (401, 403) | `AuthError` | `Authenticated=False`, `VersionSupported=Unknown`, `Ready=False` | 2m |
| anything else — `*TransientError`, `*NotFoundError`, `*ValidationError`, a context deadline | `ProbeFailed` | `Authenticated=Unknown`, `VersionSupported=Unknown`, `Ready=False` | 30s |

`ProbeFailed` is the catch-all, and it covers more than "NetBox is down". A URL pointing
at something that is not NetBox produces a 404 (`*NotFoundError`); a reverse proxy
returning an HTML error page produces a `*ValidationError` whose message is the page's
first line (`internal/netbox/do.go:122`–`:136`). Both land here.

There are **no client-side retries on the probe**. `buildConfig` sets
`MaxRetries: netbox.Retries(0)`, so one `Reconcile` makes exactly one HTTP attempt and
the 30-second requeue is the retry. That is deliberate: the controller already requeues,
one worker serves every endpoint, and four retries behind a 30-second timeout would let a
single black-holed NetBox stall every other endpoint for minutes. The client's retry
machinery (`internal/netbox/do.go:24`) still governs every other call — see
[errors and retries](errors-and-retries.md), which this page does not restate.

### 6. Parse and gate the version

`netbox.SupportedVersion(status.Version)` parses the string and compares it
against the compiled-in range `netbox.MinVersion` = `4.2.0`, `netbox.MaxVersion` =
`5.0.0`, half-open (`internal/netbox/version.go:18`–`:21`, `:76`–`:90`).

| Outcome | Reason | Condition set | Requeue |
|---|---|---|---|
| Unparseable version string | `VersionUnparseable` | `VersionSupported=False`, `Ready=False` | 10m |
| Parsed but outside `[4.2.0, 5.0.0)` | `VersionUnsupported` | `VersionSupported=False`, `Ready=False` | 10m |

`status.netboxVersion` and `status.plugins` are recorded **before** the gate and then
persisted by `fail`, so both failure paths leave the version visible to
`kubectl get netboxendpoint`. Whatever NetBox said is the most useful thing in status when
the answer is "that is not a version", and knowing what it found is how anyone diagnoses
either case.

The lower bound is the guard that matters. NetBox 4.2 replaced the `site` foreign key on
`Prefix`, `Cluster`, `WirelessLAN` and `VLANGroup` with a polymorphic `(scope_type,
scope_id)` pair and a read-only `_site` cache
(`docs/netbox-schema.md` → `dcim.CachedScopeMixin`, plus the preamble's rule that
`_`-prefixed columns are NetBox-maintained caches). On 4.2+ a write to `site` silently
no-ops. An operator pointed at 4.1 would therefore not fail — it would appear to work and
change nothing, which is strictly worse. Refusing to hand out a client is the only honest
response.

### 7. Cache the client

`r.Cache.put(clientKey{...}, nbClient)`. See
[the client cache](#the-client-cache-and-secret-resourceversion).

### 8. Set conditions and requeue

Three conditions go to `True` with reason `Ready`: `Authenticated`
("token accepted"), `VersionSupported` ("netbox <version>"), `Ready` ("client
available"). A single log line at info level records the URL, version, mode, plugin list and whether
certificate verification is disabled.

Status is written first, and the requeue is returned on its own afterwards — never both
at once, for the reason at the end of the next section. A pass that computed exactly the
status already stored writes nothing at all: `writeStatus` compares the whole status
against the copy taken before the pass began — `status.plugins` is sorted for that
comparison to mean anything — and skips the `Status().Update`, which is the rule
`internal/reconciler`'s `finish` already applies to object kinds. That matters to anything
watching the object: an unconditional write bumps `resourceVersion` every `resyncPeriod`
forever and wakes every watcher, which is an Argo CD refresh and an audit entry per
endpoint per interval for a change that is not one. `resyncPeriod` uses
`spec.resyncPeriod` when positive and otherwise
falls back to `internal/reconciler`'s `DefaultResync` — belt and braces, since the CRD
already defaults the field to `10m`
(`api/v1alpha1/netboxendpoint_types.go:107`–`:110`). It borrows the engine's constant
rather than restating the literal, so the two halves of the binary cannot end up with
different notions of "the default".

## Tiered backoff, and why

`failureBackoff` picks the requeue delay from the reason alone:

| Reason | Delay | Reasoning |
|---|---|---|
| `VersionUnsupported`, `VersionUnparseable` | 10m | Nothing the operator does changes the answer. NetBox has to be upgraded, or the endpoint pointed elsewhere — both of which bump `metadata.generation` and trigger an immediate reconcile anyway. |
| `AuthError`, `InvalidConfig` | 2m | Needs a human to fix a token's permissions or a manifest. A Secret edit arrives on the watch, so the timer is only the floor. |
| `SecretMissing`, `TokenMissing`, `CABundleMissing`, `ProbeFailed`, anything else | 30s | Plausibly self-correcting: a Secret is about to be created by a sealed-secrets controller or about to be labelled, or NetBox is mid-restart. |

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

### Every requeue carries jitter

Both requeues — the success path's `resyncPeriod` and every tier `failureBackoff` picks —
are returned through `internal/reconciler`'s `Jitter`, the same ±10% spread the engine
applies to object requeues (`docs/concepts/engine.md`). Every requeue delay quoted anywhere
on this page is therefore the *centre* of a range rather than an exact interval: a 30s tier
requeues somewhere in 27s–33s, a 10m one in 9m–11m.

The reason is the one the engine already wrote down. Endpoints arrive in a manifest applied
all at once, so they reconcile in the same pass, and a bare `RequeueAfter` keeps them
aligned for the life of the process — lab, staging and prod re-probing in lockstep, and
every endpoint pointed at the same NetBox hitting `/api/status/` at the same instant. The
blast radius is smaller than the engine's, because endpoints are few and the probe is
cheap, but two components of one binary should not disagree about a convention one of them
has already reasoned through. There is one `Jitter`, exported from the engine's package and
called by both.

±10% and not full jitter, deliberately. The NetBox client uses full jitter — uniform in
`[0, backoff]` — for HTTP retries, where collapsing a delay to nearly nothing is harmless
and de-synchronising is the entire point (`docs/concepts/errors-and-retries.md`). Here the
delay carries meaning: the tiers exist to say how likely a retry is to help, and a
10-minute wait that jitters down to 4 would be a worse experience than one that stays at
10, as well as demoting the tier into the one below it. A tenth either way spreads the
schedule without ever moving an interval out of its tier.

## Why NetBox being down is never a returned error

`Reconcile`'s doc comment states the rule: it never returns an error for
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

**The two must never be returned together.** An error and a `RequeueAfter` are mutually
exclusive and the error wins, so returning both means a status-update conflict silently
discards the resync schedule — and earns a warning from controller-runtime for the
contradiction. `Reconcile` therefore writes status, returns `ctrl.Result{}, err` if that
write fails, and only then returns the bare `RequeueAfter`. The general rule: decide which
of the two exits you mean, and return exactly one of them.

## Watches, and why a Secret event matters

`SetupWithManager` does two things beyond the obvious `For(&NetBoxEndpoint{})`.

**A field index.** `mgr.GetFieldIndexer().IndexField` registers the index
`spec.secretRefs` (the constant `secretRefIndex`) over `NetBoxEndpoint`, extracting
**both** referenced Secret names: `spec.tokenSecretRef.name` and, when set,
`spec.tlsConfig.caBundleSecretRef.name`.

**A Secret watch with a map function.** `Watches(&corev1.Secret{},
handler.EnqueueRequestsFromMapFunc(r.endpointsForSecret))`. There is no predicate: every
Secret event in the cluster invokes the map function, which is cheap only because of the
index. `endpointsForSecret` lists endpoints in the changed Secret's namespace with
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

The index covers the CA bundle Secret for the same reason, so rotating a trust bundle is
noticed as promptly as rotating a token rather than waiting out a resync.

## The client cache and Secret `resourceVersion`

`ClientCache` (`internal/controller/clientcache.go:26`) maps `clientKey` to
`*netbox.Client`. The key is `{namespace, name, generation, secretVersion}`
(`clientcache.go:18`), and `secretVersion` is the token Secret's
`metadata.resourceVersion` as returned by `readToken`.

What actually invalidates is worth being precise about, because the obvious reading is
wrong. `Reconcile` does **not** read through the cache before building: it constructs a
client every pass and calls `put`, which evicts any existing entry for the same endpoint
before inserting (`clientcache.go:42`–`:52`). So invalidation comes from that eviction,
not from a key miss — there is no lookup on the write path that could miss.

The version and generation are carried in the key anyway, for two reasons stated in the
code: they make an entry self-describing in a dump, and a future read-through path would
need exactly this key. That is the shape to keep in mind if anyone adds one, because a
read-through cache keyed on the credential's version is what makes invalidation
structural rather than a piece of logic — "if the Secret changed, throw the old client
away" — living in some code path somebody has to remember to call. Every real
cache-invalidation bug is that sentence.

`Lookup` (`clientcache.go:56`) matches on namespace and name only: object controllers ask
"the client for endpoint X", not "the client for endpoint X at generation 4 with Secret
version 91827". Without `put`'s eviction a rotation would leave two entries for one
endpoint and `Lookup` would return whichever the map iteration reached first. The eviction
is what guarantees at most one live client per endpoint.

Eviction also releases the old client's idle connections. `put` and `Forget` both call
`CloseIdleConnections` on the client they drop (`clientcache.go:47`, `:75`; the method
itself is `internal/netbox/client.go:158`–`:163`). Without it,
every reconcile — including every resync tick — would leave behind a transport holding an
idle keep-alive pool, and pools would accumulate for the lifetime of the process: one per
endpoint per `resyncPeriod`, forever.

### Why a failing endpoint must actively forget

`fail` calls `r.Cache.Forget(e.Namespace, e.Name)` as its first action, before it touches
conditions. `Forget` (`clientcache.go:70`) deletes every entry for that endpoint and
closes its idle connections.

Leaving the client in place would be worse than useless. Object controllers read the
cache, and a miss means "not Ready, wait" — that is the contract
(`clientcache.go:54`–`:55`). A client left behind after a 403 is a client that every
object controller in the namespace will keep writing through, generating an identical
`AuthError` per object, when the endpoint already knows the answer and has recorded it.
The endpoint's job is to be the single place that failure is diagnosed; a stale cache entry would scatter it back across every
CR in the cluster, which is exactly what typing the error was meant to prevent
(`internal/netbox/errors.go:41`–`:43`).

## Requeue versus error: the decision table

| Situation | What the code does | Why |
|---|---|---|
| Endpoint object not found | `Forget`, return success | Nothing left to reconcile. Requeuing a deleted object is a slow no-op loop. |
| Get on the endpoint fails for any other reason | return the error | The informer cache is broken. That is a controller fault and belongs in the error metric. |
| Token Secret absent | condition, requeue 30s | Ordering, most likely. A Secret arriving is not a controller failure. |
| Token key absent or empty | condition, requeue 30s | User error; a Secret edit is watched, so the timer is a floor. |
| CA bundle Secret absent | condition, requeue 30s | A missing Secret is a missing Secret, whichever field referenced it. |
| CA bundle Secret present but missing its key, or holding no usable certificate | condition, requeue 2m | Needs a human. |
| `netbox.New` rejects the URL or the CA bundle | condition, requeue 2m | Needs a spec edit, which bumps `generation` and reconciles at once. |
| `GET /api/status/` returns 401 or 403 | condition, requeue 2m | Someone has to fix the token or its NetBox permissions. |
| NetBox unreachable, 5xx, timeout, non-NetBox URL | condition, requeue 30s | Somebody else's outage. Must not touch the operator's error rate. |
| Version outside `[4.2.0, 5.0.0)`, or unparseable | condition, requeue 10m | Cannot self-correct. Fast retries are noise. |
| Everything succeeded | conditions `True`, requeue after `resyncPeriod` | Periodic re-probe catches a NetBox upgrade, a revoked token, an expired certificate. |

Every delay in the table is jittered by ±10%, for the reason above.
| `Status().Update` fails | return the error, **and no `RequeueAfter`** | A conflict means the object moved under us; exponential backoff and a fresh read is the right answer. Returning a `Result` too would discard it silently. |

The through-line: **return an error only for a failure of the operator's own
machinery.** Everything about the outside world is state, and state goes in `status`.

## A declared reference is a precondition for the write

**Decided** on [#195](https://github.com/ricardomolendijk/netbox-operator/issues/195), option C.
The rule, in one sentence: **a reference the spec declares must resolve before the object is
created or updated.** Three states, and the middle one is the change:

| The spec | The engine |
|---|---|
| declares the reference, and it resolved | writes it, exactly as before |
| **declares the reference, and it did not resolve** | **writes nothing at all** — no `POST`, no `PATCH`, not even the natural-key lookup — and reports which references it is waiting for |
| does not declare the reference | writes immediately, with no key for it in the payload |

Declared means *the spec sets the key*. Not "the kind has such a field", which would make one
unreachable target Kind hold up every object that merely could have referenced it and turn
every optional reference into a required one. `scope` absent is a global prefix and is created
on the first pass; `scope: {siteRef: {name: home}}` waits for that site.

For a to-many field, `[]` also counts as declared — [field ownership](field-ownership.md) is
what makes an explicitly-empty field distinguishable from an absent one, and an empty list is a
real instruction: NetBox's many-to-many write is a full replacement, so `[]` is how the column
is cleared. It has nothing in it to resolve, so it is never a precondition. Blocking on it
would deadlock every object that clears a list.

### What it replaced, and why that was not a design

The outcome used to depend on whether the reference happened to be part of the kind's natural
key, which nobody chose:

- `ipam.Prefix` is unique on `prefix`, so a candidate was applicable without the scope. An
  unresolvable `scope` member got the row **created with the pair omitted**, holding
  `RefsResolved=False` and `Ready=False`. NetBox held an unscoped prefix for as long as the
  target was missing.
- `dcim.Location` has `site_id` in every candidate, so an unresolved `siteRef` left no
  applicable candidate and the engine wrote **nothing at all**.

Same class of failure, opposite outcomes. The argument for creating anyway was convergence
speed while a referenced Kind is unimplemented — a transient state of this project rather than
of anyone's cluster — against a standing cost that does not go away: an object in NetBox that
does not match its manifest, which nothing reading NetBox can tell from a finished one. That is
close enough to what [ADR-0005](../decisions/0005-gitops-coexistence.md) exists to prevent to
settle it.

### The reason still says which failure it was

The rule is one rule; the diagnosis is not flattened into it. `RefsResolved` carries the
resolver's own reason — `RefKindUnavailable` for a reference whose target Kind has no
descriptor, `RefNotReady` for a target that exists and holds no id yet, `RefNotFound` for one
that is not there, and the five others in [references](references.md#what-happens-when-it-does-not-resolve).
`Ready` reports `WaitingForRef`, because that is the question a `kubectl wait` is asking, and
its message names the references that withheld the write. Nothing is logged at error and no
Event is emitted: a graph applied in any order puts every object with a forward reference
through this state, so it is normal.

`WaitingForKey` is no longer how an unresolved identity-bearing reference reports itself. "No
usable natural key" was the symptom; the unresolved reference is the cause, and the rule now
fires before the lookup that produced the symptom.

### Deferred fields are the exception, and the only one

A [deferred field](object-lifecycle.md) does not block. That is not a hole in the rule — it is
the case the rule cannot apply to. `DeferAlways` exists for a reference that *cannot* resolve
before the object exists: `dcim.Device.primary_ip4` needs an address that needs an interface
that needs the Device, so there is no apply order in which the precondition is satisfiable, and
refusing to create the Device until it is would deadlock. `DeferIfUnresolved` is the same trade
made conditionally, for a `parent` outside the natural key.

So the engine creates such an object, strips or omits the deferred column, records the field in
`status.deferredPending`, and applies it with a follow-up `PATCH`. A descriptor declaring
`Deferred` is its author stating exactly that trade, for exactly that field — which is why it
is data on the descriptor and not a rule in the engine.

The omission is still not silent: a pending deferral holds `Ready=False`, with
`WaitingForRef` while the reference has not resolved and `DeferredFieldPending` once it has.

## Condition conventions

`setCondition` wraps `meta.SetStatusCondition`, which is what keeps
`lastTransitionTime` honest — it only moves when `status` actually changes, so a condition
that has been `False` for an hour says so instead of resetting on every reconcile.

**Every condition carries `ObservedGeneration`**, and `writeStatus` sets
`status.observedGeneration` on every path, success and failure alike. This is what lets a
reader tell "reconciled and healthy" from "not yet looked at since the last edit". Without
it, `kubectl wait --for=condition=Ready` returns immediately on a stale `True` from
before the spec change, and any automation built on that quietly does the wrong thing.

**The three condition types** (`api/v1alpha1/netboxendpoint_types.go:8`–`:17`):

| Type | Meaning | Set to `False` by |
|---|---|---|
| `Ready` | There is a usable client. Object controllers wait on this one. | every failure |
| `Authenticated` | The token was accepted. | `AuthError`, `TokenMissing`, `SecretMissing` — and set to `Unknown` by `CABundleMissing`, `InvalidConfig` and `ProbeFailed`, which never asked |
| `VersionSupported` | The server's version is in range. | `VersionUnsupported`, `VersionUnparseable` |

`fail`'s switch sets the condition the failure actually speaks to and sets the others to
`Unknown`, rather than leaving a previous answer standing. An authentication failure
happens upstream of the version probe, so it reports `VersionSupported=Unknown` with the
message "not probed: authentication failed first". A `ProbeFailed` or `InvalidConfig`
answers neither question, so `Authenticated` and `VersionSupported` both go `Unknown`.

`Unknown` plus a reason is a documented state, not an absence of one. The alternative was
a stale `Authenticated=True` sitting next to `Ready=False`, which reads as "the token is
fine" — not something that reconcile established. Anything other than `False` on
`Authenticated` or `VersionSupported` means the operator did not get far enough to have an
opinion; `Ready` is the one to act on.

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

### An Event or an error log on a repeating state is keyed on the transition

**The rule, in one sentence: an Event or an error-level log line on a state a pass can find
itself in twice is emitted only on the transition into that state, and the condition
carries the standing state.** It is not a rule about failures. It follows from
level-triggering: nothing distinguishes the first pass from the thousandth, so an
announcement that is not keyed on a change is an announcement per object per resync
period, forever.

The condition is written on every pass regardless, because the condition *is* the standing
state — and that is exactly why the announcement need not repeat. Anyone who wants the
current state reads the condition; the Event and the error line say only that the state is
new. Repeats drop to debug, so the detail is still there for whoever turns the verbosity
up.

Events are the expensive half. An Event is an API object: it costs etcd, it counts against
the namespace's retention, and a duplicate every resync evicts the Events somebody was
actually watching for. An error-level line is the same flood into a different sink, and it
buries whatever is genuinely new.

Metrics are the deliberate exception — `netbox_operator_reconcile_total`,
`netbox_operator_endpoint_reconcile_total` and `netbox_operator_drift_detected_total` count
every pass, because they are counters of reconciles rather than of changes, and a `rate()`
over them is how the retry rate of a stuck object, or the size of standing drift, is
visible at all. The asymmetry between an Event that fires once and a counter that keeps
moving is correct; both sides carry a comment saying so, because it reads like an
inconsistency to fix.

Three places apply the rule, and what counts as a change differs in the third:

| Site | Announced on a change in |
|---|---|
| `NetBoxEndpointReconciler.fail` / the Ready transition (NBO-010) | `Ready`'s `status` and `reason` |
| `pass.stop`, every non-success exit of the engine (NBO-081) | `Ready`'s `status` and `reason` |
| `pass.applyWrite`, the suppressed-write branch (NBO-087) | `Synced`'s `reason`, plus `DriftDetected`'s `message` |

The condition's `message` is excluded from the first two on purpose — a timeout whose
wording differs by a millisecond, or a NetBox body listing the same field errors in another
order, is not a state change, and keying on it would re-fire on every retry. It is included
in the third on purpose: there the message is the list of fields NetBox and the spec
disagree on, so a second field edited in NetBox underneath the operator is new information
about the same standing state. The test is whether the message *is* the state or merely a
rendering of it.

The third site is worth spelling out because it is not a failure at all. A `mode: DryRun`
endpoint, or one whose `driftMode` is `Report`, writes nothing and therefore finds the same
drift on every resync; the `Normal` Event saying what it would have written is a duplicate
per object per interval for as long as the mode is left on. `Report` is designed to be left
on for a week over an entire NetBox — maximum standing drift over maximum objects — so it
is precisely where a per-resync Event does the most damage while looking least like a bug.

## What this page does not cover

- Which NetBox failures are retried, where, and why — [errors and retries](errors-and-retries.md).
- How a live object is compared against the desired payload — [drift detection](drift.md).
- The per-object create/adopt/update loop, and the two-pass write a deferred field takes —
  [object lifecycle](object-lifecycle.md).
- Symptom-first diagnosis — [troubleshooting](../troubleshooting.md).
