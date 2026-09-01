# `NetBoxASNRange`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxASNRange` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbasnrange` |
| Status subresource | yes |
| Lands with | NBO-055 (M10) |

A `NetBoxASNRange` is one `ipam.ASNRange` in NetBox: a span of autonomous system numbers set
aside for allocation.

It is a **declaration of intent, not an allocation source**. Nothing in this kind hands out
numbers, and the operator does not draw from it — [`NetBoxASN`](netboxasn.md) is where an
allocated number is recorded, and creating one inside a range's span is something a human or a
higher-level tool does. NetBox's own UI offers "available ASNs" for a range; that is a view,
not a claim.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxASNRange
metadata:
  name: private-16-bit
  namespace: default
spec:
  endpointRef: homelab
  name: Private 16-bit
  slug: private-16-bit
  rirRef:
    name: rfc-1918
  start: 64512
  end: 65534
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxASNRange
metadata:
  name: private-16-bit
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  name: Private 16-bit
  slug: private-16-bit

  rirRef:
    name: rfc-1918            # required
  tenantRef:
    name: acme
    namespace: netbox-catalog

  start: 64512
  end: 65534

  description: RFC 6996 private ASNs
  comments: Allocated per-site from the bottom up.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.name`, `spec.slug`

| | `name` | `slug` |
|---|---|---|
| Type | `string` | `string` |
| Required | **yes** | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

`ipam.ASNRange` is the one entry in this app whose digest line reads
`shadows inherited: name (OrganizationalModel), slug (OrganizationalModel)` — the model
**redeclares** both columns rather than inheriting them. It changes nothing: both
redeclarations carry `REQ UNIQUE len=100`, exactly as the base class does, so the shadowing is
a NetBox implementation detail and not a difference in identity.

`slug` is the natural key. `name` carries the same `UNIQUE` and deliberately is not a second
candidate — a rename should not orphan the object.

### `spec.rirRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxRIR`](netboxrir.md) |
| Required | **yes** |

`rir ForeignKey REQ -> ipam.RIR on_delete=PROTECT`. Required, so an unresolved reference writes
nothing at all. `PROTECT`, so not a containment parent.

### `spec.start`, `spec.end`

| | |
|---|---|
| Type | `int64` |
| Required | **yes** |
| Validation | `Minimum=1`, `Maximum=4294967295` |

`start ASNField REQ` and `end ASNField REQ`, bounded by `BGP_ASN_MIN` and `BGP_ASN_MAX`
(`netbox/ipam/fields.py` lines 17–18 and 120–125). `int64` for the same reason
[`NetBoxASN.spec.asn`](netboxasn.md#specasn) is.

Both inclusive.

**If they are wrong:** out-of-range values are rejected by admission. `end < start` is **not**
— it needs a comparison between two fields, and NetBox's own `clean()` already rejects it with
a better message than a CEL rule would produce, so it arrives as a `400` reported on the object
as `Ready=False, Reason=Invalid` carrying NetBox's text.

Neither is part of the identity. Nothing in NetBox makes `(start, end)` unique — the table has
no `meta.constraints` at all — so two ranges may legitimately cover the same span under
different names, and keying on the pair would adopt one for the other.

### `spec.tenantRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxTenant`](netboxtenant.md) |
| Required | no |

`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`.

An ordinary reference, same- or cross-namespace through the normal
[`NetBoxRefGrant`](netboxrefgrant.md) path. The `PROTECT` consequence is the most confusing
failure mode in the whole design and is worth stating plainly:

> A `NetBoxASNRange` holding a tenant **blocks that tenant's deletion in NetBox.** The *tenant*
> reports `Deleting=False, Reason=Protected`, and the message names this range's namespace and
> name — which may be a namespace the person deleting the tenant cannot see.

See [`NetBoxTenant`](netboxtenant.md#deleting-a-tenant-is-usually-refused) for the same story from the other end.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `OrganizationalModel`.
Omit either to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(slug)` | `?slug=` | `slug SlugField REQ UNIQUE len=100` (redeclared on the model) |

## Deletion

**`deletionPolicy` defaults to `Retain`** (decision #176).

## Ownership

**No containment parent.** `rir` and `tenant` are both `on_delete=PROTECT`, so neither
cascades: NetBox refuses to delete either while this range exists. An owner reference on a
`PROTECT`ed FK would promise a cluster-side cascade the server declines — garbage collection
removes the CR, the finalizer's `DELETE` is refused, and the row outlives the object.
`ErrContainmentNotCascade` makes that a boot failure rather than a convention
([ownership](../concepts/ownership.md)).

## `status`

Identical to every other kind. `OrganizationalModel` carries the whole provenance stamp.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the range exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared ref resolved | one did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Printer columns

```
NAME             SLUG             START   END     ID   READY   AGE
private-16-bit   private-16-bit   64512   65534   44   True    6m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `START` | `.spec.start` |
| `END` | `.spec.end` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `Ready=False`, `Reason=Invalid` naming start and end | | `end < start`; NetBox's `clean()` refused it | fix the numbers — admission cannot catch this one |
| Nothing written, RIR named | `RefsResolved=False`, `Reason=RefNotFound` | `rirRef` points at a missing `NetBoxRIR` | create it, or use `slug`/`id` mode |
| A `NetBoxTenant` will not delete, and the blocker is in a namespace you cannot see | on the *tenant*: `Deleting=False`, `Reason=Protected` | a `NetBoxASNRange` in another namespace holds `tenantRef` | the condition names the range's namespace and name. Remove the `tenantRef` there, or delete the range |
| Two ranges cover the same span and both are `Ready` | none | `(start, end)` is not unique in NetBox, by design | expected; the identity is `slug` |

## Related

- [`NetBoxRIR`](netboxrir.md) — required, and `PROTECT`ed
- [`NetBoxASN`](netboxasn.md) — an individual allocation
- [`NetBoxTenant`](netboxtenant.md) — the other end of the `PROTECT` story
- [deletion](../concepts/deletion.md) — what `Protected` means and how to clear it
