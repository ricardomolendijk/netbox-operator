# Generic references

What a polymorphic foreign key is, why one CR field writes two NetBox columns, and how a
new one is added without touching the engine.

> **Status.** The mechanism is built (NBO-019): the union pattern, CEL validation of the
> one-of-N shape, resolution to an `(object type, id)` pair, paired drift detection, and
> ref watches over every allowed target. Two unions ship, both through that one mechanism:
> [`IPAssignment`](../reference/genericref.md), because `NetBoxIPAddress` needs it
> ([NBO-025](https://github.com/ricardomolendijk/netbox-operator/issues/37)), and
> [`ScopeRef`](#the-scope-pair) — NetBox's `(scope_type, scope_id)`
> ([NBO-018](https://github.com/ricardomolendijk/netbox-operator/issues/30)). Several of
> their target Kinds arrive in M4, so until then those members are reported
> `RefKindUnavailable` in all four modes. See [Kinds that do not exist
> yet](#kinds-that-do-not-exist-yet).
>
> Not built: a generic FK is **not** followed by the cycle check. It *does* contribute an
> owner reference when it is the containment ref ([ownership](ownership.md)). Both are stated
> below with the reason.

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
same typed errors, same [condition vocabulary](references.md#what-happens-when-it-does-not-resolve).

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

## The scope pair

NetBox's scope is a generic FK like any other, and it is documented here rather than on a page
of its own for exactly the reason this mechanism exists: there is one implementation of it, so
there is one page about it. What follows is what is true of *this* pair and of no other.

> **"Scope" here is NetBox's, not Kubernetes'.** They are unrelated and the collision will
> confuse someone, so it is settled first.
>
> | | What it means | Where it is set |
> |---|---|---|
> | **Kubernetes CRD scope** | whether a CRD's objects live in a namespace or in the cluster | `Descriptor.Scope`, `Namespaced` for every kind in `v1alpha1` ([ADR-0002](../decisions/0002-crd-scoping.md)) |
> | **NetBox scope** | which Region, SiteGroup, Site or Location a NetBox object hangs off | `spec.scope` on a scoped kind |
>
> Nothing about `spec.scope` changes where a CR lives. A `NetBoxPrefix` in `team-a` scoped to
> a Site is still a namespaced object in `team-a`.

Until NetBox 4.1 several models carried an ordinary `site` foreign key. NetBox 4.2 replaced it
with `CachedScopeMixin`, which is two writable columns and four read-only ones
(`docs/netbox-schema.md` → `dcim.CachedScopeMixin`):

```
scope_type    ForeignKey            -> contenttypes.ContentType   writable
scope_id      PositiveBigIntegerField                             writable
scope         GenericForeignKey                                   not a column
_location     ForeignKey            -> dcim.Location              read-only cache
_site         ForeignKey            -> dcim.Site                  read-only cache
_region       ForeignKey            -> dcim.Region                read-only cache
_site_group   ForeignKey            -> dcim.SiteGroup             read-only cache
```

The `REQ` the digest prints against the `scope` row is the artefact described in [the `REQ`
trap](#the-req-trap-in-the-schema-digest). Neither real column carries `REQ`, so an unscoped
object is legal and the rule is `<= 1`.

### The failure this prevents

**Writing `site` to a scoped model does not fail. It is ignored.**

NetBox drops a column it does not know rather than rejecting it, so
`POST /api/ipam/prefixes/` with `{"prefix": "192.0.2.0/24", "site": 3}` returns `201`. The
prefix is created. It has no scope. Every subsequent read agrees with the spec, because the
spec's `site` is compared against a column that does not exist — so nothing ever drifts,
nothing is ever reported, and the object says `Ready=True` forever.

That is the bug `netbox-populator` shipped, and it is why no scoped kind here has a `siteRef`
— not even as sugar expanding into `scope.siteRef`. A field called `siteRef` on a
`NetBoxPrefix` would read as the foreign key NetBox no longer has, and the point of the union
is that the mistake cannot be expressed.

The four `_`-prefixed columns are the mirror image, and the reason `GenericFKSpec` has a
[`Cached`](descriptor.md) list. They are caches NetBox maintains from `(scope_type, scope_id)`
and they come back on every read, so they are useful to *filter* on and fatal to *write*: an
attempt to set `_site` is dropped exactly like `site`, the next read finds it unchanged, and
the operator PATCHes it again on every resync — a hot loop for as long as the object exists.
Every column named in `Cached` must also be in `ReadOnly`, which `Validate` enforces at boot,
so this cannot be got wrong one kind at a time.

### The union, and one line per scoped kind

```go
type ScopeRef struct {
    RegionRef    *RegionRef    `json:"regionRef,omitempty"`    // -> "dcim.region"
    SiteGroupRef *SiteGroupRef `json:"siteGroupRef,omitempty"` // -> "dcim.sitegroup"
    SiteRef      *SiteRef      `json:"siteRef,omitempty"`      // -> "dcim.site"
    LocationRef  *LocationRef  `json:"locationRef,omitempty"`  // -> "dcim.location"
}
```

The type strings in those comments are not written down against the members. Each is the
target Kind's own `Descriptor.ObjectType`, so `dcim.sitegroup` is spelled once in the
codebase — and it is the Django `model` attribute, lowercased and unpunctuated, so never
`dcim.SiteGroup` or `dcim.site_group`.

The descriptor half is written once too, in `internal/registry/scope.go`, so a scoped kind is
one line and cannot get the spelling or the cache list wrong:

```go
GenericFKs: []registry.GenericFKSpec{registry.ScopeFK("scope")},
ReadOnly:   append(registry.ScopeCacheColumns(), "created", "last_updated", …),
```

A kind carrying the pair and no caches — `ipam.VLANGroup` declares `scope_type` / `scope_id`
on the model itself — clears `Cached` on the returned value.

Everything else about a scope is the mechanism above: the [three
instructions](#resolution-one-union-one-pair), the [atomic pair](#why-the-pair-is-atomic) and
the [watches](#watches-what-makes-a-polymorphic-reference-converge). Unlike `IPAssignment`,
every member of this union now has a Descriptor —
[NBO-066 (#79)](https://github.com/ricardomolendijk/netbox-operator/issues/79) added
`NetBoxSiteGroup` and `NetBoxLocation` — so none of the four reports
[`RefKindUnavailable`](#kinds-that-do-not-exist-yet).

## What a generic FK deliberately does not do

**It is not followed by the cycle check.** The [cycle walk](references.md#cycles) follows an
edge if and only if the referrer *cannot be created* until that edge resolves. Every union
that ships today sits on a nullable pair, so the object is created with the columns unset
and the reference PATCHed in later — it never blocks, and a ring through it is not a
deadlock. A `REQ` pair does block, and the first one to ship (`ipam.Service`'s
`parent_object_*`) has to declare that in its Descriptor for the walk to follow it.

**It contributes an owner reference exactly as a typed reference does.**
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 has a containment generic FK
contribute a non-controller owner reference, and `Descriptor.ContainmentRef` accepts a generic
FK's spec field. This needed no separate implementation: `ResolveAll` files a resolved union
under the *union's own* spec field, keyed the same way an ordinary reference is, so the
ownership step reads it with the same lookup. A member whose target Kind is not registered is
refused as `RefKindUnavailable` and therefore never owned — see
[ownership](ownership.md#an-unregistered-target-kind).

**It is not usable in a natural key yet.** A lookup on a polymorphic pair needs two filters
and there is no single value to offer, so a resolved union is not written into the spec the
way a resolved typed reference is. No shipped Descriptor names one in a natural key —
not even the scoped kinds, whose keys are all on scalars. The first that will is
`ipam.VLANGroup`, unique on `(scope_type, scope_id, slug)`. Until it lands such a candidate is
refused loudly by `params()` rather than sending a lookup with half an identity.

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

A union more than one kind carries gets a constructor next to it, as
`registry.ScopeFK` is, so the second kind to use it restates nothing.

`internal/reconciler` and `internal/resolver` do not change, and neither does
`internal/controller`. If a union needs a change in any of those three, the thing it needs
is missing from the Descriptor — add it there.
