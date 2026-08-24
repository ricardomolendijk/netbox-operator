# Scopes

**"Scope" on this page is NetBox's, not Kubernetes'.** They are unrelated, and the collision
is guaranteed to confuse someone, so it is settled first.

| | What it means | Where it is set |
|---|---|---|
| **Kubernetes CRD scope** | whether a CRD's objects live in a namespace or in the cluster | `Descriptor.Scope`, and it is `Namespaced` for every kind in `v1alpha1` ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| **NetBox scope** | which Region, SiteGroup, Site or Location a NetBox object hangs off | `spec.scope` on a scoped kind, this page |

Nothing about `spec.scope` changes where a CR lives. A `NetBoxPrefix` in the `team-a`
namespace scoped to a Site is still a namespaced object in `team-a`.

## Why the field exists at all

Until NetBox 4.1, several models carried an ordinary `site` foreign key. NetBox 4.2 replaced
it with `CachedScopeMixin`, which is two writable columns and four read-only ones
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

`scope` is a `GenericForeignKey`: a Python-level accessor over the two real columns, not a
column of its own. The `REQ` the schema extractor prints against it is an artefact — a
`GenericForeignKey` takes no `null=` kwarg — and must not be read as "a scope is required".
Neither real column carries `REQ`, so a globally-scoped prefix is legal and common.

### The failure this prevents

**Writing `site` to a scoped model does not fail. It is ignored.**

NetBox drops a column it does not know rather than rejecting it, so `POST /api/ipam/prefixes/`
with `{"prefix": "192.0.2.0/24", "site": 3}` returns `201`. The prefix is created. It has no
scope. Every subsequent read agrees with the spec, because the spec's `site` is compared
against a column that does not exist, so nothing ever drifts and nothing is ever reported.
The object says `Ready=True` forever and the scope was never set.

That is the bug `netbox-populator` shipped, and it is why this operator's API has no `siteRef`
on any scoped kind — not even as sugar that expands into `scope.siteRef`. A field called
`siteRef` on a `NetBoxPrefix` would read as the foreign key NetBox no longer has, and the
point of the union is that the mistake cannot be expressed.

The four `_`-prefixed columns are the mirror image. They are caches NetBox maintains from
`(scope_type, scope_id)` and they come back on every read, so they are useful to *filter* on
and fatal to *write*: an attempt to set `_site` is dropped exactly like `site`, the next read
finds it unchanged, and the operator PATCHes it again on every resync — a hot loop against
the API for as long as the object exists.

## The union

```go
type ScopeRef struct {
    RegionRef    *RegionRef    `json:"regionRef,omitempty"`
    SiteGroupRef *SiteGroupRef `json:"siteGroupRef,omitempty"`
    SiteRef      *SiteRef      `json:"siteRef,omitempty"`
    LocationRef  *LocationRef  `json:"locationRef,omitempty"`
}
```

Four optional typed references, at most one set. It is a struct of typed members rather than
a `{kind, name}` pair with a discriminator string for the same reason `ObjectRef` has no
`kind` field ([references.md](references.md)): the field name pins the target Kind, so the
resolver and CEL both enforce it statically, and `kubectl explain` shows the four legal
choices instead of asking the user to know a content-type spelling.

| Member set | `scope_type` written | Target model |
|---|---|---|
| `regionRef` | `dcim.region` | `dcim.Region` |
| `siteGroupRef` | `dcim.sitegroup` | `dcim.SiteGroup` |
| `siteRef` | `dcim.site` | `dcim.Site` |
| `locationRef` | `dcim.location` | `dcim.Location` |
| *none* | `null`, with `scope_id: null` | — |

The type string is **not** written down against the member. It is the target Kind's own
`Descriptor.ObjectType`, so `dcim.sitegroup` is spelled once in the whole codebase. The
spelling is the Django `model` attribute — lowercased and unpunctuated — so `dcim.sitegroup`
and never `dcim.SiteGroup` or `dcim.site_group`, both of which are a `400`. This is the most
likely place to lose an afternoon.

`scope_type` is a `ForeignKey` to `contenttypes.ContentType` on the model, but the REST API
accepts and returns the `app_label.model` *string*. There is no ContentType id to resolve.

### What the API server rejects

One CEL rule, on the `ScopeRef` type rather than on each field that uses it, so a new scoped
kind cannot forget it:

```
[has(self.regionRef), has(self.siteGroupRef), has(self.siteRef), has(self.locationRef)]
    .filter(x, x).size() <= 1
```

`<= 1` and not `== 1`: no scope at all is a legal NetBox state, so it has to be a legal spec.
Two members are refused by `kubectl apply` with *at most one of regionRef, siteGroupRef,
siteRef or locationRef may be set* — at admission, not as a condition the operator writes
after the fact.

The resolver refuses two members as well, with `RefsResolved=False, Reason=Invalid`. That
path is unreachable through the API server and it is not dead code: the alternative to
refusing is silently picking one of the two scopes the user asked for.

## Absent, empty, and set — three different instructions

This is the one place a scope departs from how every other field behaves, and it is
deliberate.

| Spec | Payload | Meaning |
|---|---|---|
| no `scope` key | neither column sent | "do not manage the scope" — whatever NetBox holds stays |
| `scope: {}` | `scope_type: null, scope_id: null` | "no scope" — clear it |
| `scope: {siteRef: {...}}` | `scope_type: dcim.site, scope_id: 5` | scoped to that site |

An omitted field is left as NetBox has it, which is what lets the operator co-exist with a
human editing the same object ([ADR-0005](../decisions/0005-gitops-coexistence.md)). If an
empty union were treated the same way there would be no way to *clear* a scope through this
API at all: set it once and it would stay forever.

## The pair is written as a pair

`(scope_type, scope_id)` is one logical reference in two columns, and every stage treats it as
one unit.

- **Payload.** Both columns are written together or neither is. A scope that has not resolved
  yet leaves both out and reports `RefsResolved=False`; half a reference is worse than none.
- **Diff.** `netbox.Changes` diffs the pair first and emits both halves whenever either
  differs ([drift.md](drift.md)). Moving a prefix from a Region to a Site is **one** change
  covering two columns, and clearing a scope produces `scope_type: null, scope_id: null`
  rather than an omitted key.
- **Why it matters.** A `scope_id` PATCHed without its `scope_type` is not necessarily
  rejected. NetBox may interpret the new id against the type it still holds, which points the
  object at whatever row happens to own that primary key in a different model.

## Resolution, grants and watches

A scope reference is an ordinary reference in every respect that matters:

- All four `ObjectRef` modes work — `name`, `slug`, `lookup`, `id`.
- A `name` reference into another namespace needs a `NetBoxRefGrant` in the target namespace,
  checked **before** the read, exactly as for any other reference
  ([references.md](references.md#crossing-a-namespace)).
- The cycle check follows it.
- Each member Kind is watched, so a `NetBoxSite` gaining its `status.id` re-enqueues every
  object scoped to it rather than making it wait a resync interval.

None of that is scope-specific code. The union's members are `Descriptor` data
(`GenericFKSpec.Members`), and the resolver, the index and the watch builder all read the
same list — so a fifth member, or a new scoped kind, is a data change.

### When the target Kind does not exist yet

`NetBoxSiteGroup` and `NetBoxLocation` have no CRD in this build
([NBO-066 (#79)](https://github.com/ricardomolendijk/netbox-operator/issues/79) and NBO-048).
A `siteGroupRef` or
`locationRef` therefore resolves to nothing, in **every** mode, and is reported as:

```
RefsResolved=False, Reason=RefKindUnavailable
scope -> netboxsitegroup/team-a/west: target kind unavailable (no descriptor is registered
for netbox.kubeforge.org/v1alpha1, Kind=NetBoxSiteGroup)
```

`RefKindUnavailable` and not `RefNotFound`, because the manifest is correct and the fix is an
operator upgrade rather than a spec change. Nothing is written, `Ready` is `False` with
`WaitingForRef`, and the retry is the ten minutes a human-cleared state gets.

This includes the `slug`, `lookup` and `id` modes, which is worth being explicit about: those
three resolve against NetBox and would not need a CRD, but they *do* need the target's REST
endpoint, and the endpoint is a per-kind fact that lives on the target's `Descriptor`
(`dcim.VirtualChassis` is at `dcim/virtual-chassis` — it is looked up, never derived). No
descriptor, no endpoint, no lookup. The union member starts working for all four modes the
moment its Kind is registered, with no change to this mechanism.

## Migrating from netbox-populator

If you are coming off `netbox-populator`, the scope is the change to look at hardest.

| netbox-populator | here |
|---|---|
| `site: <name>` on a prefix | `scope: {siteRef: {name: <name>}}` |
| pruning keyed on the object's `site` | identity is the natural key; the scope is not a prune key |
| a `site` that appeared to work | it never did — check NetBox for prefixes with no scope |

Before migrating, list the objects the old tool claimed to scope and confirm in NetBox that
they actually are. `GET /api/ipam/prefixes/?scope_id__empty=true` is the query that tells you
how much of it silently did nothing.

## Related

- [The Descriptor](descriptor.md) — `GenericFKSpec`, and what `Validate` rejects
- [References](references.md) — the four modes, grants, cycles, watches
- [Drift detection](drift.md) — why the pair is diffed as a unit
- [`ScopeRef` reference](../reference/scoperef.md) — the field-by-field user-facing shape
- [ADR-0002](../decisions/0002-crd-scoping.md) — why every CRD is namespaced
- [ADR-0003](../decisions/0003-ownership-and-references.md) — NetBox foreign keys versus
  Kubernetes owner references
