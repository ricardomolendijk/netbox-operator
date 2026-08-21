# 0002 — CRD scoping: namespaced vs cluster-scoped

**Status:** Accepted · 2026-08-21

## Context

`CustomResourceDefinition.spec.scope` is a single immutable value per CRD. Changing
it means deleting and recreating the CRD, which deletes every object of that kind.
"Support both" is therefore not a thing you can add later — it has to be decided per
kind, up front.

Three options:

| | Approach | Cost |
|---|---|---|
| A | Duplicate every kind, cert-manager style (`NetBoxSite` + `NetBoxClusterSite`) | ~240 CRDs; every ref field needs a target-scope discriminator. Tekton deprecated `ClusterTask` for exactly this reason. |
| B | **Split the catalogue: each kind gets the one scope that fits it** | Needs a defensible line, but gets both scopes with no duplication. |
| C | Everything namespaced | Global catalogues have to live in some team's namespace, and every catalogue reference becomes a cross-namespace reference needing a grant. |

## Decision

**B**, with the line computed from the schema rather than chosen by taste:

> A kind may be cluster-scoped only if **every required foreign key in its transitive
> closure is also cluster-scoped.**

Everything else is namespaced. This is forced, not preferred: Kubernetes garbage
collection requires that a cluster-scoped dependent may only have a cluster-scoped
owner. A namespaced owner on a cluster-scoped object is treated as absent and raises
an `OwnerRefInvalidNamespace` event. The closure rule is what keeps the ownership
graph in [0003](0003-ownership-and-references.md) legal.

`hack/classify-scope.py` computes the split from `models.json`, so it is verifiable
and re-runnable against a new NetBox release rather than being a hand-maintained list.

## Result

Seeded with NetBox's catalogue-shaped models — everything based on
`OrganizationalModel`, `NestedGroupModel` or `TagBase`, plus the conceptual catalogues
NetBox happens to model as `PrimaryModel` because they carry description/comments
(`DeviceType`, `ModuleType`, `RackType`, `VirtualMachineType`, `ServiceTemplate`, the
VPN proposals and policies, the `extras` definitions) — the set is **closure-stable at
50 kinds with zero demotion rounds**. `NetBoxEndpoint` and `NetBoxSweep` are also
cluster-scoped. The remaining ~70 tenant-data kinds are namespaced.

Three findings worth keeping:

1. **`Location` is demoted to namespaced** despite being a `NestedGroupModel`, because
   `Location.site` is a required FK to `Site`, which is namespaced. A base-class
   heuristic gets this wrong; the closure rule catches it.
2. **The 10 `*Template` kinds must be cluster-scoped**, because `DeviceType` and
   `ModuleType` are and the templates are their owned inline children.
3. **Exactly three optional FKs point "downward"** from a cluster-scoped kind into a
   namespaced one, and they are all the same field: `CircuitGroup.tenant`,
   `ASNRange.tenant`, `VLANGroup.tenant` — all `null=True, on_delete=PROTECT`.

## The downward refs, and the failure mode they create

They stay, as namespace-qualified refs gated by `NetBoxRefGrant`. The coupling is
real and must be surfaced: because NetBox uses `PROTECT`, a cluster-scoped
`NetBoxVLANGroup` holding a `tenant` **blocks deletion of that namespaced
`NetBoxTenant`**. The tenant's `Deleting` condition must name the offending
cluster-scoped object, so a team can see why their delete is stuck and who to ask.
Left silent, this is the most confusing failure mode in the whole design.

## What this buys

- Catalogues stop living in an arbitrary team's namespace.
- A namespaced object referencing a cluster-scoped catalogue needs **no grant** —
  there is no namespace to cross. That removes roughly 80% of the grant surface,
  leaving grants covering namespaced→namespaced refs, which is where a capability
  check is actually wanted.
- Per-team RBAC still works, because everything tenant-owned stayed namespaced.
- CRD count stays near 120 rather than 240.

Residual footgun: two namespaces can both claim `NetBoxSite/slug=home`, and the
loser gets a `Conflict` condition. Accepted.

## Reversibility

Option A remains available per-kind and additively: the M7 generator makes emitting a
`Cluster*` twin mechanical — shared spec struct, two thin controllers, one extra
discriminator on `ObjectRef`. Recorded so this is a scheduling choice later, not a
rewrite.
