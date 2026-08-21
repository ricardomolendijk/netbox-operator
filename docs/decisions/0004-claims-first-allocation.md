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
apiVersion: netbox.populator.io/v1alpha1
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
- **An idempotency key** — the claim's UID, written to a custom field or a tag on the
  allocated object — makes a retry after a lost response recoverable: the controller
  looks for its own key before allocating again. Without this, a timeout on the POST
  leaks an address on every retry.
- **Read-after-write** verification before `status.address` is set.
- A claim deleted and re-created gets a **new** address. Documented, and avoidable with
  `deletionPolicy: Retain`.

## Exhaustion

`Ready=False, Reason=PoolExhausted`, an Event, and a long backoff. Not an error that
retrying fast can fix, and the operator must not spin on it.
