# Troubleshooting

Symptom first, then the command that shows the cause. Only the `NetBoxEndpoint`
controller exists today, so every failure mode enumerated below is one it can produce —
between `api/v1alpha1/netboxendpoint_types.go` and `internal/netbox/errors.go` that set is
complete.

For why the operator reports rather than errors, and why the retry delays differ by
reason, see [reconciliation](../concepts/reconciliation.md).

## Start here

```
kubectl get netboxendpoints -A
```

The printer columns are `URL`, `MODE`, `VERSION`, `READY`, `AGE`
(`api/v1alpha1/netboxendpoint_types.go:148`–`:152`). `READY` is the one that matters;
`VERSION` being populated tells you the probe reached NetBox at least once.

```
kubectl describe netboxendpoint <name> -n <namespace>
```

Read the `Conditions` block bottom-up. `Ready` is always current, and it is the one to
act on. `Authenticated` and `VersionSupported` are set to `Unknown` whenever the reconcile
failed before it could establish them, with a reason saying so — `not probed:
authentication failed first`, for instance. So the three conditions never disagree: a
condition other than `False` on `Authenticated` or `VersionSupported` means the operator
did not get far enough to have an opinion, never that a stale answer is being reported as
current.

For the raw values, including `observedGeneration`:

```
kubectl get netboxendpoint <name> -n <namespace> -o jsonpath='{.status}' | jq
```

If `status.observedGeneration` is behind `metadata.generation`, the operator has not
finished a pass since your last edit. Compare them directly:

```
kubectl get netboxendpoint <name> -n <namespace> \
  -o custom-columns='GEN:.metadata.generation,OBSERVED:.status.observedGeneration,READY:.status.conditions[?(@.type=="Ready")].status'
```

## Symptom table

| Symptom | Command that shows the cause | Condition and `Reason` | What to do |
|---|---|---|---|
| `READY` is empty and `describe` shows no conditions at all | `kubectl logs -n <ns> deploy/<manager> \| grep netboxendpoint` | none — no reconcile has run | The controller is not running, not watching this namespace, or the manager is crash-looping. Check the pod, then check leader election if you run more than one replica. |
| `Ready=False`, message names a Secret | `kubectl get secret <name> -n <ns>` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown`, `Reason=SecretMissing` | A referenced Secret does not exist **in the endpoint's own namespace** — either `spec.tokenSecretRef.name` or `spec.tlsConfig.caBundleSecretRef.name`; the message says which. Cross-namespace refs are not supported by design. Retries every 30s. |
| A Secret that plainly exists reports `SecretMissing` | `kubectl get secret <name> -n <ns> --show-labels` | `Ready=False`, `Authenticated=False`, `Reason=SecretMissing`, message says the Secret "may exist but be invisible to the operator" | **The Secret is not labelled.** The operator reads Secrets through a label-scoped cache, so an unlabelled Secret is genuinely invisible to it even though `kubectl` shows it. Fix: `kubectl label secret <name> -n <ns> netbox.populator.io/endpoint-credential=true`. The operator cannot tell "absent" from "unlabelled" without an uncached read of the very Secret it is trying not to read, which is why the message covers both. See [RBAC](rbac.md). |
| `Ready=False`, message says `reading ca bundle secret` | `kubectl get secret <name> -n <ns>` | `Ready=False`, `Authenticated=Unknown`, `Reason=CABundleMissing` | The CA bundle Secret is absent. Distinct from `SecretMissing` on purpose: the token read fine, so `Authenticated` is `Unknown` rather than `False` and you are pointed at the right Secret. Retries every 30s. |
| `Ready=False`, message says `has no key "token"` | `kubectl get secret <name> -n <ns> -o jsonpath='{.data}' \| jq 'keys'` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown`, `Reason=TokenMissing` | The key is absent or its value is empty. Default key is `token`; set `spec.tokenSecretRef.key` if yours differs. An empty value counts as missing. Retries every 30s. |
| `Ready=False`, message mentions `401` or `403` | `kubectl describe netboxendpoint <name> -n <ns>` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown`, `Reason=AuthError` | The token is wrong, revoked, expired, or disabled. A read-only token is *not* this symptom — `/api/status/` only needs an authenticated read, so a token with no write permission still probes `Ready=True` and fails later, on the first write. Rotate the Secret — the watch picks it up immediately, no restart. Retries every 2m. |
| `Ready=False`, message quotes a version and a range | `kubectl get netboxendpoint <name> -n <ns> -o jsonpath='{.status.netboxVersion}'` | `Ready=False`, `VersionSupported=False`, `Reason=VersionUnsupported` | NetBox is outside `[4.2.0, 5.0.0)`. Upgrade NetBox, or point the endpoint at a supported instance. Retries every **10m** — an edit reconciles at once, so you will not wait. |
| `Ready=False`, message says `netbox reported version "..."` | `kubectl get netboxendpoint <name> -n <ns> -o jsonpath='{.status.netboxVersion}'` | `Ready=False`, `VersionSupported=False`, `Reason=VersionUnparseable` | The `netbox-version` value did not parse as `major.minor[.patch]`. Usually a proxy or a stub answering `/api/status/` instead of NetBox. The offending string is recorded in `status.netboxVersion` as well as the condition message. Retries every 10m. |
| `Ready=False`, message mentions the URL, a scheme, or a CA bundle | `kubectl get netboxendpoint <name> -n <ns> -o jsonpath='{.spec.url}{"\n"}'` | `Ready=False`, `Authenticated=Unknown`, `VersionSupported=Unknown`, `Reason=InvalidConfig` | `spec.url` is empty, unparseable, or not `http`/`https`; or the CA bundle Secret exists but is missing its key (default `ca.crt`) or holds no usable certificate. A CA bundle Secret that is *absent* reports `SecretMissing` instead. Retries every 2m. |
| `Ready=False`, message says `connection refused`, `no such host`, `i/o timeout`, `context deadline exceeded` | `kubectl describe netboxendpoint <name> -n <ns>` | `Ready=False`, `Authenticated=Unknown`, `VersionSupported=Unknown`, `Reason=ProbeFailed` | NetBox is unreachable from the operator's pod. Check DNS, NetworkPolicy, and the service. One attempt per reconcile — there are no client-side retries on the probe. Retries every 30s. |
| `Ready=False`, message says `netbox object not found at status` | `curl -s -o /dev/null -w '%{http_code}' <url>/api/status/` | `Ready=False`, `Reason=ProbeFailed` | `spec.url` points at something that is not a NetBox API root — a 404 on `/api/status/`. Check for a path prefix stripped or added by an ingress. |
| `Ready=False`, message says `unparseable response from status: <html…>` | `curl -sI <url>/api/status/` | `Ready=False`, `Reason=ProbeFailed` | A proxy, WAF or login page answered instead of NetBox. The message carries the response's first line, truncated to 200 characters (`internal/netbox/do.go:138`–`:148`). |
| TLS errors in the message: `x509: certificate signed by unknown authority` | `kubectl get netboxendpoint <name> -n <ns> -o jsonpath='{.spec.tlsConfig}'` | `Ready=False`, `Reason=ProbeFailed` | Supply the internal CA via `spec.tlsConfig.caBundleSecretRef`. `insecureSkipVerify: true` works and is reported on the object, because it is not a thing to forget about. |
| Token was rotated but 401s continue | `kubectl get netboxendpoint <name> -n <ns> -o jsonpath='{.status.conditions}' \| jq` | `Authenticated=False`, `Reason=AuthError` | Rotation is watched and should take effect in one reconcile. If it did not, the Secret you edited is not the one referenced, or it is in a different namespace. Confirm with the command in the next section. |
| A CA bundle was rotated and nothing changed | `kubectl get netboxendpoint <name> -n <ns> -o jsonpath='{.spec.tlsConfig.caBundleSecretRef.name}{"\n"}'` | n/a | Both referenced Secrets are indexed and watched, so a CA rotation takes effect on the next reconcile, same as a token. If it did not, the Secret you edited is not the one referenced, or it is in another namespace. |
| `Ready=True` but object controllers report the endpoint is not ready | — | n/a | No object Kinds exist yet (NBO-008 onward). If you see this, it is from a build not described by these docs. |
| Endpoint deleted but NetBox traffic continues | `kubectl logs -n <ns> deploy/<manager> \| grep 'endpoint not ready\|endpoint ready'` | n/a | The cached client is dropped when the reconcile observes the deletion. If traffic persists, the reconcile has not run — check the manager is alive. |
| `kubectl delete` hangs on an object | `kubectl get <kind> <name> -n <ns> -o jsonpath='{.status.conditions}' \| jq` | `Deleting=False`, `Reason=Protected` | NetBox refuses the delete because another object references it through a protected foreign key. The condition message names what blocks it. Delete the referencing object first; the retry backs off and will complete on its own. This is not an error to retry faster. |
| `kubectl delete` hangs and NetBox is down | `kubectl get netboxendpoint -n <ns>` | `Deleting=False`, `Reason=WaitingForEndpoint` | The object is real and its id is known, so the finalizer holds rather than orphaning it in NetBox. It completes once the endpoint is `Ready`. To force it through and accept the orphan, annotate `netbox.populator.io/skip-finalizer=true` — the condition message names this. See [deletion](../concepts/deletion.md). |

## Confirming which Secret is actually in use

The controller indexes endpoints by both referenced Secret names. To see the reference the
operator resolves, rather than the one you think you set:

```
kubectl get netboxendpoint <name> -n <namespace> \
  -o jsonpath='{.spec.tokenSecretRef.name}{"/"}{.spec.tokenSecretRef.key}{"\n"}'
```

An empty key means the default, `token`. Then check the value is non-empty:

```
kubectl get secret <secret> -n <namespace> -o jsonpath='{.data.token}' | base64 -d | wc -c
```

Anything other than a positive byte count produces `TokenMissing`.

## Forcing a reconcile

There is no `kubectl reconcile`. Any change to the object enqueues it, so an annotation
edit is the standard way:

```
kubectl annotate netboxendpoint <name> -n <namespace> \
  netbox.populator.io/force-sync="$(date -u +%FT%TZ)" --overwrite
```

Editing the referenced Secret has the same effect, via the Secret watch. Note that an
annotation change does **not** bump `metadata.generation` — only spec changes do — so use
the condition's `lastTransitionTime` or the manager log to confirm a pass happened, not
`observedGeneration`.

## Reading the logs

The controller's name is `netboxendpoint`
(`internal/controller/netboxendpoint_controller.go:269`), which is the value of the
`controller` field on every log line it emits.

Everything from this controller:

```
kubectl logs -n <ns> deploy/<manager> | grep '"controller":"netboxendpoint"'
```

Successful probes — one line per reconcile, carrying URL, version, mode and plugins:

```
kubectl logs -n <ns> deploy/<manager> | grep 'endpoint ready'
```

Failures, with the classified reason attached:

```
kubectl logs -n <ns> deploy/<manager> | grep 'endpoint not ready'
```

One object, following:

```
kubectl logs -n <ns> deploy/<manager> -f | grep '"name":"<endpoint-name>"'
```

Request bodies are logged at `-v=1` only, through a tested redaction pass rather than a
convention: `auth_psk`, `psk`, `preshared_key`, `password`, `token`, `secret`,
`private_key` and `api_key` are masked, and `custom_fields` collapse to their key names
(`internal/netbox/do.go:150`–`:174`). The API token itself never appears at any level. If
you need the wire detail, raise verbosity with `--zap-log-level=1`.

## Metrics

controller-runtime's standard metrics are labelled with the controller name. The manager
serves them on `--metrics-bind-address`, which defaults to `0` (disabled) — set it to
`:8443` to scrape.

| Metric | Use |
|---|---|
| `controller_runtime_reconcile_total{controller="netboxendpoint",result="requeue_after"}` | Normal operation. Every failure path lands here, not in `error`. |
| `controller_runtime_reconcile_total{controller="netboxendpoint",result="error"}` | Should be near zero. A rising value means the operator's own machinery is failing — status-update conflicts, informer-cache reads — not that NetBox is down. |
| `controller_runtime_reconcile_errors_total{controller="netboxendpoint"}` | Same signal. Safe to alert on precisely because NetBox's uptime does not feed it. |
| `controller_runtime_reconcile_time_seconds{controller="netboxendpoint"}` | A slow NetBox shows up here. A single reconcile can legitimately take minutes: see below. |

## A slow NetBox delays other endpoints

The endpoint controller runs with controller-runtime's default
`MaxConcurrentReconciles` of 1, so endpoints are reconciled one at a time. A NetBox that
black-holes packets rather than refusing them occupies that single worker for up to
`spec.timeout` — `30s` by default — and every other endpoint waits.

The probe deliberately does **one** attempt per reconcile: `buildConfig` sets
`MaxRetries: netbox.Retries(0)`, so the client does not retry and the 30-second requeue
is the retry. Before that, four client-side retries behind a 30-second timeout could hold
the worker for the better part of three minutes. It is bounded at one timeout now, but the
serialisation is still there.

Symptom: one unreachable endpoint, and every other endpoint's conditions updating a
timeout later than expected. Mitigation is to lower `spec.timeout` on the endpoint that is
timing out.

## Known reason constants

Complete as of the current code
(`api/v1alpha1/netboxendpoint_types.go:20`–`:29`). Anything not on this list is not
something this operator emits.

| `Reason` | Conditions it sets | Requeue |
|---|---|---|
| `Ready` | `Ready=True`, `Authenticated=True`, `VersionSupported=True` | `spec.resyncPeriod`, default `10m` |
| `SecretMissing` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown` | 30s |
| `TokenMissing` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown` | 30s |
| `AuthError` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown` | 2m |
| `ProbeFailed` | `Ready=False`, `Authenticated=Unknown`, `VersionSupported=Unknown` | 30s |
| `VersionUnsupported` | `Ready=False`, `VersionSupported=False` | 10m |
| `VersionUnparseable` | `Ready=False`, `VersionSupported=False` | 10m |
| `InvalidConfig` | `Ready=False`, `Authenticated=Unknown`, `VersionSupported=Unknown` | 2m |

`SecretMissing` covers either referenced Secret. `VersionUnsupported` and
`VersionUnparseable` leave `Authenticated` at `True`, because by then the token has
provably been accepted.

## Underlying NetBox error types

The reason is a translation of a typed client error, not an independent diagnosis
(`internal/controller/netboxendpoint_controller.go:211`). Full table and retry policy in
[errors and retries](../concepts/errors-and-retries.md).

| HTTP from NetBox | Client type | Becomes |
|---|---|---|
| 401, 403 | `*netbox.AuthError` | `AuthError` |
| 404 | `*netbox.NotFoundError` | `ProbeFailed` |
| 400, other 4xx, unparseable body | `*netbox.ValidationError` | `ProbeFailed` |
| 429 | `*netbox.RateLimitError` | `ProbeFailed` — the probe does not retry client-side |
| 5xx, transport failure | `*netbox.TransientError` | `ProbeFailed` — the probe does not retry client-side |
| 409 or a protected-FK body | `*netbox.ProtectedError` | not reachable from a `GET /api/status/` |
| >1 match on a single-object lookup | `*netbox.AmbiguousError` | not reachable from a `GET /api/status/` |

The last two exist for the object loop, which is
[designed and not yet implemented](../concepts/object-lifecycle.md).

## The blast radius

Two halves, and only one of them is fixed.

**The informer cache is scoped.** Since NBO-072 the manager applies a label selector to
both the LIST and the WATCH, so it caches only Secrets carrying
`netbox.populator.io/endpoint-credential=true` (`SecretCacheOptions()` in
`internal/controller/secretcache.go`). Manager memory therefore scales with the number of
credential Secrets, not with the cluster's total. That is why an unlabelled Secret is
invisible — see the symptom row above, which is the most common consequence.

**The RBAC is still cluster-wide.** The generated manager role remains a `ClusterRole`
granting `get`, `list` and `watch` on `secrets` across the cluster
(`config/rbac/role.yaml`). All three verbs are required: a scoped informer still LISTs and
WATCHes, and the selector becomes a query parameter rather than changing the verb, so none
of them can be dropped while the cache works this way.

So the operator's *cache* is narrow and its *permission* is not. A compromised controller
could still read every Secret in the cluster; it simply does not do so in normal operation.
Treat it accordingly when reviewing the deployment.

Narrowing the permission needs namespace-enumerated `Role`s instead of a `ClusterRole`,
which requires deploy-time knowledge of which namespaces hold credentials — that is
[issue #100](https://github.com/ricardomolendijk/netbox-operator/issues/100), still open,
and it is expected to arrive with the Helm chart. See
[RBAC](rbac.md) for what to label and the `kubectl auth can-i` check.

If the manager is being OOM-killed, the cache is no longer the first thing to suspect;
check how many Secrets carry the credential label.
