# Generic references

What a polymorphic foreign key is, why one CR field writes two NetBox columns, and how a
new one is added without touching the engine.

> **Status.** The mechanism is built (NBO-019): the union pattern, CEL validation of the
> one-of-N shape, resolution to an `(object type, id)` pair, paired drift detection, and
> ref watches over every allowed target. One union ships —
> [`IPAssignment`](../reference/genericref.md) — because `NetBoxIPAddress` needs it
> ([NBO-025](https://github.com/ricardomolendijk/netbox-operator/issues/37)); its three
> target Kinds arrive in M4, so today its members resolve by `slug`, `lookup` or `id` only
> once their Descriptors exist. See [Kinds that do not exist
> yet](#kinds-that-do-not-exist-yet).
>
> Not built: a generic FK is **not** followed by the cycle check, and does **not**
> contribute an owner reference. Both are stated below with the reason.

## What a generic foreign key is

Most NetBox foreign keys point at one model. Some point at *whichever* model, and Django
spells that as a `GenericForeignKey`: a `*_type` column naming a model and a `*_id` column
naming a row in it.

```
ipam.IPAddress
  assigned_object_type  ->  "dcim.interface" | "virtualization.vminterface" | "ipam.fhrpgroup"
  assigned_object_id    ->  8
```

Over the REST API the type half is written as an **`"app_label.model"` string**. That
string is Django's `ContentType`, and its `model` component is **lowercased and
unpunctuated**:

| Correct | Wrong | Why the wrong one is tempting |
|---|---|---|
| `virtualization.vminterface` | `virtualization.VMInterface` | The schema digest lists the Python *class* name |
| `ipam.fhrpgroup` | `ipam.FHRPGroup` | Same |
| `dcim.interface` | `dcim.Interface` | Same |

Getting it wrong is the quiet kind of wrong. NetBox does not reject an unknown column
value the way it rejects a bad field name — the write can succeed and point the object at
nothing, or at a row of the wrong model that happens to share that primary key. So the
spelling is written down **exactly once**, on the target Kind's own
[`Descriptor.ObjectType`](descriptor.md), and read from there. Nothing else in the codebase
is allowed to spell it.

`Descriptor.Validate` enforces the pattern `^[a-z_]+\.[a-z0-9_]+$` on every object type and
on every entry of an `AllowedTypes` list, so `virtualization.VMInterface` fails the boot
rather than a reconcile.

## The `REQ` trap in the schema digest

[`docs/netbox-schema.md`](../netbox-schema.md) records a generic FK as **three** rows: the
two columns, and a non-column `GenericForeignKey` row above them. That third row always
carries `REQ`:

```
assigned_object       GenericForeignKey  REQ      <- not a column; the REQ is an artefact
assigned_object_type  ForeignKey                  <- nullable
assigned_object_id    PositiveBigIntegerField     <- nullable
```

`GenericForeignKey` takes no `null=` kwarg at all, so the extractor's default fills in
`REQ`. **Read nullability off the `*_type` / `*_id` pair and never off the
`GenericForeignKey` row.** Believing the artefact makes an unassigned IP address illegal,
which NetBox permits and which the API would then reject at `kubectl apply`.

## The CR shape: a named union

Every polymorphic FK gets a **named union struct** with one typed-ref member per legal
target:

```go
type IPAssignment struct {
    InterfaceRef   *InterfaceRef   `json:"interfaceRef,omitempty"`   // -> "dcim.interface"
    VMInterfaceRef *VMInterfaceRef `json:"vmInterfaceRef,omitempty"` // -> "virtualization.vminterface"
    FHRPGroupRef   *FHRPGroupRef   `json:"fhrpGroupRef,omitempty"`   // -> "ipam.fhrpgroup"
}
```

Not one `ObjectRef` plus a `kind` discriminator. The per-target field name is what lets CEL
and the resolver both enforce the target Kind, and it makes `kubectl explain
netboxipaddress.spec.assignedObject` a complete list of what the field accepts. A
discriminator would make every union accept every Kind and push the check to runtime.

### One-of-N, enforced at admission

The union carries a CEL rule, so a malformed union is rejected by `kubectl apply` rather
than becoming a condition nobody reads. There are two shapes, chosen by the nullability of
the column pair:

| Pair | Rule | Accepts | Rejects |
|---|---|---|---|
| both columns nullable | `[...].filter(x, x).size() <= 1` | none, one | two or more |
| both columns `REQ` | `[...].filter(x, x).size() == 1` | one | none, two or more |

For a `REQ` pair the union field itself must also be **required** in the CRD: a CEL rule on
an absent field is never evaluated, so an optional `== 1` union would be satisfied by
leaving it out.

Both shapes are proved against a real API server by `TestUnionCELShapes`, over a test-only
CRD in `internal/controller/testdata/crd/`. The fixture exists because `IPAssignment` is on
no shipped CRD until `NetBoxIPAddress` lands, and a CEL rule no CRD carries is compiled by
nothing; `TestUnionCELRuleMatchesTheAPIType` asserts the fixture's rule is byte-identical to
the one on the Go type, so it cannot drift away from what it stands in for.

## The Descriptor side

A generic FK is declared on the referrer's Descriptor, not in its `Fields` map — a `Field`
maps one spec name to one API name, and this reference has two:

```go
GenericFKs: []registry.GenericFKSpec{{
    TypeField:    "assigned_object_type",
    IDField:      "assigned_object_id",
    Spec:         "assignedObject",
    AllowedTypes: []string{"dcim.interface", "virtualization.vminterface", "ipam.fhrpgroup"},
    Members: []registry.GenericFKMember{
        {Spec: "interfaceRef", Target: v1alpha1.InterfaceRef{}.TargetGVK()},
        {Spec: "vmInterfaceRef", Target: v1alpha1.VMInterfaceRef{}.TargetGVK()},
        {Spec: "fhrpGroupRef", Target: v1alpha1.FHRPGroupRef{}.TargetGVK()},
    },
}}
```

`Members` and `AllowedTypes` are two independent statements and both are load-bearing:

- **`Members`** is the resolver's dispatch table — which CR field selects which *Kind*. It
  holds the Kind and never the type string, so the `app_label.model` spelling stays in one
  place.
- **`AllowedTypes`** is what NetBox will accept in that column, in NetBox's own vocabulary.

They have to agree, and the operator enforces the agreement in both directions:

| When | What is checked | Failure |
|---|---|---|
| boot | every member whose Kind is registered resolves to an object type in `AllowedTypes` | the manager does not start |
| resolve | the member the object set is declared, and its resolved object type is in `AllowedTypes` | `RefsResolved=False, Reason=RefTypeNotAllowed` |

The runtime check is not redundant with the boot check. It is what catches a *stored* object
whose CRD or Descriptor has narrowed under it, and a member whose Kind was not registered at
boot and therefore could not be cross-checked then.

`RefTypeNotAllowed` is **terminal**: it has no requeue, because no object appearing anywhere
makes an illegal target legal. Only an edit clears it, and an edit arrives as a watch event.
The message names both halves — what was given and what the column accepts — because those
two together are the whole fix.

## Resolution: one union, one pair

Dispatch is a **table lookup keyed on the union's JSON member name**, out of
`GenericFKSpec.Members`. Not a type switch, not a switch on Kind. Once the member's target
Kind is known, a union member *is* an ordinary reference: same four modes, same grant check,
same typed errors, same [condition vocabulary](references.md#the-condition-vocabulary).

```
spec.assignedObject.vmInterfaceRef: {name: eth0}
  -> member "vmInterfaceRef" -> Kind NetBoxVMInterface
  -> that Kind's Descriptor  -> endpoint + ObjectType "virtualization.vminterface"
  -> resolve as an ordinary `name` reference -> id 8
  -> assigned_object_type = "virtualization.vminterface", assigned_object_id = 8
```

Three outcomes, and the empty one is the interesting one:

| Union as written | Payload |
|---|---|
| field absent | neither column is written — spec omission means *do not manage* |
| `assignedObject: {}` | **both** columns set to `null` — an empty union is an instruction to clear |
| one member set | **both** columns set together |

### Why the pair is atomic

There is exactly one function that writes either column, and it writes both from one
resolved result. That is a structural guarantee rather than a convention: no code path can
produce an id without the object type it was resolved with.

The same holds on the way back in. [Drift detection](drift.md) treats the pair as one unit —
a change to only the id half emits **both** columns, and clearing emits both nulls. A
`scope_id` sent without its `scope_type` is rejected by NetBox at best, and at worst
interpreted against the old type.

## Watches: what makes a polymorphic reference converge

A `name`-mode union member is indexed and watched exactly like a typed reference, so an
IP address waiting on an interface is woken when that interface becomes Ready — not on the
endpoint's resync.

That needs a lookup nothing else in the operator needed: `AllowedTypes` is written in
NetBox's vocabulary and a watch is registered in Kubernetes's, so an `app_label.model`
string has to become a GVK. The registry carries the **reverse of
`Descriptor.ObjectType`** for it, `registry.ByObjectType`, which is one-to-one — two Kinds
claiming one object type is refused at registration, because an ambiguous answer there is a
reference resolved against the wrong Kind.

- **Watch targets** come from `AllowedTypes` through that reverse index: every allowed type
  this build carries a Descriptor for is watched, and one it does not is skipped — there is
  no informer to watch.
- **Index keys** come from the member the object actually set, through its `Members` entry.

Both are derived from descriptor data, so a new union member is a data change and adds no
watch code. `TestEveryAllowedTypeIsWatched` walks the registry rather than naming kinds.

### Kinds that do not exist yet

`IPAssignment` names `dcim.Interface`, `virtualization.VMInterface` and `ipam.FHRPGroup`,
and none of the three has a Kind before M4. A member naming a Kind with no registered
Descriptor resolves to `RefsResolved=False, Reason=RefKindUnavailable` — reported and
never silently dropped. The union ships now because `NetBoxIPAddress` needs it now, and a
stub that accepted `interfaceRef` and dropped it would report success while writing
nothing.

The Descriptor is what is missing, not merely the CRD: `slug`, `lookup` and `id` all need
the target's NetBox endpoint, which only a Descriptor holds. So all four modes wait on the
target Kind being registered, and only `name` additionally waits on its CRD being installed.

## What a generic FK deliberately does not do

**It is not followed by the cycle check.** The [cycle walk](references.md#cycles) follows an
edge if and only if the referrer *cannot be created* until that edge resolves. Every union
that ships today sits on a nullable pair, so the object is created with the columns unset
and the reference PATCHed in later — it never blocks, and a ring through it is not a
deadlock. A `REQ` pair does block, and the first one to ship (`ipam.Service`'s
`parent_object_*`) has to declare that in its Descriptor for the walk to follow it.

**It contributes no owner reference.** `docs/decisions/0003-ownership-and-references.md`
§4 says a containment generic FK should contribute a non-controller owner reference, and
`Descriptor.ContainmentRef` accepts a generic FK's spec field. Nothing in the engine writes
owner references yet, for typed references either, so there is nothing here to extend.

**It is not usable in a natural key yet.** A lookup on a polymorphic pair needs two filters
and there is no single value to offer, so a resolved union is not written into the spec the
way a resolved typed reference is. No shipped Descriptor names one in a natural key; the
first that will is `ipam.VLANGroup`, unique on `(scope_type, scope_id, slug)`
([NBO-018](https://github.com/ricardomolendijk/netbox-operator/issues/30)). Until then such
a candidate is refused loudly rather than sending a lookup with half an identity.

**Reverse accessors are absent from every spec.** `nat_outside` and `l2vpn_terminations`
are Django `GenericRelation` reverse accessors, not columns. They are read-only views of
somebody else's foreign key and there is nothing to write.

## Adding a union

Three edits, none of them in the engine:

1. **`api/v1alpha1`** — the typed alias for any new target Kind (`TargetGVK`,
   `AsObjectRef`), the union struct with one member per legal target, and the CEL rule in
   the `<= 1` or `== 1` shape.
2. **`internal/registry`** — the `GenericFKSpec` on the referrer's Descriptor: the two
   columns, the spec field, `AllowedTypes`, and one `Members` entry per union member.
3. **Docs** — a row in [`reference/genericref.md`](../reference/genericref.md).

`internal/reconciler` and `internal/resolver` do not change, and neither does
`internal/controller`. If a union needs a change in any of those three, the thing it needs
is missing from the Descriptor — add it there.
