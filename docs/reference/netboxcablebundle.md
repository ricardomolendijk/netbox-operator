# `NetBoxCableBundle`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCableBundle` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcablebundle` |
| Status subresource | yes |

A `NetBoxCableBundle` is one `dcim.CableBundle` in NetBox: a named grouping of cables that are
pulled together — a trunk, a riser, a patch bundle.

**The model exists**, which is worth saying because NBO-049 asked for the Kind without
evidence and the instruction was not to invent one. `dcim.CableBundle` is in
[`docs/netbox-schema.md`](../netbox-schema.md), its endpoint `dcim/cable-bundles` is in the
endpoint map, and `CableBundleSerializer` is at `netbox/dcim/api/serializers_/cables.py:28`.
(The other kind NBO-049 asked for, `NetBoxCableTermination`, does *not* ship — see
[`NetBoxCable`](netboxcable.md#there-is-no-netboxcabletermination).)

It is the plainest `PrimaryModel` in the catalogue: **one column of its own**,
`name CharField REQ UNIQUE len=100`, on top of the base class's `description` and `comments`.

It is also a new shape of identity. [`NetBoxClusterType`](netboxclustertype.md) keys on `slug`
because it is an `OrganizationalModel`; a `PrimaryModel` has no `slug` at all, so this kind
keys on `name`. That is legal here where it is not on
[`NetBoxContact`](netboxcontact.md) — whose `name` is backed by an index and no constraint, so
two contacts of one name is legal server state and a `Conflict` — because this column carries a
real column-level `UNIQUE` and the database refuses the second row.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCableBundle
metadata:
  name: riser-a
  namespace: team-a
spec:
  endpointRef: homelab
  name: Riser A
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCableBundle
metadata:
  name: riser-a
  namespace: team-a
spec:
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly  (default Fail)
  deletionPolicy: Delete      # Delete | Retain           (default Delete)

  name: Riser A
  description: Rack 1 to rack 8, 48-strand OM4
  comments: |
    Installed 2026-03-11. Strands 25-48 are spare.
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ UNIQUE len=100` |

The bundle's label, and its natural key.

Globally unique in NetBox over namespaced CRs, exactly as a Site's `slug` is: two namespaces
cannot both own `Riser A`, and the second gets `Ready=False, Reason=Conflict` rather than a
second bundle. Keep bundles in one namespace per physical site and reference them across it
with a [`NetBoxRefGrant`](netboxrefgrant.md).

**If it is wrong.** An empty or over-long name is rejected by `kubectl apply` — admission, no
condition. A name another bundle already holds reports `Ready=False, Reason=Conflict` under the
default `onConflict: Fail`, and `status.naturalKey` shows what was searched for.

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description` (`PrimaryModel`), `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it. The two are different
instructions ([field ownership](../concepts/field-ownership.md)).

### `spec.comments`

| Type | `string`, unbounded |
|---|---|
| Required | no |
| NetBox column | `comments` (`PrimaryModel`), `TextField` |

Unbounded on purpose: the column is a `TextField`, so NetBox declares no length and a
`MaxLength` here would be a limit the operator invented.

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### What is deliberately absent

- **`slug`.** `dcim.CableBundle` is a `PrimaryModel` and has none. A `slug`-mode
  [`ObjectRef`](../concepts/references.md) pointing at this Kind therefore cannot resolve —
  use `name`, `lookup` or `id`.
- **`cables`.** There is no inline child list and no reverse field. A cable names its bundle
  ([`NetBoxCable.spec.bundleRef`](netboxcable.md#specbundleref)), not the other way round.
- **`cable_count`.** A read-only `IntegerField` on the serializer
  (`netbox/dcim/api/serializers_/cables.py:28`), returned and never accepted.
- **`owner`.** `users.Owner` is an excluded endpoint, so nothing will ever write it
  ([coverage](../coverage.md)).

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate, no null pin and no fallback. `name` is registered on `CableBundleFilterSet` as a
`MultiValueCharFilter` from `Meta.fields` (`netbox/dcim/filtersets.py:2620`) — checked rather
than assumed, because django-filter drops a parameter it does not recognise and answers with
the *unfiltered* set, so a guessed filter name is a lookup that matches everything.

The column is `REQ UNIQUE`, so there is no state in which it is absent and nothing weaker to
fall back to. Unlike [`NetBoxContact`](netboxcontact.md), the filter here identifies **at most
one** object by construction.

There is no `?name__ie=` case-insensitive variant, and none is needed: the constraint is a
plain column `UNIQUE` rather than one over `Lower('name')`, so NetBox itself distinguishes
`Riser A` from `riser a` and so does the lookup. Contrast
[`NetBoxDevice`](netboxdevice.md) and [`NetBoxVirtualMachine`](netboxvirtualmachine.md), whose
constraints *are* case-folded and which therefore need the modifier to adopt rather than
duplicate.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

Nothing is cleared on failure: `id` and `url` keep naming the object the operator last saw, so
a bundle that fails to update is still traceable to the row it describes.

`dcim.CableBundle` is a `PrimaryModel`, so it mixes in both `TagsMixin` and
`CustomFieldsMixin` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set
([provenance](../operations/provenance.md)).

## Conditions

| Type | `True` when | `False` when | Reasons |
|---|---|---|---|
| `Ready` | the bundle exists in NetBox and matches the spec | anything below is refused or failed | `Synced`, `WaitingForEndpoint`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `RefsResolved` | always — this kind declares no references | never | `AllResolved` |
| `Synced` | the last write succeeded, or there was nothing to write | drift was found and not written | `Synced`, `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `DriftDetected` | NetBox differs from the spec | it does not | `DriftDetected`, `DriftCorrected`, `NoDrift` |
| `Conflict` | another writer's stamp is on this bundle | it is not | `ForeignCluster`, `ForeignOwner` |
| `Deleting` | the CR is being deleted and the bundle is gone | the delete is blocked | `Protected`, `PendingDependents`, `APIError` |

There is no `ParentOwned` condition: this kind has no references at all, so nothing could be a
containment parent.

### Reason glossary

| Reason | Means | Retried |
|---|---|---|
| `Synced` | the bundle exists and matches | — |
| `NoDrift` | the last comparison found nothing to send | — |
| `Conflict` | a bundle with this `name` exists and `onConflict: Fail` | every resync |
| `AdoptOnly` | `onConflict: AdoptOnly` and nothing matched, so nothing was created | every resync |
| `Invalid` | NetBox refused the body | every resync |
| `APIError` | NetBox is unreachable, rate-limiting, or returned a 5xx | with backoff |

### Retry intervals

The endpoint's `resyncPeriod` for everything except an unreachable or rate-limited NetBox,
which backs off. See [errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### Deleting a bundle does not delete its cables

`dcim.Cable.bundle` is `on_delete=SET_NULL` (`docs/netbox-schema.md` → `dcim.Cable`), so
deleting a bundle clears the column on every cable in it and destroys none of them.

That is what makes the default safe. `deletionPolicy` defaults to `Delete` here: a bundle is a
*label*, and losing one loses no record of a connection — unlike an IPAM allocation, which is
the exception decision #176 carved out. And it is why a cable's
[`bundleRef`](netboxcable.md#specbundleref) is an ordinary reference and not a containment one:
the server does not cascade, so an owner reference would delete the cable's CR while NetBox
kept the cable.

There is also nothing to protect against in the other direction — the delete is not
`PROTECT`ed, so unlike [`NetBoxClusterType`](netboxclustertype.md) it does not report
`Deleting=False, Reason=Protected` while something still uses it. It simply goes, and the
cables that named it end up with no bundle. If a cable's CR still says `bundleRef`, the next
reconcile finds the cleared column as drift and PATCHes it back — pointing at a bundle that no
longer exists, which reports `RefsResolved=False, Reason=RefNotFound` on the *cable*. Delete
the bundle and the `bundleRef` lines together.

### No references, and no containment parent

The only foreign key `dcim.CableBundle` has is `owner`, which this operator does not manage.
The relation runs the other way, so this Kind is a **leaf pointed at** rather than a pointer:
`RefsResolved` is `True` from the first reconcile and there is no `ParentOwned` condition.

That makes it the cheapest kind in `dcim` to place in a shared catalogue namespace: nothing it
declares can cross a namespace, so the only grant needed is the one on *this* namespace, for
the cables elsewhere that point at it.

## Printer columns

```
NAME      NAME      ID   READY   AGE
riser-a   Riser A   3    True    5m
patch-3   Patch 3   4    True    5m
```

`NAME` appears twice: `metadata.name`, which `kubectl` always prints, and `spec.name`, which is
the NetBox value. They are related only by convention — `metadata.name` is a DNS-1123 label and
`spec.name` keeps the literal NetBox string, so `Riser A` cannot be both.

| Column | JSONPath |
|---|---|
| `NAME` (second) | `.spec.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply`, `metadata.name` | none — admission | a space or capital copied from `spec.name` | `metadata.name` is a DNS-1123 label; `spec.name` keeps the literal string |
| Rejected by `kubectl apply`, `spec.name` | none — admission | empty, or over 100 characters | The column is `REQ` and `len=100` |
| `Ready=False`, `Reason=Conflict` | `Ready` | a bundle with this `name` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt`; `status.naturalKey` shows what was searched |
| `Ready=False`, `Reason=AdoptOnly` | `Ready` | `onConflict: AdoptOnly` and no bundle of that name exists | Create it by hand in NetBox, or drop to `onConflict: Adopt` |
| A cable reports `RefsResolved=False` naming `bundleRef` | on the *cable* | this CR is in another namespace with no grant, or was deleted | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in this namespace, or remove `bundleRef` from the cable |
| A `slug`-mode reference to this Kind never resolves | on the *referrer* | `dcim.CableBundle` has no `slug` column | Use `name`, `lookup` or `id` |
| Two bundles differing only in case both apply | — | the `UNIQUE` is not case-folded | That is NetBox's own behaviour: `Riser A` and `riser a` are two bundles |

## Related

- [`NetBoxCable`](netboxcable.md) — what points at this kind, and the hard identity next door
- [`NetBoxClusterType`](netboxclustertype.md) — the same shape of "one column and a natural
  key", on an `OrganizationalModel` that has a `slug`
- [`NetBoxContact`](netboxcontact.md) — keying on `name` when *no* constraint backs it
- [`NetBoxRefGrant`](netboxrefgrant.md) — referencing a shared catalogue namespace
- [Deletion](../concepts/deletion.md) — why this kind defaults to `Delete`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
