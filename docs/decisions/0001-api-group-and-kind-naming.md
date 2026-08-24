# 0001 — API group and kind naming

**Status:** Accepted · 2026-08-21 · **domain amended 2026-08-24**

## Decision

- API group **`netbox.kubeforge.org`**, version `v1alpha1`.

> **Amended 2026-08-24 (issue #16).** The domain was originally `populator.io`, inherited
> from the `netbox-populator` CLI this operator succeeds and owned by nobody. It is now
> `kubeforge.org` — Kubeforge being a non-profit open-source organisation, with a possible
> CNCF donation later, so the group carries a domain that is actually owned.
>
> The reasoning below is unchanged and still load-bearing: the point was never the specific
> domain, it was **not** `netbox.dev` (which upstream uses, and which would make the two CRD
> sets mutually un-installable) and **not** unprefixed Kind names.
>
> Done early rather than at `v1beta1` (issue #19) for two reasons. The group appears in the
> `apiVersion` of every manifest a user ever writes, so the cost grows with adoption and with
> every doc page and example added. And the `Finalizer` key prefix moved with it — changing
> that key leaves any object already carrying the old one **undeletable**, because the
> controller looks for a finalizer it no longer recognises. Free while nothing is deployed;
> a support incident afterwards.
- Kind names are **prefixed with `NetBox`**: `NetBoxSite`, `NetBoxPrefix`,
  `NetBoxIPAddress`, `NetBoxVirtualMachine`.

The group string lives in exactly one place (`api/v1alpha1/groupversion_info.go`) if
it ever needs to change.

## Why not `netbox.dev`

`netbox.dev` is used by `netbox-community/netbox-operator`. Sharing the group would
make the two CRD sets mutually un-installable on one cluster — a CRD is keyed by
`<plural>.<group>`, so `prefixes.netbox.dev` can only have one definition. Using a
distinct group means someone already running upstream's IPAM operator can adopt this
one incrementally instead of migrating in a single cut.

## Why prefix the kinds

The unprefixed names collide with kinds people already have installed:

| Unprefixed | Collides with |
|---|---|
| `Service` | core `v1` `Service` — the worst case; `kubectl get service` silently resolves to core |
| `Cluster` | `cluster.x-k8s.io` (Cluster API), and several others |
| `Role` | `rbac.authorization.k8s.io` `Role` |
| `Interface`, `Tag`, `Provider`, `Group` | various; `Provider` is Crossplane's |

Kubernetes resolves a short name to the first match in discovery order, so an
unprefixed `Service` kind means every `kubectl get svc` in the cluster is a coin
flip away from confusion, and every script has to write
`services.netbox.kubeforge.org`. Prefixing costs verbosity once, in the type name,
and buys unambiguous short names everywhere else: `kubectl get netboxprefix`,
`kubectl get nbip`.

Short aliases are registered per kind (`nbip`, `nbprefix`, `nbvm`, `nbsite`) so the
verbosity does not reach the command line.

## Cost accepted

~120 kinds all carrying a 6-character prefix, some of them long
(`NetBoxConsolePortTemplate`). This is a real readability cost in the Go type names
and in `kubectl get crd` output. It is preferred over collisions, which are a
correctness problem rather than an aesthetic one.

## Divergence from the original plan

The pre-publication design used bare kind names (`Site`, `Prefix`, `IPAddress`) on
the theory that the API group already disambiguates. It does, for the API machinery
— but not for short-name discovery, which is what humans and scripts actually use.
Changed before any CRD shipped, so there is no migration.
