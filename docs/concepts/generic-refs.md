# Generic references

What a polymorphic foreign key is, why one CR field writes two NetBox columns, and how a
new one is added without touching the engine.

> **Status.** The mechanism is built (NBO-019): the union pattern, CEL validation of the
> one-of-N shape, resolution to an `(object type, id)` pair, paired drift detection, and
> ref watches over every allowed target. Four unions ship, all through that one mechanism:
> [`IPAssignment`](../reference/genericref.md), now on a real CRD as
> [`NetBoxIPAddress.spec.assignedObject`](../reference/netboxipaddress.md#assignedobject)
> ([NBO-025](https://github.com/ricardomolendijk/netbox-operator/issues/37));
> [`ScopeRef`](#the-scope-pair) — NetBox's `(scope_type, scope_id)`
> ([NBO-018](https://github.com/ricardomolendijk/netbox-operator/issues/30)); and
> [`ContactAssignmentTarget`](../reference/netboxcontactassignment.md#objectref) — the widest
> of the three at 11 members, and the first on a **`REQ`** pair
> ([NBO-056](https://github.com/ricardomolendijk/netbox-operator/issues/57)); and
> [`CableTerminationTarget`](../reference/genericref.md#cableterminationtarget) — the first
> **to-many** pair, and the one that made `GenericFKSpec` grow a cardinality
> ([NBO-049](https://github.com/ricardomolendijk/netbox-operator/issues/50)). Several of
> their target Kinds arrive later, so until then those members are reported
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
CRD in `internal/controller/testdata/crd/`. The fixture came first, because a CEL rule no CRD
carries is compiled by nothing and `IPAssignment` was on no shipped CRD until
[`NetBoxIPAddress`](../reference/netboxipaddress.md) landed; it stays, because it is the only
place the `== 1` shape is exercised over *its own* members.
[`NetBoxContactAssignment.spec.objectRef`](../reference/netboxcontactassignment.md#objectref) is
now a real `REQ` pair on a shipped CRD (NBO-056), so the shape no longer exists only as a
fixture -- but the fixture's members are not that union's, so the two are not interchangeable.
`TestUnionCELRuleMatchesTheAPIType` asserts the fixture's rule is byte-identical to the one on
the Go type, so it cannot drift away from what it stands in for.

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

## A to-many pair

Every union above sits on **two columns holding one pair**. `dcim.Cable`'s terminations do
not, and they are the reason `GenericFKSpec` grew a cardinality
([NBO-049](https://github.com/ricardomolendijk/netbox-operator/issues/50)).

A cable terminates on two ends, each polymorphic, and NetBox 4.x permits **several
terminations per end**. The rows live on a separate model, `dcim.CableTermination`, with
`cable`, `cable_end`, `termination_type`, `termination_id`, `connector`, `positions` and four
denormalised `_device` / `_rack` / `_location` / `_site` caches
([`docs/netbox-schema.md`](../netbox-schema.md) → `dcim.CableTermination`). None of that is
writable: `CableTerminationSerializer.Meta` sets `read_only_fields = fields`
(`netbox/dcim/api/serializers_/cables.py:71`), so **the whole `dcim/cable-terminations`
endpoint is read-only** and there is no Kind for it
([`coverage.md`](../coverage.md)).

The writable form is on the cable itself:

```
a_terminations  GenericObjectSerializer(many=True, required=False)   dcim/api/serializers_/cables.py:40
b_terminations  GenericObjectSerializer(many=True, required=False)   same
```

and `GenericObjectSerializer` is `{object_type, object_id}` plus a read-only `object`
(`netbox/netbox/api/serializers/generic.py:15`). So the pair is **nested inside a list
element**, under keys of its own, and there are no sibling columns of the cable at all.

The to-one shape fits in neither respect, which is worth stating plainly because it is the
verdict NBO-049 existed to reach:

| | To-one pair | `dcim.Cable`'s terminations |
|---|---|---|
| Cardinality | one `(type, id)` | zero or more |
| Where the pair lives | two top-level columns | two keys inside a list element |
| What the payload carries | `scope_type`, `scope_id` | `a_terminations: [{object_type, object_id}]` |
| Drift rule | [rule 7](drift.md), the pair as a unit | [rule 9](drift.md), a set of pairs |

### The CR side did not change

`CableTerminationTarget` is an ordinary union: one member per legal target, the `== 1` rule,
and no discriminator — exactly the shape [`IPAssignment`](../reference/genericref.md) has. It
is *used* to-many, as a bounded list, and that is a fact about the field rather than about the
union:

```go
// +kubebuilder:validation:MinItems=1
// +kubebuilder:validation:MaxItems=16
ATerminations []CableTerminationTarget `json:"aTerminations"`
```

So a user who has read [the union shape](../reference/genericref.md) needs to learn nothing
new, and the eight members whose Kinds have not landed report
[`RefKindUnavailable`](#kinds-that-do-not-exist-yet) exactly as `deviceRef` does on a contact
assignment.

### The Descriptor side did

One new field, and everything else about the pair is unchanged:

```go
GenericFKs: []registry.GenericFKSpec{{
    // On a to-many pair these two are *filter* names rather than columns -- see below.
    TypeField:    "termination_a_type",
    IDField:      "termination_a_id",
    Spec:         "aTerminations",
    AllowedTypes: cabledObjectTypes(),
    Members:      members,
    List: &registry.GenericFKList{
        APIField: "a_terminations",   // the one field the whole list is written as
        TypeKey:  "object_type",      // the keys the pair takes *inside* an element
        IDKey:    "object_id",
    },
}}
```

`GenericFKList.APIField` is what goes in `RecreateOn` and what
[drift](drift.md) reports a change against, so a to-many pair's diff is **one change on one
field** rather than N. `TypeKey` and `IDKey` are declared rather than constant because they
are the *serializer's* names and not the model's — the columns behind them are
`termination_type` and `termination_id` — and a second to-many pair on a serializer that
spells them differently must not have to edit the struct.

`Validate` refuses three shapes at boot: a `List` missing any of the three names (a list
written under no field name reaches no payload, and an element missing either key is half a
reference), a to-many pair declaring `Cached` (a cable's `_device`, `_rack`, `_location` and
`_site` are columns of the *termination row* and not of the kind carrying the list, so the
engine has nowhere to read or write them), and an `APIField` an ordinary `Field` also claims.

### What the engine does with it

Three places, and none of them is a branch on kind:

- **Resolution.** `decodeUnions` is the only code in `internal/resolver` that reads
  `List` at all: it turns the spec value into one union per element, and everything
  downstream works a single union at a time. So a cable's list of terminations resolves
  through exactly the code one prefix's scope does — same four modes, same grant check, same
  typed errors. `resolver.FieldRefs` was already a slice, so the to-one case files a
  one-element one and this files N; no new carrier was needed.
- **All-or-nothing per field.** One of two ends resolving is worse than neither: NetBox
  replaces the termination rows from what the field carries, so a half list is a cable
  connected at one end — and on a `Recreate` kind, correcting that means delete-and-create
  rather than a PATCH. The rule is [`resolveField`'s](references.md), applied to a union list.
- **Rendering.** `applyGenericFKList` sorts by `(object type, id)` and deduplicates before
  writing, for the reason `FieldRefs.IDs` does: NetBox does not preserve the order, so the
  order the spec listed them in is not data and rendering it would invite a reader to believe
  an order the comparison ignores. Duplicates collapse because
  `unique(termination_type, termination_id)` makes two rows on one object impossible anyway.

### A message names the element

A blocked element reports under its **indexed** path, because a cable end may carry several
and the union's own name would not say which:

```
RefsResolved  False  RefKindUnavailable
  bTerminations[0].rearPortRef -> netboxrearport/team-a/panel-1-rear-14: target kind unavailable
```

Unindexed on a to-one pair, so no existing message changes.

### A natural key over a to-many pair

`ipam.VLANGroup` showed that a pair can be matched on by column name. `dcim.Cable` needs
something harder: it has **no `meta.constraints` at all**, so the terminations are the only
identity there is — and a *list* has no single value a filter could take.

The answer is a **representative element**. `applyGenericFKList` files the first element
after sorting under the pair's `TypeField` and `IDField`, which on a to-many pair are
therefore *filterset parameter* names rather than columns:

```
termination_a_type  MultiValueContentTypeFilter over terminations__termination_type   dcim/filtersets.py:2637
termination_a_id    MultiValueNumberFilter, method filter_by_cable_end_a              same
termination_b_type  MultiValueContentTypeFilter over terminations__termination_type   same
termination_b_id    MultiValueNumberFilter, method filter_by_cable_end_b              same
```

Two properties make that sound rather than a guess. **Sorted, so the question is stable**:
the same cable written with its terminations in a different order asks the same query, which
is what makes "reordering produces zero API writes" true of the lookup as well as of the diff.
And **one element is enough**, because of a constraint on the other model:
`unique(termination_type, termination_id)` means an object is terminated by at most one cable,
globally — so an A-end termination names one cable or none. See
[`reference/netboxcable.md`](../reference/netboxcable.md) for what a `Conflict` means when the
key is a query rather than a constraint.

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
deadlock.

A `REQ` pair does block, and two have now shipped:
[`tenancy.ContactAssignment.object_*`](../reference/netboxcontactassignment.md) and
[`dcim.CableTermination.termination_*`](../reference/netboxcable.md). Neither is walked, and in
both cases that is a fact about *that* pair rather than a gap left open — each object is a
**leaf in the reference graph**, so a ring through it is unconstructible rather than unchecked:

- **Nothing in NetBox points at a ContactAssignment**, so there is no `contactAssignmentRef`
  anywhere in this API.
- **Nothing points at a Cable that this API can write.** `dcim.CabledObjectModel.cable` is a
  real column on every cabled component, and `CabledObjectSerializer` declares it
  `read_only=True` (`netbox/dcim/api/serializers_/cables.py:110`) — NetBox sets it itself when
  the cable is created. So there is no `cableRef` on any Kind here either, and the eight-plus
  component Kinds that will point *at* a cable's terminations cannot point back.

The Descriptor flag that makes the walk follow a blocking pair is therefore still unwritten, and
the union that needs it is `ipam.Service`'s `parent_object_*`, whose targets *are* pointed at by
other Kinds.

**It contributes an owner reference exactly as a typed reference does.**
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 has a containment generic FK
contribute a non-controller owner reference, and `Descriptor.ContainmentRef` accepts a generic
FK's spec field. This needed no separate implementation: `ResolveAll` files a resolved union
under the *union's own* spec field, keyed the same way an ordinary reference is, so the
ownership step reads it with the same lookup. A member whose target Kind is not registered is
refused as `RefKindUnavailable` and therefore never owned — see
[ownership](ownership.md#an-unregistered-target-kind).

**Reverse accessors are absent from every spec.** `nat_outside` and `l2vpn_terminations`
are Django `GenericRelation` reverse accessors, not columns. They are read-only views of
somebody else's foreign key and there is nothing to write.

## Natural keys

A lookup on a polymorphic pair needs **two** filters, and the union's own spec field has no
single value to offer one — so a natural key names the pair by its two **column** names
instead:

```go
NaturalKeys: []registry.NaturalKey{
    {Fields: []registry.KeyField{
        {Filter: registry.ScopeTypeField, Spec: registry.ScopeTypeField},
        {Filter: registry.ScopeIDField, Spec: registry.ScopeIDField},
        {Filter: "slug", Spec: "slug"},
    }},
    {
        Fields: []registry.KeyField{{Filter: "slug", Spec: "slug"}},
        NullFields: []registry.NullField{
            {Filter: registry.ScopeTypeField, Spec: "scope"},
            {Filter: registry.ScopeIDField, Spec: "scope"},
        },
    },
}
```

`applyGenericFK` writes the resolved pair into the decoded spec under those two names, so each
half then renders exactly as `{Filter: "vrf_id", Spec: "vrfRef"}` does, and
`declaresSpecField` accepts them so a misspelling still fails at boot. `ipam.VLANGroup` is the
kind this exists for: it is unique on `(scope_type, scope_id, slug)` and
`(scope_type, scope_id, name)` (`docs/netbox-schema.md` → `ipam.VLANGroup`,
`meta.constraints`) and could not state its own identity otherwise — see
[`reference/netboxvlangroup.md`](../reference/netboxvlangroup.md).

Three properties of that shape are load-bearing rather than incidental:

- **Both halves or neither.** A generic FK's id is only unique *within* its type, so
  `?scope_id=31&slug=mgmt` matches a group scoped to the site with id 31 and one scoped to the
  region with id 31 alike. It is the same argument as [the atomic
  pair](#why-the-pair-is-atomic), one step further out.
- **The type half is the `app_label.model` string, not a ContentType id.** That is what the
  filterset takes: `VLANGroupFilterSet` declares
  `scope_type = MultiValueContentTypeFilter()` (NetBox 4.6.8,
  `netbox/ipam/filtersets.py:948`), which splits the value on `.` and resolves it through
  `ContentType.objects.get_by_natural_key` (`netbox/utilities/filters.py:186-207`).
  `scope_id` is an ordinary filter from `Meta.fields` (`netbox/ipam/filtersets.py:980`).
- **A cleared or unresolved pair resolves neither column.** So the value-matching candidate
  becomes inapplicable and the null-pinned one takes over — which is the whole difference
  between "globally scoped" and "scoped to something that does not exist yet". A union that is
  *declared* and unresolved matches neither candidate and the engine waits, rather than
  adopting the global object of the same slug and PATCHing a scope onto it.

The per-target filters `VLANGroupFilterSet` also offers (`?site=3`, `?region=2`, and six more)
are deliberately not used. They would put the union's dispatch table into the natural key as
well, spelled a second way, in a kind whose whole point is that the union is written down
once.

## Adding a union

Three edits, none of them in the engine:

1. **`api/v1alpha1`** — the typed alias for any new target Kind (`TargetGVK`,
   `AsObjectRef`), the union struct with one member per legal target, and the CEL rule in
   the `<= 1` or `== 1` shape. A union used to-many is a bounded list of that struct, and
   needs `+kubebuilder:validation:MaxItems` like any other list of references
   ([a list needs a bound](references.md#a-list-needs-a-bound)).
2. **`internal/registry`** — the `GenericFKSpec` on the referrer's Descriptor: the two
   columns, the spec field, `AllowedTypes`, and one `Members` entry per union member; plus a
   [`GenericFKList`](#the-descriptor-side-did) if the pair is to-many.
3. **Docs** — a row in [`reference/genericref.md`](../reference/genericref.md).

A union more than one kind carries gets a constructor next to it, as
`registry.ScopeFK` is, so the second kind to use it restates nothing.

`internal/reconciler` and `internal/resolver` do not change, and neither does
`internal/controller`. If a union needs a change in any of those three, the thing it needs
is missing from the Descriptor — add it there.
