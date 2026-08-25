# `NetBoxAggregate`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxAggregate` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbagg` |
| Status subresource | yes |
| Lands with | NBO-055 (M10) |

A `NetBoxAggregate` is one `ipam.Aggregate` in NetBox: a top-level block of address space, as
allocated to you by a registry, that the [prefixes](netboxprefix.md) underneath it are carved
from.

**Its identity is a convention, not a constraint.** `docs/netbox-schema.md` → `ipam.Aggregate`
declares only `meta.ordering: ('prefix', 'pk')` and one non-unique index on `('prefix', 'id')`
— **no `meta.constraints` at all**. So two aggregates with the same prefix under the same
registry are a legal server state, and more than one match is reported as a `Conflict` naming
the candidate ids rather than resolved by taking the first.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxAggregate
metadata:
  name: ten-slash-eight
  namespace: default
spec:
  endpointRef: homelab
  prefix: 10.0.0.0/8
  rirRef:
    name: rfc-1918
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxAggregate
metadata:
  name: ten-slash-eight
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  prefix: 10.0.0.0/8

  rirRef:
    name: rfc-1918            # required, and half the identity
  tenantRef:
    name: acme
    namespace: netbox-catalog

  dateAdded: "2026-01-01"     # YYYY-MM-DD, or "" to clear

  description: The whole of RFC 1918's 10/8
  comments: Carved into per-site /16s. See the prefixes underneath.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.prefix`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=43` |

`prefix IPNetworkField REQ`, and half the identity.

**Write the network address.** NetBox masks host bits on an aggregate exactly as it does on a
[prefix](netboxprefix.md#specprefix), so `10.0.0.1/8` is stored as `10.0.0.0/8` — and the
differ would then find a difference it cannot fix, PATCHing forever.

`MaxLength=43` is the longest an IPv6 CIDR string gets (39 characters plus `/128`).

### `spec.rirRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxRIR`](netboxrir.md) |
| Required | **yes** |

`rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`, and the other half of the identity.

Required, so an unresolved reference writes nothing at all — and it also makes **no candidate
applicable**, so the engine waits instead of adopting. That is the correct outcome, and the
alternative is the failure this shape exists to prevent: `?prefix=10.0.0.0/8` alone would find
the same block filed under a different registry, and the follow-up `PATCH` would move it
([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

`PROTECT`, so not a containment parent.

### `spec.tenantRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxTenant`](netboxtenant.md) |
| Required | no |

`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`. An aggregate holding this reference
blocks that tenant's deletion; the tenant reports `Deleting=False, Reason=Protected` naming this
object.

### `spec.dateAdded`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `Pattern=^(\d{4}-\d{2}-\d{2})?$` |

`date_added DateField` — nullable, and when the block was allocated to you.

The pattern **admits the empty string on purpose**, and the reason is the same one
[`NetBoxSite`'s coordinates](netboxsite.md#speclatitude-speclongitude) have: the column is
nullable and a `DateField` rejects `""` outright, so an emptied value has to go over the wire
as `null` to clear rather than to fail. The descriptor declares `EmptyIsNull` for exactly that.
Without it, clearing the field would be a `400` on every reconcile, forever.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. Those are different
instructions ([field ownership](../concepts/field-ownership.md)).

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`. Omit
either to leave NetBox's own value alone; set it to `""` to clear it.

## Natural key

**One candidate, and there cannot be a second:**

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(prefix, rir)` | `?prefix=&rir_id=` | nothing — no `meta.constraints` on the model |

`rir` is `REQ`, so there is no state in which it is absent and therefore no null variant to pin.
An aggregate whose RIR has not been created yet matches nothing and the engine waits.

Because no constraint backs it, two `NetBoxAggregate` CRs with the same `(prefix, rir)` behave
like this: the first creates the row, the second's lookup finds it, and whichever does not own
the [provenance stamp](../concepts/provenance.md) reports `Ready=False, Reason=Conflict` naming
the id. One row in NetBox, one `Ready`, one `Conflict` — never two rows.

## Deletion

**`deletionPolicy` defaults to `Retain`** (decision #176). An aggregate is the record of a
registry allocation; deleting it destroys the change log and journal entries that say when the
block was assigned.

Note that NetBox does **not** protect an aggregate from deletion when prefixes exist inside it:
prefixes are matched to aggregates arithmetically, by containment, not by a foreign key. So
deleting the aggregate does not fail, it just loses the record.

## Ownership

**No containment parent.** Both mapped foreign keys are `on_delete=PROTECT` — `rir` and
`tenant` — so neither cascades. `ipam.RIR` declares no `aggregates` `GenericRelation` either,
so there is no second mechanism to check
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

## `status`

Identical to every other kind. `ipam.Aggregate` is a `PrimaryModel`, so the provenance stamp
applies in full — and it is load-bearing here, because the stamp is what tells the operator's
own aggregate from a duplicate a human created.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the aggregate exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared ref resolved | one did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Printer columns

```
NAME              PREFIX       RIR        ID   READY   AGE
ten-slash-eight   10.0.0.0/8   rfc-1918   52   True    7m
```

| Column | JSONPath |
|---|---|
| `PREFIX` | `.spec.prefix` |
| `RIR` | `.spec.rirRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| One of two identical CRs is stuck | `Ready=False`, `Reason=Conflict` naming an id | `(prefix, rir)` is a convention, not a constraint — the other CR owns the row | delete the duplicate CR, or set `onConflict: Adopt` if it should take the row over |
| Nothing written at all | `RefsResolved=False`, `Reason=RefNotFound` | `rirRef` is unresolved, so no candidate is applicable | create the `NetBoxRIR`; the engine waits rather than adopting by prefix alone |
| `PATCH` on every reconcile, prefix never settles | `Synced=True` but writes keep coming | host bits were written — NetBox masked them | write the network address |
| `dateAdded: ""` gives a `400` | `Ready=False`, `Reason=Invalid` | an older build without `EmptyIsNull` | upgrade; the descriptor sends `null` for an emptied date |

## Related

- [`NetBoxRIR`](netboxrir.md) — required, and half the identity
- [`NetBoxPrefix`](netboxprefix.md) — the prefixes carved out underneath, matched by containment
  rather than by a foreign key
- [lookups](../concepts/lookups.md) — why a required filter is never omitted
- [deletion](../concepts/deletion.md) — why IPAM defaults to `Retain`
