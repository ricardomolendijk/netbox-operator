# `NetBoxIPRangeClaim`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPRangeClaim` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbiprangeclaim`, `nbrngc` |
| Status subresource | yes |
| Allocates | `ipam.IPRange` via `POST ipam/ip-ranges/` — **no advisory lock** |
| Lands with | NBO-064 (M6) |

A `NetBoxIPRangeClaim` reserves one run of consecutive addresses inside a prefix, **once**:
"reserve me 64 consecutive addresses in `10.0.30.0/24`", and the answer is
`10.0.30.64/24`–`10.0.30.127/24`.

It is the third of [ADR-0004](../decisions/0004-claims-first-allocation.md)'s claim kinds and
the only one NetBox does not serialise for us. **That is the whole of what makes it different,
and it is worth two paragraphs before the fields.**

## Why this kind is not like the other two

NetBox 4.6.8 offers exactly three allocation endpoints (`netbox/ipam/api/urls.py`):

```
prefixes/{id}/available-ips/         an address out of a prefix     NetBoxIPAddressClaim
prefixes/{id}/available-prefixes/    a child prefix out of a prefix NetBoxPrefixClaim
ip-ranges/{id}/available-ips/        an address out of a range      (no claim kind yet)
```

There is **no `available-ranges`**. The third entry looks like it might be one and is the
opposite operation: an address out of a range, not a range out of a prefix. So this kind cannot
ask NetBox for a free block; it has to work one out and then create it, with a plain
`POST ipam/ip-ranges/` that takes no advisory lock.

Two claims can therefore compute the same placement. **NetBox is still the arbiter:**
`IPRange.clean()` refuses to save a range that overlaps another in the same VRF
(`netbox/ipam/models/ip.py`), and every API write runs it, because NetBox's
`ValidatedModelSerializer.validate()` calls `full_clean()` before saving
(`netbox/netbox/api/serializers/base.py`). The loser of a race gets a 400, recomputes from
fresh state and tries again — up to five times in one reconcile — and reports
`AllocationContended` if it keeps losing.

No client-side lock is taken and none would help: the other writer may be another cluster, or a
human in the NetBox UI. Every claim kind in this operator has to be able to name which
server-side guarantee it relies on — a lock, or a rejection — and one that can name neither does
not ship. See
[locked and unlocked allocation](../concepts/claims.md#locked-and-unlocked-allocation).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPRangeClaim
metadata:
  name: dhcp-pool
  namespace: homelab
spec:
  endpointRef: homelab
  parentPrefixRef:
    name: home-lan
  size: 64
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPRangeClaim
metadata:
  name: dhcp-pool
  namespace: homelab
spec:
  endpointRef: homelab

  parentPrefixRef:
    name: home-lan

  size: 64
  alignment: PowerOfTwo

  markPopulated: true
  markUtilized: false
```

```console
$ kubectl get nbrngc -n homelab
NAME        START            END              SIZE   READY   AGE
dhcp-pool   10.0.30.64/24    10.0.30.127/24   64     True    30s
```

## `spec`

### `spec.endpointRef`

As on the other two claim kinds, and with the same hard requirement:
`spec.managedBy.clusterID` must be set on the endpoint, or the claim refuses to allocate
(`Reason=IdempotencyKeyUnavailable`). It matters more here than anywhere else — the creating
POST has no lock behind it, so a lost response is the ordinary failure rather than the rare one,
and the identity is the only thing that makes it recoverable.

### `spec.parentPrefixRef`

The prefix to reserve inside. A [`NetBoxPrefix`](netboxprefix.md) reference, resolved the same
four ways as any other and subject to the same [`NetBoxRefGrant`](netboxrefgrant.md) check
across namespaces.

The parent supplies three things:

- **its bounds**, which the placement is computed inside;
- **its VRF**, which the created range is given explicitly. A plain POST inherits nothing —
  unlike `available-prefixes`, which injects the parent's `vrf` itself — and NetBox's overlap
  check is `filter(vrf=self.vrf)`. A range placed against the ranges of one VRF and validated
  against another is how two teams get the same block;
- **its mask**, which both endpoints are written with, because `IPRange.clean()` requires the
  two to match.

**Immutable**, and this kind has more reason for it than the others: repointing it while keeping
the claim's name is exactly the mistake `ReclaimedOutsidePool` exists to report.

### `spec.size`

How many consecutive addresses, counted **inclusively**: `64` means an end address 63 above the
start. Required and immutable — growing a range is not this claim doing more of what it did,
because the addresses above it may not be free.

Capped at `65536`. NetBox's own ceiling is `2^32-1` (`IPRange.clean`), which for an IPv6 parent
is a range nobody meant to ask for and for an IPv4 one is more addresses than exist; the cap
here keeps the `size` column a number a human reads rather than parses, and `65536` and `655360`
differ by one keystroke.

`size` is **never sent to NetBox.** `ipam.IPRange.size` is `editable=False` and derived in
`save()` from the two endpoints, so a payload carrying it would have it dropped in silence. It
travels as a placement input the client consumes, and the created range's own derived `size` is
checked against it before anything is written to status.

### `spec.alignment`

`Any` (the default) or `PowerOfTwo`.

| | Parent `10.0.30.0/24`, `10.0.30.0`–`10.0.30.12` already taken, `size: 64` |
|---|---|
| `Any` | `10.0.30.13`–`10.0.30.76` — the first placement that fits |
| `PowerOfTwo` | `10.0.30.64`–`10.0.30.127` — the next multiple of 64 |

`PowerOfTwo` exists because an unaligned pool is a bug report waiting to happen in every
downstream config generator: 64 addresses from `10.0.30.64` is `10.0.30.64/26` in a dnsmasq or
Kea config, and 64 addresses from `10.0.30.13` is a hand-written pair of bounds somebody
eventually gets wrong. The multiple is the next power of two **greater than or equal to**
`size`, so `size: 65` aligns to 128.

It is a *placement* input, not a NetBox field — NetBox has no opinion about where a range starts
— so it never reaches a payload. It is also not immutable, and that is honest rather than
generous: editing it after an allocation does nothing at all, because a claim allocates once.

### `spec.markPopulated`, `spec.markUtilized`

The two pass-through fields, and they are exactly the fields that describe *the reservation
itself* rather than the thing reserved. `markPopulated` stops NetBox creating `ipam.IPAddress`
objects inside the block, which is usually the point: the leases belong to whatever hands them
out.

Both are pointers, so absent leaves NetBox's value alone and `false` writes false.

Everything else about the created range — `description`, `roleRef`, `tenantRef`, `status` — is
the desired state of a NetBox object, which belongs to a [`NetBoxIPRange`](netboxiprange.md) CR
rather than to a claim that writes once and can never correct itself.

### `spec.allocationIdentity`

As on the other claim kinds. Leave it out unless carrying a reservation across a rename.

### There is no `deletionPolicy`

A claim always retains its NetBox object. Deleting the claim emits an `AddressRetained` Event
naming the range, its id and its identity, and increments
`netbox_operator_allocations_retained_total{kind="NetBoxIPRangeClaim"}`. To free the addresses,
delete the NetBox object.

## `status`

| Field | Meaning |
|---|---|
| `startAddress` | the first address of the reserved range. **Written once, never rewritten.** |
| `endAddress` | the last address, inclusive |
| `size` | how many addresses the range covers |
| `netboxID`, `url`, `pool`, `allocationIdentity`, `claimUID`, `allocatedAt`, `provenance` | as on every claim |

`startAddress` is the value the engine's guard clause reads and the value its read-after-write
proved: the range exists, carries this claim's identity, and starts inside the parent.

`endAddress` and `size` are derived from it and from `spec.size`, and they cannot disagree with
NetBox: the client refuses the allocation unless the `size` NetBox computed **from its own two
endpoints** equals the size that was asked for. A start address and that size pin the end
address exactly, so re-reading it would confirm a value already proven.

## Conditions

The engine's, unchanged: `Allocated`, `RefsResolved`, `Ready`. One reason exists that no other
claim kind can report.

#### `AllocationContended`

Every placement this pass computed was rejected because another writer created an overlapping
range between the read and the write. Five attempts, jittered, then this.

**It is not `PoolExhausted`, and conflating the two would send a human looking for space that
exists.**

| | Meaning | Fix |
|---|---|---|
| `PoolExhausted` | the parent has no run of `size` free addresses | widen the parent, or delete a range in NetBox |
| `AllocationContended` | the space is there; somebody else keeps getting it first | nothing — it retries. If it persists, more claims are competing for the parent than it has room for |

Both wait 10 minutes and both wake immediately if the parent's own CR changes. The condition
message carries NetBox's own overlap sentence, which names the range that won.

#### `PoolNotAllocatable`

One cause: `mark_utilized` on the parent prefix. `status: container` is **not** a cause — a DHCP
scope inside a container prefix is ordinary — and neither is any other status, so this kind
records no expected status and emits no `PoolUnexpectedStatus` warning.

## Placement is arithmetic, never enumeration

The occupied set is the other **ranges** in the parent's VRF, and never the addresses inside
them:

- an IPv6 `/64` parent has 2^64 addresses and 0 or 1 ranges, and only one of those two numbers
  can be asked about;
- individual `ipam.IPAddress` objects inside a candidate range are **not** a conflict — NetBox
  permits a range to contain addresses — so they are not consulted at all.

A range allocation therefore issues zero requests to `available-ips` and zero to
`ipam/ip-addresses`, which is asserted by a test rather than left as an intention. What it does
issue is one `GET ipam/ip-ranges/?vrf_id=<id>`, whose result size is bounded by how many ranges
exist.

Two filters are deliberately *not* used, and both were checked against the pinned 4.6.8 source:

- **`?parent=<cidr>`** matches only ranges whose start *and* end are inside the prefix
  (`IPRangeFilterSet.search_by_parent`, `net_host_contained` on both). A range straddling the
  boundary is invisible to it — and a placement computed from a list that cannot see it would
  overlap it on every attempt, reporting contention forever. The intersection is done
  client-side instead.
- **`?vrf_id__isnull=true`** does not exist. `vrf_id` is a `ModelMultipleChoiceFilter`, for
  which NetBox generates only the `__n` negation (`FILTER_NEGATION_LOOKUP_MAP`,
  `netbox/netbox/filtersets.py`), and django-filter ignores an unregistered parameter — so the
  pin would silently match every VRF. A parent in the global table is filtered from the rows
  instead.

The placement walk itself is a pure function with its own table test
(`FirstGap` in `internal/netbox/placement.go`), because the failure it prevents is invisible in
production until two DHCP servers answer for one subnet.

## Printer columns

```console
$ kubectl get nbrngc -n homelab
NAME          START           END              SIZE   READY   AGE
dhcp-pool     10.0.30.64/24   10.0.30.127/24   64     True    4m
guest-pool                                            False   4m
```

| Column | JSONPath |
|---|---|
| `START` | `.status.startAddress` |
| `END` | `.status.endAddress` |
| `SIZE` | `.status.size` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`nbiprangeclaim` and `nbrngc` both resolve.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `START` empty, `READY` False forever | `Reason=IdempotencyKeyUnavailable` | the endpoint has no identity store | set `spec.managedBy.clusterID` |
| `START` empty, retrying every 10m, message names five attempts | `Reason=AllocationContended` | the parent is busy | wait; if it persists, split the claims across parents |
| `START` empty, retrying every 10m, message names the parent | `Reason=PoolExhausted` | no run of `size` addresses is free | widen the parent, or delete a range in NetBox |
| `START` empty, refused immediately | `Reason=PoolNotAllocatable` | the parent has `mark_utilized` | clear the flag |
| `START` empty, `Reason=Invalid`, "masks must match" | — | should be impossible: the client writes both endpoints with the parent's mask | report it |
| `START` empty, message names a range outside the parent | `Reason=ReclaimedOutsidePool` | `parentPrefixRef` was repointed, or the claim's name reused | delete the stale range, or set `spec.allocationIdentity` |
| two ranges overlap in NetBox | — | not from this operator: the server rejects an overlap in the same VRF. Check whether they are in *different* VRFs, where overlapping is legal | — |
| a range nobody claims | `AddressRetained` Event | a claim was deleted, or renamed | delete the NetBox object; search `cf_k8s_allocation_identity` |

## Related

- [Claims](../concepts/claims.md#locked-and-unlocked-allocation) — why this kind can say
  "contended" and the other two cannot.
- [`NetBoxIPRange`](netboxiprange.md) — the kind that maintains the reserved range.
- [`NetBoxPrefixClaim`](netboxprefixclaim.md) — the locked sibling.
- [`NetBoxPrefix`](netboxprefix.md) — the parent.
- [ADR-0004](../decisions/0004-claims-first-allocation.md),
  [ADR-0005 §3](../decisions/0005-gitops-coexistence.md).
