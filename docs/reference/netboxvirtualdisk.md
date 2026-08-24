# `NetBoxVirtualDisk`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVirtualDisk` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvdisk` |
| Status subresource | yes |
| Lands with | NBO-029 (M4) |

A `NetBoxVirtualDisk` is one `virtualization.VirtualDisk` in NetBox: a disk attached to a
[`NetBoxVirtualMachine`](netboxvirtualmachine.md).

It is the smallest kind in the catalogue. `virtualization.VirtualDisk` declares exactly one
column of its own — `size PositiveIntegerField REQ` — and inherits the other three from
`virtualization.ComponentModel` (`docs/netbox-schema.md`). Its descriptor is four fields, one
natural key, no deferral and no generic FK, driven by the same one-line controller as every
other kind. That contrast with [`NetBoxVirtualMachine`](netboxvirtualmachine.md) is worth
reading side by side: same engine, same shape of file, an order of magnitude less to say.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVirtualDisk
metadata:
  name: dns-disk0
  namespace: default
spec:
  endpointRef: homelab
  virtualMachineRef:
    name: dns
  name: disk0
  size: 20480
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVirtualDisk
metadata:
  name: dns-disk0
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  virtualMachineRef:
    name: dns
  name: disk0
  size: 20480                 # MB or MiB, per the instance's DISK_BASE_UNIT

  description: Root disk
```

That is every field. There is no `full` example meaningfully larger than the minimal one, which
is the honest shape of this kind.

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.virtualMachineRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxVirtualMachine` |
| Required | **yes** |

`virtual_machine ForeignKey REQ -> virtualization.VirtualMachine on_delete=CASCADE` on
`virtualization.ComponentModel`.

Half the identity and the containment parent, on exactly
[`NetBoxVMInterface`](netboxvminterface.md#specvirtualmachineref)'s terms: the
`(virtual_machine, name)` pair is unique per VM rather than globally, so an unresolved
reference means the operator waits instead of looking `disk0` up across every VM in NetBox.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=64` |

Case-sensitive: `ComponentModel`'s constraint carries no `Lower()`, unlike all four of
`virtualization.VirtualMachine`'s.

### `spec.size`

| | |
|---|---|
| Type | `int32` |
| Required | **yes** |
| Validation | `Minimum=0`, `Maximum=2147483647` |

`size PositiveIntegerField REQ` (`docs/netbox-schema.md` → `virtualization.VirtualDisk`).

Required, and therefore **not a pointer**: the three states an optional field has do not apply
to a column NetBox will not accept as null, and a disk of no stated size is not a thing to let
through admission. `size: 0` is legal and explicit; omitting the key is rejected by the API
server.

The unit is whatever the NetBox instance's `DISK_BASE_UNIT` says — MB or MiB
(`netbox/virtualization/forms/model_forms.py` line 498, NetBox 4.6.8) — and it is the same unit
as [`NetBoxVirtualMachine.spec.disk`](netboxvirtualmachine.md#specmemory-specdisk), which is
what makes the two comparable at all.

### `spec.description`

`MaxLength=200`, inherited from `virtualization.ComponentModel`. Omit it to leave NetBox's own
value alone; set it to `""` to clear it. Those are different instructions
([field ownership](../concepts/field-ownership.md)).

## The interaction with `NetBoxVirtualMachine.spec.disk`

Use one or the other.

A VM's `disk` column must equal the sum of its virtual disks' sizes. NetBox fills `disk` from
the aggregate when it is `None`, and raises `ValidationError` when it is set and disagrees
(`netbox/virtualization/models/virtualmachines.py` lines 330–341, NetBox 4.6.8). So:

- **Only `NetBoxVirtualDisk` CRs** — NetBox computes the VM's total. This is the shape to
  prefer.
- **Only `spec.disk` on the VM** — NetBox stores what you gave it.
- **Both, consistently** — works, and is two places to keep one number.
- **Both, inconsistently** — the *VM* reports `Ready=False, Reason=Invalid` carrying NetBox's
  message naming both numbers. Loud, not a drift loop.

NBO-029's spec proposed a guard clause in the operator on the hypothesis that NetBox recomputes
`disk` silently, which would have made the contradiction an endless `PATCH`. The source says it
complains instead, so no guard ships and there is nothing left for one to prevent.

## Natural key

One candidate, the same one [`NetBoxVMInterface`](netboxvminterface.md#natural-key) has and
from the same place:

| # | Candidate | Query |
|---|---|---|
| 1 | `(virtual_machine, name)` | `?virtual_machine_id=&name=` |

`VirtualDisk` lists only `meta.ordering` of its own; the constraint is
`UniqueConstraint(fields=('virtual_machine', 'name'))` on `virtualization.ComponentModel`.

## `status`

Identical to every other kind. `virtualization.ComponentModel` mixes in both `TagsMixin` and
`CustomFieldsMixin`, so the provenance stamp applies in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the disk exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | `virtualMachineRef` resolved | it did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable` |
| `ParentOwned` | the VM's owner reference is set | it cannot be | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

There is no `DeferredFieldPending`: this kind defers nothing, because its only reference is
required and part of its identity.

## Printer columns

```
NAME        VM    SIZE    ID   READY   AGE
dns-disk0   dns   20480   61   True    4m
```

| Column | JSONPath |
|---|---|
| `VM` | `.spec.virtualMachineRef.name` |
| `SIZE` | `.spec.size` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |
