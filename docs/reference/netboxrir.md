# `NetBoxRIR`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRIR` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrir` |
| Status subresource | yes |
| Lands with | NBO-055 (M10) |

A `NetBoxRIR` is one `ipam.RIR` in NetBox: a regional internet registry — ARIN, RIPE NCC — or
a private allocation authority standing in for one, such as RFC 1918.

It is the root of the allocation registry. [`NetBoxASN`](netboxasn.md),
[`NetBoxASNRange`](netboxasnrange.md) and [`NetBoxAggregate`](netboxaggregate.md) each declare
`rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT` (`docs/netbox-schema.md`), so none of them
can be created before an RIR exists and none of them lets its RIR be deleted afterwards.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRIR
metadata:
  name: rfc-1918
  namespace: default
spec:
  endpointRef: homelab
  name: RFC 1918
  slug: rfc-1918
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRIR
metadata:
  name: rfc-1918
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  name: RFC 1918
  slug: rfc-1918
  isPrivate: true

  description: Private IPv4 address space
  comments: |
    Not globally routable. Aggregates filed here are the RFC 1918 blocks.
```

That is every field. `ipam.RIR` declares exactly one column of its own.

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` |

`name (OrganizationalModel) CharField REQ UNIQUE len=100`.

Column-unique, and deliberately not the identity — see `spec.slug`.

**If it is wrong:** an empty or over-long name is rejected by admission. A name another RIR
already holds is a `400` from NetBox reported as `Ready=False, Reason=Invalid` carrying
NetBox's own message.

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

`slug (OrganizationalModel) SlugField REQ UNIQUE len=100`, and this kind's natural key.

Globally unique in NetBox over namespaced CRDs: two namespaces cannot both own `rfc-1918`. The
second one to try reports `Ready=False, Reason=Conflict` naming the id it found, and writes
nothing.

### `spec.isPrivate`

| | |
|---|---|
| Type | `*bool` |
| Required | no |
| Default | none in the CRD; `False` in NetBox |

`is_private BooleanField def=False`.

A **pointer**, and the reason is the column's Django default. A plain `bool` cannot tell "not
managed" from "managed as false", so adopting an RIR a human had marked private would silently
clear the flag on the first reconcile. Nil leaves NetBox's value alone; `false` writes false.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second — `comments` is a `TextField`. Both inherited
from `OrganizationalModel`, and an inherited column is as writable as a declared one.

Omit either to leave NetBox's own value alone; set it to `""` to clear it. Those are different
instructions ([field ownership](../concepts/field-ownership.md)).

## Natural key

One candidate, and it is a real database guarantee rather than a convention:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(slug)` | `?slug=` | `slug SlugField REQ UNIQUE len=100` |

`ipam.RIR` carries **no `meta.constraints`** and does not need any: a unique column identifies
at most one row, so there is no conditional constraint to express as a second candidate and no
parent to pin to null. `name` carries the same `UNIQUE` and is deliberately not a second
candidate — a kind gets one identity, and on a unique column a second candidate is only ever
reached when the object does not exist.

## Deletion

**`deletionPolicy` defaults to `Delete`**, like every kind, since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) reversed decision #176. The reasoning that used to put `Retain` here still describes a real cost
([deletion](../concepts/deletion.md#why-this-reversed)). What answers it now is NetBox rather
than a default: an RIR that anything points at cannot be deleted, so the CR stays and says so.

Deleting an RIR in NetBox is refused while anything points at it, and recreating one gives
every aggregate and ASN underneath a different id.

## Ownership

**No containment parent.** `ipam.RIR` declares no foreign key at all besides
`owner (OwnerMixin) -> users.Owner`, which the operator does not map, so there is no FK the
server cascades for an owner reference to mirror
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). That is a consequence
rather than a gap.

## `status`

Identical to every other kind. `OrganizationalModel` mixes in both `TagsMixin` and
`CustomFieldsMixin`, so the provenance stamp applies in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the RIR exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | always — this kind has no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

There is no `ParentOwned` and no `DeferredFieldPending`: this kind has no reference to own it
and none to defer.

`Deleting=False, Reason=Protected` is the one worth expecting here, and it is the *normal*
outcome of deleting an RIR that still has ASNs, ranges or aggregates. The message names the
NetBox objects blocking it.

## Printer columns

```
NAME       SLUG       PRIVATE   ID   READY   AGE
rfc-1918   rfc-1918   true      12   True    3m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `PRIVATE` | `.spec.isPrivate` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Stuck, and NetBox has an RIR with this slug | `Ready=False`, `Reason=Conflict` | another namespace, or a human, already owns the slug | pick a different slug, or set `onConflict: Adopt` to take the existing row over |
| Will not delete | `Deleting=False`, `Reason=Protected` | ASNs, ASN ranges or aggregates still reference it | delete or repoint them first; the message names them |
| `isPrivate` keeps flipping back in the UI | `Synced=True`, no drift reported | `isPrivate` is unset in the CR, so the operator does not manage it | set it explicitly if the cluster should own it |

## Related

- [`NetBoxASN`](netboxasn.md), [`NetBoxASNRange`](netboxasnrange.md),
  [`NetBoxAggregate`](netboxaggregate.md) — the three kinds that require one
- [deletion](../concepts/deletion.md#why-this-reversed) — why every kind defaults to `Delete`
- [ADR-0002](../decisions/0002-crd-scoping.md) — why every kind is namespaced
