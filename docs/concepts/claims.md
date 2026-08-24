# Claims: allocating an address, exactly once

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
For a container, allocate a child prefix instead (`NetBoxPrefixClaim`, NBO-064) or write a
`NetBoxIPAddress` with an explicit address. `reserved` and `deprecated` are *not* refused:
both are ordinary operational states, and holding a reserved prefix is done by allocating out
of it.

There is no special case for a `/31`, `/32`, `/127` or `/128`. NetBox's own
`Prefix.get_available_ips()` already decides whether the network and broadcast addresses are
usable — mask length and `is_pool` both feed that — and duplicating the arithmetic
client-side is how the two drift apart. A full `/32` is an ordinary `PoolExhausted`.

## Deleting a claim

**A claim always retains its NetBox object.** There is no `deletionPolicy` field on a claim:
a single-valued knob is not one, and `Retain` is the value that makes the deterministic
identity worth having
([#182](https://github.com/ricardomolendijk/netbox-operator/issues/182)).

What stops that from being a silent leak is that the operator **reports** it. Deleting a
claim emits

```
AddressRetained  netbox ipam/ip-addresses/412 (10.0.20.37/24) was left in place and not
                 deleted; it still carries allocation identity f3fb013aa1d2ffc2, so
                 re-applying this claim reclaims it -- to free it, delete the netbox object
```

and increments `netbox_operator_allocations_retained_total{kind}` — a counter, because the
Event ages out of its namespace within the hour and "how many addresses has this cluster left
behind" has to stay answerable. This is the only moment the operator holds the address, the
NetBox id and the identity together; after the CR is gone there is no status left to read them
from.

The operator **never deletes** an object it cannot prove is unused, and it cannot prove that
of an allocated address. To free one: delete the NetBox object, in NetBox.

The deletion pass makes **no NetBox call at all**, which is why this finalizer cannot make a
namespace undeletable however unreachable NetBox is.

## What reclaim can and cannot recover

The honest table, because "the identity is deterministic" is necessary and not sufficient:

| What happened | The NetBox object | Re-applying the same manifest |
|---|---|---|
| Cluster torn down, namespace deleted, finalizers stripped | survives | **reclaims the same address** |
| Claim deleted with `kubectl delete` | survives (claims always retain) | **reclaims the same address** |
| Claim renamed, `spec.allocationIdentity` set to the old identity | survives | **reclaims the same address** |
| Claim renamed, nothing set | survives, now orphaned and reported | allocates a **new** address; the old one stays until somebody deletes it |
| The NetBox object deleted in NetBox | gone | allocates a new address — there is nothing left to reclaim |
| NetBox restored from empty | gone | allocates a new address; see [restoring NetBox from backup](../operations/gitops.md#restoring-netbox-from-backup) |

## The three refusals a human has to clear

All three wait on the same ten-minute tier, emit an Event, and write nothing.

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

### `IdempotencyKeyUnavailable` — nowhere to store an identity

See above. **Fix:** give the endpoint a `spec.managedBy.clusterID`, or create the custom
field by hand.

## What is not here yet

Two things this page will grow, named so that their absence is not mistaken for a gap in the
design:

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
- [`NetBoxIPAddressClaim`](../reference/netboxipaddressclaim.md): every field, condition and
  reason.
- [Coexisting with Flux and Argo CD](../operations/gitops.md#rebuilding-a-cluster-from-git):
  the rebuild and restore walkthroughs.
- [Provenance](../operations/provenance.md): what the stamp writes, and the custom fields the
  identity needs.
