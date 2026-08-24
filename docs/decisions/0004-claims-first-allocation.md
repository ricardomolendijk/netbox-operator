# 0004 — Allocation is a claim, not a mode

**Status:** Accepted · 2026-08-21
**Amended:** 2026-08-24 — the inline form is sugar over a real claim CR, not a second
allocation path ([#174](https://github.com/ricardomolendijk/netbox-operator/issues/174)), the
inline key is `claimFrom` rather than `fromPrefixRef`
([#183](https://github.com/ricardomolendijk/netbox-operator/issues/183)), and
[exhaustion](#exhaustion) waits rather than failing terminally
([#178](https://github.com/ricardomolendijk/netbox-operator/issues/178)).
**Amended:** 2026-08-24 — [deleting a claim frees its address](#deleting-a-claim-frees-its-address)
([#225](https://github.com/ricardomolendijk/netbox-operator/issues/225), reversing
[#182](https://github.com/ricardomolendijk/netbox-operator/issues/182)).

## Decision

"Give me a free address" is a **separate kind** with its own lifecycle, not a field on
the resource kind:

| Kind | Meaning |
|---|---|
| `NetBoxIPAddress` | "This address is mine." Declarative, drift-corrected, reconciled forever. |
| `NetBoxIPAddressClaim` | "Give me one from here." Allocates once, then immutable. |
| `NetBoxPrefixClaim` | Allocate a child prefix of length *N* from a container. |
| `NetBoxIPRangeClaim` | Allocate a range from a prefix. |

A claim resolves into a real resource CR, owned by the claim:

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddressClaim
metadata: {name: dns-eth0, namespace: homelab}
spec:
  endpointRef: homelab
  prefixRef: {name: prefix-servers}     # or ipRangeRef
  preferredFamily: IPv4
  dnsName: dns.home.arpa
  assignedObject:
    vmInterfaceRef: {name: vm-dns-eth0}
status:
  address: 10.20.0.37/24
  ipAddressRef: {name: dns-eth0-ip}     # the NetBoxIPAddress it created and owns
  conditions: [{type: Ready, status: "True"}]
```

## Why a separate kind rather than `spec.address` XOR an allocation field

1. **The lifecycles genuinely differ.** A resource is reconciled toward a fixed
   desired state forever. A claim does one irreversible thing and then stops. Encoding
   both in one kind means half the spec fields are meaningless in either mode, and the
   immutability rule ("once allocated, never re-allocate") has to be enforced by
   controller logic instead of by the type.
2. **Allocation becomes the cause of an object, not a mode of it.** `kubectl get
   netboxipaddress` shows real addresses; `kubectl get netboxipaddressclaim` shows who
   asked for what. The mode-flag design makes `status.address` mean something different
   depending on a sibling field.
3. **It is what upstream converged on.** `netbox-community/netbox-operator` splits
   `IpAddress` / `IpAddressClaim`, `Prefix` / `PrefixClaim`, `IpRange` /
   `IpRangeClaim`. Matching that shape makes migration between the two operators
   mechanical, and it is a design that has already met real users.
4. **CEL mutual exclusion is a worse contract than two types.** Two kinds document
   themselves; a one-of does not.

## The inline form is sugar over this, not a second path

**Decided** on
[#174](https://github.com/ricardomolendijk/netbox-operator/issues/174): a VM or an interface
gets its address inline, *and* the inline form materialises a real claim.

```yaml
kind: NetBoxVirtualMachine
spec:
  name: web-01
  interfaces:
    - name: eth0
      addresses:
        - claimFrom: {prefixRef: {name: mgmt-net}}  # allocate one -> materialises a claim
        - address: 10.0.0.9/24                      # or state one  -> materialises an address
```

One CR describes the whole VM, which is the ergonomics that were asked for. What makes it safe
is that it is *sugar over* the claim above rather than a second implementation:

1. **One allocation engine.** An inline entry that names a prefix instead of an address
   materialises an actual `NetBoxIPAddressClaim` child — the same kind, the same controller,
   the same advisory-locked POST. There is no second code path that could diverge from this
   one.
2. **The claim is a real CR either way.** `kubectl get netboxipaddressclaims` shows it,
   `status.address` is where the allocated value lives, and it carries the controller owner
   reference of [ADR-0003 rule 3](0003-ownership-and-references.md), so deleting the VM prunes
   it. Nothing hides.
3. **It stays droppable in `v1beta1`.** Because the inline field is optional and the child is
   identified by its marker rather than by its parent's spec, removing the sugar at a version
   boundary breaks nobody — the property [ADR-0003 rule 5](0003-ownership-and-references.md)
   requires of any inline field ([#17](https://github.com/ricardomolendijk/netbox-operator/issues/17)).

**The inline form does not have to express everything, and deliberately does not.** A claim
that needs a specific VRF, a role, a DNS name, or a non-default `deletionPolicy` is still
written as its own `NetBoxIPAddressClaim`. Inline covers the common case; the standalone claim
stays the complete one, which is what keeps the sugar from growing into a mirror of the claim
spec.

### The inline key is `claimFrom`, and it is nested

**Decided** on
[#183](https://github.com/ricardomolendijk/netbox-operator/issues/183): the inline key is
`claimFrom`, carrying the pool reference inside it.

```yaml
addresses:
  - claimFrom: {prefixRef: {name: mgmt-net}}
```

Not `fromPrefixRef`, which earlier drafts of this ADR and of
[ADR-0003 rule 5](0003-ownership-and-references.md) used. The reason is the shape of what comes
next rather than the spelling: a claim may allocate out of an ip-range as well as a prefix
(`NetBoxIPRangeClaim`, NBO-064), and `fromPrefixRef` generalises only by growing a second
sibling key that is mutually exclusive with the first — two flat fields, a CEL rule to keep
them apart, and no place to put a third. `claimFrom` generalises by adding a member:

```yaml
- claimFrom: {ipRangeRef: {name: dhcp-pool}}
```

which is the same union shape a [generic reference](../concepts/generic-refs.md) already has,
with exactly-one-of enforced inside one field instead of across several. It also reads as what
it is — "claim from here" — where `fromPrefixRef` reads as a reference *to* a prefix, which is
what `prefixRef` on the standalone claim already is.

## Correctness under concurrency

NetBox exposes advisory-locked allocation endpoints
(`ipam/api/views.py:121-129, 352-427`):

- `POST /api/ipam/prefixes/{id}/available-ips/`
- `POST /api/ipam/prefixes/{id}/available-prefixes/`
- `POST /api/ipam/ip-ranges/{id}/available-ips/`

The POST is a single atomic call under NetBox's own lock, so two controller workers —
or two clusters — cannot land on the same address. On top of that:

- **Allocation happens exactly once**, guarded by `status.address`. After that the
  object is reconciled by ID and never re-allocated.
- **An idempotency key** written to a custom field or tag on the allocated object makes
  a retry after a lost response recoverable: the controller looks for its own key before
  allocating again. Without this, a timeout on the POST leaks an address on every retry.
  The key is **deterministic**, derived from `(endpoint, namespace, kind, name)` rather
  than from the claim's UID, so a claim re-created from the same Git manifest reclaims
  the same address — see [ADR-0005 §3](0005-gitops-coexistence.md). The UID is recorded
  in `status` for debugging and to detect a *different* claim taking the same name.
- **Read-after-write** verification before `status.address` is set.
- A claim deleted and re-created **reclaims its previous address**, via the deterministic
  identity above — which is what makes rebuilding a cluster from Git converge instead of
  re-rolling every address. That reclaim needs the NetBox object to have survived, which by
  default it does **not**: see [deleting a claim frees its
  address](#deleting-a-claim-frees-its-address). `deletionPolicy: Retain` is what keeps it
  alive while the claim is gone, and re-applying a `Delete` claim gets the same address only
  if nothing has taken it in the meantime.

## `status.address` is immutable. The operator never re-allocates.

If the allocated NetBox object is deleted out from under a live claim — someone with write
access removes it, or a restore rolls it back — the claim does **not** pick a new address.

Recovery belongs to the child `NetBoxIPAddress`, and the engine already does it: `status.id`
404s, the id is cleared, the natural-key lookup finds nothing, and the object is
re-created — **at the same address**, because by then the address is literal in the child's
spec. The claim goes `Bound=True` again and nothing else happens.

If re-creation is genuinely impossible — a third party has taken the address, or the pool
prefix is gone — the claim reports `Bound=False`, `Ready=False`,
`Reason=AllocationLost` with a long backoff, and waits for a human.

The reason this is worth stating as a guarantee rather than leaving to the code: by the
time a claim has handed out an address, something outside Kubernetes is using it — a NIC's
static configuration, a DNS record, a firewall rule. Restoring the same address is always
safe. Silently picking a new one converts a NetBox bookkeeping accident into a live network
change nobody asked for, and the operator is the last component that should be making that
call.

`Allocated` therefore stays `True` forever once it is true — it is a historical fact, not a
liveness signal — and `Bound` carries liveness. That split is why there is no `Degraded`
condition on a claim.

## Deleting a claim frees its address

**Decided** on
[#225](https://github.com/ricardomolendijk/netbox-operator/issues/225), which **reverses**
[#182](https://github.com/ricardomolendijk/netbox-operator/issues/182): a claim's
`spec.deletionPolicy` defaults to `Delete`, and the field exists so that a specific claim can
opt into `Retain`.

Issue #182 had been answered the other way — uniform `Retain`, plus a garbage-collection reporting
path — and that answer shipped. It is recorded here as a reversal rather than quietly replaced,
because the reasoning behind it was not wrong, it was outweighed.

### The rule

Not "IPAM is destructive, so retain". The rule is **whether anything still names the NetBox
object once the CR is gone.**

A claim's CR is the only record that its allocation exists. "Give me any free address out of
`mgmt-net`" is not a statement about `10.0.20.37` — the manifest names a prefix, and nothing
anywhere in Git names the address. So when the claim goes, nothing refers to what it was handed:
the address is not wrong in NetBox, it is *unattributable*, and that makes it invisible by
construction rather than merely untidy.

A `NetBoxIPAddress` with an explicit `spec.address` is the exact opposite. Somebody typed
`10.0.0.9/24`, and something outside Kubernetes very likely agrees with them — a NIC's static
configuration, a DNS record, a firewall rule. There `Retain` protects real intent, and its
default is unchanged
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)).

The two cases genuinely differ, so encoding the difference is honest rather than inconsistent —
and it is the same argument the [immutable `status.address`](#statusaddress-is-immutable-the-operator-never-re-allocates)
rests on, pointed the other way. A live claim never moves its address, because something is
using it. A deleted claim frees it, because nothing is left that says so.

### What settled it

The [inline form](#the-inline-form-is-sugar-over-this-not-a-second-path) materialises a claim
owned by the VM. Under uniform `Retain` that is **one leaked address per VM deletion**, and in
a CI-driven cluster that creates and destroys VMs on every run it exhausts the pool.
[Exhaustion](#exhaustion) is a wait-forever state by design, so the leak did not degrade
allocation — it eventually **stopped** it, and the only remedy was a human deleting rows in
NetBox by hand with nothing in the cluster to tell them which ones.

### What it costs

**A freed address can be reallocated immediately, so an accidental `kubectl delete` on a claim
is unrecoverable.** Re-applying the same manifest derives the same allocation identity, but if
something has taken the address meanwhile the claim gets a different one — and whatever was
configured to use the old address is now pointed at somebody else's. Under `Retain` the same
mistake was recoverable by hand.

That is a real regression in one direction, accepted in exchange for the other. It is not a free
improvement and the [documentation says so](../concepts/deletion.md#the-claim-is-the-exception-to-the-exception)
rather than selling it.

### What #182's answer keeps

Its reporting half stays, and is still correct for the `Retain` case it was built for: the
`AddressRetained` Event naming the address, the id and the identity; the
`netbox_operator_allocations_retained_total{kind}` counter; and `NetBoxSweep` reporting
provenance-stamped orphans. Those were never only about the claim default — a namespace deleted
while the operator was down, a restore and a hand-deleted CR all produce the same orphan, and
after the Events age out the counter is the only thing that keeps "how many has this cluster
left behind" answerable.

### The property that had to be bought back

The `Retain`-only deletion pass made **zero NetBox calls**, which made "a claim's finalizer
cannot wedge a namespace" structural rather than argued. `Delete` necessarily puts a call on the
deletion path, so the guarantee is re-earned: a refusal or a failure is a requeue with capped
backoff and a `DeleteBlocked` Event, never a returned error, and after a bounded number of
attempts the claim **releases its finalizer anyway** and reports the address as retained through
the Event and counter #182 introduced.

Degrading to the outcome that already shipped is no worse than what shipped. Wedging a namespace
would have been a new failure mode, and a better default is not allowed to cost that.

## Exhaustion

The pool is full and NetBox's `available-ips` returns nothing. That is neither transient
(retrying the same request cannot help) nor permanent (somebody freeing one address makes it
succeed), so it gets its own arm.

**Decided** on
[#178](https://github.com/ricardomolendijk/netbox-operator/issues/178): **the claim waits, and
it also watches its prefix.**

- `Ready=False, Reason=PoolExhausted`, an Event, and a requeue at a **fixed 10 minutes** — the
  same tier as a [`TruncatedError`](../concepts/errors-and-retries.md#runaway-lists), reused
  for the same reason: nothing clears it but a human, so a fast retry only burns API budget.
  Not terminal, because the claim is not misconfigured — a terminal failure would sit there
  after the fix landed, waiting for somebody to touch the object.
- The claim **watches its `NetBoxPrefix`** as any reference does
  ([references](../concepts/references.md#ordering-and-convergence)), so widening the prefix in
  Git re-enqueues the claim immediately instead of up to ten minutes later. It is one line on
  top of the timer, because the ref-watch edge already exists.

**The watch covers one of the two fixes, and only one.** Widening the prefix is a change to the
`NetBoxPrefix` CR, so the watch sees it. **Freeing an address inside NetBox is not** — no
Kubernetes object changed, and nothing tells the operator. That case is caught by the
10-minute timer and by nothing else, so the timer is not redundant with the watch; each covers
a fix the other cannot see.

Two details the condition has to get right:

- **It names the pool and its utilisation**, not just "exhausted". A claim that cannot allocate
  should say which prefix it tried and how full that prefix is, or the first thing every reader
  does is go and look it up by hand.
- **An exhausted claim holds no address it did not get.** `status.address` stays empty, and the
  never-re-allocate rule above is *never re-allocate*, not *never allocate*: a claim that has
  failed to allocate has allocated nothing, and its next pass is still its first allocation.
