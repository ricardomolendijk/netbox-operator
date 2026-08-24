# `NetBoxSiteGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxSiteGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbsitegroup` |
| Status subresource | yes |
| Lands with | NBO-066 (M3) |

A `NetBoxSiteGroup` is one `dcim.SiteGroup` in NetBox: a named, nestable **functional**
grouping of sites — branch, campus, colo — and a valid target for
[`spec.scope`](genericref.md#scoperef) on every scoped kind.

It is the same NetBox model as [`NetBoxRegion`](netboxregion.md) under a different name. Both
are `NestedGroupModel`s with the same inherited columns and the same four unique constraints
(`docs/netbox-schema.md` → `dcim.SiteGroup`), and NetBox keeps the two hierarchies
independent: a site has both a `region` and a `group`, and neither implies the other.

Its reason for existing this early is the scope union. `siteGroupRef` was a declared member of
`ScopeRef` with no Descriptor behind it, so it resolved to `RefKindUnavailable` in **all four**
reference modes — a Descriptor is the only thing that holds a Kind's REST endpoint, and
`slug`, `lookup` and `id` all need one. This Kind is what turns that member on.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSiteGroup
metadata:
  name: retail
  namespace: default
spec:
  endpointRef: homelab
  name: Retail
  slug: retail
```

No `parentRef`, so this is a top-level group — which is a *different identity* rather than the
same identity with a field missing. See [Natural keys](#natural-keys).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSiteGroup
metadata:
  name: retail-eu
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Retail EU
  slug: retail-eu
  description: European retail estate

  # Nests this group. Applying parent and child in either order converges.
  parentRef:
    name: retail
```

A runnable pair is [`../../config/samples/netbox_v1alpha1_netboxsitegroup.yaml`](../../config/samples/netbox_v1alpha1_netboxsitegroup.yaml).

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared
envelope and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

Required. The group's name, up to 100 characters.

**Not globally unique.** `dcim.SiteGroup.meta.constraints` makes it unique per parent, with a
separate constraint covering top-level groups. Two groups called `EU` under different parents
are a legitimate NetBox state, not a collision.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`.

`slug` is **not** this kind's natural key, exactly as on [`NetBoxRegion`](netboxregion.md). Two
of NetBox's four constraints are on `(parent, slug)` and `slug WHERE parent IS NULL`, so a slug
identifies a group no better than a name does — and a kind gets one identity, which here is the
pair the constraints lead with.

### `spec.parentRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at another `NetBoxSiteGroup`.

Self-referential: `dcim.SiteGroup.parent` is a `TreeForeignKey` to `dcim.SiteGroup`. Omitting
it makes a **top-level** group.

It is also this kind's **containment reference**, so a child group carries a non-controller
owner reference to its parent — see
[a child group is owned by its parent](#a-child-group-is-owned-by-its-parent).

Written to NetBox as `parent`, filtered as `parent_id`. Both spellings appear below, because
the write name and the filter name genuinely differ for a foreign key.

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and
set are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

Two candidates, tried in this order, straight out of `dcim.SiteGroup.meta.constraints`:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(parent, name)` | `?parent_id=<id>&name=<name>` | `parentRef` **resolves** to an id |
| 2 | `name` where `parent IS NULL` | `?parent_id__isnull=true&name=<name>` | `parentRef` was **never declared** |

Not a fallback chain. Candidate 2 is not "what to try if 1 fails" — it is the identity of a
*different* object, a top-level group. A child whose parent has not been created yet matches
**neither**, and the engine waits: falling through would find an unrelated top-level group of
that name, adopt it, and the follow-up PATCH would reparent somebody else's data.

`parent_id__isnull=true` is pinned rather than omitted. A query with `parent_id` merely left
out matches a group of that name under *any* parent. See
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.SiteGroup` is a `NestedGroupModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

`status.naturalKey` records which of the two candidates ran, filter by filter, so
`{"parent_id__isnull": "true", "name": "Retail"}` tells you the engine treated the object as
top-level.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `parentRef` is unset or resolved | `parentRef` does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `ParentOwned` | `parentRef` resolved to a group in this namespace, so deleting it cascades | `parentRef` resolved to a group in another namespace, to a raw `id` or `slug`, or ownership was declined | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A child group is owned by its parent

`parentRef` is this kind's containment reference, so a child group carries a non-controller
owner reference to its parent and `kubectl delete` on the parent takes its children with it.

Not a convenience, and not a preference either: the containment parent is whichever foreign key
the *server* cascades, and `dcim.SiteGroup.parent` is `on_delete=CASCADE`. Deleting a site group
in NetBox deletes its descendants server-side, so without the owner reference the child CR would
outlive the row it described, find nothing at `status.id`, and be **re-created** by the engine's
create-if-absent step — a group NetBox deleted on purpose, put back.

The owner reference is only set when the parent is in the same namespace, because an owner
reference may never cross one. A group whose parent lives in a shared catalogue namespace
reports `ParentOwned=False, Reason=CascadeUnavailable` and does not cascade — the common shape,
so read that condition before relying on the cascade. See
[ownership](../concepts/ownership.md) for the whole rule and the opt-out annotation.

### A child converges off its parent's event, in either order

Apply the child first and it reports `RefsResolved=False, Reason=RefNotFound` and
`Ready=False, Reason=WaitingForRef`, and performs **zero NetBox writes** — candidate 1 needs
`parentRef` resolved, candidate 2 needs it undeclared, so neither applies. Applying the parent
re-enqueues the child directly through the reference watch, so it converges without waiting for
the endpoint's `resyncPeriod`.

### A `parentRef` cycle is reported on both objects

`a.parentRef → b` and `b.parentRef → a` cannot resolve in any order. The operator walks the
reference graph before it makes any NetBox request, and reports
`RefsResolved=False, Reason=RefCycle` naming the ring, on **every** member of it — a user who
saw it on one object and not the other would conclude the other was fine. See
[cycles](../concepts/references.md#cycles).

### Renaming changes identity

`name` participates in both natural keys, so editing `spec.name` does not rename the NetBox
group — it changes what the CR is looking for, and the next reconcile creates a second group,
leaving the first behind. `slug` and `description` are safe to edit.

### `_depth` and `_children` are never written

`dcim.SiteGroup` is an MPTT tree, so NetBox maintains `_depth` and `_children` itself
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns). Both are in the descriptor's
read-only list. Writing either would not fail — NetBox ignores it — which is precisely why it
has to be declared: an ignored write produces a difference the next reconcile finds again, and
PATCHes forever.

### Every field here is inherited

None of `name`, `slug`, `description` or `parent` is declared on `dcim.SiteGroup` itself:
`OrganizationalModel` gives the first three and `NestedGroupModel` adds `parent`. The schema
entry lists only the model's `GenericRelation`s, and its own preamble warns against deriving a
CRD's fields from an entry without reading the model's bases.

### What is not here yet

`tags` and `customFields` are columns this model carries and the CRD does not expose yet — the
provenance stamp writes both, a user cannot. `tenant` does not exist on this model at all.

## Printer columns

```
$ kubectl get nbsitegroup
NAME        SLUG        PARENT   ID   READY   AGE
retail      retail               52   True    3m
retail-eu   retail-eu   retail   53   True    3m
```

`PARENT` reads `.spec.parentRef.name`, so it shows the *intent* even while the reference is
unresolved and `ID` is empty.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Ready=False`, `Reason=WaitingForRef`, `parentRef` set | The parent CR does not exist, or holds no `status.id` yet. `RefsResolved` says which. |
| `Ready=False`, `Reason=WaitingForKey`, no `parentRef` | Not expected — check `spec.name` is non-empty. |
| `Ready=False`, `Reason=Conflict` | More than one NetBox group matched, or one matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. |
| `RefsResolved=False`, `Reason=RefCycle` | Two groups name each other as parent. Edit either one. |
| `RefsResolved=False`, `Reason=RefDenied` | A cross-namespace `parentRef` with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. |
| A second group appeared after an edit | `spec.name` was changed. See [Renaming changes identity](#renaming-changes-identity). |
| Deleting the parent left the children behind | `ParentOwned` says why. Almost always `CascadeUnavailable` because the parent is in another namespace, where an owner reference is illegal. |

## Related

- [`NetBoxRegion`](netboxregion.md) — the same NetBox model, the geographic hierarchy
- [`NetBoxLocation`](netboxlocation.md) — the third nested-group kind, the one with a required site
- [Generic references](genericref.md#scoperef) — the union this Kind is a member of
- [References](../concepts/references.md) — the four ref modes, cycles, and grants
- [Ownership](../concepts/ownership.md) — why `parentRef` is the containment parent and when the cascade is unavailable
- [Lookups](../concepts/lookups.md) — why a null filter is pinned rather than omitted
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
