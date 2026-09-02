# Claims: allocating from a pool, exactly once

A `NetBoxIPAddress` says *"this address is mine"*. A **claim** says *"give me one from
here"* — and the difference is not a convenience, it is a different lifecycle. A declarative
object is driven towards a fixed desired state forever. A claim does one irreversible thing
and then stops ([ADR-0004](../decisions/0004-claims-first-allocation.md)).

This page is about the irreversible thing: what "exactly once" means when the network is
lossy, what gets your address back after you rebuild the cluster, when it does not, and what
each refusal is telling you to go and fix.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddressClaim
metadata: {name: dns-eth0, namespace: homelab}
spec:
  endpointRef: homelab
  prefixRef: {name: home-lan}
status:
  address: 10.0.20.37/24            # what you got. Immutable.
  netboxID: 412
  pool: {display: 10.0.20.0/24, endpoint: ipam/prefixes, id: 11}
  allocationIdentity: f3fb013aa1d2ffc2
  allocatedAt: "2026-08-24T10:14:02Z"
```

Field by field, the reference page is [`NetBoxIPAddressClaim`](../reference/netboxipaddressclaim.md).

There are three claim kinds, and everything on this page is true of all three unless it says
otherwise. They share one engine — one identity, one search, one read-after-write, one
exhaustion tier, one report — and differ only in what they ask for:

| Kind | Asks for | Mechanism |
|---|---|---|
| [`NetBoxIPAddressClaim`](../reference/netboxipaddressclaim.md) | one address out of a prefix | `POST prefixes/{id}/available-ips/`, locked |
| [`NetBoxPrefixClaim`](../reference/netboxprefixclaim.md) | a child prefix out of a container | `POST prefixes/{id}/available-prefixes/`, locked |
| [`NetBoxIPRangeClaim`](../reference/netboxiprangeclaim.md) | N consecutive addresses in a prefix | `POST ip-ranges/`, **unlocked** — see below |

## Two claims never get the same address

Not because the operator is careful about ordering, and not because of leader election.
NetBox takes a `select_for_update` advisory lock for the whole of one allocating request
(`ipam/api/views.py:121-129`, `:352-427`), so

```
POST /api/ipam/prefixes/11/available-ips/
```

is a single atomic call: it picks a free address and creates the object under the same lock.

The consequence is that this operator takes **no client-side lock** and does **not**
serialise per pool. A client-side lock would be unnecessary inside one process and wrong
across two — two clusters pointed at one NetBox are safe here, and they are safe for the same
reason two workers of one cluster are.

## Locked and unlocked allocation

The sentence above — "NetBox takes an advisory lock" — is true of two of the three claim kinds
and **not** of the third. That difference is the only thing about `NetBoxIPRangeClaim` an
operator has to hold in their head, so it is worth stating plainly.

NetBox 4.6.8 has exactly three allocation endpoints, and none of them places an ip-range:

```
prefixes/{id}/available-ips/         locked   an address out of a prefix
prefixes/{id}/available-prefixes/    locked   a child prefix out of a prefix
ip-ranges/{id}/available-ips/        locked   an address out of a range
```

There is no `available-ranges`. So a range claim computes the placement itself — from the
*other ranges* in the parent's VRF, never from the addresses inside them — and creates it with
an ordinary POST. Two claims can compute the same block.

**They still cannot both have it.** `IPRange.clean()` refuses to save a range that overlaps
another in the same VRF, and every API write runs it, so the loser gets a 400 that proves
nothing was created. It recomputes from fresh state and tries again, five times, with jitter.

| | Address and prefix claims | Range claims |
|---|---|---|
| Who prevents a collision | NetBox's advisory lock, before the write | `IPRange.clean()`, by rejecting the write |
| What a loser sees | nothing — it is serialised, and gets a different object | a 400 naming the range that won |
| What the operator does | one POST | up to five, one per recomputation |
| Reasons it can report | `PoolExhausted` | `PoolExhausted` **or** `AllocationContended` |

That last row is the visible consequence, and the reason the two reasons are not one:

- **`PoolExhausted`** — the parent has no run of that size free. The fix is to widen it or to
  delete a range.
- **`AllocationContended`** — the space is there, and somebody else took this attempt's
  candidate five times running. The fix is to wait; if it persists, more claims are competing
  for that parent than it has room for.

Told "exhausted" when the truth is "contended", a human goes looking for space that exists.
Told "contended" when the pool is full, they wait forever. So the two are separate reasons with
separate messages, and both wait ten minutes and both wake at once if the parent's CR changes.

Neither mechanism is a client-side lock, and that is the rule rather than an accident of these
three kinds: **any future claim kind must be able to name which server-side guarantee it relies
on — a lock, or a rejection — and one that can name neither does not ship.**

## "Exactly once" survives a lost response

The interesting failure is not two workers racing. It is one POST that succeeded and whose
answer never came back.

A retry there allocates a *second* address. Nothing raises an error and nothing reports it:
one address is burned per attempt, and the first anybody hears of it is that the /24 is full.
Two mechanisms make it not happen.

### The POST is never retried

Every other write the operator makes retries a `5xx` or a dropped connection, because those
are safe to repeat. An allocating POST is not idempotent, so it opts out: one attempt, and a
transport failure comes straight back to the reconciler.

### Before allocating, the claim looks for its own allocation identity

On **every** pass that is about to allocate — unconditionally, not only after a suspected
failure — the claim first issues one indexed lookup:

```
GET /api/ipam/ip-addresses/?cf_k8s_allocation_identity=f3fb013aa1d2ffc2
```

One GET is cheaper than one leaked address.

| Matches | What happens |
|---|---|
| 0 | Allocate. |
| 1 | **Reclaim it.** `Allocated=True, Reason=ReclaimedByIdentity`, an `AllocationReclaimed` Event, zero POSTs. |
| 2 or more | **Never allocate.** `Ready=False, Reason=AllocationConflict` naming every match, and nothing is deleted. |

The identity is written into the allocated object's custom field **by the allocating POST
itself**. NetBox's `available-ips` view honours the full write serializer and injects only
`address` (and the parent's `vrf`), so `custom_fields` and `tags` ride along on the atomic
call. There is therefore no window in which an allocated address exists without the value
that says whose it is — which is exactly what makes the search above able to recover
*everything*:

- a lost HTTP response,
- a pod evicted between the POST and the status write,
- a controller-runtime retry,
- a cluster rebuilt from Git.

Those are one code path and one condition reason, not four recovery modes, because from
NetBox's side they are one situation: the object is there and it says whose it is.

## The allocation identity, and why it is stable across a cluster rebuild

```
identity = sha256(netbox url + "\n" + namespace + "\n" + kind + "\n" + name)[:16]
```

It is **deterministic** rather than random, and that is the whole of
[ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git).
The obvious thing to key an idempotency record on is the CR's UID — and a UID is regenerated
at precisely the moment you most want the old address back. Delete the cluster, apply the
same manifest to a fresh one, and every claim has a new UID and the same *name*.

The four components are the four things that make an allocation a different allocation:

| Component | Why it is in there |
|---|---|
| the NetBox URL | the same claim pointed at a second NetBox is a second allocation; searching the wrong NetBox for the first one finds nothing and allocates twice |
| namespace | two teams may legitimately have a `dns-eth0` |
| Kind | a future `NetBoxPrefixClaim` named `dns-eth0` is not this claim |
| name | the obvious one |

They are joined on a **newline**, not concatenated: `("a", "bc")` and `("ab", "c")` would
otherwise render to one string and two different claims would share one identity — which is
the `AllocationConflict` state that can never be resolved automatically.

The URL is normalised, so `https://nb`, `https://nb/` and `https://nb/api` are one NetBox
rather than three identities. And it is taken from the client that is about to do the POST
rather than read from the `NetBoxEndpoint` CR: an identity derived from a field that was
momentarily unreadable would allocate a second address exactly once, silently, on the
unluckiest pass.

The derivation is pinned by a golden unit test. Changing it re-rolls every allocated address
in every cluster that upgrades — every claim would search for an identity nothing carries,
find nothing, and allocate again — so it breaks the build instead.

### Renaming a claim

The derived identity cannot survive a rename, by construction: the name is in the hash. So
there is an escape hatch.

```yaml
spec:
  allocationIdentity: f3fb013aa1d2ffc2   # the identity the old name derived
```

Set it and the renamed claim reclaims the original address. It is immutable once set —
addable, not changeable, because an identity that moves is a claim pointed at somebody else's
address. `status.allocationIdentity` on the old claim is where you read the value from, and
it is why that field is reported at all.

### An identity store is mandatory, and there is no override

The provenance stamp is optional for an ordinary object: an unstamped object is merely
unattributed. For a claim the identity store is what makes a lost response recoverable, so a
claim on an endpoint with nowhere to keep one **allocates nothing at all**:
`Reason=IdempotencyKeyUnavailable`, zero POSTs.

In practice: set `spec.managedBy.clusterID` on the `NetBoxEndpoint` and let the bootstrap
create the `k8s_allocation_identity` custom field, or create it by hand with `type: text`,
`filter_logic: exact`, and `ipam.ipaddress` among its object types
([provenance](../operations/provenance.md)). `filter_logic: exact` is not cosmetic: NetBox's
default is `loose`, which makes `?cf_...=` a substring match.

## `status.address` is immutable. The operator never re-allocates.

Once a claim holds an address, the reconciler's first guard clause returns before anything
can allocate again. That guard is also why the steady state of every claim in the cluster is
a reconcile that makes **no NetBox request at all**.

If somebody deletes the allocated object in the NetBox UI, the claim does **not** pick a new
address. By the time a claim has handed one out, something outside Kubernetes is using it — a
NIC's static configuration, a DNS record, a firewall rule. Restoring the same address is
always safe; picking a new one never is, and the operator is the last component that should
be making that call ([ADR-0004](../decisions/0004-claims-first-allocation.md)).

**"Never re-allocate" is not "never allocate."** A claim that failed to allocate has
allocated nothing, and its next pass is still its first allocation. An exhausted pool leaves
`status.address` empty and keeps trying.

## An exhausted pool waits

The pool is full. That is neither transient — retrying the same request cannot help — nor
permanent — somebody freeing one address makes it succeed — so it gets its own arm
([#178](https://github.com/ricardomolendijk/netbox-operator/issues/178)):

- `Ready=False, Reason=PoolExhausted`, an Event, and a requeue at a **fixed ten minutes**.
  Not the workqueue's exponential backoff, which starts in milliseconds and would spin
  against a query that cannot succeed; not terminal either, because the claim is not
  misconfigured and a terminal failure would sit there after somebody widened the prefix.
- The claim also **watches its `NetBoxPrefix`**, so widening the prefix in Git re-enqueues it
  immediately rather than up to ten minutes later.

**The watch covers one of the two fixes, and only one.** Widening the prefix is a change to a
Kubernetes object, so the watch sees it. **Freeing an address inside NetBox is not** — no
Kubernetes object changed and nothing tells the operator. That case is caught by the
ten-minute timer and by nothing else. The two are not redundant; each covers a fix the other
cannot see.

The condition **names the pool and states its utilisation**, because a reader told only
"exhausted" goes and looks the prefix up by hand:

```
pool 10.0.20.0/24 (netbox ipam/prefixes/11) is fully utilised: netbox has no free address
to allocate ({"detail": "Insufficient resources are available to satisfy the request"}).
Widen the prefix, or free one in netbox -- this claim retries every 10m0s, and immediately
if the pool's own CR changes
```

The utilisation is 100% **by NetBox's own answer**, not by a count. The operator never asks
how much of a pool is free: an IPv6 `/64` has 2^64 addresses, the answer is capped by
`MAX_PAGE_SIZE` anyway, and a number that is both expensive and misleading is worse than no
number. That is also why there is no free-address gauge and no "pool nearly full" condition —
exhaustion is detected *only* by the POST's own `409`.

## Pools the operator refuses to allocate out of

Two states of a prefix are a refusal rather than an attempt, and both are the NetBox operator
having said something that `available-ips` does not honour on its own.

| Pool state | Why it is refused |
|---|---|
| `mark_utilized: true` | It only forces NetBox's utilisation gauge to 100%; `available-ips` would still hand out an address. The flag means "the free space here is not really free — it is delegated to DHCP or to another IPAM", so honouring it has to be the operator's job. |
| `status: container` | A container's free space is subdivided by child prefixes rather than populated by addresses. A bare address out of one is almost always a mistake. |

Both report `Reason=PoolNotAllocatable` naming the flag, and there is no override for either.
For a container, allocate a child prefix instead
([`NetBoxPrefixClaim`](../reference/netboxprefixclaim.md)) or write a `NetBoxIPAddress` with an
explicit address. `reserved` and `deprecated` are *not* refused: both are ordinary operational
states, and holding a reserved prefix is done by allocating out of it.

**`status: container` is a refusal for one claim kind and a precondition for another**, and that
is the clearest illustration of why both lists are per-kind data rather than rules in shared
code:

| Kind | `mark_utilized: true` | `status: container` |
|---|---|---|
| `NetBoxIPAddressClaim` | refused | **refused** — a container is subdivided, not populated |
| `NetBoxPrefixClaim` | refused | **expected** — subdividing is what it does |
| `NetBoxIPRangeClaim` | refused | allowed, unremarked — a DHCP scope inside a container is ordinary |

A prefix claim against a parent that is *not* a container allocates anyway and records a
`PoolUnexpectedStatus` warning Event. Subdividing a network that is already in service is
unusual rather than wrong, and NetBox's own `available-prefixes` view does not consult `status`
at all — so refusing would be this operator inventing a rule the server does not have.

There is no special case for a `/31`, `/32`, `/127` or `/128`. NetBox's own
`Prefix.get_available_ips()` already decides whether the network and broadcast addresses are
usable — mask length and `is_pool` both feed that — and duplicating the arithmetic
client-side is how the two drift apart. A full `/32` is an ordinary `PoolExhausted`.

## Deleting a claim

**Deleting a claim frees its address.** `spec.deletionPolicy` defaults to `Delete`
([#225](https://github.com/ricardomolendijk/netbox-operator/issues/225), which reverses
[#182](https://github.com/ricardomolendijk/netbox-operator/issues/182)):

```
Deleted  freed netbox ipam/ip-addresses/412 (10.0.20.37/24), which is available for
         reallocation again
```

### Why the default is `Delete` here and `Retain` on `NetBoxIPAddress`

A claim's CR **is the only record that the allocation exists.** "Give me any free address out
of `home-lan`" is not a statement about `10.0.20.37` — the manifest names a prefix, and nothing
in Git names the address. So when the claim goes, nothing anywhere refers to the address it was
handed: it is not wrong in NetBox, it is unattributable, and that makes it invisible by
construction rather than merely untidy.

A [`NetBoxIPAddress`](../reference/netboxipaddress.md) with an explicit `spec.address` is the
opposite. Somebody typed `10.0.0.9/24`, and something outside Kubernetes very likely agrees
with them — a NIC's static configuration, a DNS record, a firewall rule. There, `Retain`
protects real intent, and its default is unchanged
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)).

The distinction is not who created the object. It is **whether anything still names the address
once the CR is gone.** For a claim, nothing does.

### What this costs, plainly

**A freed address can be reallocated immediately, so an accidental `kubectl delete` on a claim
is unrecoverable.** Re-applying the same manifest derives the same allocation identity, but if
something else has taken the address meanwhile the claim gets a different one — and whatever
was configured to use the old address is now pointed at somebody else's. Under the previous
uniform `Retain` the same mistake was recoverable: re-apply and get the address back.

That is a real regression in one direction. What it buys is the other direction: an inline
`claimFrom` on a VM materialises a claim owned by that VM
([#174](https://github.com/ricardomolendijk/netbox-operator/issues/174)), so uniform `Retain`
leaked one address per VM deletion, and in a CI-driven cluster that eventually **exhausts the
pool** — which is a [wait-forever state](#an-exhausted-pool-waits), so the
leak did not degrade allocation, it stopped it.

### `deletionPolicy: Retain`

Set it on a claim whose address something outside Kubernetes depends on and cannot be told
about. It calls NetBox not at all, and reports what it is leaving behind:

```yaml
spec:
  deletionPolicy: Retain
```

```
AddressRetained  spec.deletionPolicy is Retain: netbox ipam/ip-addresses/412 (10.0.20.37/24)
                 was left in place and not deleted; it still carries allocation identity
                 f3fb013aa1d2ffc2, so re-applying this claim reclaims it -- to free it,
                 delete the netbox object
```

It also increments `netbox_operator_allocations_retained_total{kind}` — a counter, because the
Event ages out of its namespace within the hour and "how many addresses has this cluster left
behind" has to stay answerable. This is the only moment the operator holds the address, the
NetBox id and the identity together; after the CR is gone there is no status left to read them
from.

### The finalizer still cannot wedge a namespace

The previous behaviour made **no NetBox call at all** on deletion, which made that guarantee
free. A `Delete` policy spends it, so it is bought back deliberately:

| What happens | What the claim does |
|---|---|
| `DELETE` succeeds | Release, `Normal`/`Deleted`. |
| `DELETE` returns 404 | Release, `Normal`/`Deleted`. Already gone is the end state that was asked for, reached by somebody else. |
| `DELETE` refused (409, a `PROTECT`ed relation such as a NAT pairing) | Keep the finalizer, `Deleting=False, Reason=Protected`, capped backoff. Never an error — that would put the workqueue's millisecond backoff on top of an interval chosen deliberately. |
| The endpoint is not `Ready`, or NetBox is unreachable | Keep the finalizer, reason and interval from the [error table](errors-and-retries.md). |
| Any of the above, **8 attempts in** (~20 minutes) | **Release anyway**, `Warning`/`AddressRetained`, and count it. |

That last row is the whole of it. The declarative engine keeps its finalizer forever on a
delete NetBox will not accept and relies on a human writing
`netbox.kubeforge.org/skip-finalizer=true` onto the CR; a claim cannot afford that, because
claims are created by machinery rather than by hand and a namespace full of them would have to
be unwedged one CR at a time. So a claim gives up, and gives up **into the behaviour that
shipped before this change**: the address is left allocated, the `AddressRetained` Event names
it and the counter counts it. Leaking an address the operator has reported is no worse than the
default being reversed here; a namespace that will not delete would be a new failure mode.

The break-glass annotation works on a claim too, and skips the `DELETE` entirely.

An endpoint whose `driftMode` is `Report`, or whose `mode` is `DryRun`, deletes **nothing** —
it is handed a client that physically cannot mutate NetBox, so the `DELETE` is made and never
leaves the process. The claim reports `AddressRetained` and releases.

## What reclaim can and cannot recover

The honest table, because "the identity is deterministic" is necessary and not sufficient:

| What happened | The NetBox object | Re-applying the same manifest |
|---|---|---|
| Cluster torn down, namespace deleted, finalizers stripped | survives | **reclaims the same address** |
| Claim deleted with `kubectl delete`, `deletionPolicy: Retain` | survives | **reclaims the same address** |
| Claim deleted with `kubectl delete`, default `deletionPolicy: Delete` | **freed, and may already be somebody else's** | allocates whatever is free now — the same address only if nothing took it |
| Claim renamed, `spec.allocationIdentity` set to the old identity | survives | **reclaims the same address, once the object's owner stamp names the new claim** — see [`ForeignAllocation`](#foreignallocation--the-identity-names-an-object-this-claim-cannot-be-shown-to-own) |
| Claim renamed, nothing set | survives, now orphaned and reported | allocates a **new** address; the old one stays until somebody deletes it |
| The NetBox object deleted in NetBox | gone | allocates a new address — there is nothing left to reclaim |
| NetBox restored from empty | gone | allocates a new address; see [restoring NetBox from backup](../operations/gitops.md#restoring-netbox-from-backup) |

## The four refusals a human has to clear

All four wait on the same ten-minute tier, emit an Event, and write nothing.

### `AllocationConflict` — two objects carry one identity

A previous over-allocation. The operator will not choose between them and will not delete
either: a NIC or a DNS record may be pointing at either one, and the operator cannot prove
which. The message names every match.

**Fix:** look at both in NetBox, decide which is in service, delete the other. The claim
reclaims the survivor on its next pass.

### `ReclaimedOutsidePool` — the identity resolves outside the prefix

An object carrying this claim's identity exists and is not inside the pool the claim names
*now*. Reachable by repointing a claim, renaming a prefix, or reusing a claim name for a
different purpose — and reachable **because** the identity is deterministic, where a
UID-keyed one never could be. It is the price of the paragraph above about cluster rebuilds.

Allocating a second address here would leave two objects carrying one identity, and silently
accepting the out-of-pool object would make `prefixRef` a lie.

**Fix:** either delete the stale NetBox object, or set `spec.allocationIdentity` to the
identity of the object this claim should keep.

### `ForeignAllocation` — the identity names an object this claim cannot be shown to own

Only reachable with `spec.allocationIdentity` set. The object carrying the identity this claim
was *given* is either stamped as belonging to a different CR or cluster, or carries no
provenance this endpoint can read at all.

The identity is the whole of a claim's ownership proof: one custom field is matched and the
match is adopted. That is safe for a derived identity, which is
`sha256(url, namespace, kind, name)` and therefore already contains the claim's own namespace —
no namespace can compute another's. It is not safe for a value somebody types, and the value
they would need is printed in the other claim's `status.allocationIdentity`. Without this
refusal a claim in one namespace could adopt another namespace's address, report it as its own,
and delete the live NetBox object with itself under the default `deletionPolicy: Delete`.

**Why an unreadable stamp is refused too.** A stamp is read back by the field names of the
endpoint doing the reading, and for a claim in another namespace that is an endpoint that
namespace wrote. Rename `uidField`, `clusterField` and `ownerField` in its `spec.managedBy` —
keeping `allocationIdentityField`, which has to match or nothing would be found — and every
object the endpoint next door stamped reads back as unstamped. So "unstamped" cannot be read as
"unowned": that made the guard switchable off by the party it guards against
([#299](https://github.com/ricardomolendijk/netbox-operator/issues/299)), and an object
carrying somebody's allocation identity that this endpoint cannot attribute is now refused
rather than adopted.

This is a fail-closed choice with a cost, and the cost falls on one case: a **given** identity
pointed at an object nobody stamped — a pre-existing NetBox address being migrated, or the
object a claim held under an earlier name. Both are refused until the object is stamped for the
claim that should have it. Nothing is written, deleted or lost in the meantime, and every
**derived** identity is untouched: cluster rebuilds, re-applied manifests and recovery from a
lost status write never reach this check.

**Fix:** either unset `spec.allocationIdentity` and let the claim derive its own and allocate;
or have the other owner release the object; or — when the object really is this claim's — hand
it over explicitly in NetBox by setting the endpoint's owner custom field (`k8s_owner` by
default) on it to `<lowercased kind>/<namespace>/<name>`, e.g.
`netboxipaddressclaim/homelab/dns-eth0`. The refusal message names the field and the value to
use. The next pass reclaims the object.

### `IdempotencyKeyUnavailable` — nowhere to store an identity

See above. **Fix:** give the endpoint a `spec.managedBy.clusterID`, or create the custom
field by hand.

## What is not here yet

Three things this page will grow, named so that their absence is not mistaken for a gap in the
design:

- **`spec.ipRangeRef` on `NetBoxIPAddressClaim`.** `ip-ranges/{id}/available-ips/` is a real,
  advisory-locked endpoint, and allocating an address out of a *reserved range* is a sensible
  thing to want. It is not here because a claim descriptor names exactly one pool field today,
  and two mutually-exclusive pool sources on one Kind means a pool *list* in the shared engine:
  per-pool value fields, per-pool refusals, and `status.pool.kind` to say which one answered.
  That is a change to the allocation engine rather than a new descriptor beside it, so it is its
  own ticket rather than a rider on this one.
- **The child `NetBoxIPAddress`.** A claim's job is to *get* an address; the ongoing desired
  state of the address it got belongs to a declarative `NetBoxIPAddress` that the claim owns
  and materialises (`status.ipAddressRef`, a `Bound` condition). That kind lands with NBO-025
  and the materialiser with NBO-032. Until then the claim's spec deliberately carries **no**
  pass-through fields — no `dnsName`, no `role`, no `description`, no `assignedObject` —
  because a field the operator writes once at allocation and can never correct is a field
  that lies the first time somebody edits it.
- **The inline form.** `claimFrom: {prefixRef: ...}` on a VM's interface materialises a real
  `NetBoxIPAddressClaim` rather than being a second allocation path
  ([ADR-0004](../decisions/0004-claims-first-allocation.md)). It is sugar over this, and it
  arrives with the materialiser.

## Related

- [ADR-0004 — Claims-first allocation](../decisions/0004-claims-first-allocation.md): why a
  separate kind, why the inline form is sugar, why exhaustion waits.
- [ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git):
  the deterministic identity, and §4 for why the address is not written back to Git.
- [`NetBoxIPAddressClaim`](../reference/netboxipaddressclaim.md),
  [`NetBoxPrefixClaim`](../reference/netboxprefixclaim.md),
  [`NetBoxIPRangeClaim`](../reference/netboxiprangeclaim.md): every field, condition and reason.
- [Coexisting with Flux and Argo CD](../operations/gitops.md#rebuilding-a-cluster-from-git):
  the rebuild and restore walkthroughs.
- [Provenance](../operations/provenance.md): what the stamp writes, and the custom fields the
  identity needs.
