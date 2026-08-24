# 0003 — Ownership and references

**Status:** Accepted · 2026-08-21
**Amended:** 2026-08-24 — rule 5 added, settling whether inline child sugar belongs in
`v1alpha1` at all ([#17](https://github.com/ricardomolendijk/netbox-operator/issues/17)).
**Amended:** 2026-08-24 — rules 3 and 4 settled as the answer to *which* relationships get an
owner reference, and what cross-namespace containment therefore gives up
([#175](https://github.com/ricardomolendijk/netbox-operator/issues/175)). The
[deletion policy](#deletion-policy) default is now per Kind
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)).

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

So that `kubectl delete netboxsite home` also removes the prefixes scoped to it, a
ref that is genuinely a parent-child containment relationship (`scopeRef`,
`siteRef`, `clusterRef`, `deviceRef`, `vrfRef`, `prefixRef`, `assignedObject`)
contributes a **non-controller** owner reference to the referring object. Non-controller so it never
competes with rule 3; GC still counts it, so the cascade works.

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

**At most one containment owner reference per kind.** Kubernetes garbage collection
deletes a dependent only once *every* owner is gone. Two containment references therefore
give AND semantics — the object survives until both parents are deleted — while a user
reading the manifest expects OR: "delete the site *or* the VRF and the prefix goes." The
gap is silent and only shows up as an object that refuses to disappear.

So each kind nominates exactly **one** containment ref, and it is the required FK:
`siteRef` for `NetBoxDevice`, `clusterRef` (else `siteRef`) for
`NetBoxVirtualMachine`, `deviceRef` / `virtualMachineRef` for components, `scopeRef` for
`NetBoxCluster` and `NetBoxPrefix`. Catalogue references — `manufacturerRef`,
`deviceTypeRef`, `platformRef`, `tags` — contribute none; a catalogue is not a parent, and
in the all-namespaced model those refs usually cross namespaces anyway, where an owner
reference is illegal. A controller owner reference and a containment owner reference that
name the same parent dedupe to one.

**`assignedObject` is on that list for a correctness reason, not an aesthetic one.**
NetBox deletes an interface's IP addresses server-side through a `GenericRelation` when
the interface goes away. Without an owner reference, the `NetBoxIPAddress` CR outlives
the object it described, finds nothing at `status.id` on the next reconcile, and the
engine's create-if-absent step **recreates the address** — resurrecting data NetBox
deliberately deleted. The owner reference is what makes the CR disappear with its
parent instead. Any ref whose target's deletion cascades server-side belongs in this
list for the same reason; the general rule is *server-side cascade implies an owner
reference*, and the list is the enumeration of where that is true.

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
through them natively. An inline address that says `fromPrefixRef` rather than a literal
materialises a **claim** child, so there is still exactly one allocation code path
([ADR-0004](0004-claims-first-allocation.md)).

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

Not built yet; the materialisation ticket is NBO-032
([#45](https://github.com/ricardomolendijk/netbox-operator/issues/45)), tracked in
[object lifecycle](../concepts/object-lifecycle.md).

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

**The default depends on the Kind**: `Retain` for the IPAM kinds, `Delete` everywhere else
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176)). The table of which
is which, and the reasoning, live in
[deletion — the default depends on the Kind](../concepts/deletion.md#the-default-depends-on-the-kind).

A `PROTECT`-ed deletion is not an error to retry quickly: it becomes a
`Deleting=False, Reason=Protected` condition naming the objects that block it, and a
backed-off requeue.
