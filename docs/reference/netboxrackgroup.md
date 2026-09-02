# `NetBoxRackGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRackGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrackgroup` |
| Status subresource | yes |

A `NetBoxRackGroup` is one `dcim.RackGroup` in NetBox: a named grouping of racks — `Cage 1`,
`DMZ`, `Overflow`.

**It is not a hierarchy.** Every other `*Group` shipped so far — [`NetBoxSiteGroup`](netboxsitegroup.md),
[`NetBoxTenantGroup`](netboxtenantgroup.md), [`NetBoxContactGroup`](netboxcontactgroup.md) — is a
`NestedGroupModel` with a self-referential `parent` and an identity that is a pair.
`dcim.RackGroup` is an `OrganizationalModel` with no `parent` column at all, so this Kind has no
`parentRef`, no `parent IS NULL` natural-key variant, no tree and nothing for a cycle check to
check. Its identity is `slug` alone. The evidence is in
[not a nested group](#not-a-nested-group), because the name is the whole trap.

## Not a nested group

The schema entry is short enough to quote in full
(`docs/netbox-schema.md` → `dcim.RackGroup`):

```
## dcim.RackGroup   (dcim/models/racks.py)
   bases: OrganizationalModel
   (no own columns — every field is inherited from OrganizationalModel)
     name (OrganizationalModel)   CharField   REQ UNIQUE len=100
     slug (OrganizationalModel)   SlugField   REQ UNIQUE len=100
     description (OrganizationalModel)  CharField   len=200
     comments (OrganizationalModel)     TextField
   meta.ordering: ('name',)
```

No `parent`, no MPTT base, no `site`, and **no `meta.constraints`**. Two independent readings
agree:

- the serializer's write path is
  `('id', 'url', 'display_url', 'display', 'name', 'slug', 'description', 'owner', 'comments',
  'tags', 'custom_fields', 'created', 'last_updated', 'rack_count')` — no `parent`, no `site`
  (`hack/testdata/ir-4.6.8.json.gz` → `dcim.RackGroup.write_path`);
- `RackGroupFilterSet` declares nothing of its own over `OrganizationalModelFilterSet` and its
  `Meta.fields` are `('id', 'name', 'slug', 'description')` — there is **no `parent_id` filter**
  (NetBox 4.6.8, `netbox/dcim/filtersets.py:320`).

That last point is why guessing here would be expensive rather than merely wrong. NetBox's
`BaseFilterSet` **drops** a query parameter it does not recognise instead of rejecting it, so a
`?parent_id=null` pin would vanish on the wire, the lookup would match every group of that slug,
and the engine would adopt one of them.

NBO-051's issue text asks for `(parent, slug)` plus the `parent IS NULL` variant, an MPTT
`parentRef` and a webhook cycle check. None of that is expressible against 4.6.8, so none of it
ships. That issue also footnotes a "schema gap: `dcim.RackGroup` has an endpoint but no model
entry" — the entry exists at 4.6.8 and is the one quoted above.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackGroup
metadata:
  name: cage-1
  namespace: default
spec:
  endpointRef: homelab
  name: Cage 1
  slug: cage-1
```

That is the whole Kind, bar two optional free-text fields. There is no `parentRef` to omit and
no `siteRef` either — a rack group is not scoped by anything, and it is the *rack* that names
both its site and its group.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackGroup
metadata:
  name: cage-1
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Cage 1
  slug: cage-1
  description: North hall, rows A–C
  comments: |
    Shared cage; the colo provider holds the keys.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | yes | — | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | yes | — | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |
| `description` | `string`, ≤200 | no | — | `description` (`OrganizationalModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`OrganizationalModel`), `TextField` |

### `spec.name`

Required. The group's name, up to 100 characters.

**Column-unique**, and that is the sentence worth reading twice: on
[`NetBoxSiteGroup`](netboxsitegroup.md) and [`NetBoxContactGroup`](netboxcontactgroup.md) a
group's name is unique only *per parent*, so two groups may share one. Here there is no parent
to scope it, and `name` is unique across the whole NetBox
(`docs/netbox-schema.md` → `dcim.RackGroup`).

It is a candidate key and deliberately not the lookup key: a kind gets one identity and `slug`
is the stable one, so a rename that collides comes back as NetBox's own 409 reported as
`Ready=False, Reason=Invalid` rather than being adopted under the other candidate.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and **globally unique** across the whole NetBox.

It is usable as an identity *because* the base class is `OrganizationalModel`:
`OrganizationalModel.slug` carries `UNIQUE` and `NestedGroupModel.slug` does not. See
[the base class decides, not the name](#the-base-class-decides-not-the-name).

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and
set are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`
(`docs/netbox-schema.md` → `dcim.RackGroup`, `comments (OrganizationalModel) TextField`), so it
has no `max_length` and there is no length marker to derive from one.

Clearable on the same three-state terms as `description`.

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

`dcim.RackGroup` declares **no `meta.constraints` at all** — its `Meta` carries only
`ordering: ('name',)`. So the identity does not come from a constraint list; it comes from
`OrganizationalModel`'s *column-level* `UNIQUE` on `slug`. Uniqueness is global, so there is
nothing to pin to null and nothing a candidate could be conditional on.

The filter is registered: `RackGroupFilterSet.Meta.fields = ('id', 'name', 'slug',
'description')` (NetBox 4.6.8, `netbox/dcim/filtersets.py:320`).

Same derivation as [`NetBoxRackRole`](netboxrackrole.md) in the same ticket,
[`NetBoxManufacturer`](netboxmanufacturer.md) and [`NetBoxContactRole`](netboxcontactrole.md).

## The base class decides, not the name

[`NetBoxContactGroup`](netboxcontactgroup.md) is the same shape of name, in a NetBox source
file one app over, and its natural key is `(parent, name)` — it cannot use `slug` at all,
because `NestedGroupModel.slug` carries no `UNIQUE`
(`netbox/netbox/models/__init__.py:183-186`) while `OrganizationalModel.slug` does (`:232-236`).
[`NetBoxContactRole`](netboxcontactrole.md) already makes that argument for its own neighbour;
this Kind is the same lesson with `Group` in the name instead of `Role`.

| | `dcim.RackGroup` | `tenancy.ContactGroup`, `dcim.SiteGroup`, `tenancy.TenantGroup` |
|---|---|---|
| Base class | `OrganizationalModel` | `NestedGroupModel` |
| `parent` column | none | `TreeForeignKey` |
| `slug` uniqueness | column-level `UNIQUE` | none |
| Natural key | `slug` | a pair, with a null-pinned variant for the top level |
| Containment parent | none | `parentRef` where the FK cascades |

So: read the `bases:` line in `docs/netbox-schema.md` before assuming a `*Group` nests.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.RackGroup` is an `OrganizationalModel`, so it carries both `tags` and `custom_fields` and
is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved` is unconditionally `True` here, and that is the observable consequence of there
being no `parent`: there is no state in which this Kind waits for another object, so no
`WaitingForRef` and no `RefCycle` are reachable.

## Kind-specific behaviour

### A hand-made group is adopted, not duplicated

`slug` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one group in NetBox rather than two. Creating a second one would be
refused by the unique index anyway, so adoption is the only outcome that works.

### Two namespaces claiming one slug is one group

NetBox's uniqueness is a database constraint and a namespace boundary does not partition it. The
first CR to reconcile creates or adopts the group; the second finds it already claimed and
reports `Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)).

### No containment parent, in either direction

`dcim.RackGroup` has **no foreign key at all** bar `owner`, so there is nothing that could be a
containment parent ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4) — and no
`parent` for one either, which is the difference from every nested group that does have the
slot filled.

The reference pointing *at* it is `Rack.group ForeignKey -> dcim.RackGroup
on_delete=PROTECT` (`docs/netbox-schema.md` → `dcim.Rack`), so deleting a group in use is
**refused** by NetBox rather than cascading: the CR reports `Deleting=False, Reason=Protected`
naming the blocker. Move the racks out of the group, or delete them, first.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A rack group is a label a manifest recreates
verbatim; deleting one frees no resource anybody else can take, which is what `Retain` was
reserved for. See [deletion](../concepts/deletion.md).

### `rack_count` is never written

`rack_count` is a `CounterCacheField` NetBox maintains from the racks pointing at the group
(`docs/netbox-schema.md`, preamble on every `CounterCacheField`), and it is in the descriptor's
read-only list. Writing it would not fail — NetBox drops it — which is precisely why it has to
be declared: a dropped write produces a difference the next reconcile finds again, and PATCHes
forever.

### Renaming the slug changes identity

`slug` is the natural key, so editing it does not rename the NetBox group — it changes what the
CR is looking for, and the next reconcile creates a second group, leaving the first behind.
`name`, `description` and `comments` are safe to edit.

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at. `tags` and `customFields`
are written by the provenance stamp and not by a user.

Nothing else is missing: this model has four writable columns of substance and the CRD maps all
four.

## Printer columns

```
$ kubectl get nbrackgroup
NAME       SLUG       ID   READY   AGE
cage-1     cage-1     84   True    2m
overflow   overflow   85   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

There is no `PARENT` column, and that is deliberate rather than an omission — see
[not a nested group](#not-a-nested-group).

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this slug, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Pick a different slug, or set `onConflict: Adopt` in the namespace that should own it. |
| `Ready=False`, `Reason=Invalid`, message names `name` | `Ready` | A rename collided with the column-level `UNIQUE` on `name` — which is global here, unlike on the nested-group kinds. | Pick another name; `slug` is what identifies the object. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the group re-enqueues off its event. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A rack still points at this group — `Rack.group` is `PROTECT`. | Move or delete those racks, or set `deletionPolicy: Retain`. |
| A second group appeared after an edit | — | `spec.slug` was changed. | See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |
| `spec.parentRef` rejected by `kubectl apply` | — | There is no such field. `dcim.RackGroup` has no `parent` column. | Group racks by one flat label; nest the *locations* instead, with [`NetBoxLocation`](netboxlocation.md). |

## Related

- [`NetBoxRackRole`](netboxrackrole.md) — the other `OrganizationalModel` in this ticket, same identity derivation
- [`NetBoxContactGroup`](netboxcontactgroup.md) — a real `NestedGroupModel`, and the identity this Kind does *not* have
- [`NetBoxSiteGroup`](netboxsitegroup.md) — the same, one app over, with the cascade this Kind has no column for
- [`NetBoxContactRole`](netboxcontactrole.md) — where the "base class decides the identity" argument is spelled out
- [`NetBoxLocation`](netboxlocation.md) — the kind that *does* nest, and the one to reach for when you wanted a rack-group tree
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
