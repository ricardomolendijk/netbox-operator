# netbox-operator

A Kubernetes operator that turns [NetBox](https://netbox.dev) into a declarative,
continuously reconciled resource. Every NetBox object is a Custom Resource, every
NetBox foreign key is a Kubernetes reference, and `kubectl apply` / `kubectl delete`
are the only verbs you need.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
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
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPAddressClaim         # "give me a free address from that prefix"
metadata:
  name: dns-eth0
  namespace: homelab
spec:
  endpointRef: homelab
  prefixRef: {name: prefix-servers}
  dnsName: dns.home.arpa
```

**New here? [Start with the ten-minute walkthrough](docs/getting-started.md)** — CRDs, chart,
one Secret, one `NetBoxEndpoint`, one `NetBoxSite`, and the object visible in NetBox.

> **Status: pre-alpha, under active construction.** The API group is `v1alpha1` and is
> expected to change. All 64 CRDs ship, and `v0.0.9` is the first release to publish an
> installable chart, CRD bundle and signed image — every tag before it carries no assets, so
> pin the version rather than taking the newest tag. See
> [`docs/install.md`](docs/install.md#what-is-published).

## Why this exists

It is the successor to `netbox-populator`, a one-shot CLI that walked an
`inventory.yaml` and POSTed it into NetBox. The reconcile semantics carry over
verbatim — create, patch-on-drift, dependency ordering, dry run — but as a control
loop rather than a tree walk. Consequences:

- Drift made by a human in the NetBox UI gets corrected, not just drift on apply.
- Dependency ordering falls out of reference resolution instead of a hand-maintained
  table, so you can apply a graph in any order and watch it converge — proved against a real
  NetBox in [the e2e ordering gate](docs/operations/e2e.md), which applies the same graph
  forwards, backwards and in twenty seeded random orders and requires an identical end state.
- Deletion is `kubectl delete`, with finalizers handling NetBox-side removal and
  `PROTECT` ordering.

## Design in one page

| Principle | What it means |
|---|---|
| **One CR = one NetBox object** | No composite "topology" kinds in the core API. This is what makes refs, ownership, cascade delete and drift all work with stock Kubernetes semantics. |
| **Refs, not IDs** | You never type a NetBox integer ID. `vlanRef: {name: vlan-mgmt}` points at a sibling CR; the resolver reads that CR's `.status.id`. |
| **Convenience is sugar** | A `NetBoxVirtualMachine` may declare interfaces and addresses inline; the controller materialises them as real child CRs with owner references. Nothing is hidden. |
| **The operator only touches what it owns** | Adoption of pre-existing NetBox objects is explicit and opt-in. There is no global prune. |
| **Spec omission means "don't manage"** | A field you never set is left as NetBox has it, so the operator co-exists with humans editing the same object. A field you set to empty *is* sent, and clears NetBox's value: absent, empty and set are three states, and the operator tells them apart from `metadata.managedFields` rather than from the Go value. See [field ownership](docs/concepts/field-ownership.md). |
| **Never crash, never lie** | Every failure becomes a Condition, an Event and a backed-off requeue. `status.id` is set only once the object provably exists server-side. |

Longer form, one page each:

- [The Descriptor](docs/concepts/descriptor.md) — how one engine drives the whole catalogue
  with no per-kind code, and how an object's identity is established before it has an ID.
- [Drift detection](docs/concepts/drift.md) — why what NetBox returns is not what you
  wrote, and the comparison rules that keep a reconcile from PATCHing forever.
- [Lookups](docs/concepts/lookups.md) — how a natural key becomes a query string, and the
  two silent failures that come from getting it wrong.
- [Errors and retries](docs/concepts/errors-and-retries.md) — every NetBox failure as a
  typed error, what gets retried, and why an ambiguous lookup is never a silent choice.
- [Coexisting with Flux and Argo CD](docs/operations/gitops.md) — why the operator never
  writes a `spec`, how that is enforced rather than intended, and what the three
  `driftMode` values each do.
- [Provenance](docs/operations/provenance.md) — the tag and custom fields the operator
  stamps onto every object it manages, how the definitions get created, and how to turn the
  whole thing off.
- [Sweeps](docs/operations/sweeps.md) — what this cluster has left behind in NetBox, why the
  answer is a report and never a deletion, and why a sweep only ever considers objects
  stamped with its own cluster id.

Full index: [`docs/README.md`](docs/README.md). When something goes wrong:
[`docs/troubleshooting.md`](docs/troubleshooting.md), which lists every condition reason the
operator emits. Decisions and their rationale:
[`docs/decisions/README.md`](docs/decisions/README.md). What the operator can read and the
label every credential Secret needs: [`docs/operations/rbac.md`](docs/operations/rbac.md).

## Installing

```sh
# The CRDs are their own artefact, not part of the Helm release.
kubectl apply --server-side --force-conflicts \
  -f https://github.com/ricardomolendijk/netbox-operator/releases/download/v0.0.9/netbox-operator-crds-0.0.9.yaml

kubectl create namespace homelab

helm install netbox-operator oci://ghcr.io/ricardomolendijk/charts/netbox-operator \
  --version 0.0.9 \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={homelab}
```

[Getting started](docs/getting-started.md) walks the whole path including the first object;
[`docs/install.md`](docs/install.md) has every value, both upgrade commands, and the one thing
to know before you upgrade — the CRDs are applied by you, before the chart, every time. The
chart values that change GitOps and drift behaviour are documented alongside the behaviour, in
[`docs/operations/gitops.md`](docs/operations/gitops.md#chart-values).

## Target NetBox version

**NetBox 4.6.8.** Every CRD field is derived from the real Django models, not from
hand-reading the REST docs — see [`docs/netbox-schema.md`](docs/netbox-schema.md)
(generated: 159 models, 138 API endpoints) and
[`docs/regenerating.md`](docs/regenerating.md) to retarget a newer release.
What of those 138 endpoints is implemented, deliberately excluded or still missing is
audited on every run of the test suite and written to
[`docs/coverage.md`](docs/coverage.md).

## Supported kinds

**64 CRDs ship today**, and the catalogue is no longer delivered a milestone at a time:
61 of them are NetBox objects driven by the same engine, and three are not NetBox objects at
all — [`NetBoxEndpoint`](docs/reference/netboxendpoint.md) is the connection,
[`NetBoxRefGrant`](docs/reference/netboxrefgrant.md) authorises references between
namespaces, and [`NetBoxSweep`](docs/reference/netboxsweep.md) reports what this cluster has
left behind in NetBox.

| Group | Kinds |
|---|---|
| Connection and authorisation | `NetBoxEndpoint`, `NetBoxRefGrant`, `NetBoxSweep` |
| `dcim`, sites and locations | `NetBoxRegion`, `NetBoxSiteGroup`, `NetBoxSite`, `NetBoxLocation` |
| `dcim`, physical plant | `NetBoxRackRole`, `NetBoxRackType`, `NetBoxRackGroup`, `NetBoxRack`, `NetBoxRackReservation`, `NetBoxCable`, `NetBoxCableBundle` |
| `dcim`, devices | `NetBoxManufacturer`, `NetBoxDeviceRole`, `NetBoxDeviceType`, `NetBoxPlatform`, `NetBoxDevice`, `NetBoxInterface`, `NetBoxMACAddress` |
| `tenancy` | `NetBoxTenantGroup`, `NetBoxTenant`, `NetBoxContactGroup`, `NetBoxContactRole`, `NetBoxContact`, `NetBoxContactAssignment` |
| `ipam` | `NetBoxVRF`, `NetBoxRouteTarget`, `NetBoxVLANGroup`, `NetBoxVLAN`, `NetBoxPrefix`, `NetBoxIPRange`, `NetBoxIPAddress`, `NetBoxRIR`, `NetBoxAggregate`, `NetBoxASN`, `NetBoxASNRange`, `NetBoxRole`, `NetBoxFHRPGroup`, `NetBoxFHRPGroupAssignment`, `NetBoxService`, `NetBoxServiceTemplate` |
| Claims | `NetBoxIPAddressClaim`, `NetBoxPrefixClaim`, `NetBoxIPRangeClaim` |
| `virtualization` | `NetBoxClusterType`, `NetBoxClusterGroup`, `NetBoxCluster`, `NetBoxVirtualMachine`, `NetBoxVMInterface`, `NetBoxVirtualDisk` |
| `wireless` | `NetBoxWirelessLANGroup`, `NetBoxWirelessLAN`, `NetBoxWirelessLink` |
| `extras` | `NetBoxTag`, `NetBoxCustomField`, `NetBoxCustomFieldChoiceSet`, `NetBoxCustomLink`, `NetBoxSavedFilter`, `NetBoxExportTemplate`, `NetBoxConfigTemplate`, `NetBoxConfigContextProfile`, `NetBoxConfigContext` |

Every one of them has a reference page — the index is
[`docs/README.md`](docs/README.md#reference). What of NetBox's 138 REST endpoints that
leaves implemented, deliberately excluded or still missing is audited on every run of the
test suite and written to [`docs/coverage.md`](docs/coverage.md); circuits, power, modules
and VPN are the largest remaining gaps.

## Migrating an existing NetBox

`nbctl export` reads a live NetBox and writes CR manifests for a human to review and
commit:

```sh
NETBOX_TOKEN=... go run ./cmd/nbctl export \
  --url https://netbox.example.com --endpoint homelab -n homelab -o manifests/
```

It writes files, and only files. It never writes to NetBox, to a cluster, or to Git --
Git stays authoritative because a person puts the export there. See
[exporting a live NetBox](docs/operations/exporting.md).

## Relationship to `netbox-community/netbox-operator`

Upstream's operator is an **IPAM allocation** operator: six kinds
(`IpAddress`, `Prefix`, `IpRange` and their `*Claim` twins) on the `netbox.dev`
group, with no DCIM, no virtualization, no tenancy and no references between kinds.

This project is a **NetBox-wide provider**: the whole catalogue, with a real
reference system between kinds. It borrows upstream's best idea — the `*Claim` split,
where "allocate me one" is a separate kind with a separate lifecycle from "here is
the address I want" — and deliberately uses a different API group
(`netbox.kubeforge.org`) so both CRD sets can be installed on one cluster.

## Contributing

See [`CONTRIBUTING.md`](CONTRIBUTING.md). Work is tracked as
[GitHub issues](https://github.com/ricardomolendijk/netbox-operator/issues), one feature per
pull request.

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
