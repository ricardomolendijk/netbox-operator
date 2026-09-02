# Examples

Every example here is **runnable as it stands**. Each one carries the credential `Secret` and
the `NetBoxEndpoint` it needs, so it is a single `kubectl apply` and not a prerequisite hunt.
Edit the URL and the token first.

If this is your first one, [`site.yaml`](site.yaml) is the one the
[getting-started walkthrough](../getting-started.md) uses line by line.

```sh
kubectl apply -f docs/examples/site.yaml
kubectl get nbep,nbsite -n netbox-demo
```

Every kind ships a short name (`nbep`, `nbsite`, `nbtag`, …), and `kubectl api-resources
--api-group=netbox.kubeforge.org` lists all 69 of them with theirs.

## What is here

| Example | What it shows |
|---|---|
| [`tag.yaml`](tag.yaml) | The smallest complete thing: a credential, a connection and one NetBox object |
| [`site.yaml`](site.yaml) | A site, exercising a choice field and two decimals, plus a second site with nothing optional set |
| [`contacts.yaml`](contacts.yaml) | A contact group tree, two roles, one contact, and the same contact attached to a tenant, a site and an unmanaged prefix |
| [`extras.yaml`](extras.yaml) | NetBox's own configuration: a custom field, its choices, a link, a filter, two templates and a config context |
| [`racks.yaml`](racks.yaml) | A rack role, a flat rack group, a catalogue rack type, one rack in a location and one deliberately without, and a reservation on the first |
| [`ipam-remainder.yaml`](ipam-remainder.yaml) | The allocation registry, an address role, an FHRP group with its assignment, and services |
| [`cables.yaml`](cables.yaml) | A bundle, a patch lead, and a two-strand trunk landing on two ports at one end |
| [`circuits.yaml`](circuits.yaml) | A provider with an ASN, a billing account, a provider network, a circuit type and two circuits -- the catalogue half of the `circuits` app, with no terminations, because that kind is not shipped |

## What is not here yet

The kinds below all ship — every CRD in the catalogue is installed by `make install-crds`,
and the reference page for each is in [the index](../README.md#reference). What is missing is
the *worked example*, not the kind, so nothing here is blocked on the operator:

| Example | Would show |
|---|---|
| `graph-any-order.yaml` | Applying a dependency graph in reverse and watching it converge. The same thing is proved in [the e2e ordering gate](../operations/e2e.md) against a real NetBox |
| `ipam-core.yaml` | Tenant, VRF, VLAN, prefix, addresses in one file |
| `vm-and-device.yaml` | Clusters, VMs, devices, interfaces |
| `vm-inline.yaml` | One `NetBoxVirtualMachine` CR that materialises its interfaces and IPs as child CRs — see [inline children](../concepts/inline-children.md) |
| `claims.yaml` | Allocating addresses and prefixes instead of hardcoding them — see [claims](../concepts/claims.md) |
| `homelab.yaml` | A full real-world topology, end to end |
