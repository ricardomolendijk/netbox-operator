# `NetBoxLocation`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxLocation` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nblocation` |
| Status subresource | yes |
| Lands with | NBO-066 (M3) |

A `NetBoxLocation` is one `dcim.Location` in NetBox: a nestable subdivision **within a site** —
a floor, a room, a rack row — and a valid target for [`spec.scope`](genericref.md#scoperef) on
every scoped kind.

It is the third `NestedGroupModel` kind and the first one with a **required** reference:
`site ForeignKey REQ -> dcim.Site on_delete=CASCADE` (`docs/netbox-schema.md` →
`dcim.Location`). That single column makes this Kind different from its two siblings in three
ways that are all visible below — its identity is a *pair* of references, it declares a
containment parent, and it cannot make any NetBox write until its site resolves.

> `dcim.Location` is the kind [ADR-0002](../decisions/0002-crd-scoping.md) singles out. It
> reads like a catalogue object, so a cluster-scoped CRD looks right — but a cluster-scoped
> location pointing at a namespaced site would be a reference nobody could authorise. It is
> namespaced like everything else in `v1alpha1`.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxLocation
metadata:
  name: ground-floor
  namespace: default
spec:
  endpointRef: homelab
  name: Ground floor
  slug: ground-floor
  siteRef:
    name: home          # a NetBoxSite in this namespace
```

`siteRef` is not optional. There is no such thing as a location outside a site.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxLocation
metadata:
  name: rack-row-1
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Rack row 1
  slug: rack-row-1
  status: active              # planned | staging | active | decommissioning | retired
  facility: Building A
  description: Cold aisle

  siteRef:
    name: home
    # namespace: netbox-catalog   # crossing one needs a NetBoxRefGrant

  # Nests this location under another one. Identity is (site, parent, name), so *both*
  # references have to resolve before this object can be looked up at all.
  parentRef:
    name: ground-floor
```

A runnable pair is [`../../config/samples/netbox_v1alpha1_netboxlocation.yaml`](../../config/samples/netbox_v1alpha1_netboxlocation.yaml).

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared
envelope and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

Required. Up to 100 characters.

Unique per `(site, parent)` rather than globally
(`docs/netbox-schema.md` → `dcim.Location.meta.constraints`). Two rooms called `Ground floor`
in two different sites are an ordinary NetBox state.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`.

Not the natural key. NetBox constrains `(site, parent, slug)` and `(site, slug) WHERE parent IS
NULL`, so a slug identifies a location no better than a name does, and a kind gets one identity.

### `spec.siteRef`

**Required.** An [`ObjectRef`](../concepts/references.md) pointing at a `NetBoxSite`.

Two things follow from it, and neither is optional behaviour:

- **It is in every natural key.** So an unresolved `siteRef` leaves no applicable candidate
  either, on top of the engine-wide rule that a declared reference is a precondition for the
  write — two reasons for one outcome, *zero* NetBox writes. See
  [Kind-specific behaviour](#an-unresolved-siteref-writes-nothing-at-all).
- **It is the containment reference.** The descriptor declares it as such, which under
  [ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 makes the `NetBoxSite` this
  location's containment parent — the Kubernetes counterpart of NetBox's own
  `on_delete=CASCADE`. Exactly one field may carry that, which is why `parentRef` does not,
  even though `parent` is `CASCADE` too: Kubernetes garbage collection waits for *every*
  owner, so two containment owners would silently turn "delete the site" into "delete the site
  **and** the parent location". See
  [why the site and not the parent](#why-the-site-and-not-the-parent).

### `spec.parentRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at another `NetBoxLocation`.

Self-referential (`dcim.Location.parent` is a `TreeForeignKey` to `dcim.Location`). Omitting it
makes a location at the top level *of its site*, which is a different identity rather than the
same identity with a field missing.

### `spec.status`

| | |
|---|---|
| Type | `string`, one of `planned` `staging` `active` `decommissioning` `retired` |
| Required | no |
| Default | `active`, which is also NetBox's own default |

A choice column, rejected at admission by the CRD's enum, so a typo fails your `kubectl apply`
rather than surfacing as a NetBox 400 one reconcile later.

Because it carries a default it is never absent: the operator manages it from the first
reconcile, and a status changed in the NetBox UI is drift and is corrected back.

`LocationStatusChoices` and `SiteStatusChoices` happen to hold the same five values and are
still two separate Go types here, because they are two separate `ChoiceSet`s in NetBox — both
extensible through `FIELD_CHOICES`, so one shared enum would make a value added to one of them
silently legal on the other.

### `spec.facility`, `spec.description`

Optional free text — `facility` up to 50 characters, `description` up to 200.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and
set are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

Two candidates, tried in this order, from `dcim.Location.meta.constraints`:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(site, parent, name)` | `?site_id=<id>&parent_id=<id>&name=<name>` | `siteRef` **and** `parentRef` resolve |
| 2 | `(site, name)` where `parent IS NULL` | `?site_id=<id>&parent_id=null&name=<name>` | `siteRef` resolves and `parentRef` was **never declared** |

Both start at `site`, because every constraint NetBox declares on the model does. That is the
substantive difference from [`NetBoxRegion`](netboxregion.md): a location's name is unique
*within a site*, never globally, so a lookup with `site_id` merely omitted would match a
location of that name in somebody else's site — and adopting it would PATCH it into this one.

`parent_id=null` is pinned rather than omitted, for the same reason it is on the other
two nested-group kinds. See
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.Location` is a `NestedGroupModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the location exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `siteRef`, and `parentRef` if set, resolved | either does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `ParentOwned` | `siteRef` resolved to a site in this namespace, so deleting it cascades | `siteRef` resolved to a site in another namespace, to a raw `id` or `slug`, or ownership was declined | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### An unresolved `siteRef` writes nothing at all

Point `siteRef` at a `NetBoxSite` that does not exist and the object reports:

```
RefsResolved  False  RefNotFound    siteRef -> home: no NetBoxSite of that name
Ready         False  WaitingForRef
```

and **makes no NetBox request that writes anything**. That is stronger than "the reference is
reported", and it is asserted on recorded traffic in
`internal/controller/dcim_location_controller_test.go`, because a version that reported the
reference and created the object anyway would look identical in the status.

This kind is **not special in that**, and used to be. It is what every kind does with a
declared reference that did not resolve
([a declared reference is a precondition](../concepts/reconciliation.md#a-declared-reference-is-a-precondition-for-the-write),
[#195](https://github.com/ricardomolendijk/netbox-operator/issues/195)). Before that decision,
a kind whose identity did *not* include the reference — `ipam.Prefix` and its `scope` — created
the object with the column omitted instead, and the difference between the two was an accident
of natural-key membership rather than anything anyone chose. What this page can still claim as
its own is that `siteRef` is *required*, so there is no shape of a `NetBoxLocation` that gets
created without one.

### Why the site and not the parent

`siteRef` is this kind's containment reference, so the `NetBoxSite` becomes a
**non-controller owner** of this CR and `kubectl delete netboxsite home` takes its locations
with it — matching what `on_delete=CASCADE` already does inside NetBox.

This is the one kind where the choice needs an argument, because **both** candidates cascade:
`dcim.Location.site` and `dcim.Location.parent` are each `on_delete=CASCADE`, and
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 allows exactly one. `siteRef`
wins on three counts:

- **`site` is required and `parent` is not.** A containment parent on the optional `parentRef`
  would leave every *top-level* location — the common shape — with no owner reference at all.
  `siteRef` is set on every location there can be.
- **Deleting the site is the larger deletion.** It cascades to every location in the site,
  nested ones included, so its rows are a superset of what deleting any one parent location
  takes.
- **The parent path is already covered, by identity rather than by ownership.** Every natural-key
  candidate on this kind reads `parent_id` or pins it null, so a location whose `parentRef`
  stops resolving has *no applicable candidate*: the engine reports
  `Ready=False, Reason=WaitingForKey` and waits, instead of re-creating a row NetBox
  cascade-deleted. That is the failure the containment rule exists to prevent, and on this path
  it is unreachable without the owner reference.

The owner reference is only set when the site is in the same namespace, because an owner
reference may never cross one. A location whose site lives in a shared catalogue namespace
reports `ParentOwned=False, Reason=CascadeUnavailable` and does not cascade — the common shape,
so read that condition before relying on it. See [ownership](../concepts/ownership.md).

### A `parentRef` cycle is reported on both objects

`a.parentRef → b` and `b.parentRef → a` cannot resolve in any order, and the operator reports
`RefsResolved=False, Reason=RefCycle` naming the ring on every member of it, before making any
NetBox request. See [cycles](../concepts/references.md#cycles).

### `_depth` and `_children` are never written

`dcim.Location` is an MPTT tree, so NetBox maintains `_depth` and `_children` itself
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns). Both are in the descriptor's
read-only list. Writing either would not fail — NetBox ignores it — which is precisely why it
has to be declared: an ignored write produces a difference the next reconcile finds again, and
PATCHes forever.

### Renaming changes identity

`name` participates in both natural keys, so editing `spec.name` changes what the CR is looking
for rather than renaming the NetBox location. So does **repointing `siteRef`**: a location is
identified by its site, so moving it is a new identity and the old object is left behind.
`slug`, `status`, `facility` and `description` are safe to edit.

### What is not here yet

**`tenant` is deliberately absent.** `dcim.Location` has the foreign key, but `NetBoxTenant` is
NBO-021 and the `tenantRef` field belongs to that ticket. A field the CRD accepted and the
payload dropped would report success while writing nothing, which is worse than a field that is
not there.

`tags` and `customFields` are columns this model carries and the CRD does not expose yet — the
provenance stamp writes both, a user cannot.

## Printer columns

```
$ kubectl get nblocation
NAME           SLUG           SITE   PARENT         ID   READY   AGE
ground-floor   ground-floor   home                  61   True    3m
rack-row-1     rack-row-1     home   ground-floor   62   True    3m
```

`SITE` and `PARENT` read `.spec.siteRef.name` and `.spec.parentRef.name`, so they show the
*intent* even while a reference is unresolved and `ID` is empty — which is the pair you want
side by side while diagnosing a `WaitingForRef`.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `siteRef` | The `NetBoxSite` does not exist, or holds no `status.id` yet. Nothing was written. |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `parentRef` | Same, for the parent location. Candidate 1 needs it resolved and candidate 2 needs it undeclared, so neither applies. |
| `RefsResolved=False`, `Reason=RefDenied` | `siteRef.namespace` crosses a namespace with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target one. |
| `RefsResolved=False`, `Reason=RefCycle` | Two locations name each other as parent. Edit either one. |
| `Ready=False`, `Reason=Conflict` | More than one NetBox location matched, or one matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. |
| A second location appeared after an edit | `spec.name` or `spec.siteRef` was changed. See [Renaming changes identity](#renaming-changes-identity). |
| The site was deleted and the location CR is still there | Read `ParentOwned`. `CascadeUnavailable` means the site was in another namespace, where an owner reference is illegal. See [why the site and not the parent](#why-the-site-and-not-the-parent). |
| A delete hangs, `Reason=Protected` | Racks or devices in NetBox still reference the location. The message names them. |

## Related

- [`NetBoxSite`](netboxsite.md) — the required target of `siteRef`, and this kind's owner
- [`NetBoxSiteGroup`](netboxsitegroup.md) — the sibling nested-group kind with no required reference
- [`NetBoxRegion`](netboxregion.md) — the first nested-group kind
- [Generic references](genericref.md#scoperef) — the union this Kind is a member of
- [References](../concepts/references.md) — the four ref modes, cycles, and grants
- [ADR-0003](../decisions/0003-ownership-and-references.md) — why exactly one containment owner
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
