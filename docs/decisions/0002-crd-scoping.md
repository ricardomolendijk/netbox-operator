# 0002 — CRD scoping: everything is namespaced in v1alpha1

**Status:** Accepted · 2026-08-21
**Supersedes:** an earlier draft of this record that split the catalogue by scope. See
[Rejected alternative](#rejected-alternative-splitting-the-catalogue) below — it is kept
because the analysis is still the input to any future change.

## Context

`CustomResourceDefinition.spec.scope` is a single immutable value per CRD. Changing it
means deleting and recreating the CRD, **which deletes every object of that kind**.
So this is not a decision that can be deferred per kind and revisited casually — it can
only move at an API version boundary, with a documented migration.

## Decision

**Every kind in `v1alpha1` is namespaced.** Without exception:

- the tenant-data kinds (`NetBoxSite`, `NetBoxPrefix`, `NetBoxIPAddress`,
  `NetBoxDevice`, `NetBoxVirtualMachine`, …),
- the catalogue kinds (`NetBoxManufacturer`, `NetBoxDeviceType`, `NetBoxTag`,
  `NetBoxTenantGroup`, `NetBoxClusterType`, the `*Template` kinds, …),
- and the operator's own kinds: `NetBoxEndpoint` and `NetBoxSweep`.

There are no cluster-scoped CRDs. Cluster scope is **deferred, not rejected**; see
[Revisiting](#revisiting).

## Why

One scope for everything is the simplest thing that can work, and it makes several
hard problems disappear at once rather than requiring machinery to manage them:

1. **Owner-reference legality collapses to one rule.** Kubernetes garbage collection
   requires a cluster-scoped dependent to have a cluster-scoped owner; a namespaced
   owner on a cluster-scoped object is treated as absent and raises
   `OwnerRefInvalidNamespace`. With a single scope, the rule for
   [ADR-0003](0003-ownership-and-references.md) becomes **same namespace, or no owner
   reference** — and inline child materialisation never has to reason about a
   parent/child scope mismatch at all.
2. **No closure rule to compute or maintain.** The split version required a
   transitive-closure analysis over every required FK (`hack/classify-scope.py`) and
   produced non-obvious results — `Location` had to be demoted to namespaced because
   `Location.site` is required, the 10 `*Template` kinds had to be promoted because
   `DeviceType` was. That analysis has to be re-run and re-reviewed on every NetBox
   release. It is now unnecessary.
3. **Per-team RBAC is uniform.** A `RoleBinding` in a namespace grants full control of
   everything NetBox-related in that namespace. There is no second, cluster-scoped tier
   that needs `ClusterRole`s and a separate review.
4. **It is reversible in the direction that matters.** Promoting a kind from namespaced
   to cluster-scoped later is a decision we can make with real usage data. Shipping the
   split now and finding it wrong means destroying objects to fix it.

## Costs, accepted

These are real and should not be discovered later:

1. **Catalogues live in a namespace.** The convention is a shared namespace —
   `netbox-catalog` — holding `NetBoxManufacturer`, `NetBoxDeviceType`, `NetBoxTag` and
   friends. That namespace is somebody's responsibility, which is exactly the wart the
   split version was trying to avoid.
2. **Catalogue references become cross-namespace references.** This is the big one.
   Under the split, a namespaced object referring to a cluster-scoped catalogue crossed
   no namespace and needed no grant — that removed roughly 80% of the
   `NetBoxRefGrant` surface. That saving is gone: `deviceTypeRef`, `manufacturerRef`,
   `tags` and similar are now cross-namespace refs from a team namespace into the
   catalogue namespace, and each needs a grant. **`NetBoxRefGrant` (NBO-014) is
   therefore load-bearing everyday machinery, not an edge case**, and it must support a
   selector/wildcard form — "this catalogue namespace is readable by every namespace" as
   a single object — or the design will not survive contact with more than three teams.
3. **`NetBoxEndpoint` is per-namespace.** `endpointRef` resolves in the referring
   object's own namespace by default. A namespace that wants to reconcile against a
   shared NetBox creates its own `NetBoxEndpoint` pointing at the same URL and token
   secret. Cross-namespace `endpointRef` works, with a grant. This duplicates a small
   object per namespace; in exchange, each namespace can point at a different NetBox
   (lab / staging / prod) with no extra machinery.
4. **Name collisions are a routine failure mode.** Two namespaces can both create
   `NetBoxTenant/acme` or `NetBoxDeviceType/model=dcs-7050`, and NetBox's own
   `meta.constraints` will reject the second. The loser gets a `Conflict` condition
   naming the winner. Under the split this was confined to tenant data; now it applies
   to catalogues too, so conflict reporting has to be good.
5. **`NetBoxSweep` is namespace-bounded.** A sweep can only reclaim what it can prove
   is an orphan of its own namespace's endpoint, which makes the managed-by tag
   load-bearing and keeps dry-run as the default. This is a genuine safety improvement:
   the blast radius of the one feature that can delete data nobody asked it to is now
   bounded by a namespace.

## Rejected alternative: splitting the catalogue

The prior draft applied this rule:

> A kind may be cluster-scoped only if every required FK in its transitive closure is
> also cluster-scoped.

Computed from `models.json` by `hack/classify-scope.py`, it was closure-stable at **50
cluster-scoped catalogue kinds** and ~70 namespaced tenant-data kinds, with one demotion
(`Location`) and one forced promotion (the 10 `*Template` kinds). It also surfaced that
exactly three optional FKs point "downward" from a cluster-scoped kind into a namespaced
one, all the same field: `CircuitGroup.tenant`, `ASNRange.tenant`, `VLANGroup.tenant`.

It was rejected for `v1alpha1` as premature optimisation of an ergonomic problem we have
not felt yet, at the cost of complexity we would have to carry from the first commit.
`hack/classify-scope.py` stays in the tree, unwired, because its output is the input to
any future promotion. The numbers above were computed before NBO-071 corrected how that
script derives required-ness (an M2M is never required; `blank=True` does not make a
`NOT NULL` FK optional), so re-running it is expected to move some of them.

Also rejected: **duplicating every kind** cert-manager style
(`NetBoxSite` + `NetBoxClusterSite`). ~240 CRDs, and every ref field needs a
target-scope discriminator. Tekton deprecated `ClusterTask` for exactly this reason.

## Revisiting

On the `v1beta1` agenda (NBO-062) as a named open question, not a decision. The inputs
will be: how painful `NetBoxRefGrant` actually is in practice, whether anyone is running
more than one NetBox per cluster, and whether the catalogue namespace has become a
bottleneck. The M7 generator keeps `scope` as a per-kind IR attribute — currently forced
to `Namespaced`, overridable in `overrides.yaml` — so a promotion is a generator change
plus a new API version, not a redesign.

The `PROTECT` consequence noted under the old model survives unchanged and unrelated to
scope: a `NetBoxVLANGroup` holding a `tenant` blocks deletion of that
`NetBoxTenant`, and the tenant's `Deleting` condition must name the offender.
