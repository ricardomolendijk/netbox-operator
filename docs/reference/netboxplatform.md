# `NetBoxPlatform`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxPlatform` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbplatform` |
| Status subresource | yes |

A `NetBoxPlatform` is one `dcim.Platform` in NetBox: the operating system or firmware a device
runs. `dcim.Device.platform` and `dcim.DeviceType.default_platform` both point here.

It is the one `NestedGroupModel` in the catalogue whose uniqueness is **not** scoped by its own
tree. `dcim.Platform.meta.constraints` is keyed on `manufacturer`, not on `parent`, which makes
its natural key the mirror image of [`NetBoxDeviceRole`](netboxdevicerole.md)'s — see
[Natural keys](#natural-keys). Getting it the wrong way round means adopting the wrong platform.

## Start with the grant

Catalogues live in a namespace like everything else in `v1alpha1`, so the deployment this Kind is
built for is a shared catalogue namespace that team namespaces reference into. Crossing a
namespace boundary requires a [`NetBoxRefGrant`](netboxrefgrant.md) **in the namespace being
referenced** — this one. Without it, every `NetBoxDevice` or `NetBoxDeviceType` naming a platform
across the boundary sits at `RefsResolved=False, Reason=RefDenied`.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readers
  namespace: netbox-catalog        # the namespace holding the platforms
spec:
  from:
    namespaces: All                # or a selector; see NetBoxRefGrant
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPlatform
metadata:
  name: unifi-os
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  name: UniFi OS
  slug: unifi-os
```

A team namespace then names it across the boundary:

```yaml
spec:
  platformRef:
    namespace: netbox-catalog
    name: unifi-os
```

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPlatform
metadata:
  name: unifi-os
  namespace: default
spec:
  endpointRef: homelab
  name: UniFi OS
  slug: unifi-os
```

No `manufacturerRef`, so this is a **vendor-neutral** platform — a *different identity* rather
than the same identity with a field missing. See [Natural keys](#natural-keys).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPlatform
metadata:
  name: unifi-os-4
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: UniFi OS 4
  slug: unifi-os-4
  description: Ubiquiti's device operating system, major 4

  # Part of the identity: with it, the lookup is `?manufacturer_id=<id>&slug=unifi-os-4`.
  manufacturerRef:
    name: ubiquiti

  # Not part of the identity, and therefore deferrable: applying parent and child in either
  # order converges without a resync.
  parentRef:
    name: unifi-os
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxplatform.yaml`](../../config/samples/netbox_v1alpha1_netboxplatform.yaml).

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

Required. The platform's name, up to 100 characters.

**Not globally unique**: `meta.constraints` scopes it per manufacturer, with a separate
constraint for manufacturer-less platforms.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and unique *per manufacturer* rather than globally — two platforms called `ios`
under different manufacturers are two legitimate objects, and both reconcile `Ready`.

### `spec.manufacturerRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxManufacturer`](netboxmanufacturer.md), limiting this platform to one vendor's devices.

**Part of the identity**, and the surprising part. Leaving it unset makes a *vendor-neutral*
platform, which is a different natural key rather than the same key with a filter omitted — so the
lookup pins `manufacturer_id__isnull=true` instead of dropping the filter. Declaring it and having
it not resolve yet makes neither candidate applicable, and the engine waits rather than adopting an
unrelated vendor-neutral platform of the same slug.

`on_delete=PROTECT`, so NetBox refuses to delete a manufacturer while a platform points at it.

Written to NetBox as `manufacturer`, filtered as `manufacturer_id`.

### `spec.parentRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at another `NetBoxPlatform`.

Self-referential (`parent TreeForeignKey -> dcim.Platform on_delete=CASCADE`), and **outside the
identity**: no constraint on `dcim.Platform` mentions `parent`, so a platform is findable by its
slug whatever its place in the tree.

That is what makes this reference safe to **defer**. If it does not resolve yet, the engine creates
the platform top-level and PATCHes `parent` on when the reference arrives, reporting
`DeferredFieldPending` in between — so a parent and a child applied in one batch converge without a
resync. A resolved parent goes into the create payload; deferral is `IfUnresolved`, not `Always`.
[`NetBoxDeviceRole`](netboxdevicerole.md#a-child-writes-nothing-until-its-parent-exists) is the
same base class and the opposite answer, because its identity *does* include `parent`.

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

Two candidates, tried in this order, straight out of `dcim.Platform.meta.constraints`:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(manufacturer, slug)` | `?manufacturer_id=<id>&slug=<slug>` | `manufacturerRef` **resolves** to an id |
| 2 | `slug` where `manufacturer IS NULL` | `?manufacturer_id__isnull=true&slug=<slug>` | `manufacturerRef` was **never declared** |

The evidence is the constraint's own condition, and the column it names:

```
UniqueConstraint(fields=('manufacturer', 'slug'), name='..._manufacturer_slug')
UniqueConstraint(fields=('slug',), name='..._slug', condition=Q(manufacturer__isnull=True))
```

`manufacturer__isnull`, not `parent__isnull`. `parent` appears in **no** candidate at all, which is
what this Kind exists to demonstrate: the base class does not decide a natural key, the constraint
list does. Compare [`NetBoxDeviceRole`](netboxdevicerole.md#natural-keys) (same base class, pins
`parent_id`) and [`NetBoxTenantGroup`](netboxtenantgroup.md) (same base class, no pin at all).

Not a fallback chain. Candidate 2 is the identity of a *different* object, a vendor-neutral
platform. A platform whose manufacturer has not been created yet matches **neither**, and the
engine waits: falling through would find an unrelated vendor-neutral platform of that slug, adopt
it, and the follow-up PATCH would attach somebody else's platform to this manufacturer.

`manufacturer_id__isnull=true` is **pinned rather than omitted**. A query with `manufacturer_id`
merely left out asks "this slug under any manufacturer". See
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.Platform` is a `NestedGroupModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

`status.naturalKey` records which candidate ran, filter by filter, so
`{"manufacturer_id__isnull": "true", "slug": "unifi-os"}` tells you the engine treated the platform
as vendor-neutral.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the platform exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending`, `DeferredFieldPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `manufacturerRef` and `parentRef` are unset or resolved | either does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### Two platforms with one slug under different manufacturers both reconcile

`(manufacturer, slug)` is the constraint, so `ubiquiti/ios` and `cisco/ios` are two objects and
neither is a conflict. Two *vendor-neutral* platforms with one slug are one object, and the second
CR to reach it reports `Conflict`.

### A child converges off its parent's event, and does not wait for it

Unlike every other nested-group kind here, a platform whose `parentRef` does not resolve is still
created: its identity does not include `parent`, so the engine creates it top-level, reports
`DeferredFieldPending`, and PATCHes `parent` on when the reference resolves. The reference watch
re-enqueues it directly, so applying parent and child in one batch converges without a resync.

### A `parentRef` cycle is reported on both objects, and nothing is written

`a.parentRef → b` and `b.parentRef → a` cannot resolve in any order. The operator walks the
reference graph before it makes any NetBox request and reports
`RefsResolved=False, Reason=RefCycle` naming the ring, on **every** member of it. A cycle is
refused outright rather than deferred: see [cycles](../concepts/references.md#cycles).

### Renaming the slug changes identity

`slug` is in both natural keys, so editing it does not rename the NetBox platform — it changes what
the CR is looking for, and the next reconcile creates a second platform, leaving the first behind.
The same is true of adding or removing `manufacturerRef`, which moves the object from one identity
to the other. `name` and `description` are safe to edit.

### Deleting one is *not* refused, and that is the thing to know

Both references into this model are `on_delete=SET_NULL` — `dcim.Device.platform` and
`dcim.DeviceType.default_platform` (`docs/netbox-schema.md`). So unlike
[`NetBoxManufacturer`](netboxmanufacturer.md) and [`NetBoxDeviceRole`](netboxdevicerole.md), whose
deletes NetBox refuses while anything points at them, deleting a `NetBoxPlatform` **succeeds** and
silently clears the platform off every device that had it. `parent` is `CASCADE` on top of that, so
the delete takes the whole subtree with it.

`deletionPolicy` defaults to `Delete`, so `kubectl delete nbplatform` is enough to do all of it.
Set `deletionPolicy: Retain` on a platform in real use if you want the CR removable without the
NetBox object going too.

### `_depth` and `_children` are never written

`dcim.Platform` is an MPTT tree, so NetBox maintains `_depth` and `_children` itself
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns). Both are in the descriptor's read-only
list: an ignored write produces a difference the next reconcile finds again, and PATCHes forever.

### What is not here yet

`configTemplateRef` (`config_template -> extras.ConfigTemplate`) is out of scope until
`extras.ConfigTemplate` has a Kind (NBO-059). `comments` is a column this model carries and the CRD
does not expose (NBO-060). `tags` and `customFields` are written by the provenance stamp and not by
a user.

## Printer columns

```
$ kubectl get nbplatform
NAME         SLUG         MANUFACTURER   ID   READY   AGE
unifi-os     unifi-os                    90   True    7m
unifi-os-4   unifi-os-4   ubiquiti       91   True    7m
```

`MANUFACTURER` reads `.spec.manufacturerRef.name`, so it shows the *intent* even while the
reference is unresolved and `ID` is empty — and an empty cell means a vendor-neutral platform,
which is a different identity rather than a missing value.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Ready=False`, `Reason=WaitingForRef`, `manufacturerRef` set | The manufacturer CR does not exist, or holds no `status.id` yet. Nothing was written. |
| `Ready=False`, `Reason=DeferredFieldPending` | `parentRef` has not resolved. The platform exists top-level and `parent` is applied by a follow-up PATCH. |
| `Ready=False`, `Reason=Conflict` | Another namespace already owns this `(manufacturer, slug)`, or one NetBox object matched and `onConflict` is `Fail`. |
| `RefsResolved=False`, `Reason=RefCycle` | Two platforms name each other as parent. Edit either one. |
| `RefsResolved=False`, `Reason=RefDenied` | A cross-namespace ref with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. |
| A second platform appeared after an edit | `spec.slug` or `spec.manufacturerRef` was changed; both are identity. See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |
| The wrong platform was adopted | Check `status.naturalKey`. A `manufacturer_id__isnull` term where you expected `manufacturer_id` means `manufacturerRef` was never declared. |

## Related

- [`NetBoxDeviceRole`](netboxdevicerole.md) — the same base class, pinned on `parent_id` instead
- [`NetBoxTenantGroup`](netboxtenantgroup.md) — the same base class with no pin at all
- [`NetBoxManufacturer`](netboxmanufacturer.md) — what scopes this kind's uniqueness
- [`NetBoxDeviceType`](netboxdevicetype.md) — holder of `defaultPlatformRef`
- [Lookups](../concepts/lookups.md) — why a null filter is pinned rather than omitted
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
