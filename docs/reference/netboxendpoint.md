# `NetBoxEndpoint`

| | |
|---|---|
| API version | `netbox.populator.io/v1alpha1` |
| Kind | `NetBoxEndpoint` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbep`, `nbendpoint` |
| Status subresource | yes |
| Lands with | NBO-004 (M1) |

A `NetBoxEndpoint` is a connection to one NetBox instance: a URL, a token, and the result
of probing them. Every object CR points at one through `endpointRef`, and gets a client
only once its endpoint is `Ready`.

Making the connection a CR rather than an operator flag buys three things: several
NetBoxes per cluster, credential rotation without a restart, and one place to gate on the
NetBox version.

## Minimal example

The fewest fields that work — everything else is defaulted.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: netbox-token
  namespace: homelab
type: Opaque
stringData:
  token: "0123456789abcdef0123456789abcdef01234567"
---
apiVersion: netbox.populator.io/v1alpha1
kind: NetBoxEndpoint
metadata:
  name: homelab
  namespace: homelab
spec:
  url: https://netbox.home.arpa
  tokenSecretRef:
    name: netbox-token
```

## Full example

Every field set explicitly, with the defaults written out.

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: netbox-token
  namespace: homelab
type: Opaque
stringData:
  token: "0123456789abcdef0123456789abcdef01234567"
---
apiVersion: v1
kind: Secret
metadata:
  name: netbox-ca
  namespace: homelab
type: Opaque
stringData:
  ca.crt: |
    -----BEGIN CERTIFICATE-----
    MIIBkTCB+wIJAJ...
    -----END CERTIFICATE-----
---
apiVersion: netbox.populator.io/v1alpha1
kind: NetBoxEndpoint
metadata:
  name: homelab
  namespace: homelab
spec:
  url: https://netbox.home.arpa            # a trailing /api is accepted and normalised
  tokenSecretRef:
    name: netbox-token
    key: token                             # default
  tlsConfig:
    insecureSkipVerify: false              # default
    caBundleSecretRef:
      name: netbox-ca
      key: ca.crt                          # default
  timeout: 30s                             # default
  mode: Apply                              # default; the other value is DryRun
  driftMode: Correct                       # default; also Report or Off
  resyncPeriod: 10m                        # default; ignored when driftMode is Off
  rateLimit:
    qps: 10
    burst: 20                              # ignored unless qps > 0
```

Both Secrets and the `NetBoxEndpoint` must be in the **same namespace** — `SecretKeyRef`
has no `namespace` field, deliberately. A namespace that needs its own endpoint creates its
own Secret. See [Secret RBAC](#secret-rbac-and-the-current-blast-radius) for why, and for
what the operator's Secret permissions currently are.

## `spec`

### `spec.url`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Default | none |
| Validation | ``+kubebuilder:validation:Pattern=`^https?://` `` |

Base URL of the NetBox instance. A trailing `/api` is accepted: the client strips one and
appends exactly one, so `https://netbox.home.arpa` and `https://netbox.home.arpa/api` are
equivalent. A path prefix is preserved — `https://host/netbox` becomes
`https://host/netbox/api`.

**If it is wrong.** The pattern is enforced at admission, so `netbox.home.arpa` (no scheme)
and `ftp://…` are rejected by `kubectl apply` before the object is stored. A URL that
matches the pattern but cannot be parsed fails at reconcile with
`Ready=False, Reason=InvalidConfig`. A URL that parses but does not answer gives
`Ready=False, Reason=ProbeFailed` with the transport error in the message.

### `spec.tokenSecretRef`

| | |
|---|---|
| Type | `object` (`SecretKeyRef`) |
| Required | yes |
| Default | none |

The Secret holding the NetBox API token, in this namespace.

### `spec.tokenSecretRef.name`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Default | none |
| Validation | `+kubebuilder:validation:MinLength=1` |

Name of the Secret. Empty is rejected at admission.

**If it is wrong.** A Secret that does not exist gives
`Ready=False, Authenticated=False, Reason=SecretMissing`, message
`reading token secret homelab/netbox-token: secrets "netbox-token" not found`. Retried
every 30s, and the Secret watch re-reconciles as soon as it is created.

### `spec.tokenSecretRef.key`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Default | `token`, applied by the controller |
| Validation | none |

Which key of the Secret holds the token. The default is applied in the controller rather
than by a `+kubebuilder:default` marker, because `SecretKeyRef` is shared with
`caBundleSecretRef`, which needs a different one — a marker applies at every use of the
struct.

**If it is wrong.** A key that is absent, or present with an empty value, gives
`Ready=False, Authenticated=False, Reason=TokenMissing`, message
`token missing: secret homelab/netbox-token has no key "token"`. Retried every 30s. A key
that is present but holds a token NetBox rejects gives
`Ready=False, Authenticated=False, Reason=AuthError` instead, retried every 2 minutes.

### `spec.tlsConfig`

| | |
|---|---|
| Type | `object` (`TLSConfig`) |
| Required | no |
| Default | none (nil — system roots, verification on) |

TLS handshake settings. Omit it entirely for a NetBox with a publicly trusted certificate.

### `spec.tlsConfig.insecureSkipVerify`

| | |
|---|---|
| Type | `boolean` |
| Required | no |
| Default | `false` (Go zero value; there is no `+kubebuilder:default` marker) |
| Validation | none |

Disables certificate verification for this endpoint. Prefer `caBundleSecretRef`. The client
floors TLS at 1.2 either way.

**If it is wrong.** Nothing fails, which is the problem: `true` makes a
man-in-the-middle undetectable. There is no condition and no status field for it, but it
is not silent either — every successful reconcile logs it at `info` on the `endpoint ready`
line, so it is greppable and it shows up in whatever collects the operator's logs:

```
INFO  endpoint ready  {"url": "https://netbox.home.arpa", "netboxVersion": "4.6.8",
                       "mode": "Apply", "plugins": ["netbox_topology_views"],
                       "insecureSkipVerify": true}
```

A certificate problem with this left `false` surfaces as
`Ready=False, Reason=ProbeFailed` with the `x509` error in the message.

### `spec.tlsConfig.caBundleSecretRef`

| | |
|---|---|
| Type | `object` (`SecretKeyRef`) |
| Required | no |
| Default | none |

Additional trusted roots, PEM-encoded, in a Secret in this namespace. When set, the bundle
*replaces* the root pool for this endpoint rather than adding to the system roots.

`caBundleSecretRef.key` defaults to `ca.crt`. Like the token key, that default lives in
the controller rather than in a `+kubebuilder:default` marker, precisely because the
marker would apply to both uses of `SecretKeyRef` and default a CA bundle's key to
`token`.

**If it is wrong.**

| Fault | Reason | Backoff | Message |
|---|---|---|---|
| Secret does not exist | `SecretMissing` | 30s | `reading ca bundle secret homelab/netbox-ca: secrets "netbox-ca" not found` |
| key absent or empty | `InvalidConfig` | 2m | `ca bundle secret homelab/netbox-ca has no key "ca.crt"` |
| value is not usable PEM | `InvalidConfig` | 2m | `ca bundle contains no usable certificates` |

A missing Secret is a missing Secret whichever field referenced it, so it reports
`SecretMissing` on the same 30s backoff as a missing token Secret. One wrinkle worth
knowing: that path also sets `Authenticated=False`, even though the token itself read
fine — read the message, not just the reason.

Both Secrets are indexed and watched, so rotating a CA bundle is noticed as promptly as
rotating a token.

### `spec.timeout`

| | |
|---|---|
| Type | `string` (`metav1.Duration`, e.g. `30s`, `1m30s`) |
| Required | no |
| Default | `30s` (`+kubebuilder:default="30s"`) |
| Validation | none |

Timeout for a single HTTP request to NetBox. `0s` falls back to 30s in the client.

**If it is wrong.** The CRD schema for a `metav1.Duration` is a plain string, so an
unparseable value such as `30 seconds` is **not** rejected at admission. It fails when the
operator decodes the object, which produces no condition on the object at all — look in the
manager log. A timeout that is merely too short shows up as
`Ready=False, Reason=ProbeFailed` with `context deadline exceeded`.

### `spec.mode`

| | |
|---|---|
| Type | `string` (`EndpointMode`) |
| Required | no |
| Default | `Apply` (`+kubebuilder:default=Apply`) |
| Validation | `+kubebuilder:validation:Enum=Apply;DryRun` |

Permitted values, verbatim: `Apply`, `DryRun`.

`Apply` permits writes. `DryRun` suppresses every `POST`, `PATCH` and `DELETE` through this
endpoint; reads still hit the live API, so drift is still detected and reported against
real state.

**If it is wrong.** Any other value is rejected at admission by the enum. `DryRun` does not
fail anything — the endpoint still reaches `Ready=True`, and dependent objects report drift
without correcting it. If objects are stuck reporting drift that never clears, check this
field first.

### `spec.driftMode`

| | |
|---|---|
| Type | `string` (`DriftMode`) |
| Required | no |
| Default | `Correct` (`+kubebuilder:default=Correct`) |
| Validation | `+kubebuilder:validation:Enum=Correct;Report;Off` |

Permitted values, verbatim: `Correct`, `Report`, `Off`.

What the operator does when NetBox stops matching a CR that points at this endpoint.

| Value | Detects drift | Corrects it | Periodic resync |
|---|---|---|---|
| `Correct` | yes | yes | yes |
| `Report` | yes | no | yes |
| `Off` | only on a CR change | yes | no |

`Correct` is the default and the intended steady state: Git is authoritative, so a
NetBox-side edit is simply wrong ([ADR-0005](../decisions/0005-gitops-coexistence.md)).

`Report` detects drift and sends **nothing at all** — no `POST`, `PATCH` or `DELETE`,
including the finalizer's delete. It sets `DriftDetected=True` with the field list, emits a
`DriftDetected` Event, and moves `netbox_operator_drift_detected_total` without moving
`netbox_operator_drift_corrected_total`. It is enforced by giving the endpoint a client that
cannot mutate, so it does not depend on every write path checking a flag: a half-mutating
dry run teaches people to distrust the mode. Objects sit at
`Ready=False, Reason=ReportPending` for as long as it is on, which is honest rather than a
bug — nothing converges in `Report`, and it is migration time rather than an operating mode.

`Off` disables the periodic drift re-check, so the operator acts only when a CR changes. It
does **not** disable the retries that unblock an object waiting for its endpoint or refused a
conflicting object, and it does not affect this endpoint's own re-probe, which is about the
token and the version rather than about drift.

There is deliberately no value in which NetBox wins and the difference is promoted back into
a `spec`: that would make the operator a second writer to desired state. `nbctl export`
(NBO-040) is the supported way to turn NetBox's contents into manifests.

**If it is wrong.** Any other value is rejected at admission by the enum. An endpoint stored
before this field existed carries the empty string, which behaves as `Correct` — the CRD
default cannot retrofit itself onto a stored object, and an upgrade that silently stopped
correcting drift would be worse than a schema error. `Report` and `Off` do not fail anything:
the endpoint still reaches `Ready=True`, and the symptom is on the objects. See the
troubleshooting table below for telling `Report` apart from `mode: DryRun`, which looks
identical until you read the `Reason`.

### `spec.resyncPeriod`

| | |
|---|---|
| Type | `string` (`metav1.Duration`) |
| Required | no |
| Default | `10m` (`+kubebuilder:default="10m"`) |
| Validation | none |

How often a `Ready` endpoint re-probes NetBox even when nothing changed. This is the
requeue interval on the success path, for this endpoint and for every object that points at
it.

Ignored for objects when `spec.driftMode` is `Off`, which is the whole of what "no periodic
resync" means. The endpoint's own re-probe continues either way: that checks the token, the
version and reachability, none of which is drift.

**If it is wrong.** Zero or negative falls back to 10 minutes. A very short period
multiplies request volume against NetBox by every endpoint in the cluster; pair it with
`spec.rateLimit`. Same admission caveat as `spec.timeout` for an unparseable value.

### `spec.rateLimit`

| | |
|---|---|
| Type | `object` (`RateLimit`) |
| Required | no |
| Default | none (nil — no client-side limit) |

Client-side rate limiting, per endpoint.

### `spec.rateLimit.qps`

| | |
|---|---|
| Type | `integer` (`int32`) |
| Required | no |
| Default | none — the Go zero value `0`, which means **unlimited** |
| Validation | `+kubebuilder:validation:Minimum=0` |

Sustained requests per second. `0` means no limiter is constructed at all.

**If it is wrong.** A negative value is rejected at admission. Setting it too low does not
fail anything visible on this object: requests queue client-side and dependent objects get
slower, eventually timing out per `spec.timeout`.

### `spec.rateLimit.burst`

| | |
|---|---|
| Type | `integer` (`int32`) |
| Required | no |
| Default | none at the API level; the client uses `qps` when this is `0` |
| Validation | `+kubebuilder:validation:Minimum=0` |

Token-bucket size.

**If it is wrong.** A negative value is rejected at admission. `burst` set with `qps: 0` is
**silently ignored**, because no limiter is built when `qps` is `0` — set `qps` as well.

## `status`

Every field is optional and written by the controller. `status` is a subresource, so
`kubectl apply` never touches it.

| Field | Type | Populated by | When |
|---|---|---|---|
| `netboxVersion` | `string` | the `netbox-version` key of `GET /api/status/` | on success, **and** on `VersionUnsupported` and `VersionUnparseable` — it is recorded before the version gate runs, so the offending string is always visible |
| `plugins` | `[]string` | the `plugins` map of `GET /api/status/` | same as `netboxVersion`. Unordered. Recorded because a plugin that adds a required custom field is an otherwise baffling source of 400s |
| `observedGeneration` | `int64` | `metadata.generation` at the time status was written | every status write, success or failure. `observedGeneration < metadata.generation` means the conditions describe an older spec |
| `conditions` | `[]metav1.Condition` | the controller | every reconcile. `+listType=map`, `+listMapKey=type` |

`netboxVersion` and `plugins` are **not** cleared on failure, and are only written once
the probe has returned — so on `ProbeFailed`, `AuthError`, `SecretMissing`, `TokenMissing`
or `InvalidConfig` they hold whatever the last successful probe left there, or nothing.
Read them together with `observedGeneration` and the `Ready` condition, not on their own.

## Conditions

| Type | `True` when | `False` when | `Unknown` when | Reasons it can carry |
|---|---|---|---|---|
| `Ready` | a client was built, authenticated and version-checked, and is in the cache | any failure at all | never | `Ready`, `SecretMissing`, `TokenMissing`, `AuthError`, `ProbeFailed`, `VersionUnsupported`, `VersionUnparseable`, `InvalidConfig` |
| `Authenticated` | `GET /api/status/` was accepted with the token | the token could not be read, or NetBox rejected it | the probe never ran, so the token was not tried: `ProbeFailed`, `InvalidConfig` | `Ready`, `SecretMissing`, `TokenMissing`, `AuthError`, `ProbeFailed`, `InvalidConfig` |
| `VersionSupported` | the reported version parsed and is in `[4.2.0, 5.0.0)` | it did not parse, or is out of range | the version was never obtained: `SecretMissing`, `TokenMissing`, `AuthError`, `ProbeFailed`, `InvalidConfig` | `Ready`, `VersionUnsupported`, `VersionUnparseable`, `SecretMissing`, `TokenMissing`, `AuthError`, `ProbeFailed`, `InvalidConfig` |

`Ready` is the one object controllers wait on. A client is handed out only while it is
`True`; every failure drops the cached client, so nothing keeps writing through a
connection NetBox has since rejected.

**`Unknown` means "this reconcile did not establish it", and it is written deliberately.**
A failure upstream of a check does not leave that check's previous answer standing: an
authentication failure sets `VersionSupported=Unknown` with the message
`not probed: authentication failed first`, and a `ProbeFailed` or `InvalidConfig` sets
*both* downstream conditions to `Unknown` with the cause appended. So the three conditions
describe one reconcile rather than a mixture of this one and an older one, and a stale
`Authenticated=True` next to `Ready=False` — which reads as "the token is fine" when
nothing established that — cannot happen.

The one condition not rewritten on every path is `Authenticated` under
`VersionUnsupported` / `VersionUnparseable`. Reaching the version gate means the probe
succeeded, so the token *was* accepted; the condition is simply left as it was, which means
it can be absent on a first reconcile that lands straight on a version failure.

### Reasons

| Reason | Meaning |
|---|---|
| `Ready` | success. Carried by all three conditions when they are `True` |
| `SecretMissing` | a referenced Secret does not exist in this namespace — `spec.tokenSecretRef.name` or `spec.tlsConfig.caBundleSecretRef.name` |
| `TokenMissing` | the Secret exists but the key is absent or empty |
| `AuthError` | NetBox returned 401 or 403 — the token is wrong, expired, or lacks permission |
| `ProbeFailed` | `GET /api/status/` failed for any other reason: unreachable host, TLS failure, 5xx, timeout, or a status body with no `netbox-version` |
| `VersionUnsupported` | the version parsed and is outside `>=4.2, <5.0` |
| `VersionUnparseable` | the version string could not be parsed at all |
| `InvalidConfig` | the client could not be constructed: unparseable URL, non-http scheme, or a CA bundle Secret that exists but has no usable PEM under the key |

### Retry intervals

Retries are spaced by how likely a retry is to help.

| Outcome | Requeue after |
|---|---|
| `Ready=True` | `spec.resyncPeriod` (default 10m) |
| `VersionUnsupported`, `VersionUnparseable` | 10m — it will not fix itself; re-probing every 30s is pure noise |
| `AuthError`, `InvalidConfig` | 2m |
| `SecretMissing`, `TokenMissing`, `ProbeFailed` | 30s |

None of these is returned as a controller error. An unreachable or misconfigured NetBox is
a condition on this object, not a controller failure — otherwise the manager's error rate
becomes a function of someone else's uptime.

The probe itself makes **one attempt per reconcile**: the controller builds its client with
`MaxRetries: netbox.Retries(0)`, so there are no client-side retries on top of the requeue.
One worker serves every endpoint, and four retries behind a 30-second timeout would let a
single black-holed NetBox stall every other endpoint for minutes. The requeue in the table
above is the retry.

## The supported-version gate

The operator refuses to run against a NetBox outside `>=4.2, <5.0`
(`netbox.MinVersion = "4.2.0"`, `netbox.MaxVersion = "5.0.0"`, compared as a half-open
range). The version comes from the `netbox-version` key of `GET /api/status/`; anything
after the third component is ignored, so `4.6.8-Docker-3.2.0` parses as `4.6.8`.

**The lower bound is the whole point of the check.** NetBox 4.2 replaced the `site` foreign
key on `ipam.Prefix`, `virtualization.Cluster`, `wireless.WirelessLAN` and `ipam.VLANGroup`
with a polymorphic `(scope_type, scope_id)` pair. The first three now carry it through
`CachedScopeMixin`, whose `_site` / `_region` / `_site_group` / `_location` columns are
denormalised caches, and `ipam.VLANGroup` declares `scope_type` / `scope_id` on the model
itself (`docs/netbox-schema.md` → `dcim.CachedScopeMixin`, `ipam.Prefix`,
`virtualization.Cluster`, `wireless.WirelessLAN`, `ipam.VLANGroup`).

On 4.2+, writing `site` to those models **silently no-ops**. That is the failure mode the
gate exists for: an operator pointed at an out-of-range server does not error, it quietly
does nothing, and NetBox looks like it is ignoring you. Carrying two field mappings instead
would mean the payload for four kinds depends on the server version, so the operator
refuses to run rather than guess.

The upper bound is a guess about a major version that does not exist yet, which is the
point: refusing to touch NetBox 5.0 until someone has checked is cheaper than discovering
the difference by writing to it.

`status.netboxVersion` **is** recorded when the version is merely unsupported, so the
object tells you what it found:

```
Conditions:
  Type              Status  Reason               Message
  VersionSupported  False   VersionUnsupported   netbox 3.7.8 is outside the supported range >=4.2.0, <5.0.0
  Ready             False   VersionUnsupported   netbox 3.7.8 is outside the supported range >=4.2.0, <5.0.0
```

It is recorded on `VersionUnparseable` too — the version is written to status *before* the
gate runs, precisely because "that is not a version" is the case where seeing the raw
string matters most:

```
Conditions:
  Type              Status   Reason               Message
  VersionSupported  False    VersionUnparseable   netbox reported version "": empty version string
  Ready             False    VersionUnparseable   netbox reported version "": empty version string
```

## Rotating a token or a CA bundle

Write the new value into the Secret. That is the whole procedure — no restart, and nothing
to invalidate by hand.

The controller watches Secrets and maps a Secret event back to the endpoints that reference
it, through a field index (`spec.secretRefs`) that covers **both**
`spec.tokenSecretRef.name` and `spec.tlsConfig.caBundleSecretRef.name`. So a CA rotation is
noticed as promptly as a token rotation, rather than waiting for the next
`spec.resyncPeriod`.

What makes the new value take effect is eviction, not a cache miss: the reconciler does not
read through the client cache before building. It builds a client every reconcile and calls
`put`, which drops any previous entry for the same endpoint and closes that client's idle
connections. Editing `spec` goes through the same path. The cache key still carries the spec
generation and the token Secret's `resourceVersion`, which makes an entry self-describing in
a dump and is what a future read-through path would need.

Object controllers read the cache by `(namespace, name)`; a miss means the endpoint is not
`Ready` yet.

## Printer columns

```
$ kubectl get netboxendpoints -n homelab
NAME      URL                          MODE     DRIFT     VERSION   READY   AGE
homelab   https://netbox.home.arpa     Apply    Correct   4.6.8     True    3d
lab       https://netbox.lab.internal  DryRun   Correct   4.6.8     True    3d
legacy    https://netbox.legacy.dc     Apply    Report    4.6.8     True    2d
staging   https://netbox.stg.internal  Apply    Correct             False   11m
```

| Column | Type | Source |
|---|---|---|
| `NAME` | string | `metadata.name` |
| `URL` | string | `.spec.url` |
| `MODE` | string | `.spec.mode` |
| `DRIFT` | string | `.spec.driftMode` |
| `VERSION` | string | `.status.netboxVersion` |
| `READY` | string | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | date | `.metadata.creationTimestamp` |

An empty `VERSION` with `READY=False` means the probe never completed. `kubectl get nbep`
works too.

## Troubleshooting

| Symptom | Condition you would see | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, "should match `^https?://`" | none; admission refused the object | `spec.url` has no scheme | Write `https://host`, not `host` |
| `kubectl apply` rejected, "Unsupported value" on `mode` | none; admission refused the object | `spec.mode` is not `Apply` or `DryRun` | Use one of the two, exactly as spelled |
| `READY=False` immediately after apply | `Ready=False, Authenticated=False, Reason=SecretMissing` | a referenced Secret is absent, or in another namespace. Read the message — it names which one | Create the Secret **in the endpoint's namespace**; there is no cross-namespace ref |
| `READY=False`, Secret exists | `Ready=False, Authenticated=False, Reason=TokenMissing` | wrong key name, or the key holds an empty string | Match `spec.tokenSecretRef.key` to the Secret's key, or drop it to use `token` |
| `READY=False` after a token rotation | `Ready=False, Authenticated=False, Reason=AuthError` | NetBox returned 401/403 — token revoked, expired, or read-only | Issue a token with write permission; the Secret watch picks it up with no restart |
| `READY=False`, message names `x509` | `Ready=False, Reason=ProbeFailed`, both other conditions `Unknown` | NetBox serves a certificate the operator does not trust | Add the root via `spec.tlsConfig.caBundleSecretRef`; the key defaults to `ca.crt` |
| `READY=False`, message names `no usable certificates` | `Ready=False, Reason=InvalidConfig` | the CA Secret key holds something that is not PEM | Check the value is a PEM certificate, and that `caBundleSecretRef.key` names the right key |
| `READY=False`, message names `ca bundle secret … has no key` | `Ready=False, Reason=InvalidConfig` | the CA Secret exists but not under that key | Create the key, or point `caBundleSecretRef.key` at the one you have |
| `READY=False`, message names `context deadline exceeded` | `Ready=False, Reason=ProbeFailed` | NetBox is slow or unreachable, or `spec.timeout` is too short | Raise `spec.timeout`; check the URL is reachable from the operator's network |
| `READY=False`, `VERSION` populated | `VersionSupported=False, Reason=VersionUnsupported` | NetBox is older than 4.2 or 5.0+ | Upgrade NetBox to 4.2+. This is intentional — see the version gate above |
| `READY=False`, `VERSION` holds something that is not a version | `VersionSupported=False, Reason=VersionUnparseable` | `GET /api/status/` returned a version string the parser could not read, or a proxy replaced the body. The raw string is in `status.netboxVersion` and in the message | Confirm `curl -H "Authorization: Token …" $URL/api/status/` returns JSON with `netbox-version` |
| `READY=False`, no condition changes at all, nothing in status | none | the object failed to decode — usually an unparseable `timeout` or `resyncPeriod` | Fix the duration string (`30s`, not `30 seconds`); check the manager log |
| `kubectl apply` rejected, "Unsupported value" on `driftMode` | none; admission refused the object | `spec.driftMode` is not `Correct`, `Report` or `Off` | Use one of the three, exactly as spelled. `Off` needs quoting in YAML if your tooling folds it to a boolean |
| Objects report drift that never clears | `Ready=True` on the endpoint; objects at `Ready=False, Reason=DryRunPending` | `spec.mode: DryRun` — reads happen, writes are suppressed | Set `mode: Apply` |
| Objects report drift that never clears | `Ready=True` on the endpoint; objects at `Ready=False, Reason=ReportPending` | `spec.driftMode: Report` — a different field with the same symptom. The `Reason` is what tells them apart | Set `driftMode: Correct` |
| A NetBox UI edit is never noticed | `Ready=True` on the endpoint; objects at `Ready=True, DriftDetected=False` | `spec.driftMode: Off` — nothing re-checks on a timer | Set `driftMode: Correct`, or touch the CR |
| Objects stuck `WaitingForEndpoint` | endpoint is `Ready=False` for any reason | no client is handed out while `Ready` is false | Fix the endpoint; the objects converge on their own |
| Conditions look wrong for the spec you just applied | `status.observedGeneration` < `metadata.generation` | the reconcile for the new spec has not completed | Wait one reconcile; if it persists, check the manager log |
| `Authenticated=Unknown` and `VersionSupported=Unknown` | `Ready=False, Reason=ProbeFailed` or `InvalidConfig` | the probe never ran, so neither question was answered this reconcile. `Unknown` is the honest answer, not a bug | Fix the cause named in the `Ready` message |

## Secret RBAC, and the current blast radius

The Secret reference is same-namespace only, and that is deliberate: `SecretKeyRef` has no
`namespace` field because reading a Secret across namespaces is a privilege escalation, and
supporting it would force the operator's Secret permissions to be cluster-wide.

Be aware that today they are anyway. The controller's RBAC marker generates a `ClusterRole`
granting `get`, `list` and `watch` on Secrets **cluster-wide**, rather than a `Role` per
namespace that actually contains a `NetBoxEndpoint`. NBO-004's design note calls for the
narrower grant; narrowing it is tracked as
[issue #100](https://github.com/ricardomolendijk/netbox-operator/issues/100). Until that
lands, deploying this operator gives it read access to every Secret in the cluster — worth
knowing before you install it in a shared cluster.

## Related

- [Errors and retries](../concepts/errors-and-retries.md) — how a NetBox HTTP failure
  becomes a typed error, and why `AuthError` fails the endpoint rather than every object.
- [ADR-0002](../decisions/0002-crd-scoping.md) — why this kind is namespaced, and what
  per-namespace endpoints cost.
- [Coexisting with Flux and Argo CD](../operations/gitops.md) — the three drift modes in
  operational terms, the Argo CD and Flux snippets that make this quiet, and the
  recommended NetBox permission model.
