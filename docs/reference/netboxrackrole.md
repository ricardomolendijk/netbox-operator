# `NetBoxRackRole`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRackRole` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrackrole` |
| Status subresource | yes |
| Lands with | NBO-051 (M9–M10) |

A `NetBoxRackRole` is one `dcim.RackRole` in NetBox: what a rack is *for* — `Compute`,
`Network`, `Storage`, `Overflow`.

It is an `OrganizationalModel` with exactly **one column of its own**,
`color ColorField def='ColorChoices.COLOR_GREY'` (`docs/netbox-schema.md` → `dcim.RackRole`).
Everything else — `name`, `slug`, `description`, `comments` — is inherited. Read against
[`NetBoxDeviceRole`](netboxdevicerole.md) it is that kind minus the self-reference and minus
`vm_role`, and that subtraction is what changes its identity: see
[the contrast that decides the natural key](#the-contrast-that-decides-the-natural-key).

Nothing requires a rack role — `Rack.role` is a nullable foreign key — so this Kind is
optional in a way `dcim.Manufacturer` is not. Create the roles you actually classify by.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackRole
metadata:
  name: compute
  namespace: default
spec:
  endpointRef: homelab
  name: Compute
  slug: compute
```

`color` is not in that manifest and still reaches NetBox: it is defaulted by the CRD. See
[`spec.color`](#speccolor).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackRole
metadata:
  name: compute
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Compute
  slug: compute
  color: 9e9e9e               # the default: NetBox's own grey, six hex digits, no leading '#'
  description: Hypervisors and their storage
  comments: |
    Racks in this role are on the redundant feed.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | yes | — | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | yes | — | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |
| `color` | `string`, `^[0-9a-f]{6}$` | no | `9e9e9e` | `color`, `ColorField` |
| `description` | `string`, ≤200 | no | — | `description` (`OrganizationalModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`OrganizationalModel`), `TextField` |

### `spec.name`

Required. The role's name, up to 100 characters.

**Column-unique** (`name (OrganizationalModel) CharField REQ UNIQUE len=100`), unlike the
nested-group kinds' names. It is a candidate key and deliberately not the lookup key: a kind
gets one identity and `slug` is the stable one, so a rename that collides comes back as
NetBox's own 409 reported as `Ready=False, Reason=Invalid` rather than being adopted under
the other candidate.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and **globally unique** across the whole NetBox.

### `spec.color`

Optional, six lowercase hexadecimal digits without a leading `#`. **Defaulted to `9e9e9e`**,
NetBox's own grey (`docs/netbox-schema.md` → `dcim.RackRole`,
`color ColorField def=UNRESOLVED:ColorChoices.COLOR_GREY`; the digest records the symbol
rather than its value because the AST walk does not evaluate one).

Defaulted deliberately rather than left absent: a field NetBox fills in and the operator never
sends is a field the operator can never correct, so a colour changed in the UI would stay
changed. Because it is defaulted it is never *absent*, which is why it carries no
"omit versus empty" sentence — there is no third state for it to be in.

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and
set are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`
(`docs/netbox-schema.md` → `dcim.RackRole`, `comments (OrganizationalModel) TextField`), so it
has no `max_length` and there is no length marker to derive from one.

Clearable on the same three-state terms as `description`.

Worth noting against the earlier organisational kinds: `comments` is exposed here.
[`NetBoxRegion`](netboxregion.md), [`NetBoxSiteGroup`](netboxsitegroup.md) and
[`NetBoxClusterType`](netboxclustertype.md) map `name`/`slug`/`description` only and record
that omission in `hack/coverage-exclusions.yaml`; the kinds that shipped after them, this one
included, carry the column.

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

`dcim.RackRole` declares **no `meta.constraints` at all** — its `Meta` carries only
`ordering: ('name',)` (`docs/netbox-schema.md` → `dcim.RackRole`). So the identity does not
come from a constraint list; it comes from the base class's *column-level* `UNIQUE` on `slug`,
which `OrganizationalModel` declares. Uniqueness is therefore global and there is nothing to
pin to null.

The filter is registered: `RackRoleFilterSet` declares nothing of its own and its
`Meta.fields` are `('id', 'name', 'slug', 'color', 'description')`
(NetBox 4.6.8, `netbox/dcim/filtersets.py:328`, bases `OrganizationalModelFilterSet`).

`name` is `UNIQUE` too and is deliberately **not** a second candidate, for the reason
[`spec.name`](#specname) gives.

## The contrast that decides the natural key

[`NetBoxDeviceRole`](netboxdevicerole.md) is the same word in the same app, and its key is
`(parent, slug)` plus `slug WHERE parent IS NULL`. This one is `slug` alone. The difference is
not the app and not how role-like the Kind feels — it is the base class and the constraint
list:

| | `dcim.RackRole` | `dcim.DeviceRole` |
|---|---|---|
| Base class | `OrganizationalModel` | `NestedGroupModel` |
| `parent` column | none | `TreeForeignKey`, `on_delete=CASCADE` |
| `meta.constraints` | none | four, two of them conditional on `parent__isnull=True` |
| `slug` uniqueness | column-level `UNIQUE` | per parent |
| Natural key | `slug` | `(parent, slug)`, `slug` pinned on `parent_id=null` |
| Containment parent | none | `parentRef` |

Both readings are in `docs/netbox-schema.md`, and neither is derivable from the Kind's name.
[`NetBoxContactRole`](netboxcontactrole.md) makes the same argument from the other direction:
its neighbour in the same file cannot use `slug` at all.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.RackRole` is an `OrganizationalModel`, so it carries both `tags` and `custom_fields` and
is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the role exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A hand-made role is adopted, not duplicated

`slug` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one role in NetBox rather than two. Creating a second one would be
refused by the unique index anyway, so adoption is the only outcome that works — which is why
a fresh operator pointed at a long-running NetBox does not need a migration.

### Two namespaces claiming one slug is one role

NetBox's uniqueness is a database constraint and a namespace boundary does not partition it.
The first CR to reconcile creates or adopts the role; the second finds it already claimed and
reports `Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)).

### No containment parent, in either direction

`dcim.RackRole` has **no foreign key at all** bar `owner`, so there is nothing that could be a
containment parent ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

The reference pointing *at* it is `Rack.role ForeignKey -> dcim.RackRole on_delete=PROTECT`
(`docs/netbox-schema.md` → `dcim.Rack`), so deleting a role in use is **refused** by NetBox
rather than cascading: the CR reports `Deleting=False, Reason=Protected` naming the blocker.
Delete the racks, or move them to another role, first.

### `deletionPolicy` defaults to `Delete`

`Delete`, like every kind since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304). A rack role is configuration a manifest
recreates verbatim; nothing about deleting one frees a resource somebody else can take, which
is what `Retain` was reserved for. See [deletion](../concepts/deletion.md).

### `rack_count` is never written

`dcim.RackRole` declares one `CounterCacheField`, `rack_count`, which NetBox maintains from
the racks pointing at the role (`docs/netbox-schema.md`, preamble on every
`CounterCacheField`). It is in the descriptor's read-only list. Writing it would not fail —
NetBox drops it — which is precisely why it has to be declared: a dropped write produces a
difference the next reconcile finds again, and PATCHes forever.

### Renaming the slug changes identity

`slug` is the natural key, so editing it does not rename the NetBox role — it changes what the
CR is looking for, and the next reconcile creates a second role, leaving the first behind.
`name`, `color`, `description` and `comments` are safe to edit.

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at: a field the CRD accepted
and the payload dropped would report success while writing nothing. `tags` and `customFields`
are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbrackrole
NAME      SLUG      COLOR    ID   READY   AGE
compute   compute   9e9e9e   80   True    3m
network   network   2196f3   81   True    3m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `COLOR` | `.spec.color` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this slug, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Pick a different slug, or set `onConflict: Adopt` in the namespace that should own it. |
| `Ready=False`, `Reason=Invalid`, message names `name` | `Ready` | A rename collided with the column-level `UNIQUE` on `name`. | Pick another name; `slug` is what identifies the object. |
| `Ready=False`, `Reason=Invalid`, message names `color` | `Ready` | `color` was given with a leading `#`, or in upper case. | Six lowercase hex digits, no `#`. Admission rejects most of these first. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the role re-enqueues off its event. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A rack still has this role — `Rack.role` is `PROTECT`. | Move or delete those racks, or set `deletionPolicy: Retain`. |
| A second role appeared after an edit | — | `spec.slug` was changed. | See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |

## Related

- [`NetBoxRackGroup`](netboxrackgroup.md) — the other `OrganizationalModel` in this ticket, and the one whose name misleads
- [`NetBoxDeviceRole`](netboxdevicerole.md) — the same word, the other base class, a different identity
- [`NetBoxContactRole`](netboxcontactrole.md) — the same `slug`-alone derivation, argued from the other side
- [`NetBoxManufacturer`](netboxmanufacturer.md) — the other kind with no `meta.constraints` and a column-unique slug
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete, and which kinds default to `Retain`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
