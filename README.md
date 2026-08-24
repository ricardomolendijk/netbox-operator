# netbox-operator

A Kubernetes operator that turns [NetBox](https://netbox.dev) into a declarative,
continuously reconciled resource. Every NetBox object is a Custom Resource, every
NetBox foreign key is a Kubernetes reference, and `kubectl apply` / `kubectl delete`
are the only verbs you need.

```yaml
apiVersion: netbox.populator.io/v1alpha1
kind: NetBoxPrefix
metadata:
  name: prefix-servers
  namespace: homelab
spec:
  endpointRef: homelab
  prefix: 10.20.0.0/24
  scopeRef:                       # NetBox 4.2+ polymorphic scope
    kind: NetBoxSite
    name: home
  vrfRef: {name: vrf-home}
  tenantRef: {name: acme}
  isPool: false
---
apiVersion: netbox.populator.io/v1alpha1
kind: NetBoxIPAddressClaim         # "give me a free address from that prefix"
metadata:
  name: dns-eth0
  namespace: homelab
spec:
  endpointRef: homelab
  prefixRef: {name: prefix-servers}
  dnsName: dns.home.arpa
```

> **Status: pre-alpha, under active construction.** The API group is `v1alpha1` and
> is expected to change. Nothing here is released yet — see
> [the issue tracker](https://github.com/ricardomolendijk/netbox-operator/milestones)
> for what is landing and in what order.

## Why this exists

It is the successor to `netbox-populator`, a one-shot CLI that walked an
`inventory.yaml` and POSTed it into NetBox. The reconcile semantics carry over
verbatim — create, patch-on-drift, dependency ordering, dry run — but as a control
loop rather than a tree walk. Consequences:

- Drift made by a human in the NetBox UI gets corrected, not just drift on apply.
- Dependency ordering falls out of reference resolution instead of a hand-maintained
  table, so you can apply a graph in any order and watch it converge.
- Deletion is `kubectl delete`, with finalizers handling NetBox-side removal and
  `PROTECT` ordering.

## Design in one page

| Principle | What it means |
|---|---|
| **One CR = one NetBox object** | No composite "topology" kinds in the core API. This is what makes refs, ownership, cascade delete and drift all work with stock Kubernetes semantics. |
| **Refs, not IDs** | You never type a NetBox integer ID. `vlanRef: {name: vlan-mgmt}` points at a sibling CR; the resolver reads that CR's `.status.id`. |
| **Convenience is sugar** | A `NetBoxVirtualMachine` may declare interfaces and addresses inline; the controller materialises them as real child CRs with owner references. Nothing is hidden. |
| **The operator only touches what it owns** | Adoption of pre-existing NetBox objects is explicit and opt-in. There is no global prune. |
| **Spec omission means "don't manage"** | Only fields present in the spec are sent, so the operator co-exists with humans editing the same object. |
| **Never crash, never lie** | Every failure becomes a Condition, an Event and a backed-off requeue. `status.id` is set only once the object provably exists server-side. |

Longer form, one page each:

- [The Descriptor](docs/concepts/descriptor.md) — how one engine drives ~120 kinds with
  no per-kind code, and how an object's identity is established before it has an ID.
- [Drift detection](docs/concepts/drift.md) — why what NetBox returns is not what you
  wrote, and the comparison rules that keep a reconcile from PATCHing forever.
- [Lookups](docs/concepts/lookups.md) — how a natural key becomes a query string, and the
  two silent failures that come from getting it wrong.
- [Errors and retries](docs/concepts/errors-and-retries.md) — every NetBox failure as a
  typed error, what gets retried, and why an ambiguous lookup is never a silent choice.

Full index: [`docs/README.md`](docs/README.md). Decisions and their rationale:
[`docs/decisions/README.md`](docs/decisions/README.md). Running it, including what the
operator can read and the label every credential Secret needs:
[`docs/operations/rbac.md`](docs/operations/rbac.md).

## Target NetBox version

**NetBox 4.6.8.** Every CRD field is derived from the real Django models, not from
hand-reading the REST docs — see [`docs/netbox-schema.md`](docs/netbox-schema.md)
(generated: 159 models, 138 API endpoints) and
[`docs/regenerating.md`](docs/regenerating.md) to retarget a newer release.

## Supported kinds

`NetBoxEndpoint` is the connection; `NetBoxTag` is the first NetBox object to land, and
the one that proves the engine. The delivery order for the rest is deliberate: **the
logical model first** — tenancy, IPAM and virtualization — with physical plant (racks,
power, modules, cabling), circuits and VPN deliberately last.

| Group | Kinds | Status |
|---|---|---|
| Connection | [`NetBoxEndpoint`](docs/reference/netboxendpoint.md) | **Available** (M1) |
| `extras` | [`NetBoxTag`](docs/reference/netboxtag.md) | **Available** (M1) |
| `dcim` | [`NetBoxSite`](docs/reference/netboxsite.md) | **Available** (M1) |
| `tenancy` | `NetBoxTenantGroup`, `NetBoxTenant` | M3 |
| `ipam` | `NetBoxVRF`, `NetBoxRouteTarget`, `NetBoxVLAN`, `NetBoxVLANGroup`, `NetBoxPrefix`, `NetBoxIPAddress` | M3 |
| `virtualization` | `NetBoxClusterType`, `NetBoxClusterGroup`, `NetBoxCluster`, `NetBoxVirtualMachine`, `NetBoxVMInterface`, `NetBoxVirtualDisk` | M4 |
| `dcim` | `NetBoxManufacturer`, `NetBoxDeviceRole`, `NetBoxDeviceType`, `NetBoxPlatform`, `NetBoxDevice`, `NetBoxInterface` | M4 |
| Claims | `NetBoxIPAddressClaim`, `NetBoxPrefixClaim`, `NetBoxIPRangeClaim` | M6 |
| Physical plant, wireless, circuits, VPN | ~70 further kinds | M9–M10 |

## Relationship to `netbox-community/netbox-operator`

Upstream's operator is an **IPAM allocation** operator: six kinds
(`IpAddress`, `Prefix`, `IpRange` and their `*Claim` twins) on the `netbox.dev`
group, with no DCIM, no virtualization, no tenancy and no references between kinds.

This project is a **NetBox-wide provider**: the whole catalogue, with a real
reference system between kinds. It borrows upstream's best idea — the `*Claim` split,
where "allocate me one" is a separate kind with a separate lifecycle from "here is
the address I want" — and deliberately uses a different API group
(`netbox.populator.io`) so both CRD sets can be installed on one cluster.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md). Work is tracked as `NBO-nnn` issues;
branches are `nbo-<nnn>-<slug>`, one feature per pull request.

## Trademarks and affiliation

This project is **not affiliated with, endorsed by, or sponsored by NetBox Labs.** NetBox is
a trademark of NetBox Labs; this project claims no rights in it and uses the name only to
describe what it interoperates with.

One artefact deserves a specific note.
[`docs/netbox-schema.md`](docs/netbox-schema.md) is generated by walking the NetBox source's
Django model definitions, so unlike everything else here it is derived from NetBox's
**source** rather than its public API. It contains extracted field metadata — column names,
types, nullability, foreign-key targets and constraints — and **no NetBox code**. It exists
because deriving ~120 CRD schemas from hand-read REST documentation is how you get a field
list that is quietly wrong.

NetBox is licensed Apache 2.0, as is this project.

## License

[Apache 2.0](LICENSE).
