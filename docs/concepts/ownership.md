# Ownership: owner references and the cascade

Deleting a `NetBoxRegion` should take its sub-regions with it. Kubernetes already has the
mechanism for that — `metadata.ownerReferences` and the garbage collector — and this page is
about when the operator sets one, when it deliberately does not, and how you find out which
happened.

Implemented in `internal/reconciler/owners.go`. The decision is
[ADR-0003](../decisions/0003-ownership-and-references.md) rules 3 and 4.

## A foreign key is not an owner reference

They look alike and are not the same thing, and conflating them produces either an illegal
owner reference or a surprise cascade delete.

| | `parentRef` / `scopeRef` / typed refs | `ownerReferences` |
|---|---|---|
| Models | a NetBox foreign key | a Kubernetes lifecycle dependency |
| Written by | you, in `spec` | the operator, in `metadata` |
| Effect | resolved to a NetBox id at reconcile time | garbage collection, cascade delete |
| Legal across namespaces | yes, with a [`NetBoxRefGrant`](../reference/netboxrefgrant.md) | **no**, ever |

That last row is the whole of this page's complexity. Everything else follows from it.

## The two owner references

**A controller owner reference, on a child the operator created.** When a CR exists
*because* another CR does — a VM's inline interfaces, the `NetBoxIPAddress` a claim
materialised — the child gets `controller: true, blockOwnerDeletion: true` pointing at its
creator. Those are always in one namespace, because the operator puts them there, so this
reference is always legal. Nothing in this build creates children yet; the materialiser is
NBO-032.

**A non-controller owner reference, on an object whose containment parent is a CR in the
same namespace.** Each kind nominates exactly one containment parent, declared as
`ContainmentRef` on its [descriptor](descriptor.md), and the operator adds a
*non-controller* owner reference to whatever that reference resolved to. Non-controller
because an object has at most one controller and it belongs to whatever created the object;
garbage collection counts a non-controller owner exactly the same, so the cascade works
either way.

`NetBoxRegion.parentRef` is the containment parent. It is not an aesthetic choice:
`dcim.Region.parent` is `on_delete=CASCADE` in NetBox, so deleting a region deletes its
descendants server-side. Without the owner reference the child CR outlives the row it
described, finds nothing at `status.id` next reconcile, and the engine's create-if-absent
step **recreates the region NetBox deliberately deleted**. The general rule is *a
server-side cascade implies an owner reference*.

Only one containment reference per kind, ever. Garbage collection deletes a dependent once
*every* owner is gone, so two containment owners would silently mean "survives until both
parents are deleted" while the manifest reads like "delete either one".

## Which reference is the parent: whichever the server cascades

The containment parent is not the reference that reads most like a container. It is the one
NetBox itself deletes through, and the descriptor says which that is — `CascadeOnDelete` on the
field, taken straight from `on_delete` in `docs/netbox-schema.md`. `Validate` refuses a
`ContainmentRef` whose foreign key does not cascade, so this is a boot failure rather than a
convention somebody can forget.

| Kind | Parent | Because |
|---|---|---|
| `NetBoxRegion` | `parentRef` | `parent` is `CASCADE`, and it is the only reference |
| `NetBoxSiteGroup` | `parentRef` | same model, same `CASCADE` |
| `NetBoxLocation` | `siteRef` | `site` **and** `parent` are both `CASCADE`; `site` is the required one |
| `NetBoxPrefix` | `scopeRef` | every scope target deletes its prefixes; `vrf` is `PROTECT` |
| `NetBoxVLANGroup` | `scopeRef` | same, through `vlan_groups` on all four targets |
| `NetBoxCluster` | `scopeRef` | every scope target deletes its clusters, by two different mechanisms — see below |
| `NetBoxDevice` | *none* | every reference on it is `PROTECT` or `SET_NULL` |

`NetBoxLocation` is the only kind so far where two references qualify and there is one slot, so
it needs a tiebreak. `siteRef` wins on three counts: `site` is required, so every location has
one, where a containment parent on the optional `parentRef` would leave every top-level
location with no owner reference at all; deleting a site cascades to *every* location in it,
nested ones included, so it is the larger deletion; and the parent path is covered by identity
rather than by ownership — every one of this kind's natural-key candidates reads `parent_id` or
pins it null, so a location whose `parentRef` stops resolving has no usable identity and the
engine waits instead of creating. Choosing `parentRef` would trade all three away.

### On a polymorphic reference, the cascade is per member

`scopeRef` is one field with four legal targets, and **the cascade is a fact about each
target, not about the field.** NetBox states it per target model, in one of two places:

- a `GenericRelation` on the target — `dcim.Region` declares `clusters`, so deleting a region
  deletes the clusters scoped to it;
- or a denormalised `on_delete=CASCADE` column on the *referring* model —
  `dcim.CachedScopeMixin` gives `ipam.Prefix`, `virtualization.Cluster` and
  `wireless.WirelessLAN` a `_site` and a `_location` that are `CASCADE` (and a `_region` and
  `_site_group` that are `SET_NULL`), maintained from `(scope_type, scope_id)` on every save.

The two are complementary, which is why `clusters` is a `GenericRelation` on `dcim.Region` and
`dcim.SiteGroup` and not on `dcim.Site` or `dcim.Location`: the `SET_NULL` targets need it to
cascade at all, the `CASCADE` ones do not. Read together, every scoped kind cascades from all
four of its scopes — and each member says so for itself, with its own citation:

```go
GenericFKs: []GenericFKSpec{ScopeFK("scope", ScopeCascadesFromEvery())},
ContainmentRef: "scope",
```

So the descriptor's boot check is *some* member cascades, and **the owner reference is decided
per object, from the member that object resolved through.** A kind whose members disagree
keeps its containment parent for the members that cascade, and the objects using the others
report `False/CascadeUnavailable` naming the member:

```
ParentOwned  False  CascadeUnavailable  scope resolved to NetBoxSite team-blue/hq, and netbox
                                        does not delete this object when that is deleted, so
                                        no owner reference was added […] Another member of
                                        scope may cascade — the cascade of a polymorphic
                                        reference is declared per target model
```

**Move an object between members and the owner reference moves with it**, in one pass: the
entry for the member it left is removed as the new one is added. Moving to a member that does
not cascade removes it and adds nothing. A stale entry would be worse than none — two
containment owners are ANDed by garbage collection, and an entry naming an object this one no
longer references is a cascade waiting to delete the wrong thing.

`ScopeFK` cannot default those flags. Their value belongs to the referring kind, and the two
mechanisms behind them live in two different places in `docs/netbox-schema.md`, so a caller
supplies the table. Supplying it for *some* members and not others is a boot failure
(`ErrMemberCascadePartial`) rather than a member that silently does not cascade: that silence
is the direction that hurts, since a member wrongly reading "no cascade" leaves the CR behind
when NetBox deletes the row, and the engine then recreates the row from the CR.

### When no foreign key qualifies

**A kind whose only plausible parent is a `PROTECT` or `SET_NULL` foreign key gets no owner
reference and no cascade.** `NetBoxDevice` is the example, and it is not a small one:
`dcim.Device.site` reads exactly like a device's container and is `on_delete=PROTECT`. So
`kubectl delete netboxsite hq` does not remove the `NetBoxDevice` CRs in that site, and those
objects carry no `ParentOwned` condition at all — the same *absent* row as a catalogue kind,
because from the mechanism's point of view they are the same case: no containment parent.

This is a stated consequence rather than a gap, and the argument is that the alternative is
worse. NetBox **refuses** to delete a site that still has devices (`PROTECT` becomes
`Deleting=False, Reason=Protected` — see [deletion](deletion.md)). An owner reference there
would promise a cascade the server will not perform: garbage collection would delete the device
CR, its finalizer's `DELETE` would be rejected, and the row would outlive the object that
described it. A `SET_NULL` foreign key is the same mistake in the other direction — the row
survives with the column cleared, and the CR that described it has been deleted.

So the practical answer for those kinds is `kubectl delete` on the objects themselves, or a
label selector, in the order NetBox's own constraints allow. There is nothing for the operator
to do that would be safe, and saying so here is the whole of the mitigation.

## The namespace rule

**An owner reference is only legal inside one namespace.** Every kind is namespaced
([ADR-0002](../decisions/0002-crd-scoping.md)), so that is the entire legality test: same
namespace, or no owner reference.

This is not the operator being cautious. Kubernetes garbage collection does not resolve
across namespaces — it reads a cross-namespace owner as an owner that *does not exist*, and
deletes the dependent immediately. So a cross-namespace owner reference would not give you a
weaker cascade; it would delete your object.

A `NetBoxRefGrant` does not help here and cannot. A grant authorises the **reference** —
whether one namespace may read an object in another. Nothing authorises the owner reference,
because the problem is not permission, it is that the garbage collector has no way to follow
the link.

The consequence is the sharp edge of this design, and it is worth stating plainly: **the same
manifest cascades or does not depending on where the objects live.**

```yaml
# team-blue/prefix.yaml — cascades. Deleting the site deletes this.
kind: NetBoxPrefix
metadata: {namespace: team-blue}
spec:
  scopeRef: {siteRef: {name: hq}}        # NetBoxSite hq is in team-blue

---
# team-blue/prefix.yaml — does NOT cascade. Deleting the site leaves this behind.
kind: NetBoxPrefix
metadata: {namespace: team-blue}
spec:
  scopeRef: {siteRef: {name: hq, namespace: netbox-catalogue}}
```

Pointing a team namespace at a shared catalogue namespace is the *ordinary* shape for this
operator, not an edge case — so the second form is common, and it never cascades.

## How you find out

The `ParentOwned` condition, on the object itself. It is set on any object whose kind
declares a containment ref and whose spec sets it.

```console
$ kubectl describe netboxregion child -n team-blue
Conditions:
  Type          Status  Reason               Message
  Ready         True    Synced               netbox dcim/regions/41 matches the spec
  ParentOwned   False   CascadeUnavailable   parentRef points at NetBoxRegion
                                             netbox-catalogue/emea and an owner reference may
                                             not cross a namespace, so deleting it will not
                                             delete this object in namespace team-blue. Put
                                             the two in one namespace to get the cascade, or
                                             delete this object explicitly
```

| `ParentOwned` | Reason | What it means |
|---|---|---|
| `True` | `ParentOwned` | The owner reference is set. Deleting the parent garbage-collects this object. |
| `False` | `CascadeUnavailable` | No owner reference is possible, or the union member this object resolved through is not one netbox cascades. Deleting the parent leaves this object behind. |
| `False` | `ParentOwnershipDisabled` | You opted out with the annotation below. |
| *absent* | — | This kind has no containment parent, or the spec did not set it. Nothing to cascade. |

A condition and not an Event, deliberately. This state does not change until somebody moves
an object between namespaces or rewrites a reference, so it is standing rather than eventful
— and an Event ages out of the namespace within the hour, long before the deletion that
would otherwise be how you discovered it.

`ParentOwned` never affects `Ready`. A missing cascade is a statement about deletion; an
object whose NetBox counterpart matches its spec is Ready regardless.

### Two things that are not the namespace rule

`CascadeUnavailable` also covers two cases that have nothing to do with namespaces. The
containment reference may name a NetBox row rather than a CR — written as `slug`, `lookup`, or
the raw `id` escape hatch for a pre-existing object the operator does not manage — and then
there is no Kubernetes object for an owner reference to point at. Or it may be a polymorphic
reference resolved through a member NetBox does not cascade from, while a sibling member does
([above](#on-a-polymorphic-reference-the-cascade-is-per-member)). The message says which it
was.

**A containment reference that has not resolved gets no `ParentOwned` condition at all.**
That case is already `RefsResolved=False` naming itself, and reporting one fact under two
conditions invites them to disagree. Look at `RefsResolved` first; `ParentOwned` is only
about references that *did* resolve. The owner reference of a *previous* pass is still removed
while the reference does not resolve: the operator cannot tell "the ref was deleted" from "the
ref is not ready yet", and of the two answers only removal is safe — an object with no owner
reference is never collected, so a cascade the next pass restores costs nothing, while a
promise about a parent this object may no longer reference can delete it.

### An unregistered target Kind

A containment parent can be a member of a generic-FK union — `scopeRef` can name a
`NetBoxSiteGroup` or a `NetBoxLocation`, neither of which exists yet. Nothing special
happens: the resolver already refuses such a member with `RefsResolved=False,
Reason=RefKindUnavailable`, so the reference never resolves, so there is nothing for this
step to own and no `ParentOwned` condition. When the Kind lands, its objects start being
owned with no change here — the mechanism reads `ContainmentRef` off the descriptor and knows
nothing about any Kind.

## Opting out

```yaml
metadata:
  annotations:
    netbox.kubeforge.org/parent-ownership: "false"
```

No owner reference is added, and the object reports `ParentOwned=False,
Reason=ParentOwnershipDisabled`. Use it for an object that should outlive its parent.

Only the exact string `"false"` opts out; anything else, including a typo and the annotation
being absent, leaves the default on. The default is on because "delete the site, its prefixes
go too" is what people expect, and the alternative is silent orphans in NetBox.

There is no endpoint-wide switch. The objects that want to outlive their parent are
individual ones, and an endpoint-level field would be a third deletion knob beside
`deletionPolicy` and `onConflict`.

## What the operator will not do to your object

**It never rewrites an owner reference, and never removes one outside the containment slot.**
The slot is a *non-controller* owner reference naming one of the Kinds `ContainmentRef`
resolves to: the operator's own, and the one it would have written itself. Everything else is
left alone by construction rather than by a check somebody could forget — an owner reference
set by another controller names another Kind, or is a controller reference, and either way this
step cannot see it.

Inside the slot it does remove, because the alternative is worse. The reference the slot holds
can change — a `scopeRef` moved from a region to a site, a parent moved namespace, a ref
cleared, the opt-out annotation added — and an entry nobody removes then names an object this
one no longer references. Garbage collection reads that as a live owner: deleting that former
parent deletes this object, whose finalizer deletes a NetBox row that was never in its scope.
Two entries at once are worse still, since owners are ANDed.

Adding and removing are both idempotent: an object already in the right state takes no write at
all, which is what keeps a resync from patching every object in the cluster every ten minutes.

**It never downgrades a controller reference.** If a controller owner reference already names
the same parent — the case where child materialisation and containment point at the same
object — the two dedupe to *one*, and the one they dedupe to is the controller reference. It
is left exactly as it is. (This is why the implementation does not use
`controllerutil.SetOwnerReference`: that helper upserts, and would strip `controller: true`
off the entry.)

**It refreshes a stale uid.** If the parent is deleted and recreated under the same name, the
uid changes. Only the uid is rewritten — the `controller` and `blockOwnerDeletion` flags are
left alone. A stale uid is worse than no owner reference: the garbage collector reads an owner
it cannot resolve as an owner that is gone.

**An owner reference is `metadata`, never `spec`.** It is written as a merge patch scoped to
`metadata.ownerReferences`, so [ADR-0005 §1](../decisions/0005-gitops-coexistence.md)'s
never-write-spec invariant is untouched. The operator's field manager *does* claim
`f:metadata.ownerReferences`, deliberately: a GitOps tool that saw the field unowned would
prune it on the next sync. See [field ownership](field-ownership.md).

`blockOwnerDeletion` is **not** set on a containment reference. It only bites under
foreground deletion, where it would let a hand-written object hold up
`kubectl delete --cascade=foreground` on a shared parent — and it brings RBAC with it, since
setting it requires `update` on the owner's `finalizers` subresource wherever the
`OwnerReferencesPermissionEnforcement` admission plugin is on. A controller reference earns
that flag by having created the child; a containment reference has not.

## Adding a kind

Nothing. Name the containment field on the kind's descriptor and it is done:

```go
ContainmentRef: "siteRef",
```

Validated at boot: it must be a spec field this descriptor declares, it must be a reference,
it must not be to-many, and **its foreign key must cascade** — `CascadeOnDelete` on the field,
which is `on_delete=CASCADE` in NetBox:

```go
{Spec: "parentRef", API: "parent", Class: ClassRefOne, CascadeOnDelete: true},
```

On a polymorphic pair the cascade is stated per union member instead, and the boot check is
that **some** member cascades — the per-object question is asked once the reference resolves:

```go
GenericFKs: []GenericFKSpec{ScopeFK("scope", ScopeCascadesFromEvery())},
```

Getting either wrong is a boot failure (`ErrContainmentNotCascade`, or
`ErrMemberCascadePartial` for a union whose members are stated unevenly) rather than a
cascade that does the wrong thing in production. There is no per-kind ownership code and no
`switch` on Kind anywhere in the mechanism — which is enforced, not merely intended
(`forbidigo`).

Leave it empty for a kind with no containment parent. Every catalogue kind does: a catalogue
is not a parent, and those references usually cross namespaces anyway, where an owner
reference is illegal. So does any kind whose references are all `PROTECT` or `SET_NULL` —
see [when no foreign key qualifies](#when-no-foreign-key-qualifies).

## See also

- [ADR-0003 — Ownership and references](../decisions/0003-ownership-and-references.md)
- [Deletion](deletion.md) — `deletionPolicy`, the finalizer, and `PROTECT`-blocked deletes
- [References](references.md) — how a `parentRef` becomes a NetBox id
- [Descriptor](descriptor.md) — where `ContainmentRef` is declared
- [Field ownership](field-ownership.md) — why the operator claims the fields it writes
