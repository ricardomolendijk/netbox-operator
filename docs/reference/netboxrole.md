# `NetBoxRole`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRole` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrole` |
| Status subresource | yes |

A `NetBoxRole` is one `ipam.Role` in NetBox: what a prefix, a VLAN, an IP range or an ASN is
*for* — management, guest, IoT, point-to-point.

## Three different things called "role"

This is the near-miss the kind exists to disambiguate, and all three ship in this API:

| Written as | NetBox | Endpoint | This kind? |
|---|---|---|---|
| `roleRef` on [`NetBoxPrefix`](netboxprefix.md), [`NetBoxVLAN`](netboxvlan.md), `NetBoxASN` | `ipam.Role` | `ipam/roles` | **yes** |
| `role` on [`NetBoxIPAddress`](netboxipaddress.md) | a `CharField` with `choices=IPAddressRoleChoices` | — | no, it is a string |
| `deviceRoleRef` on a device | `dcim.DeviceRole` | `dcim/device-roles` | no, a different model |

`docs/netbox-schema.md` records the first as `role ForeignKey -> ipam.Role
on_delete=SET_NULL` and the second as `role CharField len=50
choices=IPAddressRoleChoices`. `RoleRef` and `DeviceRoleRef` are two typed aliases precisely so
Go cannot mix them up.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRole
metadata:
  name: management
  namespace: default
spec:
  endpointRef: homelab
  name: Management
  slug: management
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRole
metadata:
  name: management
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  name: Management
  slug: management
  weight: 100                 # NetBox's own default is 1000

  description: Out-of-band and infrastructure management
  comments: Reserved for switch and hypervisor management interfaces.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` |

`name (OrganizationalModel) CharField REQ UNIQUE len=100`. Column-unique, and not the identity.

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

`slug (OrganizationalModel) SlugField REQ UNIQUE len=100`, and this kind's natural key.

It is also the value a `slug`-mode `roleRef` elsewhere resolves against, which is the concrete
reason `slug` is the identity and `name` is not: `roleRef: {slug: management}` on a prefix in
another namespace needs no CR here at all.

### `spec.weight`

| | |
|---|---|
| Type | `*int32` |
| Required | no |
| Validation | `Minimum=0`, `Maximum=65535` |
| Default | none in the CRD; `1000` in NetBox |

`weight PositiveSmallIntegerField def=1000`, and `meta.ordering: ('weight', 'name')`.

A **pointer**, for the reason [`NetBoxPrefix.spec.isPool`](netboxprefix.md#specispool--specmarkutilized) is one:
the column has a Django default, so a plain `int32` cannot tell "not managed" from "managed as
0", and adopting a role a human had ordered would reset it to 0 on the first reconcile.

Not part of the identity, even though it is the model's only index. An ordering is not a
constraint, and a role whose weight was retuned in the UI must still be found by the same
lookup afterwards.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `OrganizationalModel`.

Omit either to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(slug)` | `?slug=` | `slug SlugField REQ UNIQUE len=100` |

No `meta.constraints` on `ipam.Role`, and none needed.

## Deletion

**`deletionPolicy` defaults to `Delete`**, like every kind, since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) reversed decision #176. The reasoning that used to put `Retain` here still describes a real cost. This kind has a specific reason
beyond the general one: every column that points at a role — on `ipam.Prefix`, `ipam.VLAN`,
`ipam.IPRange`, `ipam.ASN` — is `on_delete=SET_NULL`, so deleting the row does not fail. It
silently clears the role on every object that had it, which is an edit nobody asked for and
nobody sees.

## Ownership

**No containment parent.** `ipam.Role` declares no mapped foreign key, so there is nothing the
server cascades ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

## `status`

Identical to every other kind. `OrganizationalModel` carries the whole provenance stamp.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the role exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | always — no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`Deleting=False, Reason=Protected` does **not** happen on this kind: nothing protects a role,
which is exactly the problem `Retain` guards against.

## Printer columns

```
NAME         SLUG         WEIGHT   ID   READY   AGE
management   management   100      19   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `WEIGHT` | `.spec.weight` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| A `roleRef` elsewhere reports `RefKindUnavailable` | on the *referrer* | that cluster predates this kind | upgrade; the Kind exists from NBO-055 |
| A `roleRef` reports `RefNotFound` in `name` mode | on the referrer | no `NetBoxRole` CR of that name in the ref's namespace | create one, or switch the ref to `slug` mode |
| Stuck on a slug NetBox already has | `Ready=False`, `Reason=Conflict` | another namespace or a human owns it | different slug, or `onConflict: Adopt` |
| Deleted the role and prefixes lost theirs | none — NetBox did not complain | `SET_NULL`, not `PROTECT` | keep the default `deletionPolicy: Retain` |

## Related

- [`NetBoxPrefix`](netboxprefix.md), [`NetBoxVLAN`](netboxvlan.md) — the kinds whose `roleRef`
  points here
- [`NetBoxIPAddress`](netboxipaddress.md) — whose `role` is a *string*, not a reference
- [references](../concepts/references.md) — the four ref modes and the alias table
