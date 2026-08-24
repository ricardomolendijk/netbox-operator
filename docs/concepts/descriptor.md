# The Descriptor

One generic engine drives roughly 120 kinds. Everything that differs between kinds is a
value in a `Descriptor` (`internal/registry`) rather than a branch in the engine, so
adding a kind is three new files and zero edits to shared code.

```go
registry.MustRegister(registry.Descriptor{
    GVK:        schema.GroupVersionKind{Group: "netbox.kubeforge.org", Version: "v1alpha1", Kind: "NetBoxTag"},
    Endpoint:   "extras/tags",
    ObjectType: "extras.tag",
    Scope:      apiextensionsv1.NamespaceScoped,
    Fields: []registry.Field{
        {Spec: "name", API: "name"},
        {Spec: "slug", API: "slug"},
        {Spec: "objectTypes", API: "object_types", Class: registry.ClassObjectTypeList},
    },
    NaturalKeys:    []registry.NaturalKey{{Fields: []registry.KeyField{{Filter: "slug", Spec: "slug"}}}},
    UpdateStrategy: registry.UpdatePatch,
    ReadOnly:       []string{"created", "last_updated", "url", "display"},
})
```

## Why the per-kind facts are data

[`CONTRIBUTING.md`](../../CONTRIBUTING.md) states the primary architectural constraint:
adding a kind means **adding files, never editing shared logic**. A kind is exactly three
additions — a spec struct, a `Descriptor`, a controller that delegates — and no
modifications. There is no `switch` on kind anywhere in the reconcile path; that switch is
the specific smell the rule exists to prevent. If a kind needs behaviour the engine cannot
express, the missing behaviour belongs in the `Descriptor` as data, not in the engine as a
branch.

**Nothing in a `Descriptor` is a `func`.** This is the constraint that makes the rest work.
A closure cannot be emitted by a template, printed in a diff, serialised, or checked by a
linter, so a `Descriptor` field that has to be a Go function puts per-kind logic back into
hand-written code through the back door — and the M7 generator cannot produce it at all.
The natural-key type is the case in point: it was originally specified as a slice of
functions and is now a slice of structs for exactly this reason.

The practical test of whether this held is not a unit test. It is that the generator's
golden output contains no `{{if eq .Model "…"}}`.

## The fields

| Field | Type | What it is | Example |
|---|---|---|---|
| `GVK` | `schema.GroupVersionKind` | The Kubernetes kind this descriptor drives. | `netbox.kubeforge.org/v1alpha1, Kind=NetBoxIPAddress` |
| `Endpoint` | `string` | REST path relative to `/api`. Looked up, never derived by pluralising. | `ipam/ip-addresses` |
| `ObjectType` | `string` | The Django `app_label.model` spelling other kinds use to point at this one through a generic FK. One source for it. | `ipam.ipaddress` |
| `Scope` | `apiextensionsv1.ResourceScope` | The CRD scope. | `Namespaced` |
| `NaturalKeys` | `[]NaturalKey` | Lookup candidates, tried in the order given. | `(address, vrf_id)`, then `(address, vrf_id__isnull)` |
| `UpdateStrategy` | `UpdateStrategy` | `Patch` or `Recreate`. No zero value. | `Patch` |
| `RecreateOn` | `[]string` | API fields whose change forces delete-then-create. | `["a_terminations", "b_terminations"]` |
| `Deferred` | `[]DeferredField` | Fields kept out of the create payload and applied by a follow-up PATCH. | `{APIField: "primary_ip4", Mode: "Always"}` |
| `ReadOnly` | `[]string` | Fields the operator must never write. | `["_depth", "_children", "created", "last_updated", "url", "display"]` |
| `Fields` | `[]Field` | The spec-to-API map, and the field classes. See [field classes](#field-classes). | `{Spec: "importTargets", API: "import_targets", Class: RefMany, Target: …}` |
| `GenericFKs` | `[]GenericFKSpec` | The polymorphic `*_type` / `*_id` column pairs on this kind. | `{scope_type, scope_id, scope, [dcim.region dcim.sitegroup dcim.site dcim.location], [regionRef siteGroupRef siteRef locationRef]}` |
| `ContainmentRef` | `string` | The one spec ref whose target gets a non-controller owner reference. Empty for catalogue kinds. | `siteRef` |

`GenericFKSpec` is five fields. `TypeField` and `IDField` are the two columns; `Spec` is the
one CR field they are both written from — declared here rather than in `Fields`, because a
`Field` maps one spec name to one API name and this reference has two. `AllowedTypes` is what
NetBox accepts in the type column, in the same spelling as `ObjectType`. `Members` is the
resolver's dispatch table: one entry per union member, each naming the CR field that selects
it and the *Kind* it resolves against — the Kind and never the type string, so the
`app_label.model` spelling stays written down in exactly one place.

The last two are cross-checked at boot in both directions, so a union that offers a target
NetBox would reject in that column fails to start. Adding a member to a union is therefore a
change to `api/v1alpha1` and this table and nothing else. See
[Generic references](generic-refs.md).

Four entries in that table are worth spelling out.

`Endpoint` is a lookup, not a rule. `virtualization.VMInterface` lives at
`virtualization/interfaces` and `dcim.VirtualChassis` at `dcim/virtual-chassis`
(`docs/netbox-schema.md`, endpoint map) — pluralising the model name gives the wrong path
for both.

`ObjectType` is lowercased and unpunctuated, because that is the Django `ContentType`
spelling the REST API accepts for the type half of a generic FK (`docs/netbox-schema.md`,
preamble). It is `virtualization.vminterface`, never `virtualization.VMInterface`, and
`Validate` rejects the latter.

`ReadOnly` is seeded from the schema doc, which marks the classes explicitly: every
`_`-prefixed cached column (`_site`, `_depth`, `_children`), every `CounterCacheField`, and
`created` / `last_updated` / `url` / `display` (`docs/netbox-schema.md`, preamble). Writing
one of these does not fail — it silently no-ops, so the next reconcile finds the same
difference and PATCHes again. A read-only field in a write payload is a hot loop, not an
error.

`Scope` is `Namespaced` for every kind in `v1alpha1`
([ADR-0002](../decisions/0002-crd-scoping.md)). The field exists anyway, because CRD scope
is a single immutable value per CRD: promoting a kind is a new API version, and the
generator carries scope as a per-kind attribute so that a promotion is a generator change
rather than a redesign.

## Natural keys

A natural key is the set of filters that identifies at most one NetBox object. The engine
uses it to decide create-versus-adopt before the CR has a `status.id`; a kind without one
duplicates every object it was pointed at, on every fresh cluster. `Validate` rejects a
descriptor with no candidates for that reason.

`NaturalKeys` is an **ordered list of candidates**, and more than one is the normal case
rather than a fallback. Two independent reasons force that shape:

1. **Some models have no uniqueness to lean on.** `ipam.Prefix` and `ipam.IPAddress` have
   **no** `meta.constraints` at all — the schema entry lists indexes only
   (`docs/netbox-schema.md` → `ipam.Prefix`, `ipam.IPAddress`). Their identity is therefore
   a convention, and a convention is expressed as a priority list. It also means more than
   one match is a legitimate state rather than a malformed descriptor; what the engine does
   about it is in
   [errors and retries](errors-and-retries.md#why-ambiguity-is-an-error).
2. **A constraint can be conditional.** NetBox's nested-group kinds are unique on
   `(parent, name)` *plus* a separate `name WHERE parent IS NULL`. Those are two different
   queries, so they are two candidates.

`Candidates(state)` filters the declared list down to the candidates usable right now,
preserving declaration order. The engine tries them in turn.

### Worked example: `dcim.Region`

`dcim.Region` is unique on `(parent, name)`, with a second constraint on `(name)` under the
condition `parent IS NULL` (`docs/netbox-schema.md` → `dcim.Region.meta.constraints`). Two
candidates:

```go
NaturalKeys: []NaturalKey{
    {Fields: []KeyField{
        {Filter: "parent_id", Spec: "parentRef"},
        {Filter: "name", Spec: "name"},
    }},
    {
        Fields:     []KeyField{{Filter: "name", Spec: "name"}},
        NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef"}},
    },
}
```

| CR state | Candidates | Query sent |
|---|---|---|
| `name` and `parentRef` set, parent resolved | 1 | `?parent_id=<id>&name=eu-west` |
| `name` set, no `parentRef` | 1 | `?name=eu-west&parent_id__isnull=true` |
| `name` and `parentRef` set, parent **not yet resolved** | 0 | nothing is sent |

### Worked example: `ipam.IPAddress`

`ipam.IPAddress` has no `meta.constraints`. Identity is four candidates in priority order:
the assignment disambiguates a shared address, and every candidate constrains `vrf_id` one
way or the other.

```go
address  := KeyField{Filter: "address", Spec: "address"}
vrf      := KeyField{Filter: "vrf_id", Spec: "vrfRef"}
noVRF    := NullField{Filter: "vrf_id", Spec: "vrfRef"}
assigned := []KeyField{
    {Filter: "assigned_object_type", Spec: "assignedObject"},
    {Filter: "assigned_object_id", Spec: "assignedObject"},
}

NaturalKeys: []NaturalKey{
    {Fields: append([]KeyField{address, vrf}, assigned...)},
    {Fields: append([]KeyField{address}, assigned...), NullFields: []NullField{noVRF}},
    {Fields: []KeyField{address, vrf}},
    {Fields: []KeyField{address}, NullFields: []NullField{noVRF}},
}
```

| CR state | Candidates produced, in order |
|---|---|
| `address`, `vrfRef`, `assignedObject` all resolved | `(address, vrf_id, assigned_object_type, assigned_object_id)`, then `(address, vrf_id)` |
| `address`, `assignedObject`, no `vrfRef` | `(address, assigned_object_type, assigned_object_id, vrf_id__isnull)`, then `(address, vrf_id__isnull)` |
| `address`, `vrfRef` only | `(address, vrf_id)` |
| `address` only | `(address, vrf_id__isnull)` |

Note what falls out of the ordering: an assigned address always tries the assignment-bearing
candidate first and the bare one second, so a match on the assignment wins, and an address
that has been moved between interfaces is still found rather than duplicated.

### A null pin is not an omitted filter

`NullField` exists because "this foreign key is null" and "do not filter on this foreign
key" are different queries with different results, and the difference is silent.

`NullField.Param()` emits the Django `isnull` lookup — `vrf_id__isnull`, sent as `true`.
Omitting `vrf_id` instead matches that address **in every VRF**, so a global address adopts
an identical address out of somebody's VRF and starts managing it. [Lookups](lookups.md)
covers the query side, including why the value has to be the literal `true`.

The pin is also **state-dependent**, which is the part that is easy to get wrong. A
candidate that pins `parent_id` to null asserts the parent is unset, so it may only be used
while `parentRef` is unset. That is why the filter is paired with the spec field it
corresponds to:

```go
NullFields: []NullField{{Filter: "parent_id", Spec: "parentRef"}}
```

Without the `Spec` half there is nothing to consult, so a *child* Region whose parent has
not been created yet falls through to the top-level candidate, finds an unrelated top-level
Region of the same name, adopts it — and the follow-up PATCH reparents someone else's data.

### Declared and resolved are tracked separately

`Applicable` reads a `SpecState` with two lists, and each half of a candidate uses a
different one:

```go
type SpecState struct {
    Declared []string   // spec fields the user set, resolved or not
    Resolved []string   // spec fields that currently hold a filterable value
}
```

- A **matched** field must appear in `Resolved`. An optional key the user left unset makes
  that candidate inapplicable, which is how `ipam.VRF` falls from `(rd)` to `(name)` — `rd`
  is column-unique but optional, `name` is required and not unique
  (`docs/netbox-schema.md` → `ipam.VRF`) — and how `dcim.Device` falls from
  `(name, site, tenant)` to the tenant-is-null variant.
- A **null-pinned** field must be absent from `Declared`. Declared-but-unresolved is the
  dangerous state: the user asked for a parent, so the object is not top-level, but the ID
  is not available yet.

The consequence is that a kind whose `parentRef` is declared but not yet resolvable produces
**no candidate at all**. `Candidates` comes back empty, the engine waits, and it writes
nothing. That is the correct outcome, and it is only reachable because the two lists are
distinct: with a single list the object either looks top-level, and adopts a stranger, or
looks resolved, and gets filtered on an ID that is not there.

### Case-insensitive lookup

`Lookup` is a NetBox filter-expression modifier, appended to a query parameter after a
double underscore. Two values are permitted:

| `Lookup` | Emits | Use |
|---|---|---|
| `LookupExact` (`""`, the zero value) | `?slug=eu-west` | everything, by default |
| `LookupIExact` (`"ie"`) | `?name__ie=dns` | fields unique on `lower(name)` |

`internal/netbox` mirrors these constants under the same names. They are repeated rather
than shared because neither package imports the other, and because the strings are NetBox's
wire format rather than a choice either package gets to make.

`dcim.Device` is unique on `(Lower('name'), site, tenant)` and
`virtualization.VirtualMachine` on `(Lower('name'), cluster, tenant)`
(`docs/netbox-schema.md` → `dcim.Device.meta.constraints`,
`virtualization.VirtualMachine.meta.constraints`). An exact lookup for a CR named `dns` does
not find an existing device called `DNS`, so the engine goes on to create a second one —
which NetBox either rejects or accepts under a different case. This modifier is the
difference between adopting the existing device and duplicating it, which is why it is a
per-field property of the natural key rather than a client-wide setting.

The zero value is the exact match, so a field that declares nothing gets the conservative
behaviour. Substring, prefix and negation lookups are deliberately absent from the permitted
set: a natural key has to identify at most one object, and those cannot.

How these get rendered into a query string, and the step-by-step of the duplicate they
prevent, is in [lookups](lookups.md).

## `UpdateStrategy` and `RecreateOn`

`UpdateStrategy` is how a diff becomes writes:

| Value | Behaviour |
|---|---|
| `Patch` | PATCH the diff. Every kind, unless its identity lives somewhere a PATCH cannot reach. |
| `Recreate` | Delete the object and create a replacement. |

`Validate` rejects the zero value. Whether an update destroys the object first is not a
thing to default silently.

`dcim.Cable` is why `Recreate` exists. A cable's identity lives in its terminations, and
`unique(termination_type, termination_id)` keeps the wanted endpoint occupied by the old
cable until it is deleted (`docs/netbox-schema.md` →
`dcim.CableTermination.meta.constraints`), so the replacement cannot be created first.
Declaring that as data is the alternative to `if kind == Cable` in the engine.

**The enum alone is not enough data**, which is what `RecreateOn` is for. A cable's `label`
(`docs/netbox-schema.md` → `dcim.Cable`) is an ordinary PATCH; the membership of its
termination lists is not. A bare `UpdateStrategy: Recreate` would make every label edit
destructive — delete the cable, drop the connection, create a new one — so `RecreateOn`
names the fields whose change actually forces the destructive path, and every other field
still PATCHes in place.

`Validate` rejects `RecreateOn` on a kind whose strategy is `Patch`: identity-bearing fields
declared on a kind that updates in place means one of the two is wrong, and it will not be
obvious which.

## `ContainmentRef` is singular

`ContainmentRef` is the one spec field whose target gets a **non-controller** owner
reference, so that `kubectl delete netboxsite home` also removes the prefixes scoped to it
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). Non-controller so it
never competes with inline-child materialisation, which needs the single controller
reference; garbage collection counts both, so the cascade still works.

It is a `string`, not a `[]string`, and the type is the enforcement because the failure it
prevents is silent. Kubernetes garbage collection deletes a dependent only once **every**
owner is gone, so two containment owners give AND semantics: the object survives until both
parents are deleted. A user reading the manifest expects OR — "delete the site *or* the VRF
and the prefix goes" — and the gap shows up only as an object that refuses to disappear.

So each kind nominates exactly one, and it is normally the required FK: `siteRef` for
`NetBoxDevice`, `scopeRef` for `NetBoxCluster` and `NetBoxPrefix`, `parentRef` for the
nested-group kinds, `assignedObject` for `NetBoxIPAddress`. Catalogue references contribute
none — a catalogue is not a parent — so an empty `ContainmentRef` is the normal value for a
catalogue kind.

`NetBoxIPAddress` is on that list for a correctness reason rather than an aesthetic one, and
[ADR-0003](../decisions/0003-ownership-and-references.md) sets out the argument: NetBox
deletes an interface's addresses server-side through a `GenericRelation`, so without the
owner reference the CR outlives the object it described and the engine's create-if-absent
step resurrects data NetBox deliberately deleted. The general rule is *server-side cascade
implies an owner reference*.

## Field classes

Every entry in `Fields` carries a **class**, and the class is the single declaration of two
things that are really one fact: **how many** objects the field holds, and **how** its value
is compared.

| `Class` | Spec shape | Written as | Resolved from | Compared |
|---|---|---|---|---|
| `Value` (zero) | a scalar | the value | — | scalar, with NetBox's normalisations |
| `RefOne` | one `ObjectRef` | one NetBox id | the target CR or NetBox | scalar |
| `RefMany` | a list of `ObjectRef` | a list of NetBox ids — `[1, 2]` | one resolution per element | ID set, order-independent |
| `ObjectTypeList` | a list of strings | the same strings — `["dcim.device"]` | nothing; the strings *are* the value | string set, order-independent |
| `Array` | a list of scalars | the same list — `[80, 443]` | — | order-**sensitive** |

`Value` is the zero value, so a plain column stays a spec name and an API name.

The comparison sets `netbox.Drift` needs are **derived** from these classes —
`Descriptor.M2MFields()`, `ObjectTypeListFields()`, `ArrayFields()` — rather than declared
beside the field map. That is not tidying. Before
[NBO-088](https://github.com/ricardomolendijk/netbox-operator/issues/141) a reference was a
bool, `Ref: true`, which said *that* a field was a reference and nothing about how many; a
separate `M2M` list said how the same field's *value* compared. So a to-many reference was
described twice, by two declarations that could disagree, and nothing joined them. The
resolver, having no cardinality to dispatch on, skipped every JSON array and the engine
reported it `NotImplemented` — which meant no to-many reference in the catalogue was
implementable at all, `tags` and `ipam.VRF.import_targets` included.

One class per field makes that contradiction unrepresentable rather than merely checked.

### `RefMany` versus `ObjectTypeList` versus `Array`

All three arrive as JSON lists and none of them can share a class.

`extras.Tag.object_types` is a `ManyToManyField` onto `contenttypes.ContentType`
(`docs/netbox-schema.md` → `extras.Tag`), so its API values are `app_label.model` strings
rather than object ids. A resolver told to resolve one goes looking for a CR named
`dcim.device` — a CR that cannot exist, because a content type is not a NetBox object this
operator manages.

`ipam.VRF.import_targets` and `export_targets` are `RefMany`: `ManyToManyField` onto
`ipam.RouteTarget` (`docs/netbox-schema.md` → `ipam.VRF`), written as route-target ids and
resolved one element at a time.

`ipam.VLANGroup.vid_ranges` and `ipam.Service.ports` are `Array`s — Postgres `ArrayField`s
whose order is data. Comparing an array order-independently misses a reordering the user
asked for; comparing an M2M order-sensitively PATCHes forever, because NetBox does not
preserve M2M order. The comparison rules are rules 3, 5 and 8 in
[drift detection](drift.md).

### A to-many reference resolves whole or not at all

Resolution is keyed by field, and a field appears in the result **only when every element
resolved**. Three of five tags is not a smaller version of the right answer: NetBox's M2M
write replaces the list rather than adding to it, so writing the three that resolved deletes
the two that did not — and reports a successful write while doing it.

So a partially resolvable list contributes nothing to the payload, and each element that
failed is reported as its own blocker, with its own reason and its own retry interval. See
[references](references.md#what-happens-when-it-does-not-resolve).

The ids are written **sorted and deduplicated**. NetBox does not preserve M2M order and
`Drift` compares the field as a set, so the order the spec listed them in is not data —
carrying it into the payload would advertise an ordering that nothing downstream honours.

## Deferral, and the identity guard

`Deferred` names fields left out of the create payload and applied by a follow-up PATCH.

| `Mode` | Meaning |
|---|---|
| `Always` | the reference cannot exist at create time by construction |
| `IfUnresolved` | include it in the create payload when it resolves; defer only when it does not |

`Always` is for `dcim.Device.primary_ip4`, which needs an address that needs an interface
that needs the Device (`docs/netbox-schema.md` → `dcim.Device`). No apply order fixes that.

`IfUnresolved` is for a `parent`. Deferring a parent unconditionally creates the object as
top-level, where it can adopt an unrelated top-level object of the same name — and the
follow-up PATCH then reparents *that* object.

`ErrDeferredNaturalKey` is the guard: a field a candidate matches on may not be deferred
`Always`, because the object would be created under the wrong identity. Deferring the same
field `IfUnresolved` is legal, because the deferral only happens in the state where no
candidate matching that field is applicable anyway.

The check reconciles the two spellings of a foreign key before comparing, since a field is
written as `parent` and filtered as `parent_id`. Null pins are exempt: a candidate pinning a
field to null asserts the field is unset, which is exactly the state a create with that
field deferred is in, so the deferral cannot corrupt that identity.

## What `Validate()` rejects

`Validate` runs at manager start, over every registered descriptor, and reports every fault
at once rather than the first. A bad descriptor fails the boot, not a reconcile hours later.
Each failure is a distinct sentinel error, so callers and tests classify by type rather than
by matching a message.

| Sentinel | Triggered by |
|---|---|
| `ErrEmptyGVK` | a `GroupVersionKind` with group, version *and* kind all empty |
| `ErrNoEndpoint` | an empty `Endpoint` |
| `ErrInvalidObjectType` | an `ObjectType`, or a `GenericFKSpec.AllowedTypes` entry, that is not `^[a-z_]+\.[a-z0-9_]+$` |
| `ErrUnknownScope` | a `Scope` that is neither `Namespaced` nor `Cluster` |
| `ErrNoNaturalKey` | `NaturalKeys` empty |
| `ErrNoKeyFields` | a candidate with only null pins and nothing matched by value |
| `ErrEmptyFilter` | a `KeyField` or `NullField` missing its `Filter` or its `Spec` |
| `ErrUnknownLookup` | a lookup modifier other than `""` or `"ie"` |
| `ErrNullFieldConflict` | one filter both pinned to null and matched against a value in the same candidate |
| `ErrDeferredReadOnly` | a field in both `Deferred` and `ReadOnly` |
| `ErrDeferredNaturalKey` | an **unconditionally** deferred field that a candidate matches on |
| `ErrUnknownDeferMode` | a `Mode` other than `Always` or `IfUnresolved` |
| `ErrUnknownUpdateStrategy` | an `UpdateStrategy` other than `Patch` or `Recreate`, the empty string included |
| `ErrRecreateOnWithoutRecreate` | `RecreateOn` set on a kind whose strategy is `Patch` |
| `ErrUnknownFieldClass` | a `Field.Class` other than `Value`, `RefOne`, `RefMany`, `ObjectTypeList` or `Array` |
| `ErrTargetNotRef` | a `Target` on a field whose class is not `RefOne` or `RefMany` |
| `ErrToManyNaturalKey` | a natural-key candidate that matches on, or pins to null, a `RefMany` field |
| `ErrContainmentToMany` | a `ContainmentRef` naming a `RefMany` field |
| `ErrEmptyField` | an empty string in `ReadOnly` or `RecreateOn`, or a `Deferred` entry with no `APIField` |
| `ErrInvalidGenericFK` | a `GenericFKSpec` missing its `TypeField`, its `IDField`, or its `AllowedTypes` |
| `ErrDuplicateGVK` | the same GVK registered twice; the first registration wins, and `Registry.Validate` reports the collision as well |

There is no check that a field is to-many for resolution and scalar for comparison, because
one class decides both — the two cannot disagree. What remains checkable is a to-many
reference used somewhere exactly one object is required, and both of those fail *quietly*
rather than loudly, which is why they are boot checks:

- A natural-key filter carries one value. A to-many field renders none, so the candidate is
  never applicable and the object waits forever for an identity that cannot be built.
- A containment parent is one object, because Kubernetes garbage collection waits for every
  owner reference ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). A list
  of parents turns "delete the site" into "delete any one of them".

### The one thing it deliberately permits

**`NaturalKey ∩ ReadOnly` is legal**, and necessary. `virtualization.Cluster` is unique on
`(group, name)` and on `(_site, name)` (`docs/netbox-schema.md` →
`virtualization.Cluster.meta.constraints`). `_site` is a `CachedScopeMixin` column
(`docs/netbox-schema.md` → `dcim.CachedScopeMixin`) that the operator must never write —
writing it silently no-ops — but must be able to *filter* on, as `site_id`. So the cluster
descriptor lists `_site` in `ReadOnly` and keys a candidate on `site_id`, and `Validate`
says nothing about it. Read-only means "never in a write payload", not "never in a query".

## Registration

Each kind registers itself from one `init()`:

```go
func init() { registry.MustRegister(descriptor()) }
```

`MustRegister` panics on a duplicate GVK. Registration happens in an `init()`, where a
returned error is easy to drop, and a duplicate kind is a programming error that must stop
the process at boot rather than surface as a reconcile failure hours later. `Registry.Add`
returns the error instead, for tests and for callers that own their own registry.

`List()` is ordered by GVK string. Callers log, validate and generate from it, and
map-ordered output makes all three unreviewable.
