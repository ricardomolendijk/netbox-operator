# `NetBoxDeviceType`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxDeviceType` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbdtype` |
| Status subresource | yes |
| Lands with | NBO-027 |

A `NetBoxDeviceType` is one `dcim.DeviceType` in NetBox: one make and model of device, with its
rack height and depth.

`dcim.Device.device_type` is a **required** foreign key (`docs/netbox-schema.md` →
`dcim.Device`), so no `NetBoxDevice` exists without one. This Kind is also the first whose own
identity depends on a required reference to another Kind — see
[Natural keys](#natural-keys).

## Start with the grant

A device type is catalogue data and a device is not, so this Kind is the one where the namespace
boundary is crossed in earnest: a `NetBoxDevice` in `team-a` names a `deviceTypeRef` in
`netbox-catalog`, and the device type itself names a `manufacturerRef` there too. Both directions
need a [`NetBoxRefGrant`](netboxrefgrant.md) **in the namespace being referenced**. Without it
every device in every team namespace sits at `RefsResolved=False, Reason=RefDenied`.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readers
  namespace: netbox-catalog        # the namespace holding the catalogue
spec:
  from:
    namespaces: All                # or a selector; see NetBoxRefGrant
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDeviceType
metadata:
  name: ucg-ultra
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  manufacturerRef:
    name: ubiquiti                 # same namespace, so no grant needed for this one
  model: UniFi Cloud Gateway Ultra
  slug: ucg-ultra
```

A team namespace then names it across the boundary:

```yaml
spec:
  deviceTypeRef:
    namespace: netbox-catalog
    name: ucg-ultra
```

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDeviceType
metadata:
  name: ucg-ultra
  namespace: default
spec:
  endpointRef: homelab
  manufacturerRef:
    name: ubiquiti
  model: UniFi Cloud Gateway Ultra
  slug: ucg-ultra
```

`manufacturerRef` is not optional — the API server rejects a `NetBoxDeviceType` without one.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDeviceType
metadata:
  name: ucg-ultra
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  manufacturerRef:
    name: ubiquiti
  model: UniFi Cloud Gateway Ultra
  slug: ucg-ultra
  partNumber: UCG-Ultra

  # A string, not a number. See spec.uHeight.
  uHeight: "1.0"

  isFullDepth: false
  excludeFromUtilization: false
  airflow: passive            # "" | front-to-rear | rear-to-front | ... | passive | mixed
  subdeviceRole: ""           # "" | parent | child

  defaultPlatformRef:
    name: unifi-os

  description: Small-office gateway
  comments: |
    Ordered in pairs; the second one is the spare.
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxdevicetype.yaml`](../../config/samples/netbox_v1alpha1_netboxdevicetype.yaml).

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.manufacturerRef`

**Required.** An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxManufacturer`](netboxmanufacturer.md).

Required because NetBox's column is: `manufacturer ForeignKey REQ -> dcim.Manufacturer
on_delete=PROTECT`. It is also half of *both* natural keys, so until it resolves the object
reports `RefsResolved=False` naming this field and makes **no NetBox write at all** — the
[`NetBoxLocation`](netboxlocation.md) shape rather than the [`NetBoxPrefix`](netboxprefix.md) one,
where the object is created without the field. There is nothing to create: a device type without
a manufacturer is not a state NetBox has.

Written to NetBox as `manufacturer`, filtered as `manufacturer_id`.

### `spec.model`

Required. The model name, up to 100 characters.

Unique per manufacturer (`..._unique_manufacturer_model`), not globally. It is a candidate key,
and the second one the engine tries — see [Natural keys](#natural-keys).

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. Unique per
manufacturer, which is what makes `ubiquiti/ucg-ultra` and `mikrotik/ucg-ultra` two legitimate
objects.

### `spec.defaultPlatformRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxPlatform`](netboxplatform.md) — the platform a device of this type gets when it names none
of its own.

`on_delete=SET_NULL`, so deleting the platform in NetBox clears the column rather than refusing,
and the next reconcile finds the drift and PATCHes it back. Not part of the identity.

### `spec.partNumber`

Optional, up to 50 characters. The vendor's discrete part number.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it — see
[field ownership](../concepts/field-ownership.md).

### `spec.uHeight`

Optional **string**, defaulted to `"1.0"`, matching `^[0-9]{1,3}(\.[0-9])?$`.

A string and not a number, and the reason is not style. NetBox stores it as `u_height
DecimalField decimal(4,1)` and returns it padded — `"0.50"` for a spec that said `"0.5"` — while
an OpenAPI `number` round-trips through IEEE-754 on its way in and out of the API server. The
engine compares two numeric strings numerically (`internal/netbox/drift.go`, `scalarEqual`), so
`"0.5"` and NetBox's `"0.50"` are the same value and produce **no PATCH** on the second
reconcile. A `float64` field would PATCH forever, which is the bug this shape exists to avoid
([`NetBoxSite`](netboxsite.md)'s coordinates are the same decision).

The pattern is read straight off `decimal(4,1)`: four digits, one after the point, so `0` to
`999.9` in steps of a tenth. Half-height types are `"0.5"`.

Defaulted rather than left absent for the reason `color` is on
[`NetBoxDeviceRole`](netboxdevicerole.md#speccolor): a field NetBox fills in and the operator
never sends is a field the operator can never correct.

### `spec.isFullDepth`, `spec.excludeFromUtilization`

Optional booleans. `is_full_depth` defaults to **true** in NetBox and
`exclude_from_utilization` to **false**.

Pointers in the API, so three states are possible: absent leaves NetBox's value alone, `true`
writes true, `false` writes false. That is what makes adopting a hand-made half-depth type a
no-op instead of silently making it full-depth.

### `spec.subdeviceRole`

Optional enum: `""`, `parent` or `child`. A parent device houses child devices in device bays;
leave it blank for a type that is neither.

Unset leaves NetBox's own value alone; `""` clears it. `SubdeviceRoleChoices` declares no
extension key in NetBox, so this set is closed and the CRD can enforce it.

### `spec.airflow`

Optional enum: `""`, `front-to-rear`, `rear-to-front`, `left-to-right`, `right-to-left`,
`side-to-rear`, `rear-to-side`, `bottom-to-top`, `top-to-bottom`, `passive`, `mixed`.

Unset leaves NetBox's own value alone; `""` clears it, and is sent as JSON `null` because
NetBox's serializer returns `null` rather than `""` for an unset choice.

`DeviceAirflowChoices` *is* extensible through a NetBox deployment's `FIELD_CHOICES`, so a value
your NetBox has added is rejected by `kubectl apply` here until this enum is widened. That
trade-off is the same one [`NetBoxSite`](netboxsite.md)'s `status` makes.

### `spec.description`, `spec.comments`

Optional free text; `description` is capped at 200 characters and `comments` has no limit.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

Two candidates, tried in this order, from `dcim.DeviceType.meta.constraints`:

```
UniqueConstraint(fields=('manufacturer', 'model'), name='..._unique_manufacturer_model')
UniqueConstraint(fields=('manufacturer', 'slug'),  name='..._unique_manufacturer_slug')
```

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(manufacturer, slug)` | `?manufacturer_id=<id>&slug=<slug>` | `manufacturerRef` **resolves** |
| 2 | `(manufacturer, model)` | `?manufacturer_id=<id>&model=<model>` | `manufacturerRef` **resolves** |

Neither constraint is conditional, so **there is no null pin here** — unlike on
[`NetBoxDeviceRole`](netboxdevicerole.md) and [`NetBoxPlatform`](netboxplatform.md). `manufacturer`
is `REQ`; a manufacturer-less device type does not exist to be looked up.

This pair *is* a fallback chain, and the only one among this ticket's four kinds. Both `model` and
`slug` are required, so both candidates apply together and the second is reached only when the
first matched nothing. That is safe because of what the constraints say: an object candidate 2
finds is the same make and model the spec describes, and creating a second one would be a 409 on
the unique index rather than a duplicate — so adopting it and PATCHing the slug is strictly
better than failing every reconcile. `slug` leads because it is the stable identifier: a marketing
rename edits `model`, and looking up by `slug` first keeps that a PATCH rather than a second
object.

With `manufacturerRef` declared and unresolved, **no** candidate applies and the engine waits.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.DeviceType` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the device type exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `manufacturerRef` resolves and `defaultPlatformRef` is unset or resolves | either does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### An unresolved manufacturer writes nothing

Both candidates start at `manufacturer_id`, so there is no identity to look up and nothing to
create. The object reports `RefsResolved=False` naming `manufacturerRef` and performs **zero
NetBox writes** — asserted on the recorded request, because a version that reported the reference
and created the object anyway would look identical in the status.

### The eleven counter caches are never sent and never diffed

`dcim.DeviceType` declares eleven `CounterCacheField`s — `device_count`,
`interface_template_count`, `power_port_template_count` and the rest — plus `WeightMixin`'s
`_abs_weight`. NetBox maintains all of them and **ignores** a write to any, so each is in the
descriptor's read-only list: an ignored write produces a difference the next reconcile finds
again, and PATCHes forever.

### Two namespaces claiming one `(manufacturer, slug)` is one device type

NetBox has one object per `(manufacturer, slug)`, and a namespace boundary does not partition a
database constraint. The first CR to reconcile creates or adopts it; the second reports
`Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)). On a catalogue kind this is much more likely than
it is on a site — which is the argument for one shared catalogue namespace and grants.

### Deleting one is usually refused

`dcim.Device.device_type` is `on_delete=PROTECT`, so NetBox refuses to delete a device type while
any device is of that type, and the CR reports `Deleting=False, Reason=Protected`.

### What is not here yet

- **`frontImage` / `rearImage`.** `front_image` and `rear_image` are `ImageField`s, uploaded as
  multipart form data and returned as URLs. No JSON payload can write one, so a spec field for
  either would be a field the operator sends and NetBox ignores — and a CR spec is not a file
  transport. Manage the images in the UI.
- **`weight` / `weightUnit`.** Real columns from `WeightMixin`, deliberately out of scope for
  NBO-027; NBO-060 is the audit that picks them up.
- **The ten `*Template` component kinds** a device type owns — console ports, interfaces, power
  ports and the rest — are NBO-052 / NBO-053. This Kind describes the model, not its component
  templates.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbdtype
NAME        MANUFACTURER   MODEL                       U     ID   READY   AGE
ucg-ultra   ubiquiti       UniFi Cloud Gateway Ultra   1.0   80   True    6m
crs326      mikrotik       CRS326-24G-2S+              1.0   81   True    6m
```

`MANUFACTURER` reads `.spec.manufacturerRef.name`, so it shows the *intent* even while the
reference is unresolved and `ID` is empty.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `kubectl apply` rejected, message names `manufacturerRef` | The field is required by the schema, because NetBox's column is `REQ`. |
| `Ready=False`, `Reason=WaitingForRef` | `manufacturerRef` or `defaultPlatformRef` does not resolve. `RefsResolved` says which, and nothing was written. |
| `RefsResolved=False`, `Reason=RefDenied` | A cross-namespace ref with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. See [Start with the grant](#start-with-the-grant). |
| `Ready=False`, `Reason=Conflict` | Another namespace already owns this `(manufacturer, slug)`, or one NetBox object matched and `onConflict` is `Fail`. |
| A PATCH on every resync | Not expected for `uHeight` — it is compared numerically. Check `status.lastAppliedHash` and the `Synced` condition's message for the field that differs. |
| `kubectl apply` rejected on `airflow` | The value is not in the enum. A NetBox that extended `DeviceAirflowChoices` needs this CRD's enum widened. |
| `Deleting=False`, `Reason=Protected` | A device of this type still exists. |

## Related

- [`NetBoxManufacturer`](netboxmanufacturer.md) — the required reference this kind's identity needs
- [`NetBoxPlatform`](netboxplatform.md) — what `defaultPlatformRef` points at
- [`NetBoxDeviceRole`](netboxdevicerole.md) — the other half of what a `NetBoxDevice` needs
- [`NetBoxLocation`](netboxlocation.md) — the first kind with a required reference, same shape
- [Lookups](../concepts/lookups.md) — candidate order, and why a null filter is pinned
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
