# `NetBoxRegion`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRegion` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbregion` |
| Status subresource | yes |
| Lands with | NBO-011 (M2) |

A `NetBoxRegion` is one `dcim.Region` in NetBox: a named, nestable geographic grouping that
sites, prefixes, VLAN groups, clusters and wireless LANs can be scoped to.

It is the first kind whose **identity depends on a reference**, which is the only reason it
exists this early. `dcim.Region` is unique on `(parent, name)` *and* separately on `(name)`
where `parent IS NULL`, so whether `parentRef` is set decides *which* natural key applies
rather than merely changing one filter's value. That makes it the smallest honest test of
the reference machinery M2 is building.

> **Today a child region does not reach `Ready`.** Reference resolution is
> [NBO-012 (#24)](https://github.com/ricardomolendijk/netbox-operator/issues/24). A
> top-level region works end to end; one with a `parentRef` reports
> `WaitingForKey` and writes nothing. That is correct rather than broken — see
> [Kind-specific behaviour](#a-child-region-waits-rather-than-guessing).

## Minimal example

A top-level region. This works completely today.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRegion
metadata:
  name: europe
  namespace: default
spec:
  endpointRef: homelab
  name: Europe
  slug: europe
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRegion
metadata:
  name: eu-west
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Adopt
  deletionPolicy: Delete

  name: EU West
  slug: eu-west
  description: Western Europe

  # Nests this region. Not resolved yet (NBO-012).
  parentRef:
    name: europe
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

Required. The region's name, up to 100 characters.

**Not globally unique**, unlike a `NetBoxSite`'s. `dcim.Region.meta.constraints` makes it
unique per parent, with a separate constraint covering top-level regions
(`docs/netbox-schema.md` → `dcim.Region.meta.constraints`). Two regions may share a name
under different parents, and that is a legitimate NetBox state rather than a collision.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`.

Note that `slug` is **not** this kind's natural key, which is unusual — on a `NetBoxTag` it
is. NetBox constrains `(parent, slug)` rather than `slug` alone, so a slug does not identify
a region on its own any more than a name does. The natural keys below use `name` because
that is the pair the constraints lead with.

### `spec.parentRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at another `NetBoxRegion`.

Self-referential: `dcim.Region.parent` is a foreign key to `dcim.Region`. Omitting it makes
a **top-level** region, which is a different identity rather than the same identity with a
field missing — the distinction the two natural keys exist to express.

Written to NetBox as `parent`, filtered as `parent_id`. Both spellings appear below because
the write name and the filter name genuinely differ for a foreign key.

### `spec.description`

Optional free text, up to 200 characters.

## Natural keys

Two candidates, tried in this order, straight out of `dcim.Region.meta.constraints`:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(parent, name)` | `?parent_id=<id>&name=<name>` | `parentRef` **resolves** to an id |
| 2 | `name` where `parent IS NULL` | `?parent_id__isnull=true&name=<name>` | `parentRef` was **never declared** |

The order is not a fallback chain. Candidate 2 is not "what to try if 1 fails" — it is the
identity of a *different* object, a top-level region.

`parent_id__isnull=true` is pinned rather than omitted, and that is the whole point. A query
with `parent_id` merely left out matches a region of that name under *any* parent, so a
top-level region would adopt an unrelated nested one. See
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.provenance` behaves as it does on `NetBoxSite` rather than as on `NetBoxTag`:
`dcim.Region` is a `NestedGroupModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

`status.naturalKey` is worth reading on this kind in particular: it records which of the two
candidates ran, filter by filter, so `{"parent_id__isnull": "true", "name": "Europe"}`
tells you the engine treated the object as top-level.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the region exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun` |
| `RefsResolved` | `parentRef` is unset | `parentRef` is set — always, in this build | `AllResolved`, `NotImplemented` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A child region waits rather than guessing

Set `parentRef` today and the object reports two conditions that together explain
themselves:

```
RefsResolved  False  NotImplemented  references are not resolved yet and were
                                     left out of the payload: [parentRef]
Ready         False  WaitingForKey
```

`WaitingForKey` means no natural-key candidate was applicable, and tracing why is
instructive. Candidate 1 matches on `parentRef`, which requires it to be *resolved* — it is
not. Candidate 2 asserts `parentRef` was never *declared* — it was. So neither applies, and
the engine performs **zero writes**.

That is the designed outcome, not a gap. The alternative — falling through to candidate 2 —
would look up a top-level region of that name, find somebody else's, adopt it, and then
PATCH `parent` onto it, reparenting data the manifest never mentioned. The engine waits
instead, which is the behaviour NBO-015 exists to protect.

A top-level region is unaffected: `parentRef` is undeclared, candidate 2 applies, and it
creates, adopts and drift-corrects normally.

### Renaming changes identity

`name` participates in both natural keys, so editing `spec.name` does not rename the NetBox
region — it changes what the CR is looking for. The next reconcile finds nothing at the new
name and creates a second region, leaving the first behind. This is not specific to regions;
it is what a natural key means. Rename in NetBox and in the manifest together, or delete and
re-create the CR.

`slug` and `description` are safe to edit: neither is part of a natural key here.

### `_depth` and `_children` are never written

`dcim.Region` is an MPTT tree, so NetBox maintains `_depth` and `_children` itself
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns). Both are in the descriptor's
read-only list. Writing either would not fail — NetBox ignores it — which is precisely why
it has to be declared: an ignored write produces a difference the next reconcile finds
again, and PATCHes forever.

### Every field here is inherited

None of `name`, `slug`, `description` or `parent` is declared on `dcim.Region` itself:
`OrganizationalModel` gives the first three and `NestedGroupModel` adds `parent`
(`docs/netbox-schema.md`, preamble on inherited columns). The schema reference lists only
the model's `GenericRelation`s, and its own preamble warns against deriving a CRD's fields
from an entry without reading the model's bases. That warning is why this kind's field list
looks nothing like its schema entry.

## Printer columns

```
NAME     SLUG     PARENT   ID   READY   AGE
europe   europe            41   True    2m
eu-west  eu-west  europe        False   2m
```

`PARENT` reads `.spec.parentRef.name`, so it shows the *intent* even while the reference is
unresolved and `ID` is empty — which is exactly the pair you want side by side while
diagnosing a `WaitingForKey`.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Ready=False`, `Reason=WaitingForKey`, `parentRef` set | Expected in this build. Resolution is NBO-012. |
| `Ready=False`, `Reason=WaitingForKey`, no `parentRef` | Not expected — check `spec.name` is non-empty and the descriptor validated at boot. |
| `Ready=False`, `Reason=Conflict` | More than one NetBox region matched. Two CRs claiming one region, or a name duplicated under the same parent. `status.naturalKey` shows what was searched. |
| A second region appeared after an edit | `spec.name` was changed. See [Renaming changes identity](#renaming-changes-identity). |
| `Ready=False`, `Reason=WaitingForEndpoint` | The `NetBoxEndpoint` is not `Ready`. See [troubleshooting](../operations/troubleshooting.md). |

## Related

- [References](../concepts/references.md) — the four ref modes and what the API server rejects
- [Lookups](../concepts/lookups.md) — why a null filter is pinned rather than omitted
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
- [The reconcile engine](../concepts/engine.md) — the create/adopt/update decision
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
