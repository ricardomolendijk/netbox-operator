# References

How one object points at another, and what the API server rejects before the operator
ever sees it.

> **Status.** The `ObjectRef` type, its validation and the typed aliases are built
> (NBO-011). **Resolution is not** — a declared reference is reported as unresolved and
> left out of the NetBox payload, which is the contract
> [NBO-012 (#24)](https://github.com/ricardomolendijk/netbox-operator/issues/24) changes.
> Watches (#25), grants (#26), deferred application (#27) and cycle detection (#28) each
> extend this page when they land.

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
produces a field the resolver ignores and the engine writes to NetBox verbatim. The
converse — a `Ref` with no `Target` — is not rejected yet, because aliases exist for five
Kinds and the descriptors for the rest name targets with no alias to point at. NBO-012
lands the resolver and the remaining aliases together and turns that check on with them.
