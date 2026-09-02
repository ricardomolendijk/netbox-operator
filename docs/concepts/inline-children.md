# Inline children: the sugar, and the real CRs behind it

> **Status: built.** The materialiser is NBO-032
> ([#45](https://github.com/ricardomolendijk/netbox-operator/issues/45)), and two Kinds use it:
> `NetBoxDevice`'s `interfaces` and their `addresses`, NBO-034
> ([#47](https://github.com/ricardomolendijk/netbox-operator/issues/47)), and
> `NetBoxVirtualMachine`'s `interfaces`, their `addresses` and its `disks`, NBO-033
> ([#46](https://github.com/ricardomolendijk/netbox-operator/issues/46)). No other Kind carries
> an inline list. Everything below describes shipped behaviour; the exceptions are marked where
> they appear, and there are two — a device's other nine component relations have no Kind to
> materialise yet, and `claimFrom: {ipRangeRef}` is NBO-064.

Some kinds carry inline lists that the operator turns into **real child CRs**:

```yaml
kind: NetBoxVirtualMachine
spec:
  name: dns
  interfaces:
    - name: eth0
      addresses:
        - address: 10.20.0.10/24
```

That one manifest produces three objects in the cluster — the VM, a `NetBoxVMInterface`, and
a `NetBoxIPAddress` — and `kubectl delete` on the VM takes all three with it.

**Nothing is hidden.** The children appear in `kubectl get`, they carry their own conditions,
they are reconciled by their own controllers, and each one writes its own NetBox object. The
parent never writes NetBox on a child's behalf. So inline is a shorter way to say the same
thing, not a different thing:

```yaml
# Exactly equivalent, and always available.
kind: NetBoxVMInterface
spec:
  name: eth0
  virtualMachineRef: {name: dns}
```

Every inline field is optional and every kind is fully usable standalone. That is deliberate,
and it is what makes the sugar removable at a version boundary
([ADR-0003 rule 5](../decisions/0003-ownership-and-references.md)).

## What a materialised child carries

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVMInterface
metadata:
  name: dns-eth0                       # derived; see below
  namespace: homelab                   # the parent's, always
  labels:
    app.kubernetes.io/managed-by: netbox-operator
    netbox.kubeforge.org/owner-uid: 8f1c…            # the parent's uid
  annotations:
    netbox.kubeforge.org/owned-by-path: spec.interfaces[eth0]
    netbox.kubeforge.org/generated-by: netboxvirtualmachine/homelab/dns
    argocd.argoproj.io/compare-options: IgnoreExtraneous
  ownerReferences:
    - apiVersion: netbox.kubeforge.org/v1alpha1
      kind: NetBoxVirtualMachine
      name: dns
      uid: 8f1c…
      controller: true
      blockOwnerDeletion: true
spec:
  name: eth0
  endpointRef: homelab                 # inherited
  virtualMachineRef: {name: dns}
```

**Two annotations, because they carry two different facts.** `generated-by` says *which parent
object*; `owned-by-path` says *which entry of it*. Every child of one VM carries the identical
`generated-by`, so it cannot tell two inline entries apart — and telling them apart is exactly
what pruning has to do. See
[ADR-0005 §2](../decisions/0005-gitops-coexistence.md#2-objects-the-operator-creates-are-labelled-as-not-gits).

The `owner-uid` is a **label** rather than an annotation for one reason: pruning has to *list*
our children, and label selectors are indexed server-side while annotations are not.

The Argo CD annotation is on by default and is not cosmetic. An Argo `Application` containing
a parent with inline children reports `OutOfSync` **forever** without it, because the children
are live resources with no counterpart in Git. Flux prunes by its own inventory and is
unaffected, so its annotation ships disabled. Both are configurable
([ADR-0005 §5](../decisions/0005-gitops-coexistence.md#5-all-of-it-is-configurable-and-the-helm-chart-is-where));
`managed-by` and `generated-by` are not, because they are how the operator recognises its own
output.

**Inherited from the parent:** `endpointRef` and `deletionPolicy`, unless the inline entry set
them. **Not inherited:** `tags`, `customFields`, `description`. Inheriting free text and tag
sets would make a drift report lie about where a value came from.

## The name is derived, and that matters more than it looks

```
slugify( parent.metadata.name + "-" + [discriminator + "-"] key )   … per level
```

| Parent | Path | Child name |
|---|---|---|
| `dns` | `spec.interfaces[eth0]` | `dns-eth0` |
| `dns` | `spec.interfaces[eth0].addresses[10.20.0.10/24]` | `dns-eth0-ip-10-20-0-10-24` |
| `dns` | `spec.interfaces[eth0].addresses[10.20.0.10/25]` | `dns-eth0-ip-10-20-0-10-25` |

`slugify` lowercases, replaces every character outside `[a-z0-9-]` with `-`, collapses runs of
`-`, and trims them from both ends. The prefix length is part of the key, so it is part of the
name: `/24` and `/25` are two NetBox objects and two CRs.

The `ip` in the middle is the child set's **discriminator**, which is what keeps two child
kinds under one parent from colliding — a VM's `disks` and its `interfaces` can both use the
key `eth0`.

**The prefix is `metadata.name`, never `spec.name`.** A Kubernetes name is immutable, so a
name derived from `metadata.name` never changes under a live object. Deriving it from
`spec.name` — the NetBox name — would mean renaming an object in NetBox deleted and recreated
every child CR underneath it, which is the opposite of what anyone expects.

### Over 253 characters

A CR's `metadata.name` is an RFC 1123 *subdomain*, so the limit is **253** characters, not the
63 of a DNS label — nothing turns a child's name into a label. A derived name past 253 keeps
its first 246 characters, then `-`, then the first six hex characters of
`sha256(<the untruncated slug>)`. The digest is of the *untruncated* slug on purpose: two long
siblings that share a 246-character prefix differ only past the cut, so hashing the truncated
form would give them one name and one of the two would silently win.

### Two entries that derive one name

`eth0/1` and `eth0.1` are different NetBox interfaces and the same slug. When that happens the
parent reports `ChildrenReady=False, Reason=Conflict` naming both entries, and **nothing is
written at all** — not even the children that did not collide. Two entries applying one name
in turn would each overwrite the other on alternate reconciles, forever; there is no safe
partial answer, and the fix is in the parent's spec.

### Why determinism is load-bearing

A claim's allocation identity is derived from `(endpoint, namespace, kind, name)`
([ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git)),
and a materialised child's name is derived from its parent's name and its entry's key.
Composed: a VM deleted and re-applied from the same manifest materialises children with **the
same names**, whose claims compute the **same identity**, which adopt the **same NetBox
objects** — and therefore hand back **the same addresses**. An index-based path or a random
suffix would re-roll every address on every cluster rebuild.

## The path is key-based, so reordering is free

```
spec.interfaces[eth0].addresses[10.20.0.10/24]     ✅ the key
spec.interfaces[0].addresses[1]                    ❌ the index
```

Reorder an inline list with index-based paths and every entry below the edit gets a new path,
a new name, and therefore a prune and a create — which in NetBox is a delete and a create with
a new id. Keys survive the reorder. And because a key is the same string that produced the
name, the name and the path move together or not at all.

This is measurable rather than a matter of opinion: children are written with server-side
apply, and an apply of identical content does not bump `metadata.resourceVersion`. Reorder,
reconcile, and every child's `resourceVersion` is unchanged.

## Pruning: three cases, and it can tell them apart

Remove an inline entry and its child is deleted. Nothing else is.

| The live child | What happens |
|---|---|
| Carries `owned-by-path`, controller-owned by this parent, **path still declared** | Updated in place. |
| Carries `owned-by-path`, controller-owned by this parent, **path gone** | Deleted. |
| **No `owned-by-path`** — a human wrote it | Never touched, ever. |

Candidates are listed with the label selector `netbox.kubeforge.org/owner-uid=<parent uid>` —
the *uid*, not the name, so a parent deleted and recreated under the same name does not inherit
the old one's children. Then all three of the following must hold before anything is deleted:
the path annotation is present, a controller owner reference names **this** parent, and the
path is absent from the current spec. Two of those three are redundant by construction.
Requiring all three is what makes a bug in any one of them non-destructive rather than a
data-loss incident.

**The blast-radius cap.** The list call is the most dangerous line in the mechanism: a selector
that came out empty would select every object of that kind in the namespace. So there is a
second gate — a prune set larger than the declared set plus a small margin sets
`ChildrenReady=False, Reason=PruneBlocked` and deletes **nothing**. A prune that wants forty
children of a VM that declares two is a bug in the operator, not a user's intent. If you
genuinely meant to remove thirty inline entries at once, remove them in smaller commits, or
delete the parent and let the cascade take them.

## A hand-written CR is never hijacked

Before writing anything, the materialiser `GET`s the name it is about to write. If something
is already there and does **not** carry both the `owner-uid` label for this parent and a
controller owner reference to it, nothing is written to it — no PATCH, no label, no owner
reference — and the **parent** reports:

```
ChildrenReady   False   Conflict   spec.interfaces[eth0] would be NetBoxVMInterface
                                   homelab/dns-eth0, which already exists and is unowned:
                                   nothing was written to it
```

Both markers are required. One alone is what a manifest copied out of
`kubectl get -o yaml` looks like, and a CR has to go out of its way to be mistaken for ours.
The materialiser never adopts and never overwrites — which is the property that makes the
whole sugar walk-backable, because it can never take over an object somebody else declared.

## A child edited by hand

Two sub-cases, treated differently on purpose.

**A field the materialiser sets** is reverted on the next reconcile, and a
`ChildFieldReverted` Event names it. The parent's inline entry is the declared source of truth
for that field.

**A field the materialiser never sets** is kept. The operator does not manage fields it was not
told about — the same rule it applies to NetBox columns.

That distinction is server-side apply doing the work, under the field manager
`netbox-operator/children`. It is visible from outside: `f:spec` under that manager in
`metadata.managedFields` is the materialiser's own output, and `f:spec` under the plain
`netbox-operator` manager would mean [ADR-0005
§1](../decisions/0005-gitops-coexistence.md#1-the-operator-never-writes-to-spec-ever) had been
broken.

The apply is made **unforced first**. A forced apply would take the field back silently; the
API server's refusal is what names the fields, which is how they reach the Event. It costs one
extra request on the rare pass where a child was hand-edited, and nothing on every other pass.

**Deleting an owned child by hand** gets it recreated, with the same name and the same path,
because the parent still declares it. That is the same answer a Deployment gives about its
Pods.

## Three renames, three different outcomes

| What you change | What happens |
|---|---|
| The parent's `metadata.name` | Not a thing Kubernetes can do. It is a delete plus a create: the old children cascade away and a fresh set is materialised. No orphans. |
| The parent's `spec.name` | Renames the object in NetBox. **Changes nothing in Kubernetes** — no child name, no path, no `resourceVersion`. |
| An inline entry's key (`eth0` → `eth1`) | Changes both the path and the derived name, so the old child is pruned and a new one created. In NetBox that is a delete and a create: the interface's IP goes with it and a new id is issued. |

The third is documented rather than prevented. The alternative is heuristic rename detection,
which is a guess about data.

## Deletion, and what orders it

`kubectl delete` on the parent removes the children and their NetBox objects, parent last —
under both propagation policies, which take different paths through the garbage collector.

What actually orders the NetBox side is the **parent's own finalizer**: it waits while owned
child CRs exist, so the children's finalizers delete their NetBox objects first and the
parent's NetBox object last. `blockOwnerDeletion: true` is belt-and-braces for
`--cascade=foreground`; it has no effect at all under the default background propagation, so
relying on it alone would be relying on nothing most of the time.

**A cascade that fails halfway is normal, not exceptional.** A child's NetBox `DELETE` refused
with `PROTECT` means something still points at it. The child reports
`Deleting=False, Reason=Protected` with the server's message and backs off; the parent stays
`ChildrenReady=False` naming it. Nothing is forced, no finalizer is dropped, no object is left
half-deleted, and when the blocker goes away the chain completes with no manual step. A child
that is permanently blocked leaves the parent permanently deleting — the correct outcome, and
infinitely preferable to a force-delete that orphans a NetBox object nobody is tracking any
more. See [deletion](deletion.md).

## When nothing is materialised

Three guards, and each writes nothing:

| State | What the parent says |
|---|---|
| Being deleted | Nothing. The cascade is already under way, and materialising into it would recreate what it is removing. |
| No `status.id` yet | `PendingChildren` — every child's reference back to this object would sit unresolved. |
| Endpoint in `DryRun`, or `driftMode: Report` | `DryRunPending` / `ReportPending`. A rehearsal that created CRs would not be one. |

A `DryRun` endpoint never gives the parent a `status.id` in the first place, so on a first
apply the second guard is what implements the third.

## What the parent tells you

```console
$ kubectl describe netboxvirtualmachine dns
Status:
  Children:
    Path:   spec.interfaces[eth0]
    Kind:   NetBoxVMInterface
    Name:   dns-eth0
    Ready:  true
    Path:   spec.interfaces[eth0].addresses[10.20.0.10/24]
    Kind:   NetBoxIPAddress
    Name:   dns-eth0-ip-10-20-0-10-24
    Ready:  true
  Conditions:
    Type:    ChildrenReady
    Status:  True
    Reason:  AllReady
```

`ChildrenReady` reasons: `AllReady`, `PendingChildren`, `Conflict`, `PruneBlocked`, and
`APIError` when the API server could not be reached — a failed list is treated as *unknown*
rather than as an empty prune set.

**The parent is not `Ready=True` while any declared child is not.** `kubectl wait` on a VM has
to mean the VM *and* its interfaces and addresses. A `Ready=False` that the parent already set
for its own reason is left alone, though: that is the more specific answer, and overwriting it
would hide the cause.

## Allocation is another child kind, not another code path

An inline address that names a prefix instead of an address materialises a
`NetBoxIPAddressClaim`:

```yaml
addresses:
  - claimFrom:
      prefixRef: {name: mgmt-net}
```

The claim is an ordinary child: the same derived name, the same markers, the same prune rules.
The materialiser needs no special case for it, which is the test of whether the mechanism is
general enough — and it passed: the only thing the first claim child needed was for the
inheritance step to know about `NetBoxClaimSpec` as well as `NetBoxObjectSpec`, since a claim
is not an ordinary object and `endpointRef` on one is a required field. There is exactly one
allocation code path and it is the claim controller's
([ADR-0004](../decisions/0004-claims-first-allocation.md)).

**The claim entry's key is the pool**, so the derived name is `dns-eth0-claim-mgmt-net` and the
discriminator is `claim` rather than the `ip` a literal address gets. The pool rather than an
index, for the reason every key here is key-based: a claim's allocation identity is derived
from its name, so a stable key is what makes a rebuilt cluster reclaim the same address. The
cost is that two entries claiming from one pool on one interface derive one name, which is
reported as a `Conflict`; the second one is written as its own `NetBoxIPAddressClaim`.

The key is nested — `claimFrom: {prefixRef: …}` — so that allocating from an IP range later is
another key (`claimFrom: {ipRangeRef: …}`) rather than a second sibling field plus a CEL rule
saying exactly one of them may be set. `claimFrom: {ipRangeRef: …}` is NBO-064 and is not
built.

The inline form deliberately does not express everything. A claim that needs a specific VRF, a
role, a DNS name or a non-default `deletionPolicy` is written as its own
`NetBoxIPAddressClaim`. Inline covers the common case; the standalone kind stays the complete
one, which is what keeps the sugar from growing into a mirror of the claim spec.

**One thing an inline claim does not do yet, stated plainly: it does not attach the address it
allocates to the interface it was written under.** `NetBoxIPAddressClaim` carries no
`assignedObject` and does not materialise a `NetBoxIPAddress` of its own, so the
claim allocates and records an address in `status.address` and stops there. That is exactly as complete as a
standalone claim — which is the property that keeps this sugar equivalent to the longhand it
stands for rather than a better version of it — but it is a real gap and not a subtlety. An
address that has to be on the interface today is written as a literal `address`, and a
`claimFrom` entry may not set `primary` for the same reason: there is no address CR for the
parent to point at.

## The one place the sugar flows upward

Everything above flows downward: a parent declares a child and the materialiser writes it.
`primary` on an inline address is the exception, and it is worth its own section because it is
the only case where a child's identity ends up in its *parent's* payload.

```yaml
interfaces:
  - name: eth0
    addresses:
      - address: 10.20.0.10/24
        primary: true            # -> this VM's primary_ip4
```

The column is `virtualization.VirtualMachine.primary_ip4`, on the VM, and the value is the id
of an address the VM materialised. Neither of the two obvious mechanisms is available: the
materialiser may not write `spec.primaryIP4Ref`
([ADR-0005 §1](../decisions/0005-gitops-coexistence.md#1-the-operator-never-writes-to-spec-ever)
— Argo CD would revert it and the two would fight at the shorter resync interval), and
`status.children` records names rather than ids.

So the reference is **derived**, on every pass, from what the parent already knows: the child's
name is deterministic, so the parent can name it before either object exists. A Kind states its
derived references next to its inline ones —

```go
func (vm *NetBoxVirtualMachine) DerivedRefs() ([]DerivedRef, error)
```

— and the engine folds them into the spec it builds the payload from. From there they are
indistinguishable from a reference somebody typed: the same resolution, the same deferral, the
same `status.deferredPending`, the same one follow-up `PATCH`. That is what closes the
`VM → IPAddress → VMInterface → VM` ring with no second write path
([the deferred-field second pass](object-lifecycle.md#the-deferred-field-second-pass)).

**Two declarations for one column is a refusal, not a precedence rule.** Two inline addresses
of one family marked `primary`, or one beside an explicit `spec.primaryIP4Ref`, gets
`Ready=False, Reason=Conflict` naming both declarations, with zero writes — because choosing
one silently would make the other a lie that no condition mentions. It is enforced twice, by
CEL at admission and by the controller, since the CEL half is a nested list comprehension whose
cost the API server charges at the product of both lists' maxima.

## Adding inline children to a kind

One method, next to the spec struct, in the kind's own file:

```go
func (vm *NetBoxVirtualMachine) InlineChildren() []InlineChildSet {
    set := InlineChildSet{Field: "interfaces"}

    for _, iface := range vm.Spec.Interfaces {
        set.Entries = append(set.Entries, InlineChildEntry{
            Key:     iface.Name,
            Desired: iface.child(vm),   // everything but name, labels and ownerRefs
        })
    }

    return []InlineChildSet{set}
}
```

The engine's entire per-kind knowledge of children is one type assertion on that interface. A
kind that has none answers by not implementing the method, and reconciles exactly as it did
before the materialiser existed — no branch on Kind anywhere, which is enforced rather than
merely intended (`forbidigo`).

`InlineChildren()` is called on every reconcile, so it has to be pure: build the objects, do
not read the API server, do not cache.

**And nothing else on the Descriptor.** An inline list *is* a spec field no field map declares,
and the payload builder refuses an unmapped field rather than dropping it — because NetBox
ignores a column name it does not know, so a field the operator quietly dropped would report
itself synced while writing nothing. What keeps the sugar out of the payload is
`specFields.dropInlineChildren`, which removes exactly the fields `InlineChildren()` names: the
same declaration the materialiser reads, so there is nothing to keep in sync and a Kind cannot
declare an inline list the payload then tries to send.

A Kind that also wants the upward direction adds a second method beside the first —
`DerivedRefs()`, see [the one place the sugar flows
upward](#the-one-place-the-sugar-flows-upward). Still no Descriptor field, and still one type
assertion per capability.

## See also

- [Ownership](ownership.md) — the two owner references and the cascade
- [Deletion](deletion.md) — the finalizer ordering, and `PROTECT`-blocked deletes
- [Coexisting with Flux and Argo CD](../operations/gitops.md) — the `Application` and
  `Kustomization` snippets that make a cluster with materialised children quiet
- [ADR-0003 rule 5](../decisions/0003-ownership-and-references.md) — why the sugar is in
  `v1alpha1` at all, and the two constraints that make it removable
- [ADR-0005 §2](../decisions/0005-gitops-coexistence.md) — the markers a generated object
  carries, and why both annotations exist
- [ADR-0004](../decisions/0004-claims-first-allocation.md) — the inline form of a claim
