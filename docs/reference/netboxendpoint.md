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
      key: ca.crt                          # you must set this; the default is "token"
  timeout: 30s                             # default
  mode: Apply                              # default; the other value is DryRun
  resyncPeriod: 10m                        # default
  rateLimit:
    qps: 10
    burst: 20                              # ignored unless qps > 0
```

The Secret and the `NetBoxEndpoint` must be in the **same namespace**. `SecretKeyRef` has
no `namespace` field, deliberately: reading a Secret from another namespace is a privilege
escalation dressed up as convenience, and supporting it would make the operator's Secret
RBAC cluster-wide. A namespace that needs its own endpoint creates its own Secret.

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
| Default | `token` (`+kubebuilder:default=token`) |
| Validation | none |

Which key of the Secret holds the token.

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

**If it is wrong.** Nothing fails; that is the problem. `true` makes a man-in-the-middle
undetectable and produces no condition and no status field of its own. A certificate
problem with this left `false` surfaces as `Ready=False, Reason=ProbeFailed` with the
`x509` error in the message.

### `spec.tlsConfig.caBundleSecretRef`

| | |
|---|---|
| Type | `object` (`SecretKeyRef`) |
| Required | no |
| Default | none |

Additional trusted roots, PEM-encoded, in a Secret in this namespace. When set, the bundle
*replaces* the root pool for this endpoint rather than adding to the system roots.

`caBundleSecretRef.key` reuses the `SecretKeyRef` type, so **its default is `token`, not
`ca.crt`** — the `+kubebuilder:default=token` marker applies at every use of the struct.
Set `key: ca.crt` explicitly if that is what your Secret calls it.

**If it is wrong.** All three failures land on the same reason, `InvalidConfig`, retried
every 2 minutes — note that a missing CA Secret reports `InvalidConfig` and *not*
`SecretMissing`:

| Fault | Message |
|---|---|
| Secret does not exist | `reading ca bundle secret homelab/netbox-ca: secrets "netbox-ca" not found` |
| key absent or empty | `ca bundle secret homelab/netbox-ca has no key "token"` |
| value is not usable PEM | `ca bundle contains no usable certificates` |

Also note: only `spec.tokenSecretRef.name` is indexed for the Secret watch. Editing the CA
bundle Secret does **not** trigger a reconcile — wait for `resyncPeriod`, or touch the
endpoint.

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

### `spec.resyncPeriod`

| | |
|---|---|
| Type | `string` (`metav1.Duration`) |
| Required | no |
| Default | `10m` (`+kubebuilder:default="10m"`) |
| Validation | none |

How often a `Ready` endpoint re-probes NetBox even when nothing changed. This is the
requeue interval on the success path.

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
| `netboxVersion` | `string` | the `netbox-version` key of `GET /api/status/` | on success, **and** on `VersionUnsupported` — deliberately, so the message and the field agree |
| `plugins` | `[]string` | the `plugins` map of `GET /api/status/` | same as `netboxVersion`. Unordered. Recorded because a plugin that adds a required custom field is an otherwise baffling source of 400s |
| `observedGeneration` | `int64` | `metadata.generation` at the time status was written | every status write, success or failure. `observedGeneration < metadata.generation` means the conditions describe an older spec |
| `conditions` | `[]metav1.Condition` | the controller | every reconcile. `+listType=map`, `+listMapKey=type` |

`netboxVersion` and `plugins` are **not** cleared on failure, and are **not** set when the
probe never got a parseable version — so on `VersionUnparseable`, `ProbeFailed`,
`AuthError`, `SecretMissing`, `TokenMissing` or `InvalidConfig` they hold whatever the last
successful probe left there, or nothing. Read them together with `observedGeneration` and
the `Ready` condition, not on their own.

## Conditions

| Type | True when | False when | Reasons it can carry |
|---|---|---|---|
| `Ready` | a client was built, authenticated and version-checked, and is in the cache | any failure at all | `Ready`, `SecretMissing`, `TokenMissing`, `AuthError`, `ProbeFailed`, `VersionUnsupported`, `VersionUnparseable`, `InvalidConfig` |
| `Authenticated` | `GET /api/status/` was accepted with the token | the token could not be read or was rejected | `Ready`, `SecretMissing`, `TokenMissing`, `AuthError` |
| `VersionSupported` | the reported version parsed and is in `[4.2.0, 5.0.0)` | it did not parse, or is out of range | `Ready`, `VersionUnsupported`, `VersionUnparseable` |

`Ready` is the one object controllers wait on. A client is handed out only while it is
`True`; every failure drops the cached client, so nothing keeps writing through a
connection NetBox has since rejected.

`Authenticated` and `VersionSupported` are only *touched* by the failures listed against
them. A failure of another kind — `ProbeFailed`, `InvalidConfig` — sets `Ready=False` and
leaves the other two at their previous values, so you can legitimately see
`Ready=False, Reason=ProbeFailed` alongside a stale `Authenticated=True` from the last good
reconcile. Trust `Ready`, and check `observedGeneration`.

### Reasons

| Reason | Meaning |
|---|---|
| `Ready` | success. Carried by all three conditions when they are `True` |
| `SecretMissing` | the Secret named by `spec.tokenSecretRef.name` does not exist in this namespace |
| `TokenMissing` | the Secret exists but the key is absent or empty |
| `AuthError` | NetBox returned 401 or 403 — the token is wrong, expired, or lacks permission |
| `ProbeFailed` | `GET /api/status/` failed for any other reason: unreachable host, TLS failure, 5xx, timeout, or a status body with no `netbox-version` |
| `VersionUnsupported` | the version parsed and is outside `>=4.2, <5.0` |
| `VersionUnparseable` | the version string could not be parsed at all |
| `InvalidConfig` | the client could not be constructed: unparseable URL, non-http scheme, or an unreadable / unusable CA bundle |

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

It is **not** recorded on `VersionUnparseable`, because the controller never got a version
it could use.

## Token rotation

The client cache is keyed by `(namespace, name, spec generation, token Secret
resourceVersion)`, and the controller watches Secrets, mapping a Secret event back to the
endpoints that reference it by name via a field index on `spec.tokenSecretRef.name`.

So rotating a token is: write the new value into the Secret. The Secret gets a new
`resourceVersion`, the watch enqueues the endpoint, the cache key misses, and the next
reconcile builds a client with the new token. No invalidation logic, no restart. Editing
`spec` invalidates the same way, through the generation half of the key.

## Printer columns

```
$ kubectl get netboxendpoints -n homelab
NAME      URL                          MODE     VERSION   READY   AGE
homelab   https://netbox.home.arpa     Apply    4.6.8     True    3d
lab       https://netbox.lab.internal  DryRun   4.6.8     True    3d
staging   https://netbox.stg.internal  Apply              False   11m
```

| Column | Type | Source |
|---|---|---|
| `NAME` | string | `metadata.name` |
| `URL` | string | `.spec.url` |
| `MODE` | string | `.spec.mode` |
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
| `READY=False` immediately after apply | `Ready=False, Authenticated=False, Reason=SecretMissing` | Secret absent, or in another namespace | Create the Secret **in the endpoint's namespace**; there is no cross-namespace ref |
| `READY=False`, Secret exists | `Ready=False, Authenticated=False, Reason=TokenMissing` | wrong key name, or the key holds an empty string | Match `spec.tokenSecretRef.key` to the Secret's key, or drop it to use `token` |
| `READY=False` after a token rotation | `Ready=False, Authenticated=False, Reason=AuthError` | NetBox returned 401/403 — token revoked, expired, or read-only | Issue a token with write permission; the Secret watch picks it up with no restart |
| `READY=False`, message names `x509` | `Ready=False, Reason=ProbeFailed` | NetBox serves a certificate the operator does not trust | Add the root via `spec.tlsConfig.caBundleSecretRef` (remember `key: ca.crt`) |
| `READY=False`, message names `no usable certificates` | `Ready=False, Reason=InvalidConfig` | the CA Secret key holds something that is not PEM — or the key defaulted to `token` and picked up the wrong value | Set `caBundleSecretRef.key` explicitly and check the value is a PEM certificate |
| `READY=False`, message names `secrets "…" not found` but points at the CA Secret | `Ready=False, Reason=InvalidConfig` | missing CA bundle Secret. It reports `InvalidConfig`, not `SecretMissing` | Create the Secret; note the CA Secret is **not** watched, so also touch the endpoint or wait for `resyncPeriod` |
| `READY=False`, message names `context deadline exceeded` | `Ready=False, Reason=ProbeFailed` | NetBox is slow or unreachable, or `spec.timeout` is too short | Raise `spec.timeout`; check the URL is reachable from the operator's network |
| `READY=False`, `VERSION` populated | `VersionSupported=False, Reason=VersionUnsupported` | NetBox is older than 4.2 or 5.0+ | Upgrade NetBox to 4.2+. This is intentional — see the version gate above |
| `READY=False`, `VERSION` empty | `VersionSupported=False, Reason=VersionUnparseable` | `GET /api/status/` returned a version string the parser could not read, or a proxy replaced the body | Confirm `curl -H "Authorization: Token …" $URL/api/status/` returns JSON with `netbox-version` |
| `READY=False`, no condition changes at all, nothing in status | none | the object failed to decode — usually an unparseable `timeout` or `resyncPeriod` | Fix the duration string (`30s`, not `30 seconds`); check the manager log |
| Objects report drift that never clears | `Ready=True` on the endpoint | `spec.mode: DryRun` — reads happen, writes are suppressed | Set `mode: Apply` |
| Objects stuck `WaitingForEndpoint` | endpoint is `Ready=False` for any reason | no client is handed out while `Ready` is false | Fix the endpoint; the objects converge on their own |
| Conditions look wrong for the spec you just applied | `status.observedGeneration` < `metadata.generation` | the reconcile for the new spec has not completed | Wait one reconcile; if it persists, check the manager log |
| `Authenticated=True` but `Ready=False` | `Ready=False, Reason=ProbeFailed` or `InvalidConfig` | those reasons do not touch `Authenticated`, so it is stale from the last good reconcile | Believe `Ready` |

## Related

- [Errors and retries](../concepts/errors-and-retries.md) — how a NetBox HTTP failure
  becomes a typed error, and why `AuthError` fails the endpoint rather than every object.
- [ADR-0002](../decisions/0002-crd-scoping.md) — why this kind is namespaced, and what
  per-namespace endpoints cost.
