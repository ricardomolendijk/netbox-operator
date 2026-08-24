# Examples

Every example here is **runnable against the kinds that actually exist**. Nothing in
this directory is aspirational — if a manifest references a kind, that kind is
implemented and its CRD ships in `config/crd`.

Examples arrive with the milestone that makes them real:

| Example | Lands with |
|---|---|
| [`tag.yaml`](tag.yaml) — a credential, a connection and one NetBox object | **available** (M1, NBO-008) |
| [`site.yaml`](site.yaml) — a site, exercising a choice field and two decimals | **available** (M1, NBO-009) |
| `graph-any-order.yaml` — apply a dependency graph in reverse and watch it converge | M2 |
| `ipam-core.yaml` — tenant, VRF, VLAN, prefix, addresses | M3 |
| `vm-and-device.yaml` — clusters, VMs, devices, interfaces | M4 |
| `vm-inline.yaml` — one VM CR that materialises its interfaces and IPs | M5 |
| `claims.yaml` — allocate addresses and prefixes instead of hardcoding them | M6 |
| `homelab.yaml` — a full real-world topology, end to end | M9 |

Each one carries the `NetBoxEndpoint` and the credential Secret it needs, so it is a single
`kubectl apply` and not a prerequisite hunt. Edit the URL and the token first.

Run one with:

```sh
kubectl create namespace homelab
kubectl apply -f docs/examples/<name>.yaml
kubectl get nbep,nbtag -n homelab
```
