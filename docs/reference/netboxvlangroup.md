# `NetBoxVLANGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVLANGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvlangroup` |
| Status subresource | yes |

A `NetBoxVLANGroup` is one `ipam.VLANGroup` in NetBox: a named span of VLAN IDs, optionally
attached to a Region, SiteGroup, Site or Location, that VLANs can be grouped under so the same
`vid` may be reused in different parts of the network.

> ### This is the kind whose **identity contains a polymorphic pair**
>
> Every other kind in this API is looked up by scalars. `ipam.VLANGroup` is unique on
> `(scope_type, scope_id, slug)`, so its natural key includes both halves of a generic foreign
> key — a shape no `Descriptor` could state before
> [#180](https://github.com/ricardomolendijk/netbox-operator/issues/180). It now can, and
> nothing in the engine learned about scopes to make that work: the two column names appear in
> the key like any other filter. See [natural keys](#natural-keys).
>
> The consequence for you is smaller and stranger: **`slug` is not globally unique on this
> kind**, unlike every other `OrganizationalModel` in the API. See
> [`spec.slug`](#specslug).

Companion kind: [`NetBoxVLAN`](netboxvlan.md), which points at this one through
`spec.groupRef`. The pair is applied together, and the contrast between this kind's `scope` and
that kind's `siteRef` is deliberate — see
[`NetBoxVLAN`: `site` is a real foreign key](netboxvlan.md#site-is-a-real-foreign-key-and-scope-is-not).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLANGroup
metadata:
  name: house-vlans-rtm
  namespace: default
spec:
  endpointRef: homelab
  name: Donkerslootstraat VLANs
  slug: house-vlans-rtm
```

A globally-scoped group. That is a legitimate shape rather than a half-filled one — both scope
columns are nullable — but read [natural keys](#natural-keys) before relying on it: with both
columns null, neither unique constraint fires and two global groups may share this slug.

Deleting this CR asks NetBox to delete the group: `deletionPolicy` defaults to `Delete` here and
this is the one kind in `ipam` where that is deliberate — see
[`spec.deletionPolicy`](#specdeletionpolicy).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLANGroup
metadata:
  # A DNS-1123 label, and unrelated to spec.name below.
  name: house-vlans-rtm
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab

  # Shared-envelope defaults, written out.
  onConflict: Fail

  # The default, and deliberately not Retain. See spec.deletionPolicy.
  deletionPolicy: Delete

  name: Donkerslootstraat VLANs
  slug: house-vlans-rtm

  # Half of this object's identity. At most one member; omit the whole block for a
  # globally-scoped group.
  scope:
    siteRef:
      name: home

  # Omitting this is NOT the same as `[]`.
  vidRanges:
    - start: 1
      end: 100
    - start: 200
      end: 300

  # on_delete=PROTECT. Holding this blocks deletion of that tenant in NetBox.
  tenantRef:
    name: donkerslootstraat

  description: VLAN IDs governed at this site
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef` and `onConflict` come from the shared envelope and behave identically on every
kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full treatment of each.

### `spec.deletionPolicy`

| | |
|---|---|
| Type | `string` (`DeletionPolicy`) |
| Required | no |
| Default | `Delete` — **and that is deliberate on this kind** |
| Validation | `Enum=Delete;Retain` |

**This is the one kind in `ipam` that does not default to `Retain`, and the exception is the
point rather than an oversight**
([#186](https://github.com/ricardomolendijk/netbox-operator/issues/186)). The rule
[deletion](../concepts/deletion.md#the-default-depends-on-the-kind) turns on is whether deleting
the NetBox object destroys *state*: `Retain` protects an allocation, `Delete` is fine for
configuration. A VLAN group allocates nothing — it is an organisational container over
`vid_ranges` — so deleting one frees no VLAN, no address and no range, and it belongs with the
catalogue kinds it behaves like. The VLANs *inside* it default to `Retain`
([`NetBoxVLAN`](netboxvlan.md#specdeletionpolicy)): the container goes, the contents stay.

`ipam.VLAN.group` is `on_delete=PROTECT` (`docs/netbox-schema.md` → `ipam.VLAN`), so NetBox
refuses to delete a group that still holds VLANs and the operator can only
[report the refusal](../concepts/deletion.md#what-protect-looks-like). That is what makes
`Delete` cheap here: the case where it would lose something is the case the server blocks.

Write `deletionPolicy: Retain` on a group that other tooling also depends on, or one you are
handing back to a human.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` |

The group's display name. `name CharField REQ len=100` on `ipam.VLANGroup`
(`docs/netbox-schema.md` → `ipam.VLANGroup`) — **with no `UNIQUE`**.

Not unique on its own. `unique_scope_name` makes it unique only *within* a scope, so two groups
in different scopes may share a name freely.

**If it is wrong.** Length is admission. Renaming a group into a name another group in the same
scope already holds is a `409` from NetBox, surfaced as `Ready=False, Reason=Invalid` carrying
NetBox's own error verbatim, with a long backoff — retrying a payload the server already refused
is pointless before the spec changes.

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

The URL-safe identifier. `slug SlugField REQ len=100` on `ipam.VLANGroup`
(`docs/netbox-schema.md` → `ipam.VLANGroup`) — and, again, **with no `UNIQUE`**.

**This contradicts how every other `OrganizationalModel` in the API behaves, and it is worth
reading twice.** `extras.Tag`, `dcim.Site` and `tenancy.TenantGroup` all carry `UNIQUE` on the
slug column, which is what makes `?slug=<x>` a one-object lookup on
[`NetBoxTag`](netboxtag.md#a-slug-is-global-and-this-crd-is-not) and friends.
`ipam.VLANGroup` carries none. Its `meta.constraints` are

```
meta.constraints: (models.UniqueConstraint(fields=('scope_type', 'scope_id', 'name'),
   name='%(app_label)s_%(class)s_unique_scope_name'), models.UniqueConstraint(fields=('scope_type',
   'scope_id', 'slug'), name='%(app_label)s_%(class)s_unique_scope_slug'))
```

so **two VLAN groups may share a slug as long as their scopes differ**. That is why `slug` alone
is not a natural-key candidate on this kind, and why a `slug`-mode
[`VLANGroupRef`](netboxvlan.md#specgroupref) can legitimately match more than one group.

**If it is wrong.** The pattern and the length are admission. A slug that matches more than one
group is `Ready=False, Reason=Conflict` naming every candidate id, with zero writes — see
[natural keys](#natural-keys).

### `spec.scope`

| | |
|---|---|
| Type | [`ScopeRef`](genericref.md#scoperef) |
| Required | no |
| Default | none — omitted means a globally-scoped group |
| Validation | CEL: at most one of `regionRef`, `siteGroupRef`, `siteRef`, `locationRef` |

Attaches the group to a Region, SiteGroup, Site or Location. Written as NetBox's
`(scope_type, scope_id)` pair, where the type half is an `app_label.model` string —
`dcim.region`, `dcim.sitegroup`, `dcim.site`, `dcim.location`.

**On this kind it is half of the object's identity**, which is what makes it different from
`scope` on [`NetBoxPrefix`](netboxprefix.md#specscope). There, the scope is an attribute. Here,
the lookup filters on both columns alongside `slug`.

The three states are all meaningful and all different
([field ownership](../concepts/field-ownership.md)):

| You write | The operator sends |
|---|---|
| nothing | neither column; whatever NetBox holds stays |
| `scope: {}` | both columns as `null`, clearing the scope |
| `scope: {siteRef: {name: home}}` | `scope_type: dcim.site`, `scope_id: <that site's id>` |

`<= 1` rather than `== 1`, because an unscoped group is legal: neither column carries a real
`REQ` ([the `REQ` trap](../concepts/generic-refs.md#the-req-trap-in-the-schema-digest)).

`scope` is also this kind's **containment reference** — see
[ownership and cascade](#ownership-and-cascade).

**If it is wrong.** Two members is an admission rejection. An unresolvable member is
`RefsResolved=False` with `RefNotFound`, `RefNotReady` or `RefDenied`, and
`Ready=False, Reason=WaitingForRef`. A scope that is *declared but not yet resolved* is the case
to know on this kind: it matches **neither** natural-key candidate, so the engine performs no
lookup at all and waits. That is `Ready=False, Reason=WaitingForRef` with zero writes, and it is
correct — see [natural keys](#natural-keys).

### `spec.vidRanges`

| | |
|---|---|
| Type | `[]VIDRange` (a pointer to a slice in Go) |
| Required | no |
| Default | none in the CRD — **but NetBox's column has one** |
| Validation | `MaxItems=256`; per element CEL `self.start <= self.end`, and `Minimum=1`, `Maximum=4094` on both `start` and `end` |

The span of VLAN IDs this group governs. `vid_ranges ArrayField
def=UNRESOLVED:default_vid_ranges` on `ipam.VLANGroup` (`docs/netbox-schema.md` →
`ipam.VLANGroup`).

**Omitting it is not the same as sending `[]`.** The column has a Django default,
`default_vid_ranges`, which is the whole 1–4094 space. So:

| You write | The operator sends | NetBox ends up with |
|---|---|---|
| nothing | the key is absent from the payload | its own default, the whole 1–4094 space |
| `vidRanges: []` | `vid_ranges: []` | an empty array — the group governs **no** VLAN IDs |
| `vidRanges: [{start: 1, end: 100}]` | `vid_ranges: [[1, 100]]` | that one range |

Both of the last two are legal instructions and they are different ones. The middle row is the
one that surprises people: writing an empty list is a real, applied change.

A struct rather than a bare `[][]int32`, so `start` and `end` are named in `kubectl explain` and
each carries its own bound. `start == end` is a valid range of one VLAN.

#### Compared in order, and that is not a style choice

`vidRanges` is `registry.ClassArray` in the Descriptor, not `ClassRefMany`. NetBox stores the
ranges as a Postgres array and returns them in stored order, so the order **is** data. Compared
order-independently, `[[1,100],[200,300]]` and `[[200,300],[1,100]]` would look equal while
NetBox holds two different values, and a real difference would stay hidden forever. Compared in
order, a user who reorders two ranges gets exactly one corrective `PATCH` and then silence. The
second behaviour is correct and quiet; the first hides a difference. See
[drift detection](../concepts/drift.md).

#### Why there is a `MaxItems`

Not a NetBox limit. Every element carries a CEL rule, and the API server costs a rule on a list
item at the list's *maximum* length — so an unbounded validated list is rejected outright at CRD
install time with `estimated rule cost exceeds budget`. A bound is mandatory, not decorative.

256 is far above any real group: 2047 disjoint ranges is the arithmetic ceiling for a 1–4094
space, and a group with more than a handful is describing something other than a VLAN
allocation. It is also well inside the budget for a two-integer item with one rule.

**If it is wrong.** All of it is admission. `start: 0`, `end: 4095`, `start` greater than `end`,
or more than 256 entries — `kubectl apply` fails and nothing is stored:

```console
$ kubectl apply -f group.yaml
The NetBoxVLANGroup "house-vlans-rtm" is invalid: spec.vidRanges[0]: Invalid value: "object":
start must not be greater than end
```

### `spec.tenantRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxTenant` |
| Required | no |

Assigns the group to a tenant. `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.VLANGroup`).

**`PROTECT`, and on this kind that has a consequence worth knowing before you use it.** A
`NetBoxVLANGroup` holding this reference blocks deletion of that tenant in NetBox. Because a
VLAN group is catalogue-shaped it usually lives in a shared namespace, so the team that owns the
tenant gets `Deleting=False, Reason=Protected` naming a group in a namespace they cannot see.
The condition names the blocker's namespace and name for exactly that reason
([deletion](../concepts/deletion.md#what-protect-looks-like)).

Not a containment reference. A VLAN group outliving its tenant is a normal state, so this
contributes no owner reference ([ADR-0003](../decisions/0003-ownership-and-references.md)
rule 4).

**If it is wrong.** Unresolvable is `RefsResolved=False` with `RefNotFound`, `RefNotReady` or
`RefDenied`, and `Ready=False, Reason=WaitingForRef`. Crossing a namespace without a
[`NetBoxRefGrant`](netboxrefgrant.md) is `RefDenied`.

### `spec.description` / `spec.comments`

| | `description` | `comments` |
|---|---|---|
| Type | `string` | `string` |
| Required | no | no |
| Validation | `MaxLength=200` | none |

Both are inherited from `OrganizationalModel` rather than declared on `ipam.VLANGroup`
(`docs/netbox-schema.md` → `ipam.VLANGroup`, `description (OrganizationalModel) CharField
len=200`, `comments (OrganizationalModel) TextField`); an inherited column is as writable as a
declared one. `comments` is a `TextField` with no `max_length`, so there is no `MaxLength` marker
to derive.

Both are clearable, and the two empty states are different instructions: **omit** the field to
leave NetBox's own value alone, set it to `""` to clear the value in NetBox. The operator can
tell them apart because it reads `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

**If it is wrong.** A `description` over 200 characters is admission. Neither field can fail at
reconcile on its own.

## Natural keys

Two candidates, tried in this order:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(scope_type, scope_id, slug)` | `?scope_type=dcim.site&scope_id=<id>&slug=<slug>` | `scope` **resolves** to a type and an id |
| 2 | `slug` where the scope `IS NULL` | `?slug=<slug>&scope_id__empty=true` | `scope` was **never declared** |

Candidate 1 comes straight from `unique_scope_slug`. It is the first natural key in the codebase
whose filters are the two columns of a polymorphic pair, and the mechanism that makes it
possible is [#180](https://github.com/ricardomolendijk/netbox-operator/issues/180): a resolved
union is now written into the decoded spec under its **column** names, so
`{Filter: "scope_id", Spec: "scope_id"}` renders exactly as `{Filter: "vrf_id", Spec: "vrfRef"}`
does on [`NetBoxPrefix`](netboxprefix.md#natural-keys). See
[generic references → natural keys](../concepts/generic-refs.md).

**The query parameters are real, and that was checked rather than assumed.** NetBox 4.6.8's
`VLANGroupFilterSet` declares `scope_type = MultiValueContentTypeFilter()`
(`netbox/ipam/filtersets.py:948`) and lists `scope_id` in its `Meta.fields`
(`netbox/ipam/filtersets.py:980`). `scope_type` takes the `app_label.model` string, which it
splits on `.` and resolves through `ContentType.objects.get_by_natural_key`
(`netbox/utilities/filters.py:186-207`) — so `dcim.site` is the spelling, never a numeric
`ContentType` id.

**Both filters, not one.** A generic FK's id is only unique *within* its type, so
`?scope_id=31&slug=mgmt` would match a group scoped to the site with id 31 and a group scoped to
the region with id 31 alike. This is the same reason the pair is written atomically
([why the pair is atomic](../concepts/generic-refs.md#why-the-pair-is-atomic)).

**The order is not a fallback chain.** Candidate 2 is the identity of a *different* object — a
globally-scoped group rather than a scoped one. A `scope` that is declared but has not resolved
yet matches **neither**: candidate 1 needs the two columns resolved, candidate 2 needs `scope`
never declared. With nothing applicable the engine performs no lookup and waits, which is the
correct outcome. Falling through would find the global group of the same slug, adopt it, and the
follow-up `PATCH` would move somebody else's global group into this scope
([NBO-015](../concepts/references.md)).

### Neither candidate is guaranteed unique

Candidate 1 is backed by `unique_scope_slug`, so at most one group can match it — while the
scope columns hold values. Candidate 2 is where it gets interesting: **with both scope columns
null, Postgres treats the NULLs as distinct, so neither unique constraint fires at all.** Two
globally-scoped VLAN groups can legitimately share a slug and the database will not stop them.

So more than one match is a real server state on this kind, not proof of a mistake. The engine
reports `Ready=False, Reason=Conflict` naming every candidate id and writes nothing
([why ambiguity is an error](../concepts/errors-and-retries.md#why-ambiguity-is-an-error)). The
escape hatch is an `id`-mode reference or a deliberate adoption: once `status.id` is set the
object is reconciled by id and the natural key is not consulted again, so the ambiguity only
ever bites on first adoption.

A scoped group and an unscoped group with the same slug both reconcile successfully and do not
adopt each other. That is what the pinned nulls buy.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each field means and when it is
cleared. Nothing is cleared on failure: `status.id` in particular survives, which is what lets a
failing object keep reconciling by id rather than re-deriving an identity.

`status.provenance` is stamped in full: `ipam.VLANGroup` is an `OrganizationalModel`, which mixes
in both `TagsMixin` and `CustomFieldsMixin` ([provenance](../operations/provenance.md)).

`status.naturalKey` is worth reading on this kind in particular, because it is the only place
that records *which* identity was used. A `{"scope_type": "dcim.site", "scope_id": "12", "slug":
"house-vlans-rtm"}` says the group was found as a scoped object; a
`{"slug": "…", "scope_id__empty": "true"}` says it was found as a
global one. The `SCOPE` printer column reads out of here for the same reason.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefTypeNotAllowed`, `RefKindUnavailable` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | the containment parent owns this CR | it cannot | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals are shared across every object kind; see
[errors and retries](../concepts/errors-and-retries.md). The three that mean something
particular here:

- **`Conflict`** on `Ready`: more than one group matched. On this kind that is a legitimate
  NetBox state rather than proof of a mistake — see
  [neither candidate is guaranteed unique](#neither-candidate-is-guaranteed-unique).
- **`WaitingForRef`** on `Ready` with `scope` named: the scope is declared and unresolved, so
  **no lookup happened**. The object is waiting for an identity it cannot build yet, which is
  the designed outcome and not a stall.
- **`Protected`** on `Deleting`: NetBox refuses to delete a group while VLANs still reference
  it. The message names the blockers; the delete completes on its own once they are gone, and
  `status.deletionAttempts` counts the tries.

## Kind-specific behaviour

### The scope pair without the cached columns

This is the one line where this kind's `Descriptor` differs from
[`NetBoxPrefix`](netboxprefix.md)'s. `ipam.Prefix` inherits `scope_type` / `scope_id` from
`dcim.CachedScopeMixin`, which brings four read-only denormalised columns with it — `_region`,
`_site_group`, `_site`, `_location`. **`ipam.VLANGroup` declares the two columns on the model
itself and has none of the four** (`docs/netbox-schema.md` → `ipam.VLANGroup`).

So the descriptor clears `Cached` on `registry.ScopeFK("scope")` rather than restating the
union. The members, the four permitted `app_label.model` strings and the spelling all still come
from `internal/registry/scope.go` — only "this model has no caches" is stated per kind. Leaving
the list in place would put four columns this table does not have into `ReadOnly`, which
`Validate` would happily accept and which would be a lie.

The two scoped kinds in this milestone differ here and nowhere else.

### `total_vlan_ids` is never sent and never diffed

`total_vlan_ids PositiveBigIntegerField def=UNRESOLVED:VLAN_VID_MAX - VLAN_VID_MIN + 1`
(`docs/netbox-schema.md` → `ipam.VLANGroup`). NetBox maintains it from `vid_ranges`, so it is in
the Descriptor's `ReadOnly` list and no spec field maps onto it.

Writing a read-only column does not fail — it silently no-ops, so the next reconcile finds the
same difference and `PATCH`es again forever. A read-only field in a write payload is a hot loop,
not an error ([drift detection](../concepts/drift.md)).

### The unscoped candidate pins one scope column, not both

Candidate 1 matches both halves of the pair; candidate 2 pins only `scope_id`. That is not an
oversight — **NetBox registers no null filter for `scope_type` at all**, and asking for one is
worse than useless.

`scope_id` is `PositiveBigIntegerField` (`docs/netbox-schema.md` → `ipam.VLANGroup`), so it gets
`FILTER_NUMERIC_BASED_LOOKUP_MAP` and `?scope_id__empty=true` resolves to the ORM's `isnull`
(`netbox/utilities/constants.py:26`). `scope_type` is a `ForeignKey` to
`contenttypes.ContentType` behind a `MultiValueContentTypeFilter`, and neither spelling works on
it:

- `?scope_type__empty=true` is **never registered**. The `empty` ORM lookup exists only on
  `CharField` and `JSONField` (`netbox/extras/lookups.py:128-129`), so `resolve_field` raises
  `FieldLookupError` and `BaseFilterSet` skips the filter
  (`netbox/netbox/filtersets.py:232-234`). `django-filter` then ignores the parameter and the
  lookup silently widens to `?slug=<slug>`.
- `?scope_type=null` is **worse**: `'null'.lower().split('.')` raises `ValueError`, the filter
  ends up as `scope_type__in=[]`, and the request matches *nothing at all*
  (`netbox/utilities/filters.py:190-207`). The engine would conclude the group does not exist
  and create a second one.

Pinning the id half alone loses nothing, because NetBox refuses one half of the pair without the
other — `Cannot set scope_type without scope_id` and its converse
(`netbox/ipam/models/vlans.py:105-109`) — so `scope_id IS NULL` is exactly the set of groups with
no scope. [Lookups](../concepts/lookups.md#how-a-null-pin-is-spelled-and-why-it-depends-on-the-column)
has the general rule.

### The scope moves as one pair

Changing a group's scope from a site to a region produces **one** diff entry over the pair and
one `PATCH` carrying both keys. There is no code path that can set an id against a type it was
not resolved with — which is the failure NetBox answers by attaching the object to a completely
different row that happens to share a primary key
([why the pair is atomic](../concepts/generic-refs.md#why-the-pair-is-atomic)).

Because the pair is also half the identity, such a change is a **change of identity**: see
[renaming changes identity](#renaming-changes-identity).

### Ownership and cascade

`scope` is this kind's **containment reference**
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4): the scope target gets a
non-controller owner reference when that is legal, so `kubectl delete netboxsite home` also
removes the VLAN groups scoped to it.

Exactly one, and `tenantRef` is deliberately not a second. Kubernetes garbage collection deletes
a dependent only once *every* owner is gone, so two containment references would silently turn
"delete the site **or** the tenant and the group goes" into "delete both".

An owner reference is only legal within one namespace, and a catalogue-shaped VLAN group in a
shared namespace scoped to a site in another one **gets none, ever**. The operator sets
`ParentOwned=False, Reason=CascadeUnavailable` naming `scope` rather than silently skipping it.
The same is true of a scope referenced by raw `id`.

### Renaming changes identity

`slug` and `scope` both participate in the natural keys, so editing either does not rename or
re-scope the NetBox group — it changes what the CR is looking for. The next reconcile finds
nothing at the new identity and creates a second group, leaving the first behind. This is what a
natural key means and is not specific to this kind.

`name`, `vidRanges`, `tenantRef`, `description` and `comments` are all safe to edit — none is
part of an identity. Note the asymmetry: `name` is covered by `unique_scope_name` in NetBox but
is *not* used as a lookup filter, so renaming is a plain `PATCH`.

## Printer columns

```console
$ kubectl get nbvlangroup
NAME               SLUG               SCOPE   ID    READY   AGE
house-vlans-rtm    house-vlans-rtm    12      204   True    6m
global-reserved    global-reserved            205   True    6m
lab-vlans          lab-vlans                        False   6m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `SCOPE` | `.status.naturalKey.scope` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`SCOPE` reads `.status.naturalKey` rather than the spec, because on this kind the question it
answers is "which of the two identities did the lookup actually use" — and only the status can
answer that. An empty `SCOPE` next to an empty `ID` is the shape of a group whose scope has not
resolved, which is exactly the state where no lookup ran at all.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, "start must not be greater than end" | admission, nothing stored | a `vidRanges` element is inverted | Swap them. `start == end` is a valid one-VLAN range |
| `kubectl apply` rejected, `vidRanges[n].start` out of range | admission | `0` or `4095` | 1–4094. Both ends are reserved by 802.1Q |
| `kubectl apply` rejected, "at most one of regionRef…" | admission | two members of `spec.scope` | A group has one scope. Pick one |
| `kubectl apply` rejected, slug pattern | admission | a space, a dot or a slash in `spec.slug` | `^[-a-zA-Z0-9_]+$` |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `scope` | reconcile, **zero writes and zero lookups** | the scope target does not exist or is not Ready | Expected while the site is being created. Apply it; the group re-enqueues on its own. Do **not** remove `scope` to unblock it — that changes the object's identity |
| `Ready=False`, `Reason=Conflict`, `scope` set | reconcile, zero writes | more than one group matched `(scope_type, scope_id, slug)` | Should not happen — `unique_scope_slug` forbids it. Check whether the two are really in the same scope; `status.naturalKey` shows what was searched |
| `Ready=False`, `Reason=Conflict`, no `scope` | reconcile, zero writes | two globally-scoped groups share this slug | Legitimate: with both columns null the constraint does not fire. Give one a scope, or adopt deliberately by `id` |
| `Ready=False`, `Reason=Invalid` on a rename | reconcile, long backoff | `unique_scope_name` — another group in this scope holds that name | The message is NetBox's own error, verbatim. Pick another name |
| NetBox's `vid_ranges` is the whole 1–4094 space | none | `vidRanges` was omitted | Absent means "do not manage". Write the ranges, or `[]` to govern none |
| The last range will not come off | none | removing every element leaves the field absent after `omitempty` | Write `vidRanges: []` explicitly. It needs `metadata.managedFields` to be readable — see [field ownership](../concepts/field-ownership.md) |
| `total_vlan_ids` never changes when expected | none | it is server-maintained and `ReadOnly` | NetBox recomputes it from `vid_ranges`. The operator never sends it and never diffs it |
| `ParentOwned=False`, `Reason=CascadeUnavailable` naming `scope` | reconcile | the scope target is in another namespace, or referenced by `id` | Expected for a catalogue group. An owner reference cannot cross a namespace ([ADR-0003](../decisions/0003-ownership-and-references.md)) |
| A second group appeared after an edit | none | `spec.slug` or `spec.scope` was changed | See [renaming changes identity](#renaming-changes-identity) |
| Terminating forever, `Deleting` `Reason=Protected` | finalizer | VLANs still reference this group | Delete them, or switch to `deletionPolicy: Retain` to drop the finalizer without asking NetBox |
| A `NetBoxTenant` will not delete, blocker in a namespace you cannot see | `Deleting=False`, `Reason=Protected` on the *tenant* | a `NetBoxVLANGroup` in a catalogue namespace holds `tenantRef` | The condition names the group's namespace and name. Remove the `tenantRef` there, or delete the group |
| Deleting the CR deleted the group, unlike its VLANs | `Deleted` Event | this kind defaults to `deletionPolicy: Delete` on purpose — it is a container, not an allocation | Set `deletionPolicy: Retain` if the group must outlive its CR. See [`spec.deletionPolicy`](#specdeletionpolicy) |

## Related

- [`NetBoxVLAN`](netboxvlan.md) — the kind that points here through `groupRef`, and the
  `siteRef`-versus-`scope` contrast
- [`NetBoxPrefix`](netboxprefix.md) — the other scoped IPAM kind, and the one that *does* carry
  the cached scope columns
- [Generic references](../concepts/generic-refs.md) — the scope pair, why it is atomic, and the
  natural-key mechanism this kind is the first user of
- [`ScopeRef`](genericref.md#scoperef) — the union's shape in a spec
- [Lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted) — why the
  scope columns are pinned rather than omitted
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set, and how `[]`
  survives `omitempty`
- [Drift detection](../concepts/drift.md) — the ordered-array compare `vidRanges` goes through
- [Deletion](../concepts/deletion.md#the-default-depends-on-the-kind) — why most IPAM kinds
  default to `Retain` and why this one does not, and what `PROTECT` looks like
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — the
  containment reference and the cascade
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
