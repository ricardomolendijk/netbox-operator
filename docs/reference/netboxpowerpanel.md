# `NetBoxPowerPanel`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxPowerPanel` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbpowerpanel` |
| Status subresource | yes |

A `NetBoxPowerPanel` is one `dcim.PowerPanel` in NetBox: a distribution panel in a site, from
which [`NetBoxPowerFeed`](netboxpowerfeed.md) circuits hang.

Four writable columns of substance and one database-backed identity, which makes it the
plainest kind in the power block — and the reason it is worth reading next to
[`NetBoxRack`](netboxrack.md), which sits one NetBox source file away and gets the *opposite*
answer to the same question. Both have a required `site` and an optional `location`. On a rack,
every unique constraint is keyed on the optional column, so a location-less rack has no enforced
identity at all. On a panel, the constraint is `(site, name)` and `location` is in nothing — so
the identity is exactly as strong as it looks. See [natural keys](#natural-keys).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPowerPanel
metadata:
  name: panel-a
  namespace: default
spec:
  endpointRef: homelab
  name: Panel A
  siteRef:
    name: home
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPowerPanel
metadata:
  name: panel-a
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Panel A
  siteRef:
    name: home
  locationRef:
    name: rack-row-1

  description: Main distribution panel, front cage
  comments: |
    Fed from the building's B riser. The colo provider owns everything upstream.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | yes | — | `name`, `CharField REQ len=100` |
| `siteRef` | [reference](../concepts/references.md) → [`NetBoxSite`](netboxsite.md) | yes | — | `site`, `ForeignKey REQ -> dcim.Site on_delete=PROTECT` |
| `locationRef` | reference → [`NetBoxLocation`](netboxlocation.md) | no | — | `location`, `ForeignKey -> dcim.Location on_delete=PROTECT` |
| `description` | `string`, ≤200 | no | — | `description` (`PrimaryModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`PrimaryModel`), `TextField` |

### `spec.name`

Required, up to 100 characters. Unique **per site**
(`docs/netbox-schema.md` → `dcim.PowerPanel`, `meta.constraints`), so `Panel A` in two sites is
legitimate NetBox state and two in one site is not — whatever their locations.

### `spec.siteRef`

Required, because NetBox's column is `REQ` and there is no such thing as a panel outside a site.

It is half the natural key, so until it resolves the object reports
`RefsResolved=False, Reason=WaitingForRef` (or `RefNotFound`) naming this field and **makes no
NetBox write at all**. That is the important consequence rather than a nicety: `?name=Panel+A`
with the site dropped matches every panel of that name in the NetBox, and django-filter answers
an unrecognised or absent filter with the *unfiltered* set rather than an error, so the engine
would adopt a stranger's panel and PATCH its site
([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206)).

**Not a containment reference.** `on_delete=PROTECT` means NetBox refuses to delete a site while
a panel points at it, so there is no server-side deletion for an owner reference to mirror
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). `kubectl delete netboxsite`
is refused on the *site*, reported as `Deleting=False, Reason=Protected`; delete the panels
first.

### `spec.locationRef`

Optional, and the field to read next to [`NetBoxRack`](netboxrack.md#speclocationref), where
the identically named field is the single most load-bearing optional field in the spec. Here it
carries no weight in the identity at all: `dcim.PowerPanel`'s one constraint is `(site, name)`,
so setting it, clearing it or never declaring it changes nothing about which NetBox row this CR
resolves to, and two panels of one name in one site are refused however their locations differ.

The cascade differs too. `dcim.Rack.location` is `SET_NULL`; this one is `PROTECT`
(`docs/netbox-schema.md` → `dcim.PowerPanel`). Two kinds in one app pointing at one model with
two different cascades is why the answer is read per column rather than per target — and neither
is a containment parent, for the reason `siteRef` gives.

A pointer to the typed alias, so it has two states rather than three: absent means unmanaged,
and a value claims the column. Clearing the column from a manifest needs `registry.EmptyIsNull`
and an `OptionalRef`, a third state no shipped kind uses yet
([#185](https://github.com/ricardomolendijk/netbox-operator/issues/185)). Until then the way to
move a panel out of a location is to clear it in NetBox and stop declaring the field.

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`, so it has no `max_length` and
there is no length marker to derive from one. Clearable on the same three-state terms as
`description`.

## Natural keys

One candidate, no fallback and no null pin:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(site, name)` | `?site_id=<id>&name=<name>` | `siteRef` has resolved |

It is a real database constraint rather than a convention, and both halves of that claim are
checkable against committed artefacts:

```
meta.constraints: (models.UniqueConstraint(fields=('site', 'name'),
   name='%(app_label)s_%(class)s_unique_site_name'),)
```

(`docs/netbox-schema.md` → `dcim.PowerPanel`.) And the IR resolves the same constraint to the
*filters* as well as the columns — `hack/testdata/ir-4.6.8.json.gz` → `dcim.PowerPanel`,
`natural_keys` is a single entry over `{column: site, filter: site_id}` and
`{column: name, filter: name}`, with `null_fields: []` and `unusable: null`. Both filters are
registered on `PowerPanelFilterSet`.

That second artefact is why the filter names are checked rather than guessed. NetBox's
`BaseFilterSet` drops a query parameter it does not recognise and answers with the *unfiltered*
set, so a wrong filter name is a lookup that matches every panel in the NetBox — and on a kind
that adopts what it finds, that is the worst possible failure
([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206),
[#216](https://github.com/ricardomolendijk/netbox-operator/issues/216)).

Because `site` is `REQ`, every panel satisfies the constraint. So unlike
[`NetBoxRack`](netboxrack.md#natural-keys) there is no location-less carve-out, no
`?location_id=null` pin, and an ambiguous match is *impossible* rather than merely reported:
Postgres will not hold two.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.PowerPanel` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the panel exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForRef`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `siteRef` and any `locationRef` resolved | either has not | `AllResolved`, `WaitingForRef`, `RefNotFound`, `RefNotReady`, `RefKindUnavailable`, `RefForbidden`, `RefCycle` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A hand-made panel is adopted, not duplicated

`(site, name)` is a unique index, so the lookup finds an existing row and — with
`onConflict: Adopt` — the engine takes it over: `status.adopted=true`, and one panel in NetBox
rather than two. Creating a second one would be refused by the index anyway, so adoption is the
only outcome that works.

### No containment parent, in either direction

Every foreign key on the model is `PROTECT` bar `owner`, so nothing on the server side
disappears when a parent does and there is nothing for an owner reference to mirror
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). `registry.Validate` would
refuse one at boot with `ErrContainmentNotCascade`.

The reference pointing *at* it, `PowerFeed.power_panel`, is `PROTECT` too, so deleting a panel
that still has feeds is refused by NetBox rather than cascading: the CR reports
`Deleting=False, Reason=Protected` naming the blocker. Delete the feeds first.

### `powerfeed_count` is never written

`powerfeed_count` is a `RelatedObjectCountField` NetBox maintains from the feeds pointing at the
panel (`hack/testdata/api-schema-4.6.8.json.gz` → `serializers.PowerPanelSerializer.declared`),
and it is in the descriptor's read-only list. Writing it would not fail — NetBox drops it — which
is precisely why it has to be declared: a dropped write produces a difference the next reconcile
finds again, and PATCHes forever.

### `deletionPolicy` defaults to `Delete`

As on every Kind since
[#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) — there is no per-Kind
table any more. A panel is configuration a manifest recreates, and what actually keeps
`kubectl delete` from taking a live path apart is NetBox's own `PROTECT` on
`PowerFeed.power_panel`: the delete is refused while feeds still point at the panel, and the CR
stays and says so. See [deletion](../concepts/deletion.md).

### Renaming changes identity

`name` is half the natural key, so editing it does not rename the NetBox panel — it changes what
the CR is looking for, and the next reconcile creates a second panel, leaving the first behind.
Moving the panel between sites has the same effect. `locationRef`, `description` and `comments`
are safe to edit.

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at. `tags` and `customFields` are
written by the provenance stamp and not by a user. `contacts` and `images` are `GenericRelation`s
— the far end of somebody else's foreign key; a panel is a legal `ContactAssignment` target
through the union on [`NetBoxContactAssignment`](netboxcontactassignment.md), written from the
*other* object.

Nothing else is missing: this model has five writable columns of substance and the CRD maps all
five.

## Printer columns

```
$ kubectl get nbpowerpanel
NAME      SITE   LOCATION      ID    READY   AGE
panel-a   home   rack-row-1    101   True    2m
panel-b   home                 102   True    2m
```

| Column | JSONPath |
|---|---|
| `SITE` | `.spec.siteRef.name` |
| `LOCATION` | `.spec.locationRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `RefsResolved=False`, `Reason=RefNotFound`, message names `siteRef` | `RefsResolved` | No [`NetBoxSite`](netboxsite.md) of that name in the namespace. Nothing was written to NetBox. | Apply the site, or point at it by `slug`/`id` if it is unmanaged. |
| `Ready=False`, `Reason=Conflict` | `Ready` | A panel with this `(site, name)` already exists and `onConflict` is `Fail`, or another namespace owns it. `status.naturalKey` shows what was searched. | Set `onConflict: Adopt` in the namespace that should own it, or rename. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A [`NetBoxPowerFeed`](netboxpowerfeed.md) still points at this panel — `PowerFeed.power_panel` is `PROTECT`. | Delete the feeds first, or set `deletionPolicy: Retain`. |
| A second panel appeared after an edit | — | `spec.name` or `spec.siteRef` was changed; both are in the natural key. | See [Renaming changes identity](#renaming-changes-identity). |
| `spec.locationRef` set and the panel still resolves to the same row | — | Working as designed. `location` is in no natural-key candidate on this Kind. | See [`spec.locationRef`](#speclocationref). |

## Related

- [`NetBoxPowerFeed`](netboxpowerfeed.md) — the circuits that hang off this panel, and the other kind in this PR
- [`NetBoxRack`](netboxrack.md) — the same `site`/`location` pair with the opposite identity answer, and the opposite `location` cascade
- [`NetBoxSite`](netboxsite.md) — the required parent, and where a refused delete is reported
- [`NetBoxLocation`](netboxlocation.md) — the optional one
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
