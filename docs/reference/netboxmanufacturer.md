# `NetBoxManufacturer`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxManufacturer` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbmfr` |
| Status subresource | yes |

A `NetBoxManufacturer` is one `dcim.Manufacturer` in NetBox: who makes a device.

It is the root of the hardware catalogue. `dcim.DeviceType.manufacturer` is a **required**
foreign key and `dcim.Platform`'s uniqueness is scoped by `manufacturer`
(`docs/netbox-schema.md` → `dcim.DeviceType`, `dcim.Platform`), so nothing else in the
catalogue reconciles until a manufacturer does.

## Start with the grant

Catalogues live in a namespace like everything else in `v1alpha1`, so the deployment this Kind
is built for is a shared catalogue namespace that team namespaces reference into. Crossing a
namespace boundary requires a [`NetBoxRefGrant`](netboxrefgrant.md) **in the namespace being
referenced** — this one. Without it, every `NetBoxDeviceType` and `NetBoxPlatform` in every team
namespace sits at `RefsResolved=False, Reason=RefDenied`.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readers
  namespace: netbox-catalog        # the namespace holding the manufacturers
spec:
  from:
    namespaces: All                # or a selector; see NetBoxRefGrant
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxManufacturer
metadata:
  name: ubiquiti
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  name: Ubiquiti
  slug: ubiquiti
```

A team namespace then names it across the boundary:

```yaml
spec:
  manufacturerRef:
    namespace: netbox-catalog
    name: ubiquiti
```

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxManufacturer
metadata:
  name: ubiquiti
  namespace: default
spec:
  endpointRef: homelab
  name: Ubiquiti
  slug: ubiquiti
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxManufacturer
metadata:
  name: ubiquiti
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Ubiquiti
  slug: ubiquiti
  description: Ubiquiti Inc.
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxmanufacturer.yaml`](../../config/samples/netbox_v1alpha1_netboxmanufacturer.yaml).

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

Required. The manufacturer's name, up to 100 characters.

**Column-unique** (`name (OrganizationalModel) CharField REQ UNIQUE len=100`), unlike the
nested-group kinds' names. It is a candidate key and deliberately not the lookup key: a kind
gets one identity and `slug` is the stable one, so a rename that collides comes back as NetBox's
own 409 reported as `Ready=False, Reason=Invalid` rather than being adopted under the other
candidate.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key.

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and
set are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

`dcim.Manufacturer` declares **no `meta.constraints` at all** and puts column-level `UNIQUE` on
both `name` and `slug` (`docs/netbox-schema.md` → `dcim.Manufacturer`), so uniqueness is global
and there is nothing to pin to null. That is the same shape as
[`NetBoxTenantGroup`](netboxtenantgroup.md) and the opposite of
[`NetBoxDeviceRole`](netboxdevicerole.md), which is in the same ticket — the constraint list
decides, not the base class.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.Manufacturer` is an `OrganizationalModel`, so it carries both `tags` and `custom_fields`
and is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby)
is set. See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the manufacturer exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A hand-made manufacturer is adopted, not duplicated

`slug` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one manufacturer in NetBox rather than two. Creating a second one
would be refused by the unique index anyway, so adoption is the only outcome that works — which
is why a fresh operator pointed at a long-running NetBox does not need a migration.

### Deleting one is usually refused

`deletionPolicy` defaults to `Delete`, and both `dcim.DeviceType.manufacturer` and
`dcim.Platform.manufacturer` are `on_delete=PROTECT`. NetBox therefore refuses to delete a
manufacturer while any device type or platform points at it, and the CR reports
`Deleting=False, Reason=Protected` until they are gone. Delete the device types first, or set
`deletionPolicy: Retain` to keep the NetBox object and drop only the CR.

### Two namespaces claiming one slug is one manufacturer

NetBox's uniqueness is a database constraint and a namespace boundary does not partition it. The
first CR to reconcile creates or adopts the manufacturer; the second finds it already claimed and
reports `Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)). On a catalogue kind that is the likely case
rather than the exotic one — which is the argument for one shared catalogue namespace and grants,
rather than a copy per team.

### Every field is inherited

`dcim.Manufacturer` declares **no columns of its own**; the digest says so in as many words.
`OrganizationalModel` gives `name`, `slug` and `description`, and `ContactsMixin` contributes
only reverse relations.

### What is not here yet

`comments` is a column this model carries (`comments (OrganizationalModel) TextField`) and the
CRD does not expose — NBO-060 is the audit that picks up columns left off a spec struct. `tags`
and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbmfr
NAME       SLUG       ID   READY   AGE
mikrotik   mikrotik   61   True    4m
ubiquiti   ubiquiti   60   True    4m
```

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Ready=False`, `Reason=Conflict` | Another namespace already owns this slug, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. |
| `Ready=False`, `Reason=Invalid`, message names `name` | A rename collided with the column-level `UNIQUE` on `name`. Pick another name; the slug is what identifies the object. |
| `Deleting=False`, `Reason=Protected` | A device type or platform still points at this manufacturer. |
| A device type in another namespace reports `RefDenied` | No [`NetBoxRefGrant`](netboxrefgrant.md) in this namespace. See [Start with the grant](#start-with-the-grant). |

## Related

- [`NetBoxDeviceType`](netboxdevicetype.md) — the kind whose identity requires this one
- [`NetBoxPlatform`](netboxplatform.md) — the kind whose identity is *scoped* by this one
- [`NetBoxRefGrant`](netboxrefgrant.md) — what makes a cross-namespace `manufacturerRef` resolve
- [References](../concepts/references.md) — the four ref modes, cycles, and grants
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
