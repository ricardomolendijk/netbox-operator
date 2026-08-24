# `NetBoxIPAddressClaim`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPAddressClaim` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbipclaim`, `nbipc` |
| Status subresource | yes |
| Lands with | NBO-036 (M6) |

A `NetBoxIPAddressClaim` asks NetBox for one free address out of a prefix, **once**. It is not
a mode of [`NetBoxIPAddress`](netboxprefix.md) and it is not a field on one: allocating is a
different lifecycle from reconciling, so it is a different kind
([ADR-0004](../decisions/0004-claims-first-allocation.md)).

The *why* — how "exactly once" survives a lost response, why the same manifest reclaims the
same address on a rebuilt cluster, what each refusal means — is
[claims](../concepts/claims.md). This page is the fields.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddressClaim
metadata:
  name: dns-eth0
  namespace: homelab
spec:
  endpointRef: homelab
  prefixRef:
    name: home-lan
```

Two prerequisites, and both are refusals rather than errors if they are missing:

```yaml
# 1. The pool.
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata: {name: home-lan, namespace: homelab}
spec:
  endpointRef: homelab
  deletionPolicy: Retain
  prefix: 10.0.20.0/24
  status: active          # not `container`: see PoolNotAllocatable below
---
# 2. An endpoint with somewhere to keep an allocation identity. Without
#    spec.managedBy.clusterID the claim refuses to allocate at all.
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxEndpoint
metadata: {name: homelab, namespace: homelab}
spec:
  url: https://netbox.home.arpa
  tokenSecretRef: {name: netbox-token}
  managedBy:
    clusterID: homelab
```

## Full example

Every field, which is not many — see [what is deliberately absent](#what-is-deliberately-absent).

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddressClaim
metadata:
  name: dns-eth0
  namespace: homelab
spec:
  endpointRef: homelab

  # Immutable. Any of the four reference modes; a prefix in another namespace needs a
  # NetBoxRefGrant there.
  prefixRef:
    name: home-lan

  # Optional, immutable once set. Omit it unless you are renaming a claim and want to keep
  # its address.
  allocationIdentity: f3fb013aa1d2ffc2
```

## `spec`

### `spec.endpointRef`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Default | none |
| Validation | `MinLength=1` |

The `NetBoxEndpoint` to allocate through, in this claim's own namespace. There is no
cluster-wide default endpoint, so an omitted reference cannot be resolved into one.

It also participates in the allocation identity: the same claim pointed at a different NetBox
is a different allocation, and searching the second NetBox for the first one's object would
find nothing and allocate twice.

**If it is wrong.** A name no `NetBoxEndpoint` has, or one that is not `Ready`, is a *wait*
rather than a failure: `Ready=False, Reason=WaitingForEndpoint`, requeued every 30 seconds,
zero NetBox requests. An endpoint whose `spec.managedBy.clusterID` is unset is a different
state — see [`IdempotencyKeyUnavailable`](#idempotencykeyunavailable).

### `spec.prefixRef`

| | |
|---|---|
| Type | `PrefixRef` (an [`ObjectRef`](genericref.md) at a `NetBoxPrefix`) |
| Required | yes |
| Default | none |
| Validation | `XValidation: self == oldSelf` ("prefixRef is immutable; a claim allocates once, so pointing it at another prefix is a new claim") plus [`ObjectRef`'s own rules](genericref.md) |

The pool. Resolved by `name`, `slug`, `lookup` or `id` like any other reference, subject to
the same [`NetBoxRefGrant`](netboxrefgrant.md) check when it crosses a namespace.

**Immutable, enforced by the API server.** A CEL rule on the field is a better contract than a
controller comparing the spec against status after the fact: `kubectl apply` of a repointed
claim is rejected, so there is no window in which a claim's spec and its allocated address
disagree. "Point this claim at a different prefix" is a different claim.

**If it is wrong.**

| What | What you see |
|---|---|
| edited after creation | rejected by the API server, message contains `immutable` |
| omitted | rejected by the API server: the field is required |
| names a `NetBoxPrefix` that does not exist, or has no `status.id` yet | `RefsResolved=False` with the resolver's own reason (`RefNotFound`, `RefNotReady`, …), `Ready=False, Reason=WaitingForRef`, **no requeue timer** — the ref watch on the prefix re-enqueues the claim, and zero NetBox requests are made |
| crosses a namespace with no grant | `RefsResolved=False, Reason=RefDenied` ([stuck references](../operations/stuck-references.md)) |
| resolves to a prefix the operator will not allocate out of | [`PoolNotAllocatable`](#poolnotallocatable) |

### `spec.allocationIdentity`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Default | derived — `sha256(url \n namespace \n kind \n name)[:16]` |
| Validation | `MaxLength=64`, `Pattern=^[a-z0-9]+$`, `XValidation: self == oldSelf` |

Overrides the derived allocation identity. **Leave it out.** The derived value is what makes a
claim re-applied to a rebuilt cluster reclaim the same address
([ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git)),
and setting it by hand opts out of that.

The one case for it is a **rename**: the name is in the hash, so a renamed claim derives a new
identity and allocates a new address. Copy the old claim's `status.allocationIdentity` into
here and the renamed claim reclaims the original address instead.

Immutable in a specific sense: **addable, not changeable.** A transition rule is only
evaluated when the field is present in both the old and the new object, so setting it on a
claim that had none is allowed and editing it afterwards is rejected. An identity that moves is
a claim pointed at somebody else's address.

Absent and empty mean the same thing here — derive it — so this field has two states rather
than the three of [field ownership](../concepts/field-ownership.md). Nothing is written to
NetBox from it directly, so there is no NetBox value for an empty string to clear.

**If it is wrong.** An uppercase, hyphenated or punctuated value is rejected at admission: the
identity is compared for equality against a NetBox custom field, so a malformed one would
simply match nothing, allocate a fresh address, and look like it worked. A well-formed value
naming an object outside the claim's pool is
[`ReclaimedOutsidePool`](#reclaimedoutsidepool).

## `status`

| Field | Type | Populated by | When |
|---|---|---|---|
| `address` | `string` | the verified allocation | once, and **never rewritten** |
| `netboxID` | `int64` | the allocated object's primary key | with `address` |
| `url` | `string` | the allocated object's absolute NetBox URL | with `address`, when the response carried one |
| `pool.display` | `string` | the pool's `prefix` as resolved | with `address` |
| `pool.endpoint` | `string` | `ipam/prefixes` | with `address` |
| `pool.id` | `int64` | the pool's NetBox primary key | with `address` |
| `allocationIdentity` | `string` | the derived or explicit identity | with `address` |
| `claimUID` | `string` | `metadata.uid` of the claim that allocated | with `address` |
| `allocatedAt` | `metav1.Time` | when the address was allocated or reclaimed | with `address` |
| `provenance` | `ProvenanceStatus` | the stamp the allocating POST carried | on allocation only — **not** on reclaim, because a reclaim writes nothing |
| `observedGeneration` | `int64` | every pass | always, including every failure |
| `conditions` | `[]metav1.Condition` | every pass | always |

**Nothing here is cleared on failure**, and `address` least of all. It is the one field that
must never be lost: while it holds a value the reconciler's first guard clause returns before
anything can allocate again, so losing it is how a second address gets allocated. Every
refusal leaves it exactly as it was — empty if nothing was ever allocated, unchanged
otherwise.

`pool` records what `prefixRef` *resolved to*, so "which prefix did this address come from"
stays answerable after the prefix has been renamed. `allocationIdentity` is reported because it
is the one value that makes a leaked allocation findable: paste it into NetBox's custom-field
filter and the object comes back whether or not the CR still exists.

## Conditions

| Type | `True` when | `False` when | Reasons |
|---|---|---|---|
| `Allocated` | NetBox has handed this claim an object. **Once `True`, never set `False`** | nothing has been allocated *yet* | `AddressAllocated`, `ReclaimedByIdentity`, `AllocationPending`, `PoolExhausted`, `PoolNotAllocatable`, `ReclaimedOutsidePool`, `AllocationConflict`, `IdempotencyKeyUnavailable`, `Invalid`, `APIError` |
| `RefsResolved` | the pool reference resolved | it did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefKindUnavailable` |
| `Ready` | the claim holds its allocation | anything else | `AddressAllocated`, `AllocationPending`, `WaitingForEndpoint`, `WaitingForRef`, `PoolExhausted`, `PoolNotAllocatable`, `ReclaimedOutsidePool`, `AllocationConflict`, `IdempotencyKeyUnavailable`, `DryRunPending`, `ReportPending`, `Invalid`, `APIError` |

`Allocated` is a **historical fact**, not a liveness signal: the address was allocated, and no
later event un-allocates it. There is no `Degraded` condition and no `Synced` condition — a
claim has no drift to detect and no desired state to be synced with, and the liveness of the
allocated object is the declarative `NetBoxIPAddress`'s business (NBO-025).

### Reason glossary

#### `AddressAllocated`

On `Allocated` and on `Ready`. NetBox allocated an object out of the pool, on this pass or an
earlier one. The steady state.

#### `ReclaimedByIdentity`

On `Allocated`. An object already carrying this claim's allocation identity was found and
adopted rather than a second one allocated. Zero POSTs. This is what a rebuilt cluster
reports, and what a crash between the POST and the status write reports — the same code path,
because from NetBox's side they are the same situation.

The message names the object's stamped `k8s_uid` when it differs from this claim's, which is
the only signal that exists for "a different claim has held this name". On a rebuild the
handover is entirely legitimate; when two claims are given one name over time it is a mistake;
the two are indistinguishable from inside the operator, so it is reported and not judged. The
stale UID is deliberately left in place — a provenance stamp naming a CR that no longer exists
is what makes a leaked object findable, and there is nothing to gain from erasing it.

#### `AllocationPending`

On `Allocated` and on `Ready`. Nothing has been allocated yet and nothing is wrong. Every
claim's first pass reports it, as does a read-after-write that could not be confirmed — in
which case the message says what disagreed, and the next pass's identity search reconciles
whatever actually landed.

#### `PoolExhausted`

On `Allocated` and on `Ready`. NetBox has no free address in the pool: the allocating POST
answered `409`.

Requeued at a **fixed ten minutes**, never as an error, and the claim also watches its
`NetBoxPrefix` so a widened prefix converges immediately. The two are not redundant — the
watch cannot see an address being freed *inside* NetBox, and only the timer catches that.

The message names the pool and its utilisation. The utilisation is 100% by NetBox's own
answer, not by a count: the operator never asks how much of a pool is free. See
[claims](../concepts/claims.md#an-exhausted-pool-waits).

#### `PoolNotAllocatable`

On `Allocated` and on `Ready`. The resolved prefix is one this operator refuses to allocate out
of, and the message names which flag:

- `mark_utilized: true` — it only forces NetBox's utilisation gauge to 100%; `available-ips`
  would still hand out an address. The flag means the free space here is delegated to DHCP or
  another IPAM.
- `status: container` — a container's space is subdivided by child prefixes rather than
  populated by addresses.

No override for either. `reserved` and `deprecated` are **not** refused.

#### `ReclaimedOutsidePool`

On `Allocated` and on `Ready`. An object carrying this claim's identity exists and is not
inside the pool the claim names now. Zero POSTs, zero writes. Reachable by repointing a claim,
renaming a prefix, or reusing a claim name — and reachable *because* the identity is
deterministic. Fix: delete the stale NetBox object, or set `spec.allocationIdentity`.

#### `AllocationConflict`

On `Allocated` and on `Ready`. More than one NetBox object carries this claim's identity, which
means a previous over-allocation. The message names every match. The operator never allocates
and never deletes here: it cannot prove which object is unused, and a NIC or a DNS record may
be pointing at either. A human chooses.

#### `IdempotencyKeyUnavailable`

On `Allocated` and on `Ready`. This endpoint has nowhere to store an allocation identity, so
the claim will not allocate at all — zero POSTs. Without one, a lost HTTP response is
unrecoverable and every retry burns another address, so there is no unsafe override. Set
`spec.managedBy.clusterID` on the `NetBoxEndpoint`, or create the
`k8s_allocation_identity` custom field by hand with `type: text`, `filter_logic: exact` and
`ipam.ipaddress` among its object types.

#### `DryRunPending` / `ReportPending`

On `Ready`. The endpoint is in `mode: DryRun`, or its `driftMode` is `Report`. Nothing was
sent, `status.address` stays empty, and the message says what would have been allocated out of
which pool. Two reasons rather than one because the two are set in different fields and
switched off in different places ([drift modes](../operations/gitops.md#drift-modes)).

#### `WaitingForEndpoint`, `WaitingForRef`, `Invalid`, `APIError`

The shared vocabulary, behaving exactly as it does for every other kind
([errors and retries](../concepts/errors-and-retries.md)).

### Retry intervals

| State | Requeue |
|---|---|
| allocated (`Ready=True`) | **none.** There is nothing left to re-check, and a timer would be one NetBox request per claim per interval that can only conclude what status already says |
| `WaitingForEndpoint` | 30s |
| `WaitingForRef` | none — the ref watch on the `NetBoxPrefix` is what ends the wait |
| `PoolExhausted`, `PoolNotAllocatable`, `ReclaimedOutsidePool`, `AllocationConflict`, `IdempotencyKeyUnavailable` | **10m**, fixed, ±10% jitter. Never an error: returning one would hand the object to the workqueue's exponential backoff, which starts in milliseconds |
| `AllocationPending` after an unverified allocation | 30s |
| `APIError` | the shared tiers — 30s transient, 2m auth, `Retry-After` for a 429 |
| `DryRunPending`, `ReportPending` | the endpoint's `resyncPeriod` |

## Kind-specific behaviour

### There is no `deletionPolicy`

A claim always retains its NetBox object
([#182](https://github.com/ricardomolendijk/netbox-operator/issues/182)). A single-valued knob
is not one, and a field that is accepted and ignored is worse than a field that is not there.

`Retain` is also the value that makes the deterministic identity worth having: delete the
claim, re-apply the same manifest, get the same address back. What stops it from being a silent
leak is that deleting a claim emits an `AddressRetained` Event naming the address, the NetBox
id and the identity, and increments
`netbox_operator_allocations_retained_total{kind}`. To free the address, delete the NetBox
object.

The deletion pass makes no NetBox call at all, so this finalizer cannot make a namespace
undeletable however unreachable NetBox is.

### There is no `onConflict`

`onConflict` is about adopting an object that matches a natural key. A claim has no natural
key: it adopts by allocation identity and by nothing else.

### The allocating POST is never retried

Every other write the operator makes retries a `5xx`. This one does not, because an allocating
POST is not idempotent: a POST that committed and lost its response, retried, allocates a
second address. Recovery is the identity search on the next pass, and a client-side retry would
defeat it (`internal/netbox/allocate.go`).

### The identity rides on the allocating POST

NetBox's `AvailableIPsView.post` honours the full write serializer and injects only `address`
and the parent's `vrf` (`ipam/api/views.py:352-427`), so `custom_fields` and `tags` are written
by the same atomic call. There is no window in which an allocated address exists without the
value that says whose it is.

The body is a **single object**, not a one-element list: NetBox mirrors the shape it was given,
so one object in means one object out — which makes "NetBox returned more objects than were
asked for" unrepresentable rather than a failure mode to handle.

### No client-side lock

The POST is atomic under NetBox's own `select_for_update` advisory lock
(`ipam/api/views.py:121-129`), so two workers — or two clusters — cannot land on the same
address. The operator therefore takes no lock and does not serialise per pool.

### What is deliberately absent

No `dnsName`, `role`, `description`, `comments`, `tags`, `customFields`, `assignedObject`,
`tenantRef`, `natInsideRef`, `preferredFamily` or `ipRangeRef`.

A claim's job is to *get* an address. The ongoing desired state of the address it got belongs
to a declarative `NetBoxIPAddress` — the child the claim will own and materialise (NBO-025 for
the kind, NBO-032 for the materialiser, at which point `status.ipAddressRef` and a `Bound`
condition appear here). Until that exists, a pass-through field would be one the operator
writes once at allocation and can never correct: it would lie the first time somebody edited
it. `ipRangeRef` and `preferredFamily` arrive with NBO-064's other two claim kinds.

## Printer columns

```console
$ kubectl get nbipc -n homelab
NAME       ADDRESS         PREFIX         READY   AGE
dns-eth0   10.0.20.37/24   10.0.20.0/24   True    4m
web-01     10.0.20.38/24   10.0.20.0/24   True    4m
db-01                      10.0.20.0/24   False   4m
```

| Column | JSONPath |
|---|---|
| `ADDRESS` | `.status.address` |
| `PREFIX` | `.status.pool.display` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

What was asked for, what was handed out, and whether it stuck — the three things a human wants
side by side. `nbipclaim` and `nbipc` both resolve.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `ADDRESS` empty, `READY` False forever | `Ready=False, Reason=IdempotencyKeyUnavailable` | the endpoint has no identity store | set `spec.managedBy.clusterID` on the `NetBoxEndpoint` |
| `ADDRESS` empty, `PREFIX` empty | `Ready=False, Reason=WaitingForRef` | the `NetBoxPrefix` does not exist, has no `status.id`, or is denied across namespaces | create the prefix, or write the [`NetBoxRefGrant`](netboxrefgrant.md) the message names |
| `ADDRESS` empty, retrying every 10m | `Ready=False, Reason=PoolExhausted` | the prefix is full | widen the prefix (the claim wakes immediately) or free an address in NetBox (up to 10m) |
| `ADDRESS` empty, refused immediately | `Ready=False, Reason=PoolNotAllocatable` | the prefix is a `container`, or has `mark_utilized` | allocate out of a child prefix, or clear the flag |
| `ADDRESS` empty, message names two ids | `Ready=False, Reason=AllocationConflict` | two objects carry one identity | delete the one that is not in service |
| `ADDRESS` empty, message names an address outside the prefix | `Ready=False, Reason=ReclaimedOutsidePool` | the claim was repointed, or its name reused | delete the stale NetBox object, or set `spec.allocationIdentity` |
| `kubectl apply` rejected: "prefixRef is immutable" | — | a claim allocates once | write a new claim; delete the old one when the address is no longer wanted |
| a re-applied manifest got a **different** address | — | the previous NetBox object was deleted, or the claim was renamed | see [what reclaim can and cannot recover](../concepts/claims.md#what-reclaim-can-and-cannot-recover) |
| an address in NetBox nobody claims | `AddressRetained` Event, `netbox_operator_allocations_retained_total` | a claim was deleted, or renamed | delete the NetBox object; search `cf_k8s_allocation_identity` to find it |

## Related

- [Claims](../concepts/claims.md) — the concurrency, idempotency and reclaim story.
- [ADR-0004 — Claims-first allocation](../decisions/0004-claims-first-allocation.md).
- [ADR-0005 §3–§4](../decisions/0005-gitops-coexistence.md) — the deterministic identity, and
  why the address is not written back to Git.
- [`NetBoxPrefix`](netboxprefix.md) — the pool.
- [`NetBoxEndpoint`](netboxendpoint.md) — `managedBy`, `mode: DryRun`, `driftMode`.
- [Provenance](../operations/provenance.md) — the custom fields the identity needs.
- [Observability](../operations/observability.md) — `netbox_operator_allocations_total`.
