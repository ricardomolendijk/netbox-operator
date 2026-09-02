# 0003 — Ownership and references

**Status:** Accepted · 2026-08-21
**Amended:** 2026-08-24 — rule 5 added, settling whether inline child sugar belongs in
`v1alpha1` at all ([#17](https://github.com/ricardomolendijk/netbox-operator/issues/17)).
**Amended:** 2026-08-24 — rules 3 and 4 settled as the answer to *which* relationships get an
owner reference, and what cross-namespace containment therefore gives up
([#175](https://github.com/ricardomolendijk/netbox-operator/issues/175)). The
[deletion policy](#deletion-policy) default is now per Kind
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)).
**Amended:** 2026-08-24 — rule 4 built, and its mechanism recorded below: the condition it
reports, and the parts of the prose that are deliberately not built
([#175](https://github.com/ricardomolendijk/netbox-operator/issues/175)).
**Amended:** 2026-08-24 — rule 4's cascade check moved **per union member**, and the premise of
the limitation recorded below corrected: `GenericRelation` is not a generic FK's only cascade
path, so `virtualization.Cluster` regains the containment parent it shipped without
([#214](https://github.com/ricardomolendijk/netbox-operator/issues/214)). The owner reference
is now decided from the *resolved* member and **removed** when an object moves to a member that
does not cascade.
**Amended:** 2026-08-24 — rule 4 rewritten: **one** containment parent per Kind, chosen by
whether the server cascades rather than by which reference reads like a container, with the
garbage-collection reason it has to be one
([#193](https://github.com/ricardomolendijk/netbox-operator/issues/193),
[#198](https://github.com/ricardomolendijk/netbox-operator/issues/198)). The prose used to name
two references for `NetBoxPrefix` and a `PROTECT`-ed foreign key for `NetBoxDevice`; both are
corrected below, and `Descriptor.CascadeOnDelete` now makes the rule a boot check.

## The two different relationships

NetBox foreign keys and Kubernetes owner references look similar and are not the same
thing. Conflating them produces either illegal owner references or surprise cascade
deletes. So they are kept distinct and both are used, for different jobs.

| | `parentRef` / typed refs | `ownerReferences` |
|---|---|---|
| Models | a NetBox foreign key | a Kubernetes lifecycle dependency |
| Written by | the user, in `spec` | the operator, in `metadata` |
| Effect | resolved to a NetBox ID at reconcile time | garbage collection, cascade delete |
| Legal across namespaces | yes, with a `NetBoxRefGrant` | **no**, ever |

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

So that `kubectl delete netboxsite home` also removes the prefixes scoped to it, **one** ref
per Kind contributes a **non-controller** owner reference to the referring object.
Non-controller so it never competes with rule 3; GC still counts it, so the cascade works.

**Which ref that is, is not a judgement about which one reads like a container. It is
whichever foreign key the *server* cascades.** A `CASCADE` foreign key qualifies; a `PROTECT`
or `SET_NULL` one does not. The Descriptor says so per field — `Field.CascadeOnDelete`, read
straight off `on_delete` in `docs/netbox-schema.md` — and `validateContainment` **rejects a
containment ref whose foreign key does not cascade**, so a modelling error that used to be
silent is now a boot failure.

It is added **only when the reference is legal as an owner reference**. Since every kind
is namespaced ([ADR-0002](0002-crd-scoping.md)), that reduces to a single rule: **same
namespace, or no owner reference.**

That was chosen over the narrower rule — owner references *only* for children the operator
itself created (rule 3), and none for containment at all — and the cost of choosing it is
stated here rather than left to be discovered:

**An owner reference is only legal within one namespace, and a NetBox foreign key is not.**
A `NetBoxPrefix` in `team-blue` scoped to a `NetBoxSite` in `netbox-catalogue` gets no owner
reference, ever, so deleting that site does not remove the prefix — the prefix keeps working
and reports its parent gone. Move both objects into one namespace and the identical manifest
cascades. **The same YAML therefore cascades or does not cascade depending on namespace
layout**, which is a property of Kubernetes garbage collection and not something the operator
can paper over. It is why rule 4 emits `CascadeUnavailable` instead of staying quiet: the only
defence against that surprise is saying, on the object, that the cascade is not there.

It is skipped, with a `CascadeUnavailable` condition explaining why, when:

- the ref crosses namespaces (an owner reference may not), or
- the ref points at an unmanaged NetBox object by raw `id`.

Guard clause, not a best effort: the operator either sets a legal owner reference or
says out loud that deleting the parent will not clean up this child.

Opt out per object with `netbox.kubeforge.org/parent-ownership: "false"`, or per
endpoint with `spec.parentOwnership: false`. Default is on, because "delete the site,
its prefixes go too" is the behaviour people expect and the alternative is silent
orphans in NetBox.

**Exactly one containment owner reference per kind, and the reason is Kubernetes garbage
collection, not tidiness.** GC deletes a dependent only once *every* owner is gone, so owners
are **ANDed**. Two containment references therefore mean the object survives until *both*
parents are deleted, while a user reading the manifest expects OR: "delete the site *or* the
VRF and the prefix goes." An earlier version of this rule named both `scopeRef` and `vrfRef`
for `NetBoxPrefix`, which would have meant *deleting the site does not delete the prefix while
the VRF exists* — the opposite of what the prose implied. The gap is silent and shows up only
as an object that refuses to disappear, which is why `Descriptor.ContainmentRef` is a single
string and not a list.

The two rules together — one parent, chosen by cascade — give a mechanical answer per Kind
rather than a per-Kind argument, which is what the M7 generator needs to fill in ~90 Kinds
without a human deciding each one. Worked through:

| Kind | Candidates | Containment parent | Why |
|---|---|---|---|
| `NetBoxRegion`, `NetBoxSiteGroup` | `parentRef` | `parentRef` | `parent` is `CASCADE`, and it is the only FK |
| `NetBoxLocation` | `siteRef` (`CASCADE`), `parentRef` (`CASCADE`) | `siteRef` | two qualify; `site` is the **required** one, so every location has it, and deleting the site cascades to a superset of what deleting a parent location does |
| `NetBoxPrefix` | `scopeRef` (cascades), `vrfRef` (`PROTECT`) | `scopeRef` | every scope target declares a `prefixes` `GenericRelation`; `vrf` is `PROTECT`, so NetBox refuses that deletion outright |
| `NetBoxCluster` | `scopeRef` (cascades), `typeRef` / `groupRef` (`PROTECT`) | `scopeRef` | `clusters GenericRelation` on the two `SET_NULL` scope targets, `_site` / `_location` `CASCADE` on the other two — the per-member case ([#214](https://github.com/ricardomolendijk/netbox-operator/issues/214)) |
| `NetBoxIPAddress` | `assignedObject` (cascades), `vrfRef` (`PROTECT`) | `assignedObject` | the `GenericRelation` case below |
| `NetBoxDevice` | `siteRef`, `roleRef`, `deviceTypeRef`, `tenantRef`, `locationRef` (`PROTECT`), `platformRef` (`SET_NULL`) | **none** | not one of them cascades |

`NetBoxDevice` is the case the earlier prose got wrong, and it is worth leaving visible rather
than quietly dropping: `siteRef` reads exactly like a device's container, and
`dcim.Device.site` is `on_delete=PROTECT`. **When no foreign key qualifies, a Kind gets no
containment owner reference and no cascade.** That is a consequence and not a gap — NetBox
refuses to delete a site that still has devices, so there is no server-side deletion for the
owner reference to mirror, and an owner reference there would delete the CR while the row
stayed behind. It is stated in [ownership](../concepts/ownership.md) so that nobody has to
rediscover it from a descriptor.

Catalogue references — `manufacturerRef`, `deviceTypeRef`, `platformRef`, `tags` — contribute
none for the same reason and one more: a catalogue is not a parent, and in the all-namespaced
model those refs usually cross namespaces anyway, where an owner reference is illegal. A
controller owner reference and a containment owner reference that name the same parent dedupe
to one.

**`assignedObject` is on that list for a correctness reason, not an aesthetic one.**
NetBox deletes an interface's IP addresses server-side through a `GenericRelation` when
the interface goes away. Without an owner reference, the `NetBoxIPAddress` CR outlives
the object it described, finds nothing at `status.id` on the next reconcile, and the
engine's create-if-absent step **recreates the address** — resurrecting data NetBox
deliberately deleted. The owner reference is what makes the CR disappear with its
parent instead. That is the general rule — *server-side cascade implies an owner reference* —
and it is why the cascade is the selection criterion rather than one input among several:
every Kind that needs an owner reference needs it for this exact failure, and no Kind whose
foreign key does not cascade has that failure to prevent.

### How rule 4 is implemented

Built in `internal/reconciler/owners.go`
([#175](https://github.com/ricardomolendijk/netbox-operator/issues/175)); the mechanism and
its namespace rule are documented in [ownership](../concepts/ownership.md). Four things the
prose above left open, settled here:

- **`CascadeUnavailable` is a condition *reason*, not a condition type.** The condition is
  `ParentOwned`: `True/ParentOwned` when the owner reference is set,
  `False/CascadeUnavailable` when no legal one exists, `False/ParentOwnershipDisabled` when
  the annotation declined it, and *absent* on a kind with no containment parent or a spec that
  did not set one. Negative-polarity condition types are a poor fit for the standard
  vocabulary, and a positive one also gives the cascade *working* somewhere to be said.
  A condition rather than an Event because the state is standing: it does not change until an
  object moves namespace or a reference is rewritten, and an Event ages out of the namespace
  long before the deletion that would otherwise be how somebody discovered it.
- **An unresolved containment reference produces no `ParentOwned` condition.** It is already
  `RefsResolved=False` naming itself, and one fact under two conditions is two things free to
  disagree.
- **`blockOwnerDeletion` is not set on a containment owner reference**, only on a controller
  one. It bites only under foreground deletion, where it would let a hand-written object hold
  up `kubectl delete --cascade=foreground` on a shared parent, and setting it requires
  `update` on the owner's `finalizers` subresource wherever
  `OwnerReferencesPermissionEnforcement` is enabled. A controller reference earns the flag by
  having created the child; a containment reference has not.
- **The cascade rule is a boot check, not a review convention.** `Field.CascadeOnDelete` and
  `GenericFKMember.CascadeOnDelete` carry the `on_delete` a Kind's foreign key declares, and
  `registry.validateContainment` returns `ErrContainmentNotCascade` for a `ContainmentRef`
  naming one that is false. Before this, "does the target's deletion cascade server-side" was
  the one fact a Descriptor could not express and lived in `docs/netbox-schema.md` and a
  reviewer's head ([#192](https://github.com/ricardomolendijk/netbox-operator/issues/192)).
  The flag is per foreign key rather than only on the containment ref, because it is a fact
  about the column and the generator emits it from the schema either way.

- **On a polymorphic reference the cascade is per union member, and so is the decision**
  ([#214](https://github.com/ricardomolendijk/netbox-operator/issues/214)). A generic FK's
  cascade is a fact about the *referring* model **per target**: NetBox states it on each
  target model, so one member of a union can cascade while its sibling does not. A pair-wide
  flag left two options, and `virtualization.Cluster` shipped with the second — a cascade
  right for half its scopes, or no containment parent at all. So:

  - `GenericFKMember.CascadeOnDelete` carries it, per member. There is no pair-wide flag left;
    one mechanism, not two.
  - `validateContainment` still refuses at boot a `ContainmentRef` where **no** member
    cascades: nothing about such a reference can ever produce an owner reference. A union
    whose members *disagree* is legal, and the refusal moves to the object.
  - The owner reference is decided per pass, from the member the object **resolved through**
    — `Descriptor.CascadesFrom(spec, targetGVK)`. A member that does not cascade is
    `False/CascadeUnavailable`, alongside the cross-namespace and raw-`id` causes.
  - An object that moves to a member that does not cascade **loses** the owner reference of
    the member it left, and one that moves between two cascading members swaps it in a single
    pass. A stale containment owner reference is worse than none twice over: beside a new one
    it gives the object two owners, which garbage collection ANDs; alone it is a promise about
    an object this one no longer references, so deleting that former parent collects this
    object and its finalizer deletes a row that was never in its scope.
  - `ScopeFK` still cannot default the flags — the cascade belongs to the referring kind, not
    to the union — but a caller stating them for some members and not others is
    `ErrMemberCascadePartial` at boot rather than a member that silently does not cascade.

  **The premise of the limitation this section used to record was wrong, and the correction is
  the reason `virtualization.Cluster` has a parent again.** `GenericRelation` is not a generic
  FK's only cascade path. `dcim.CachedScopeMixin` — which `ipam.Prefix`,
  `virtualization.Cluster` and `wireless.WirelessLAN` all mix in — declares `_site` and
  `_location` as `on_delete=CASCADE` and `_region` and `_site_group` as `SET_NULL`, and NetBox
  maintains all four from `(scope_type, scope_id)`. That is precisely *why* `clusters` and
  `wireless_lans` are declared as a `GenericRelation` on `dcim.Region` and `dcim.SiteGroup`
  and not on `dcim.Site` or `dcim.Location`: the two `SET_NULL` targets need the
  `GenericRelation` to cascade at all, and the two `CASCADE` targets do not. Read together,
  every scoped Kind NetBox 4.6.8 ships cascades from **all four** members:

  | referring model | region | site group | site | location |
  |---|---|---|---|---|
  | `ipam.Prefix` | `prefixes` GR | `prefixes` GR | `_site` CASCADE (+ GR) | `_location` CASCADE (+ GR) |
  | `ipam.VLANGroup` (no cached columns) | `vlan_groups` GR | `vlan_groups` GR | `vlan_groups` GR | `vlan_groups` GR |
  | `virtualization.Cluster` | `clusters` GR | `clusters` GR | `_site` CASCADE | `_location` CASCADE |
  | `wireless.WirelessLAN` | `wireless_lans` GR | `wireless_lans` GR | `_site` CASCADE | `_location` CASCADE |

  So no union a shipped Kind carries disagrees today, and the per-member shape is still the
  right one: the fact is per `(referring model, target model)`, it is reached by a *different
  mechanism per member of one union*, and a `CachedScopeMixin` model without a
  `GenericRelation` on `dcim.Region` — one `GenericRelation` away — cascades from a site and
  not from a region. The old reading cost `NetBoxCluster` its cascade in exactly the direction
  that resurrects data: the CR outlived the row NetBox deleted, and the engine's
  create-if-absent step recreated it.

- **`spec.parentOwnership` on the endpoint is not built.** The per-object annotation is, and it
  covers the case; an endpoint-wide switch would be a third deletion knob beside
  `deletionPolicy` and `onConflict` for a need nobody has stated. Revisit if one is.

The dedupe rule is enforced by never *rewriting*: `controllerutil.SetOwnerReference` upserts,
so it would strip `controller: true` off an entry naming the same parent, taking away the
marker rule 5's pruning and [ADR-0005 §2](0005-gitops-coexistence.md) both read. Entries are
appended, and the only ones ever removed are those in the **containment slot** — a
*non-controller* owner reference naming one of the Kinds `ContainmentRef` resolves to, which
is the operator's own slot and the one it would have written itself. A controller reference is
never removed, and neither is an owner reference to any other Kind.

**5. Inline child sugar is in `v1alpha1`, and every inline field is optional.**

`NetBoxVirtualMachine` and `NetBoxDevice` carry inline lists that the controller
materialises into real child CRs — a VM's `interfaces`, and each interface's
`addresses`:

```yaml
kind: NetBoxVirtualMachine
spec:
  name: dns
  interfaces:                       # sugar: materialises NetBoxVMInterface + NetBoxIPAddress
    - name: eth0
      addresses:
        - address: 10.20.0.10/24
          primary: true
```

Nothing is hidden by this. The children are ordinary CRs — they appear in
`kubectl get netboxvminterface` and `kubectl get netboxipaddress`, they carry the
controller owner reference from rule 3, and `kubectl delete` on the parent cascades
through them natively. An inline address that says `claimFrom` rather than a literal
materialises a **claim** child, so there is still exactly one allocation code path
([ADR-0004](0004-claims-first-allocation.md)):

```yaml
      addresses:
        - claimFrom: {prefixRef: {name: mgmt-net}}   # -> a NetBoxIPAddressClaim child
```

`claimFrom` and not `fromPrefixRef`, which this rule used to say: it is nested so that
allocating out of an ip-range is a member of one union rather than a second mutually-exclusive
sibling key ([ADR-0004](0004-claims-first-allocation.md#the-inline-key-is-claimfrom-and-it-is-nested),
[#183](https://github.com/ricardomolendijk/netbox-operator/issues/183)).

Two constraints are what make this a decision that can be walked back rather than a
permanent part of the API:

- **Every inline field is optional, and every core kind is fully usable standalone.**
  Anything expressible inline is equally expressible as separate CRs wired together with
  `parentRef`. No shape *requires* the sugar.
- **Only materialised children are ever pruned, and a pre-existing CR is never
  hijacked.** A materialised child carries the operator-generated marker of
  [ADR-0005 §2](0005-gitops-coexistence.md#2-objects-the-operator-creates-are-labelled-as-not-gits);
  pruning is limited to children carrying it. A hand-written CR that collides with an
  inline entry is left alone and the **parent** reports `Conflict`.

Together those mean the sugar can be deprecated in `v1beta1` without breaking anyone:
removing an optional field that nobody set is a no-op, and children already materialised
survive their parent losing its sugar, because the marker — not the parent's spec — is
what identifies them.

### How rule 5 is implemented

Built in `api/v1alpha1/inline_children.go` and `internal/reconciler/children.go`
([#45](https://github.com/ricardomolendijk/netbox-operator/issues/45)); the mechanism is
documented in [inline children](../concepts/inline-children.md). What the prose above left
open, settled by the implementation:

- **A capability, not a Descriptor field.** A parent Kind implements
  `InlineParent.InlineChildren()` next to its spec struct, and the engine's entire per-kind
  knowledge of children is one type assertion. A Kind that has none answers by not
  implementing the method, and reconciles exactly as it did before the materialiser existed.
- **The name is derived, deterministic, and from `metadata.name`.**
  `slugify(parent + discriminator + key)` per level, truncated to 246 characters plus six hex
  characters of the untruncated slug's SHA-256 when it would exceed the 253-character limit on
  a CR name. Two declared children deriving one name is reported as a `Conflict` and **nothing
  is written**, because two entries applying one name in turn would flap forever.
- **Pruning needs three facts and requires all three**: the `owned-by-path` annotation, a
  controller owner reference to *this* parent, and a path absent from the parent's current
  spec. On top of that there is a hard cap — a prune set larger than the declared set plus a
  small margin sets `PruneBlocked` and deletes nothing.
- **Convergence is server-side apply under its own field manager**,
  `netbox-operator/children`, so the fields the materialiser sets are reverted on the next
  reconcile and the fields it never sets are left alone. The apply is made *unforced first*:
  the API server's refusal is what names the fields, which is how they reach the
  `ChildFieldReverted` Event.
- **`kubectl wait` on a parent means the parent and its children.** `ChildrenReady` is a
  condition in its own right, and a `Ready=True` is downgraded while any declared child is not
  Ready — but a `Ready=False` a step already set for its own reason is left alone, because it
  is the more specific answer.

## Why not make the parent ref the *controller* reference

An object can have only one controller reference. Giving it to `parentRef` would take
it away from inline-child materialisation, which needs it to know which children it
owns and may prune. Materialisation is the stronger claim — it created the object —
so it wins, and containment settles for a non-controller reference that still drives GC.

## Why inline sugar is in the API at all, given what it costs

It is the ergonomics that were asked for: creating a VM CR also creates its interface and
IP CRs, with owner references, so `kubectl delete` on the VM takes the whole set with it.
For the common case that is strictly more convenient than hand-wiring three CRs, and
because the children are real objects it costs no visibility.

The costs are real and are accepted rather than disputed:

- **Spec bloat on the core kinds.** Carried to its conclusion, `NetBoxDevice` grows
  `interfaces`, `consolePorts`, `powerPorts`, `frontPorts`, `rearPorts`, `deviceBays`,
  `moduleBays` and `inventoryItems` — eight inline lists, each a partial mirror of another
  kind's spec, each having to stay in sync with it.
- **Two ways to express the same thing**, which is two code paths, two sets of edge cases
  (a hand-written child colliding with an inline one being the obvious one), and a docs
  burden.
- **Composition is arguably not the provider API's job** at all; it is what Helm,
  Kustomize or a higher-level abstraction are for.

The alternatives were keeping the core kinds strictly 1:1 with NetBox objects and putting
composition either in a separate `CompositeVirtualMachine` kind or entirely in the
templating layer. They were not taken, because the mitigations in rule 5 make the sugar
removable at a version boundary — so shipping it in `v1alpha1` and finding it wrong is
recoverable, which is the property that decided it.

## Deletion policy

Independently of ownership, each object carries
`spec.deletionPolicy: Delete | Retain`. `Delete` removes the NetBox
object when the CR goes away. `Retain` drops the finalizer and leaves NetBox alone —
for migrating off the operator, or for objects that are shared with something else.

**The default depends on the Kind**: `Retain` for the IPAM kinds that hold an allocation,
`Delete` everywhere else
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)). The table of which
is which, and the reasoning, live in
[deletion — the default depends on the Kind](../concepts/deletion.md#the-two-policies).

A `PROTECT`-ed deletion is not an error to retry quickly: it becomes a
`Deleting=False, Reason=Protected` condition naming the objects that block it, and a
backed-off requeue.
