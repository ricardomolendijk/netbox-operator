# 0004 — Allocation is a claim, not a mode

**Status:** Accepted · 2026-08-21

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

## Why a separate kind rather than `spec.address` XOR `spec.fromPrefixRef`

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

Inline sugar keeps working: an inline address on a `NetBoxVirtualMachine` that says
`fromPrefixRef` materialises a **claim** child rather than an address child, so there
is exactly one allocation code path.

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
  re-rolling every address. `deletionPolicy: Retain` additionally keeps the NetBox object
  alive while the claim is gone.

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

## Exhaustion

`Ready=False, Reason=PoolExhausted`, an Event, and a long backoff. Not an error that
retrying fast can fix, and the operator must not spin on it.
