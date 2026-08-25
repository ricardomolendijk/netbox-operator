# `NetBoxASN`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxASN` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbasn` |
| Status subresource | yes |
| Lands with | NBO-055 (M10) |

A `NetBoxASN` is one `ipam.ASN` in NetBox: an autonomous system number allocated by a registry.

**It is the one kind in this API with no `name` and no `slug`.** `docs/netbox-schema.md` →
`ipam.ASN` declares neither; the number is the object's whole meaning and its whole identity
(`asn ASNField REQ UNIQUE`). That has one consequence worth reading before you write a
reference to it: a `slug`-mode `ASNRef` matches nothing here, because there is no such column.
Use `name` mode for a sibling CR or `lookup: {asn: "64512"}` for an ASN the operator does not
manage.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxASN
metadata:
  name: as64512
  namespace: default
spec:
  endpointRef: homelab
  asn: 64512
  rirRef:
    name: rfc-1918
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxASN
metadata:
  name: as64512
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  asn: 64512

  rirRef:
    name: rfc-1918            # required
  roleRef:
    slug: management
  tenantRef:
    name: acme
    namespace: netbox-catalog

  description: Private AS for the lab fabric
  comments: RFC 6996 space. Not advertised anywhere.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.asn`

| | |
|---|---|
| Type | `int64` |
| Required | **yes** |
| Validation | `Minimum=1`, `Maximum=4294967295` |

`asn ASNField REQ UNIQUE`, and this kind's whole natural key.

`int64` and not `int32`, deliberately. An `ASNField` is a `BigIntegerField` bounded by
`BGP_ASN_MIN = 1` and `BGP_ASN_MAX = 2**32 - 1` (`netbox/ipam/fields.py` lines 17–18 and
120–125, NetBox 4.6.8), so a 4-byte ASN such as `4200000000` overflows a signed 32-bit field.
The bounds are enforced at admission rather than arriving as a `400` three steps later.

**If it is wrong:** `0`, a negative number or anything above `4294967295` is rejected by the API
server. A number another ASN already holds is found by the lookup, so it is an adoption or a
`Conflict` rather than a `400`.

### `spec.rirRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxRIR`](netboxrir.md) |
| Required | **yes** |

`rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`.

Required, so an unresolved reference writes **nothing at all** rather than a partial object, and
the CR reports `RefsResolved=False, Reason=RefNotFound` naming the field.

Not part of the identity, and that is a decision rather than an omission: `asn` is
column-unique, so a second filter could only narrow a match that cannot be ambiguous — while it
*would* make the lookup miss an ASN a human had filed under a different registry. Finding that
row and PATCHing its RIR is right; creating a second row for the same number is not.

`PROTECT`, so it is **not** a containment parent: NetBox refuses to delete an RIR that still
has ASNs, and an owner reference would promise a cascade the server declines
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

### `spec.roleRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxRole`](netboxrole.md) |
| Required | no |

`role ForeignKey -> ipam.Role on_delete=SET_NULL`.

`ipam.Role`, the same model [`NetBoxPrefix`](netboxprefix.md) and [`NetBoxVLAN`](netboxvlan.md)
point at — not `dcim.DeviceRole`, and not the `role` *string* on
[`NetBoxIPAddress`](netboxipaddress.md). See [`NetBoxRole`](netboxrole.md) for the three-way
table.

### `spec.tenantRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxTenant`](netboxtenant.md) |
| Required | no |

`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`.

`PROTECT`, so an ASN holding this reference **blocks that tenant's deletion in NetBox**, and the
tenant reports `Deleting=False, Reason=Protected` naming this object
([deletion](../concepts/deletion.md)).

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`. Omit
either to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(asn)` | `?asn=` | `asn ASNField REQ UNIQUE` |

A real database guarantee. `ipam.ASN`'s only other table-level line is
`meta.ordering: ['asn']`.

## Deletion

**`deletionPolicy` defaults to `Retain`** (decision #176). An ASN is an allocation from a
registry: deleting the row destroys the record of who holds it, and a fresh row with the same
number is a different object with a different id.

## Ownership

**No containment parent**, and all three foreign keys are why rather than an omission:

| Field | `on_delete` | Why not the parent |
|---|---|---|
| `rirRef` | `PROTECT` | NetBox refuses the delete; there is no server-side cascade to mirror |
| `tenantRef` | `PROTECT` | same |
| `roleRef` | `SET_NULL` | the row survives with the column cleared — a cluster-side cascade would have deleted the CR describing it |

## `status`

Identical to every other kind. `ipam.ASN` is a `PrimaryModel`, so the provenance stamp applies
in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the ASN exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared ref resolved | one did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

No `ParentOwned`: nothing here cascades, so no owner reference is ever taken.

## Printer columns

```
NAME      ASN     RIR        ID   READY   AGE
as64512   64512   rfc-1918   31   True    5m
```

| Column | JSONPath |
|---|---|
| `ASN` | `.spec.asn` |
| `RIR` | `.spec.rirRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Nothing written, RIR named | `RefsResolved=False`, `Reason=RefNotFound` | `rirRef` names a `NetBoxRIR` that does not exist | create it, or use `slug`/`id` mode for an RIR the operator does not manage |
| An `ASNRef` elsewhere reports `RefNotFound` in `slug` mode | on the referrer | `ipam.ASN` has **no slug column** | use `name` mode, or `lookup: {asn: "64512"}` |
| `Ready=False`, `Reason=Conflict` | | another CR or a human owns this number | the number is globally unique; pick another or `onConflict: Adopt` |
| A `NetBoxTenant` will not delete | on the *tenant*: `Deleting=False`, `Reason=Protected` | this ASN holds `tenantRef` | remove `tenantRef` here, or delete the ASN |

## Related

- [`NetBoxRIR`](netboxrir.md) — required, and `PROTECT`ed
- [`NetBoxASNRange`](netboxasnrange.md) — the span an ASN is allocated out of, by hand
- [`NetBoxRole`](netboxrole.md) — the three things called "role"
- [references](../concepts/references.md) — the four ref modes, and why `slug` is not one here
