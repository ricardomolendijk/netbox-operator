# 0003 — Ownership and references

**Status:** Accepted · 2026-08-21

## The two different relationships

NetBox foreign keys and Kubernetes owner references look similar and are not the same
thing. Conflating them produces either illegal owner references or surprise cascade
deletes. So they are kept distinct and both are used, for different jobs.

| | `parentRef` / typed refs | `ownerReferences` |
|---|---|---|
| Models | a NetBox foreign key | a Kubernetes lifecycle dependency |
| Written by | the user, in `spec` | the operator, in `metadata` |
| Effect | resolved to a NetBox ID at reconcile time | garbage collection, cascade delete |
| Legal across namespaces | yes, with a `NetBoxRefGrant` | **no** |

## Decision

**1. Every NetBox FK is expressed as a ref in `spec`, never as an integer ID.**

```yaml
spec:
  siteRef: {name: home}            # -> resolves to that CR's .status.id
  vrfRef: {name: vrf-home}
  tenantRef: {namespace: team-a, name: acme}   # cross-namespace, needs a grant
```

`spec.<x>Ref.id` exists as an escape hatch for pre-existing NetBox objects the
operator does not manage. It is the only place a raw ID is accepted.

**2. Polymorphic FKs get a `scopeRef` / generic-FK union, not a `siteRef`.**

Since NetBox 4.2, `Prefix`, `Cluster`, `WirelessLAN` and `VLANGroup` are scoped via
`CachedScopeMixin` — `scope_type` + `scope_id`. `site` is a read-only cached column
(`_site`) and writing it **silently no-ops**. Drift is keyed on
`(scope_type, scope_id)`, never on the cached column. `netbox-populator` gets this
wrong in both directions; it is the single most important bug not to inherit.

**3. The operator sets a controller owner reference on children it creates.**

When a CR exists *because* another CR does — a `NetBoxVirtualMachine`'s inline
interfaces and addresses, or the `NetBoxIPAddress` materialised by a
`NetBoxIPAddressClaim` — the child gets
`ownerReferences[0] = {controller: true, blockOwnerDeletion: true}` pointing at its
creator. `kubectl delete` on the parent then cascades natively: Kubernetes GC removes
the child CRs, their finalizers remove the NetBox objects, and the parent's finalizer
completes last.

**4. A `parentRef` additionally sets a *non-controller* owner reference — when that is legal.**

So that `kubectl delete netboxsite home` also removes the prefixes scoped to it, a
ref that is genuinely a parent-child containment relationship (`scopeRef`,
`siteRef`, `clusterRef`, `deviceRef`, `vrfRef`, `prefixRef`) contributes a
**non-controller** owner reference to the referring object. Non-controller so it never
competes with rule 3; GC still counts it, so the cascade works.

It is added **only when the reference is legal as an owner reference**:

- same namespace, or
- a namespaced dependent with a cluster-scoped owner.

It is skipped, with a `CascadeUnavailable` condition explaining why, when:

- the ref crosses namespaces, or
- the owner is namespaced and the dependent is cluster-scoped (illegal; would raise
  `OwnerRefInvalidNamespace`), or
- the ref points at an unmanaged NetBox object by `id`.

Guard clause, not a best effort: the operator either sets a legal owner reference or
says out loud that deleting the parent will not clean up this child.

Opt out per object with `netbox.populator.io/parent-ownership: "false"`, or per
endpoint with `spec.parentOwnership: false`. Default is on, because "delete the site,
its prefixes go too" is the behaviour people expect and the alternative is silent
orphans in NetBox.

## Why not make the parent ref the *controller* reference

An object can have only one controller reference. Giving it to `parentRef` would take
it away from inline-child materialisation, which needs it to know which children it
owns and may prune. Materialisation is the stronger claim — it created the object —
so it wins, and containment settles for a non-controller reference that still drives GC.

## Deletion policy

Independently of ownership, each object carries
`spec.deletionPolicy: Delete | Retain`. `Delete` (the default) removes the NetBox
object when the CR goes away. `Retain` drops the finalizer and leaves NetBox alone —
for migrating off the operator, or for objects that are shared with something else.

A `PROTECT`-ed deletion is not an error to retry quickly: it becomes a
`Deleting=False, Reason=Protected` condition naming the objects that block it, and a
backed-off requeue.
