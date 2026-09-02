# `NetBoxRouteTarget`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRouteTarget` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrt` |
| Status subresource | yes |
| Lands with | NBO-022 (M3) |

A `NetBoxRouteTarget` is one `ipam.RouteTarget` in NetBox: a BGP extended community, written
`<asn>:<value>`, that a VRF imports or exports.

It ships with [`NetBoxVRF`](netboxvrf.md) because it is the only thing that VRF's two
many-to-many fields point at. It is also the plainest kind in the catalogue — three writable
columns and one natural key — and the interesting thing about it is what it does *not* have.

## The relation is written from the VRF side only

There is no `importedByVRFs` field here, and there will not be one.

`import_targets` and `export_targets` are declared on `ipam.VRF`
(`docs/netbox-schema.md` → `ipam.VRF`), so:

- A `NetBoxRouteTarget` has **nothing to reconcile** about its VRF membership. Create one and
  it reaches `Ready` knowing nothing about which VRFs use it.
- Two VRFs importing the same route target **do not conflict**. It is a shared object, and a
  many-to-many is by definition not containment — so neither VRF takes an owner reference on
  it ([ADR-0003](../decisions/0003-ownership-and-references.md) §4).
- Deleting a route target a VRF still imports returns `409` from NetBox, which the operator
  reports as `Deleting=False, Reason=Protected` and retries. Drop it from the VRF and the
  delete completes on its own; there is no ordering table
  ([deletion](../concepts/deletion.md)).

Getting this backwards is the easy mistake, and it would be expensive: a reverse field here
would make two objects writers of one relation, and since NetBox replaces a many-to-many
wholesale on `PATCH`, the two would overwrite each other forever.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRouteTarget
metadata:
  name: rt-65000-10-import
  namespace: default
spec:
  endpointRef: homelab
  name: "65000:10"
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRouteTarget
metadata:
  name: rt-65000-10-import
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: "65000:10"
  description: Imported into the Donkerslootstraat VRF
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared
envelope and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Validation | `+kubebuilder:validation:MinLength=1`, `+kubebuilder:validation:MaxLength=21` |

The route target itself, and this kind's natural key.

The 21-character cap is NetBox's `VRF_RD_MAX_LENGTH`
(`docs/netbox-schema.md` → `ipam.RouteTarget`, `name CharField REQ UNIQUE len=21`; the
constant is defined in `netbox/ipam/constants.py`). It is pinned as a literal so that a
NetBox release changing the constant shows up as a schema diff rather than as `400`s at
runtime.

**If it is wrong.** Longer than 21 characters or empty is rejected at admission, by the API
server, before the operator sees it. A name that collides with a route target another
namespace already owns reaches the operator and reports `Ready=False, Reason=Conflict` — see
below.

### `spec.description`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `+kubebuilder:validation:MaxLength=200` |

Free text. Inherited from `PrimaryModel`, so `docs/netbox-schema.md` lists it as
`description (PrimaryModel)` rather than under the model's own columns.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. Those are different
instructions ([field ownership](../concepts/field-ownership.md)).

### `spec.comments`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | none — a `TextField` has no `max_length` |

Long-form notes. Same three states as `description`.

### What is deliberately absent

`ipam.RouteTarget.tenant` is a `ForeignKey -> tenancy.Tenant on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.RouteTarget`) and there is **no `tenantRef` field yet**.
`NetBoxTenant` lands with NBO-021, and a field that is accepted and does nothing is worse
than a field that is not there: `kubectl apply` would report success and NetBox would never
see the value.

## Natural key

One candidate:

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

`name` carries a column-level `UNIQUE` and `ipam.RouteTarget` declares no `meta.constraints`
(`docs/netbox-schema.md` → `ipam.RouteTarget`), so this filter identifies at most one route
target and needs no second candidate and no null pin.

A route target a human created by hand is therefore **adopted rather than duplicated** — with
`onConflict: Adopt`. The default is `Fail`, which reports the existing object instead of
taking it over.

Because NetBox's uniqueness is global and these CRs are namespaced, two namespaces claiming
`65000:10` are claiming *one* route target. The second gets
`Ready=False, Reason=Conflict`. If route targets are shared infrastructure, keep them in one
namespace and reference them across it with a
[`NetBoxRefGrant`](netboxrefgrant.md).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`ipam.RouteTarget` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the route target exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind declares no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved=True/AllResolved` on a kind with no references is not noise: it says the engine
looked and found nothing outstanding, which is what distinguishes this kind from one whose
references are declared and unresolved.

## `deletionPolicy` defaults to `Delete`

Like every kind, [`NetBoxVRF`](netboxvrf.md#deletionpolicy-defaults-to-delete) included, since
[#304](https://github.com/ricardomolendijk/netbox-operator/issues/304).

This kind never had a reason to be anything else. Issue #176 defaulted the IPAM kinds holding
allocated state to `Retain` — `NetBoxPrefix`, `NetBoxIPAddress`, `NetBoxIPRange`, `NetBoxVLAN`,
`NetBoxVRF` — and a route target was not on that list and should not have been: nothing is
allocated from it, deleting one destroys no record of who owned a range, and re-creating it is
free. It is configuration, like a tag. #304 brought the rest of the catalogue here rather than
the other way round ([deletion](../concepts/deletion.md#why-this-reversed)).

Set `deletionPolicy: Retain` on route targets that other tooling also depends on.

## Printer columns

```
NAME                 TARGET     ID   READY   AGE
rt-65000-10-import   65000:10   41   True    2m
rt-65000-11-export   65000:11   42   True    2m
```

| Column | JSONPath |
|---|---|
| `TARGET` | `.spec.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`TARGET` rather than a second `NAME`: `metadata.name` is the Kubernetes object's name and
`spec.name` is the route target NetBox stores, and they are routinely different — `65000:10`
is not a legal Kubernetes object name.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `spec.name` empty or over 21 characters | Shorten it; NetBox's own column is 21 |
| `Ready=False`, `Reason=Conflict` | `Ready` | a route target with this `name` already exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt`, or pick another name. `status.naturalKey` shows what was searched |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | the `NetBoxEndpoint` is not `Ready` | See [`NetBoxEndpoint`](netboxendpoint.md) |
| `Deleting=False`, `Reason=Protected` | `Deleting` | a VRF still imports or exports this route target | Drop it from the VRF's `importTargets`/`exportTargets`; the delete then completes on the next pass |
| A second route target appeared after an edit | none | `spec.name` was changed, which changes identity | Rename in NetBox and in the manifest together, or delete and re-create the CR |

## Related

- [`NetBoxVRF`](netboxvrf.md) — where the many-to-many relation is declared and written
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Deletion](../concepts/deletion.md) — `Protected` and why there is no ordering table
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
