# `NetBoxDeviceRole`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxDeviceRole` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbdrole` |
| Status subresource | yes |
| Lands with | NBO-027 |

A `NetBoxDeviceRole` is one `dcim.DeviceRole` in NetBox: what a device *is for* — router,
access switch, hypervisor.

It serves both halves of the inventory. `dcim.Device.role` and
`virtualization.VirtualMachine.role` both target `dcim.DeviceRole`
(`docs/netbox-schema.md`) — there is no virtualization-specific role model, which is what
[`spec.vmRole`](#specvmrole) is for.

## Start with the grant

Catalogues live in a namespace like everything else in `v1alpha1`, so the deployment this Kind
is built for is a shared catalogue namespace that team namespaces reference into. Crossing a
namespace boundary requires a [`NetBoxRefGrant`](netboxrefgrant.md) **in the namespace being
referenced** — this one. Without it, every `NetBoxDevice` naming a role across the boundary sits
at `RefsResolved=False, Reason=RefDenied`.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readers
  namespace: netbox-catalog        # the namespace holding the roles
spec:
  from:
    namespaces: All                # or a selector; see NetBoxRefGrant
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDeviceRole
metadata:
  name: router
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  name: Router
  slug: router
```

A team namespace then names it across the boundary:

```yaml
spec:
  roleRef:
    namespace: netbox-catalog
    name: router
```

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDeviceRole
metadata:
  name: router
  namespace: default
spec:
  endpointRef: homelab
  name: Router
  slug: router
```

No `parentRef`, so this is a top-level role — which is a *different identity* rather than the
same identity with a field missing. See [Natural keys](#natural-keys).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDeviceRole
metadata:
  name: edge-router
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Edge router
  slug: edge-router
  color: 2196f3               # six hex digits, no leading '#'
  vmRole: false
  description: Talks to the ISP

  # Nests this role. Applying parent and child in either order converges.
  parentRef:
    name: router
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxdevicerole.yaml`](../../config/samples/netbox_v1alpha1_netboxdevicerole.yaml).

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

Required. The role's name, up to 100 characters.

**Not globally unique.** Two of `dcim.DeviceRole`'s four constraints are on it — `(parent, name)`
and `(name) WHERE parent IS NULL` — so two roles may share a name under different parents.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and unique *per parent* rather than globally.

### `spec.parentRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at another `NetBoxDeviceRole`.

Self-referential (`parent TreeForeignKey -> dcim.DeviceRole on_delete=CASCADE`). Omitting it
makes a **top-level** role, which is a different natural key rather than the same key with a
filter omitted.

Written to NetBox as `parent`, filtered as `parent_id`. Both spellings appear below, because the
write name and the filter name genuinely differ for a foreign key.

### `spec.color`

Optional, six lowercase hexadecimal digits without a leading `#`. **Defaulted to `9e9e9e`**,
NetBox's own grey (`color ColorField def='ColorChoices.COLOR_GREY'`).

Defaulted deliberately rather than left absent: a field NetBox fills in and the operator never
sends is a field the operator can never correct, so a colour changed in the UI would stay
changed.

### `spec.vmRole`

Optional boolean. NetBox defaults it to **true**, so a role the operator creates is usable by
virtual machines unless you say otherwise.

A pointer in the API, which is what makes three states possible: absent leaves NetBox's value
alone, `true` writes true, `false` writes false. Leaving it absent is why adopting a hand-made
role is a no-op rather than a silent change.

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

Two candidates, tried in this order, straight out of `dcim.DeviceRole.meta.constraints`:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(parent, slug)` | `?parent_id=<id>&slug=<slug>` | `parentRef` **resolves** to an id |
| 2 | `slug` where `parent IS NULL` | `?parent_id__isnull=true&slug=<slug>` | `parentRef` was **never declared** |

The evidence for candidate 2 is the constraint's own condition:

```
UniqueConstraint(fields=('parent', 'slug'), name='..._parent_slug')
UniqueConstraint(fields=('slug',), name='..._slug', condition=Q(parent__isnull=True))
```

Not a fallback chain. Candidate 2 is not "what to try if 1 fails" — it is the identity of a
*different* object, a top-level role. A child whose parent has not been created yet matches
**neither**, and the engine waits: falling through would find an unrelated top-level role of that
slug, adopt it, and the follow-up PATCH would pull it out of somebody else's hierarchy.

`parent_id__isnull=true` is **pinned rather than omitted**. A query with `parent_id` merely left
out asks "this slug anywhere in the tree". See
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).

That the model is a `NestedGroupModel` is *not* what decides this.
[`NetBoxTenantGroup`](netboxtenantgroup.md) has the same base class, no `meta.constraints` at
all, and therefore no pin; [`NetBoxPlatform`](netboxplatform.md) has the same base class and pins
`manufacturer_id` instead. Read the constraint list, not the bases.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.DeviceRole` is a `NestedGroupModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

`status.naturalKey` records which candidate ran, filter by filter, so
`{"parent_id__isnull": "true", "slug": "router"}` tells you the engine treated the object as
top-level.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the role exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `parentRef` is unset or resolved | `parentRef` does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A child writes nothing until its parent exists

Apply the child first and it reports `RefsResolved=False, Reason=RefNotFound` and
`Ready=False, Reason=WaitingForRef`, and performs **zero NetBox writes** — candidate 1 needs
`parentRef` resolved, candidate 2 needs it undeclared, so neither applies. Applying the parent
re-enqueues the child directly through the reference watch, so it converges without waiting for
the endpoint's `resyncPeriod`.

This is why `parent` is **not** a deferred field here, unlike on
[`NetBoxPlatform`](netboxplatform.md). Deferral means "create without the field and PATCH it on",
and that would put the role at the top level for one pass — exactly where candidate 2's null pin
says a different object lives.

### A `parentRef` cycle is reported on both objects

`a.parentRef → b` and `b.parentRef → a` cannot resolve in any order. The operator walks the
reference graph before it makes any NetBox request and reports
`RefsResolved=False, Reason=RefCycle` naming the ring, on **every** member of it, writing nothing.
See [cycles](../concepts/references.md#cycles).

### Renaming the slug changes identity

`slug` is in both natural keys, so editing it does not rename the NetBox role — it changes what
the CR is looking for, and the next reconcile creates a second role, leaving the first behind.
`name`, `color`, `vmRole` and `description` are safe to edit.

### Deleting one is usually refused

`dcim.Device.role` and `virtualization.VirtualMachine.role` are `on_delete=PROTECT`, so NetBox
refuses to delete a role while any device or VM has it, and the CR reports
`Deleting=False, Reason=Protected`.

### `_depth` and `_children` are never written

`dcim.DeviceRole` is an MPTT tree, so NetBox maintains `_depth` and `_children` itself
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns). Both are in the descriptor's
read-only list. Writing either would not fail — NetBox ignores it — which is precisely why it has
to be declared: an ignored write produces a difference the next reconcile finds again, and PATCHes
forever.

### What is not here yet

`configTemplateRef` (`config_template -> extras.ConfigTemplate`) is out of scope until
`extras.ConfigTemplate` has a Kind (NBO-059): a field the CRD accepts and the payload drops would
report success while writing nothing. `comments` is a column this model carries and the CRD does
not expose (NBO-060). `tags` and `customFields` are written by the provenance stamp and not by a
user.

## Printer columns

```
$ kubectl get nbdrole
NAME          SLUG          PARENT   ID   READY   AGE
router        router                 70   True    5m
edge-router   edge-router   router   71   True    5m
```

`PARENT` reads `.spec.parentRef.name`, so it shows the *intent* even while the reference is
unresolved and `ID` is empty.

## Troubleshooting

| Symptom | Cause |
|---|---|
| `Ready=False`, `Reason=WaitingForRef`, `parentRef` set | The parent CR does not exist, or holds no `status.id` yet. `RefsResolved` says which. |
| `Ready=False`, `Reason=Conflict` | Another namespace already owns this `(parent, slug)`, or one NetBox object matched and `onConflict` is `Fail`. |
| `RefsResolved=False`, `Reason=RefCycle` | Two roles name each other as parent. Edit either one. |
| `RefsResolved=False`, `Reason=RefDenied` | A cross-namespace `parentRef` with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. |
| `Deleting=False`, `Reason=Protected` | A device or a virtual machine still has this role. |
| A second role appeared after an edit | `spec.slug` was changed. See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |
| A VM cannot be given this role | `vmRole: false`. NetBox's default is `true`; the operator only writes the field when the spec sets it. |

## Related

- [`NetBoxPlatform`](netboxplatform.md) — the same base class, pinned on a different column
- [`NetBoxTenantGroup`](netboxtenantgroup.md) — the same base class with no pin at all
- [`NetBoxDeviceType`](netboxdevicetype.md) — the other half of what a `NetBoxDevice` needs
- [Lookups](../concepts/lookups.md) — why a null filter is pinned rather than omitted
- [References](../concepts/references.md) — the four ref modes, cycles, and grants
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
