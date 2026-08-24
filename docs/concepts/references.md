# References

How one object points at another, and what the API server rejects before the operator
ever sees it.

> **Status.** The `ObjectRef` type, its validation and the typed aliases are built
> (NBO-011), and so is resolution: all four modes, the typed errors and the condition
> vocabulary below (NBO-012). What is not built yet: **watches**
> ([#25](https://github.com/ricardomolendijk/netbox-operator/issues/25)) — until they land,
> a reference that is waiting is retried on the endpoint's resync rather than woken by an
> event; **grants** ([#26](https://github.com/ricardomolendijk/netbox-operator/issues/26)) —
> a cross-namespace reference is resolved without one; **deferred application**
> ([#27](https://github.com/ricardomolendijk/netbox-operator/issues/27)); and **cycle
> detection over a graph** ([#28](https://github.com/ricardomolendijk/netbox-operator/issues/28))
> — only a reference to the referring object itself is caught today.

## What a reference is

A reference names one NetBox object. It is not a NetBox id — except in the one escape
hatch below — because an id is not knowable from a manifest, and a manifest that has to
know one cannot be applied to a fresh NetBox.

The **field name pins the target Kind**. `parentRef` on a `NetBoxRegion` can only be a
`NetBoxRegion`; there is no `kind` discriminator on the wire. A discriminator would make
every ref field accept every Kind and push the check to runtime, so the target is carried
by the field's Go type instead — see [the typed aliases](#the-typed-aliases).

## The four modes

Exactly one of these must be set. The API server enforces that, so a malformed reference
is rejected by `kubectl apply` rather than becoming a condition nobody reads.

| Mode | Shape | Use it when |
|---|---|---|
| `name` | `{name: eu-west}`, optionally `{namespace: catalogue}` | The target is a CR. **Prefer this.** |
| `slug` | `{slug: eu-west}` | The object exists in NetBox and the operator does not manage it. |
| `lookup` | `{lookup: {vid: "20", site: "home"}}` | No slug identifies it — a VLAN's identity is a pair. |
| `id` | `{id: 12}` | Escape hatch. The only place a raw NetBox primary key is accepted. |

`name` is the mode to reach for first, and not only by convention: it is the only one that
expresses a dependency the operator can *wait on*. A `slug` or a `lookup` either resolves
now or does not, whereas a named CR that does not exist yet is a state the operator can
sit in until it does — which is what lets a dependency graph be applied in any order and
still converge.

### `namespace`, and why it is on the common path

Every Kind is namespaced in `v1alpha1`
([ADR-0002](../decisions/0002-crd-scoping.md)), so a team namespace referring to a shared
catalogue namespace is the ordinary shape rather than an exotic one. `namespace` defaults
to the referring object's own, and may only be set together with `name` — a slug or a
lookup is resolved against NetBox, where Kubernetes namespaces do not exist.

Crossing a namespace will require a `NetBoxRefGrant` in the target namespace
([NBO-014 (#26)](https://github.com/ricardomolendijk/netbox-operator/issues/26)). Until
that lands, the field is accepted and validated but the grant is not checked.

## How a reference is resolved

`internal/resolver` is the only place a reference becomes an id. It never writes — to
NetBox or to Kubernetes — so a reference that cannot be resolved cannot have changed
anything on its way to failing.

| Mode | What the operator does | Costs |
|---|---|---|
| `name` | Reads the target CR in `namespace` (default: the referrer's) and takes its `.status.id`. | One cache read. No NetBox call at all. |
| `slug` | `GET /api/<target endpoint>/?slug=<slug>`. | One NetBox read. |
| `lookup` | `GET /api/<target endpoint>/?<filter>`, keys in sorted order so the request is stable. | One NetBox read. |
| `id` | `GET /api/<target endpoint>/<id>/`, to verify the object exists. | One NetBox read. |

The target's endpoint and its `app_label.model` object type come from the **target Kind's
own `Descriptor`**, never from the field name and never from pluralising a Kind. That is
also why a reference to a Kind the operator does not know is a distinct error rather than
a not-found: there is no endpoint to ask.

`name` is the only mode that resolves without talking to NetBox, and the only one that
resolves *into* a dependency the operator can wait on. The other three either answer now
or do not.

### What a resolved reference is used for

Two things, and both matter:

- It is written to the NetBox column the `Descriptor` names — `regionRef` → `region`.
- It becomes the value the **natural key** filters on. `dcim.Region` is unique on
  `(parent, name)` and filters on `parent_id`, so the id `parentRef` resolved to is what
  decides whether the engine creates a region or adopts an existing one
  ([lookups](lookups.md)). A resolver that only fed the payload would create a duplicate
  region on every fresh NetBox.

### Reading a reference does not verify which NetBox it came from

A NetBox id is only meaningful within one NetBox, and `NetBoxEndpoint` is namespaced. A
cross-namespace `name` reference therefore takes an id from a CR whose endpoint *may* be a
different NetBox, and the operator does not currently compare the two. Until it does
(`ErrRefEndpointMismatch`, which arrives with the endpoint work), point references at
namespaces that use the same NetBox.

## What happens when it does not resolve

Every failure is one of six causes. Each maps to exactly one `RefsResolved` reason and one
retry interval; `Ready` reports `WaitingForRef` for all of them, because that is the
question a `kubectl wait` is asking.

| Cause | `RefsResolved` reason | Retried | Why that interval |
|---|---|---|---|
| Nothing to point at | `RefNotFound` | On the resync for a missing CR; **1 min** for a missing NetBox object | A CR's creation is an event the operator receives. NetBox announces nothing, so a timer is the only thing that will notice. |
| Target exists, has no id yet | `RefNotReady` | On the resync (an event, once #25 lands) | The target's own reconcile is what changes this. |
| Several NetBox objects match | `RefAmbiguous` | **10 min** | Only a human can say which one was meant. |
| Cross-namespace, no grant | `RefDenied` | On the resync (a grant event, once #26 lands) | Writing the grant is the fix. |
| The reference points at itself | `RefCycle` | Only on a spec change | No order of reconciles resolves it. |
| Target Kind has no descriptor, or its CRD is not installed | `RefKindUnavailable` | **10 min** | The manifest is correct; the fix is an operator upgrade. |

Two more reasons appear on `RefsResolved` and are not resolution failures at all:
`AllResolved`, and `NotImplemented` for a reference this build cannot dispatch on — a
[generic foreign key](descriptor.md), whose target is a union of Kinds
([NBO-019](https://github.com/ricardomolendijk/netbox-operator/issues/31)), or a **to-many**
reference such as `tags`, since neither `ObjectRef` nor `Field` carries a cardinality. Both
are left out of the payload and reported, which keeps the object off `Ready` rather than
writing one id where a list belongs.

When several references are unresolved, the condition carries the **first** blocker's
reason — a reason is a single value tooling keys on — and a message naming every one of
them. The retry is the **soonest** of their intervals, so a reference that could resolve in
a minute is not held behind one that needs a human.

`RefNotReady` is a state, not a failure. Nothing about it is logged as an error, no Event
is emitted, and no backoff is applied: a graph applied in any order converges only if "the
target does not exist yet" is normal. A target that is *failing* resolves to `RefNotReady`
too — the referrer is genuinely just waiting — but the message quotes the target's own
`Ready` reason, so the reader is sent to the object that is actually broken:

```
regionRef -> netboxregion/catalogue/emea: not ready
  (target Ready=False, Reason=Invalid: "slug must be unique")
```

### An unresolved reference is created without, and reported

The object is **still created**, with the unresolvable reference left out of the payload,
and it reports `Ready=False, Reason=WaitingForRef`
([#132](https://github.com/ricardomolendijk/netbox-operator/issues/132)). The alternative —
writing nothing at all — turns an optional field into a required one and stops a
half-applied graph from making any progress.

What makes that safe rather than silent is the `Ready` half. Without it, a reference that is
not part of the kind's identity would be dropped while the object reported `Ready=True`:
`kubectl apply` succeeds, `kubectl wait --for=condition=Ready` passes, and NetBox never
receives the value. A unit test asserts exactly that combination cannot occur.

Where the reference **is** part of the identity — `parentRef` on a `NetBoxRegion` — nothing
is written at all, one step later and for a better reason: no natural-key candidate is
applicable, so the engine cannot tell whether the object exists and reports
`Ready=False, Reason=WaitingForKey` while `RefsResolved` names the reference behind it.
Creating there would duplicate the region, and falling through to the top-level candidate
would adopt an unrelated one ([lookups](lookups.md)).

A reference that resolved yesterday and does not today does **not** clear the NetBox field.
Spec omission means "do not manage": the object stops writing that column, reports
`RefsResolved=False`, and leaves the live value alone.

An **absent** reference and a **present but empty** one are two different states, not one.
An absent field is not resolved, not blocked and not reported — nobody asked for it. A
present `{}` is refused (`RefsResolved=False, Reason=Invalid`), because a reference that
names no object names nothing, and the API server rejects it before the operator sees it
anyway. Neither one clears a live NetBox value: clearing a foreign key will be an explicit
instruction, on the back of the field ownership server-side apply makes readable
([#121](https://github.com/ricardomolendijk/netbox-operator/issues/121)).

### Why is my object waiting?

```console
$ kubectl get netboxsite ams -o jsonpath='{.status.conditions[?(@.type=="RefsResolved")]}'
{"type":"RefsResolved","status":"False","reason":"RefNotReady",
 "message":"regionRef -> netboxregion/team-a/emea: not ready (the target has no status.id yet)"}
```

Read it right to left: the reason names the class of problem, the message names the field
you wrote and the object it pointed at. From there:

- `RefNotReady` → look at the target's own `Ready` condition. If the message already quotes
  one, the target is the broken object, not this one.
- `RefNotFound` on a `name` → the CR does not exist. Check the namespace: it defaults to the
  referrer's, not to the one the catalogue lives in.
- `RefNotFound` on a `slug`, `lookup` or `id` → NetBox does not hold it. Nothing will
  announce it appearing, so this retries on a timer.
- `RefAmbiguous` → the message lists the matching ids. Replace the `slug` with a `lookup`
  that adds whatever distinguishes them, or with the `id`.
- `RefKindUnavailable` → the operator has no descriptor for that Kind, or the CRD is not
  installed. Your manifest is fine.

## What the API server rejects

Five CEL rules live on the `ObjectRef` type itself, so a new ref field cannot forget them.

| Rejected | Why |
|---|---|
| Two modes at once, or none | A reference that names two objects names neither. |
| `{name: ""}` | Emptiness is checked, not merely presence — an empty string is not a name. |
| `namespace` without `name` | Meaningless for the NetBox-side modes. |
| `{id: 0}` | NetBox primary keys start at 1, so zero is never an object. `ID` is a pointer precisely so `0` is distinguishable from unset. |
| A `lookup` key that is not a lowercase NetBox filter name | Catches `Site` for `site` before it becomes a silent no-match. |
| `lookup` containing `limit`, `offset`, `format`, `brief`, `ordering` or `q` | Pagination and formatting are not identity. |
| A `lookup` value over 200 characters | Bounds the query the operator will build. |

`q` is singled out deliberately. It is NetBox's fuzzy search, and behind a reference that
must identify exactly one object a fuzzy filter would make ambiguity the normal case
rather than an error — the failure
[errors and retries](errors-and-retries.md#why-ambiguity-is-an-error) exists to prevent.

## The typed aliases

One defined Go type per referencable Kind. The alias is where the target is written down,
and the resolver reads it from the *type* rather than from a switch on the field name.

| Alias | Kind | NetBox model | Endpoint |
|---|---|---|---|
| `TagRef` | `NetBoxTag` | `extras.Tag` | `extras/tags` |
| `RegionRef` | `NetBoxRegion` | `dcim.Region` | `dcim/regions` |
| `SiteRef` | `NetBoxSite` | `dcim.Site` | `dcim/sites` |
| `SiteGroupRef` | `NetBoxSiteGroup` | `dcim.SiteGroup` | `dcim/site-groups` |
| `TenantRef` | `NetBoxTenant` | `tenancy.Tenant` | `tenancy/tenants` |

Model and endpoint spellings are from `docs/netbox-schema.md` and its endpoint map. Only
`NetBoxTag` and `NetBoxSite` exist as Kinds so far; the other three aliases are declared
ahead of their Kinds because a reference is declarable before its target is implemented,
and the remaining ~40 arrive with the generator
([NBO-042 (#66)](https://github.com/ricardomolendijk/netbox-operator/issues/66)).

Each alias implements `RefTarget`:

```go
type RefTarget interface {
    TargetGVK() schema.GroupVersionKind
    AsObjectRef() ObjectRef
}
```

A compile-time assertion covers all five, so an alias that forgets its methods fails the
build rather than the first reconcile that needs it. The table above is pinned by a unit
test — if the two disagree, that test fails.

## How the descriptor sees a reference

Per-kind facts are data, so a reference is a `Field` in the kind's
[`Descriptor`](descriptor.md) with `Ref: true` and a `Target`:

```go
{Spec: "parentRef", API: "parent", Ref: true, Target: v1alpha1.RegionRef{}.TargetGVK()},
```

`Target` is written as the alias's own answer rather than as a fresh GVK literal, so the
alias stays the single source of truth for what it points at.

There is no closure here. Nothing in a `Descriptor` is a func — a closure cannot be
emitted by the generator, printed in a diff or linted — and none is needed, because the
engine already reads a spec through its JSON representation rather than through per-kind
accessors. The target Kind is the only per-kind fact a resolver needs.

A `Target` on a field that is not a `Ref` is rejected at manager start
(`ErrTargetNotRef`): it is almost always a forgotten `Ref: true`, and left alone it
produces a field the resolver ignores and the engine writes to NetBox verbatim.

The converse — a `Ref` with no `Target` — is still not rejected at start, because a typed
alias exists for five Kinds and a descriptor may legitimately declare a reference to a Kind
that has none yet. The resolver reports such a field as `RefKindUnavailable`, with a message
that says the descriptor names no target rather than blaming the manifest, and the object
does not reach `Ready`. Turning the check into a boot failure waits for the aliases the
generator emits ([NBO-042 (#66)](https://github.com/ricardomolendijk/netbox-operator/issues/66)).
