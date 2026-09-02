# `NetBoxPrefixClaim`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxPrefixClaim` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbprefixclaim`, `nbpfxc` |
| Status subresource | yes |
| Allocates | `ipam.Prefix` out of `POST ipam/prefixes/{id}/available-prefixes/` |

A `NetBoxPrefixClaim` asks NetBox to carve one child prefix out of a container, **once**:
"give me a /26 out of `10.0.0.0/16`", and the answer is whichever /26 NetBox hands out.

It is the second of [ADR-0004](../decisions/0004-claims-first-allocation.md)'s three claim
kinds and the easiest, for exactly one reason: `available-prefixes` runs inside NetBox's own
`advisory_lock('available-prefixes')` (`netbox/ipam/api/views.py`, NetBox 4.6.8), so its safety
story is [`NetBoxIPAddressClaim`](netboxipaddressclaim.md)'s with a different sub-path. Its
sibling [`NetBoxIPRangeClaim`](netboxiprangeclaim.md) has no such endpoint and therefore a
different argument — see
[locked and unlocked allocation](../concepts/claims.md#locked-and-unlocked-allocation).

Everything about *how* an allocation survives a lost response, and why the same manifest
reclaims the same prefix on a rebuilt cluster, is [claims](../concepts/claims.md). This page is
the fields.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefixClaim
metadata:
  name: tenant-a-net
  namespace: homelab
spec:
  endpointRef: homelab
  parentPrefixRef:
    name: container-10-0
  prefixLength: 26
```

The two prerequisites are the same as an address claim's — a pool, and an endpoint with
somewhere to keep an allocation identity:

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata: {name: container-10-0, namespace: homelab}
spec:
  endpointRef: homelab
  deletionPolicy: Retain
  prefix: 10.0.0.0/16
  status: container       # what this kind expects; see PoolUnexpectedStatus below
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxEndpoint
metadata: {name: homelab, namespace: homelab}
spec:
  url: https://netbox.home.arpa
  tokenSecretRef: {name: netbox-token}
  managedBy:
    clusterID: homelab
```

Once it is Ready:

```console
$ kubectl get nbpfxc -n homelab
NAME           PREFIX          PARENT        LENGTH   READY   AGE
tenant-a-net   10.0.64.0/26    10.0.0.0/16   26       True    30s
```

## `spec`

### `spec.endpointRef`

The `NetBoxEndpoint` to allocate through, in this claim's own namespace. It must have
`spec.managedBy.clusterID` set: the allocation identity is stored in a NetBox custom field, and
an endpoint with nowhere to store one refuses to allocate at all rather than risk a prefix per
retry (`Reason=IdempotencyKeyUnavailable`).

It also participates in the identity: the same claim pointed at a different NetBox is a
different allocation.

### `spec.parentPrefixRef`

The container to carve out of. A [`NetBoxPrefix`](netboxprefix.md) reference, resolved by
`name`, `slug`, `lookup` or `id` like any other, and subject to the same
[`NetBoxRefGrant`](netboxrefgrant.md) check when it crosses a namespace.

**Immutable.** "Carve this claim out of a different container" is a different claim. A CEL rule
is a better contract than a controller comparing spec against status after the fact: the API
server rejects the edit, so there is no window in which the claim's spec and its allocated
prefix disagree.

### `spec.prefixLength`

The mask length of the child: `26` for a /26. Required and immutable.

The CRD bounds it at `4..128` and that is deliberately the *weak* half of the check. CEL cannot
see the parent, and the family is the parent's, so three mistakes are left to the controller —
which refuses them **before any POST**, naming both numbers:

| Request | Parent | Result |
|---|---|---|
| `prefixLength: 64` | `10.0.0.0/16` | `Ready=False, Reason=Invalid` — 64 is not a mask length for a 32-bit family |
| `prefixLength: 16` | `10.0.0.0/16` | `Ready=False, Reason=Invalid` — not smaller than the pool |
| `prefixLength: 8` | `10.0.0.0/16` | `Ready=False, Reason=Invalid` — not smaller than the pool |

The middle row is why the guard exists rather than being left to NetBox.
`prefixLength: 16` on a /16 is **accepted** by NetBox: `get_available_prefixes()` subtracts the
child prefixes from the parent, which for an empty container is the whole parent, so
`available-prefixes` hands out `10.0.0.0/16` and the claim ends up holding a duplicate of its
own container, in the same VRF, reporting success. Refusing costs zero requests.

### There is no `spec.vrfRef`

The child always lands in the parent's VRF, and no field here could change that.
`AvailablePrefixesView.prep_object_data` sets `'vrf': parent.vrf.pk if parent.vrf else None` on
every requested prefix (`netbox/ipam/api/views.py`, NetBox 4.6.8), **overwriting** whatever the
request carried. A field that is accepted and ignored is worse than a field that is not there.

`status.pool` records which prefix the child came out of, and the VRF is that prefix's.

### There is no pass-through set

No `description`, no `roleRef`, no `scope`, no `tenantRef`, no `tags`. A claim's job is to *get*
a prefix; the ongoing desired state of the prefix it got belongs to a declarative
[`NetBoxPrefix`](netboxprefix.md) — the child the claim will own once the materialiser lands
(NBO-032, at which point `status.prefixRef` and a `Bound` condition appear here).

Until then a pass-through field would be one the operator writes once at allocation and can
never correct: it would lie the first time somebody edited it. The provenance stamp is the
exception, because it is not desired state — it rides on the allocating POST so that the
allocated prefix is attributable from the moment it exists.

### `spec.allocationIdentity`

Same field, same meaning and same warnings as on
[`NetBoxIPAddressClaim`](netboxipaddressclaim.md#specallocationidentity): leave it out unless
you are carrying an allocation across a **rename**, which the derived identity cannot survive
by construction. Immutable once set.

### There is no `deletionPolicy`

A claim always retains its NetBox object. A single-valued knob is not one, and `Retain` is what
makes the deterministic identity worth having: delete the claim, re-apply the same manifest, get
the same prefix back. What stops that from being a silent leak is that the operator *reports*
it — an `AddressRetained` Event naming the prefix, the NetBox id and the identity, plus
`netbox_operator_allocations_retained_total{kind="NetBoxPrefixClaim"}`. To free the prefix,
delete the NetBox object.

## `status`

| Field | Meaning |
|---|---|
| `prefix` | the allocated child prefix in CIDR notation. **Written once, never rewritten.** |
| `netboxID` | the child's NetBox primary key |
| `url` | the child's absolute NetBox URL |
| `pool` | `display`, `endpoint` and `id` of the container it came out of, as resolved |
| `allocationIdentity` | the identity written into the child's custom field |
| `claimUID` | the `metadata.uid` of the claim that allocated it |
| `allocatedAt` | when it was first allocated or reclaimed |
| `provenance` | the stamp the allocating POST carried |
| `observedGeneration`, `conditions` | standard |

`status.prefix` is written only after a read-after-write that proves three things: the object
exists, it carries this claim's identity, and it is **inside the parent**. While it holds a
value the reconciler's first guard clause returns before anything can allocate again — so if
somebody deletes the child in NetBox, the operator does not pick a new prefix. By then a router
or a firewall rule is using this one, and silently moving it turns a bookkeeping accident into a
live network change nobody asked for.

## Conditions

`Allocated`, `RefsResolved` and `Ready`, with the same meanings and the same reason vocabulary
as [`NetBoxIPAddressClaim`](netboxipaddressclaim.md#conditions) — the engine is the same one.
`Allocated=True` is a historical statement and is never set back to False.

The reason on a successful allocation is `AddressAllocated`, which for a prefix claim reads
oddly and is deliberate: the reason vocabulary is shared across all three claim kinds, and a
per-kind spelling would mean a caller keying on it had to know the Kind first. The message names
what was allocated.

Two reasons behave differently here than on an address claim:

#### `PoolNotAllocatable`

One cause rather than two: `mark_utilized` on the parent. It means the free space here is not
really free — delegated to another IPAM, or to DHCP — and `available-prefixes` hands out a child
anyway, so honouring it is this operator's job.

**`status: container` is not a cause.** It is what this kind expects; see below.

#### `Invalid`

Reached by the three `prefixLength` mistakes in the table above, before any POST, and by
anything NetBox rejects with a 400.

## The container asymmetry

`status: container` on the parent means opposite things to the two claim kinds, and neither is a
rule in shared code:

| Kind | `status: container` | Why |
|---|---|---|
| [`NetBoxIPAddressClaim`](netboxipaddressclaim.md) | **refused**, `PoolNotAllocatable` | a container's free space is subdivided by child prefixes, not populated by addresses |
| `NetBoxPrefixClaim` | **expected** | subdividing is exactly what this kind does |

Both are data on the kind's `ClaimDescriptor` — one list of forbidden statuses, one list of
expected ones — which is what lets one value be a refusal for one kind and a precondition for
the next.

Allocating out of a parent that is *not* a container still works, and says so:

```console
$ kubectl describe nbpfxc tenant-a-net -n homelab | tail -3
Events:
  Type     Reason                 Message
  Warning  PoolUnexpectedStatus   pool 10.0.0.0/16 (netbox ipam/prefixes/11) has status "active",
                                  and NetBoxPrefixClaim expects one of [container]; allocating out of it anyway
  Normal   Allocated              allocated netbox ipam/prefixes/512 (10.0.64.0/26) out of pool 10.0.0.0/16
```

A Warning and not a refusal: subdividing a network that is already in service is unusual rather
than wrong, and refusing it would overrule a decision the NetBox operator has already recorded.
NetBox's own view does not consult `status` at all, so a refusal here would be this operator
inventing a rule the server does not have.

## Printer columns

```console
$ kubectl get nbpfxc -n homelab
NAME           PREFIX          PARENT        LENGTH   READY   AGE
tenant-a-net   10.0.64.0/26    10.0.0.0/16   26       True    4m
tenant-b-net                   10.0.0.0/16   26       False   4m
```

| Column | JSONPath |
|---|---|
| `PREFIX` | `.status.prefix` |
| `PARENT` | `.status.pool.display` |
| `LENGTH` | `.spec.prefixLength` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

What was asked for, where it came from, and whether it stuck. `nbprefixclaim` and `nbpfxc` both
resolve.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `PREFIX` empty, `READY` False forever | `Reason=IdempotencyKeyUnavailable` | the endpoint has no identity store | set `spec.managedBy.clusterID` on the `NetBoxEndpoint` |
| `PREFIX` empty, message names another cr | `Reason=ForeignAllocation` | `spec.allocationIdentity` names a prefix somebody else owns | unset it, or have the owner release it |
| `PREFIX` empty, `PARENT` empty | `Reason=WaitingForRef` | the parent does not exist, has no `status.id`, or is denied across namespaces | create it, or write the [`NetBoxRefGrant`](netboxrefgrant.md) the message names |
| `PREFIX` empty, message names two lengths | `Reason=Invalid` | `prefixLength` is not smaller than the parent, or is outside its family | ask for a longer mask; a claim's length is immutable, so this is a new claim |
| `PREFIX` empty, retrying every 10m | `Reason=PoolExhausted` | no free block of that size is left | widen the container (the claim wakes immediately), or delete a child prefix in NetBox (up to 10m) |
| `PREFIX` empty, refused immediately | `Reason=PoolNotAllocatable` | the parent has `mark_utilized` | clear the flag, or allocate out of a different container |
| allocated, plus a `PoolUnexpectedStatus` warning | `Ready=True` | the parent is not a `container` | nothing, unless it was a mistake — the allocation is real either way |
| `PREFIX` empty, message names a prefix outside the parent | `Reason=ReclaimedOutsidePool` | the claim was repointed, or its name reused | delete the stale NetBox object, or set `spec.allocationIdentity` |
| a re-applied manifest got a **different** prefix | — | the previous NetBox object was deleted, or the claim was renamed | see [what reclaim can and cannot recover](../concepts/claims.md#what-reclaim-can-and-cannot-recover) |
| a child prefix nobody claims | `AddressRetained` Event | a claim was deleted, or renamed | delete the NetBox object; search `cf_k8s_allocation_identity` to find it |

## Related

- [Claims](../concepts/claims.md) — concurrency, idempotency, reclaim, and the locked/unlocked
  distinction.
- [`NetBoxIPRangeClaim`](netboxiprangeclaim.md) — the same shape without a server-side lock.
- [`NetBoxIPAddressClaim`](netboxipaddressclaim.md) — one address instead of a subnet.
- [`NetBoxPrefix`](netboxprefix.md) — the pool, and the kind that maintains the result.
- [ADR-0004](../decisions/0004-claims-first-allocation.md),
  [ADR-0005 §3](../decisions/0005-gitops-coexistence.md).
