# `NetBoxVMInterface`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVMInterface` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvmif` |
| Status subresource | yes |

A `NetBoxVMInterface` is one `virtualization.VMInterface` in NetBox: a network interface on a
[`NetBoxVirtualMachine`](netboxvirtualmachine.md).

It is also the kind that makes `IPAssignment.vmInterfaceRef` work. That union member has named
`NetBoxVMInterface` since NBO-011 and nothing registered the Kind, so every use of it reported
`RefKindUnavailable`; registering `virtualization.vminterface` puts it in the registry's
reverse index, which is what turns the member into a resolvable target and a watch
([generic references](../concepts/generic-refs.md)).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVMInterface
metadata:
  name: dns-eth0
  namespace: default
spec:
  endpointRef: homelab
  virtualMachineRef:
    name: dns
  name: eth0
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVMInterface
metadata:
  name: dns-eth0
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  virtualMachineRef:
    name: dns
  name: eth0

  enabled: true
  mtu: 1500
  mode: access                # access | tagged | tagged-all | q-in-q

  untaggedVLANRef:
    lookup:
      vid: "20"
  taggedVLANs:                # at most 256 refs
    - lookup:
        vid: "30"
  qinqSVLANRef:               # deferred when it does not resolve
    lookup:
      vid: "4000"

  parentRef:                  # self-FK, deferred when it does not resolve
    name: dns-bond0
  bridgeRef:                  # self-FK, deferred when it does not resolve
    name: dns-br0

  vrfRef:
    name: vrf-home

  description: Primary interface
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.virtualMachineRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxVirtualMachine` |
| Required | **yes** |

`virtual_machine ForeignKey REQ -> virtualization.VirtualMachine on_delete=CASCADE`, declared
on `virtualization.ComponentModel`, which is where `VMInterface` inherits it
(`docs/netbox-schema.md` → `virtualization.ComponentModel`).

It is half the identity as well as the parent. `(virtual_machine, name)` is unique **per VM**
and not globally, so an unresolved reference means the operator cannot tell whether this
interface exists — and it waits rather than looking `eth0` up across every VM in NetBox. That
is a lookup that would match somebody else's interface on almost any real instance.

It is also the **containment parent**: deleting the `NetBoxVirtualMachine` in the same
namespace takes its hand-written interfaces with it
([ADR-0003](../decisions/0003-ownership-and-references.md) §4). `on_delete=CASCADE` says the
same thing on the NetBox side. M5 replaces this with a *controller* owner reference for
interfaces the operator materialises from a VM's inline list (NBO-032); a hand-written one
stays a non-controller owner, because Kubernetes allows only one controller per object.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=64` |

Matched **case-sensitively**, unlike a VM's name. `ComponentModel`'s constraint is
`UniqueConstraint(fields=('virtual_machine', 'name'))` with no `Lower()`
(`docs/netbox-schema.md` → `virtualization.ComponentModel`, `meta.constraints`), so `Eth0` and
`eth0` are two interfaces on one VM and the lookup must not merge them.

Two `NetBoxVMInterface` CRs naming `eth0` on the same VM are claiming *one* NetBox object: the
second reports `Ready=False, Reason=Conflict`. The same two names on different VMs are two
different objects and both reconcile.

### `spec.enabled`

| | |
|---|---|
| Type | `*bool` |
| Required | no |

`enabled (BaseInterface) BooleanField def=True`. A **pointer**, because of that default: a
plain bool cannot tell "not managed" from "managed as false", so adopting an interface a human
had disabled would silently re-enable it on the first reconcile. Nil leaves NetBox's value
alone; `false` writes false ([field ownership](../concepts/field-ownership.md)).

### `spec.mtu`

| | |
|---|---|
| Type | `*int32` |
| Required | no |
| Validation | `Minimum=1`, `Maximum=65536` |

Bounded at NetBox's own validators, `INTERFACE_MTU_MIN` and `INTERFACE_MTU_MAX`
(`netbox/dcim/constants.py` lines 48–49, NetBox 4.6.8). Pinned as literals so that a NetBox
release changing them shows up as a schema diff rather than as `400`s at runtime.

### `spec.mode`

| | |
|---|---|
| Type | enum |
| Required | no |
| Default | none |
| Values | `access`, `tagged`, `tagged-all`, `q-in-q` |

How the interface treats 802.1Q tags. The column is
`mode (BaseInterface) CharField len=50 choices=InterfaceModeChoices`, and the values are read
from `netbox/dcim/choices.py` lines 1543–1555 — the digest carries the choice class and not
its members.

**Not defaulted**, unlike a VM's `status`: the column carries no `def=`, and an unset mode is a
real state in NetBox — an interface with no VLAN semantics at all — so defaulting it would make
every adopted interface drift towards a mode nobody chose. The consequence is that it also
cannot be *cleared* through this API: an enum has no empty member. Omitting it leaves NetBox's
value alone; the way back to an unset mode is the NetBox UI.

### `spec.parentRef`, `spec.bridgeRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxVMInterface` |
| Required | no |
| Deferred | when unresolved |

Two self-references: `parent ... on_delete=RESTRICT` and `bridge ... on_delete=SET_NULL`.

**Deferred when they do not resolve, and only then.** Two interfaces of one VM naming each
other cannot both be created with the reference in place, so the field comes out of the create
and goes in a follow-up `PATCH`. Conditionally rather than always, because unconditional
deferral would turn every ordinary sub-interface into two writes with a visible intermediate
state where it is briefly top-level (NBO-015). Neither is part of the identity, so a deferred
parent cannot make the operator adopt the wrong row.

Bridging is symmetric in intent and not in the database — NetBox stores one column per
interface — so two interfaces bridged to each other are two CRs each naming the other, and
each defers until the other exists.

### `spec.untaggedVLANRef`, `spec.taggedVLANs`, `spec.qinqSVLANRef`

| | `untaggedVLANRef` | `taggedVLANs` | `qinqSVLANRef` |
|---|---|---|---|
| Type | `ObjectRef` → `NetBoxVLAN` | `[]ObjectRef` → `NetBoxVLAN` | `ObjectRef` → `NetBoxVLAN` |
| Deferred | no | no | when unresolved |
| Validation | — | `MaxItems=256` | — |

`taggedVLANs` is a many-to-many with the three states every optional field has: omitting the
field leaves NetBox's own list alone, `[]` clears it, and a list replaces it. The order is not
data — NetBox does not preserve it — so the ids are sent sorted and deduplicated and the
comparison is order-independent ([drift](../concepts/drift.md)). Reordering the list produces
no write.

**All or nothing.** If any element cannot be resolved the whole field is left out of the
payload and the object reports `RefsResolved=False` naming the element that failed. Writing
the ones that did resolve would be a full-list replacement with a shorter list — a deletion,
reported as a success.

`MaxItems` is not a NetBox limit and is not decoration: `ObjectRef` carries five CEL rules, and
the API server costs each one at the list's maximum length, so an unbounded list of refs is
rejected outright with "estimated rule cost exceeds budget". 256 is the project's standard
bound (#187). A trunk needing more than 256 VLANs enumerated wants `mode: tagged-all`, which is
one field instead of a list.

`qinqSVLANRef` is deferred because a Q-in-Q service VLAN is usually applied in the same pass as
the interfaces that carry it, and NetBox cross-validates it against `mode`.

### `spec.vrfRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxVRF` |
| Required | no |

`vrf ForeignKey -> ipam.VRF on_delete=SET_NULL`, declared on `VMInterface` itself rather than
on `BaseInterface` — `dcim.Interface` has no `vrf` of its own.

### `spec.vlanTranslationPolicyRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxVLANTranslationPolicy`](netboxvlantranslationpolicy.md) |
| Required | no |

The table of VLAN ID rewrites applied to this interface.
`vlan_translation_policy (BaseInterface) ForeignKey -> ipam.VLANTranslationPolicy
on_delete=PROTECT`.

Inherited from `BaseInterface`, so it is the same column
[`NetBoxInterface`](netboxinterface.md#specvlantranslationpolicyref) carries and it points at
the same Kind: one policy can be shared by a physical interface and a VM interface at once.

Not deferred — a policy has no dependency on the interface pointing at it. `PROTECT`, so it is
not a containment parent ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4), and
pointing at a policy is what stops it being deleted.

### `spec.description`

`MaxLength=200`, inherited from `virtualization.ComponentModel`. Omit it to leave NetBox's own
value alone; set it to `""` to clear it.

### What is deliberately absent

- **`macAddress` / `primaryMACAddressRef`.** NetBox 4.2 moved the MAC to `dcim.MACAddress`
  behind a generic FK. This model's own entry lists only `mac_addresses GenericRelation` — a
  reverse relation, never a column. `NetBoxMACAddress` is NBO-048.
- **`comments`.** `virtualization.ComponentModel` is a `NetBoxModel` rather than a
  `PrimaryModel`, so there is no such column.
- **`ipAddresses`, `fhrpGroupAssignments`, `tunnelTerminations`, `l2vpnTerminations`** — all
  `GenericRelation`s, which is to say queries rather than columns. An IP address points at this
  interface, not the other way round.

## Natural key

One candidate, and it comes from the parent model:

| # | Candidate | Query |
|---|---|---|
| 1 | `(virtual_machine, name)` | `?virtual_machine_id=&name=` |

`virtualization.VMInterface` lists no `meta.constraints` of its own;
`virtualization.ComponentModel` carries
`UniqueConstraint(fields=('virtual_machine', 'name'))` (`docs/netbox-schema.md`).

Two consequences. `virtual_machine_id` is never omitted — the pair is unique per VM and `eth0`
is the most-reused interface name there is. And there is no `Lower()`, so the name filter is
exact, unlike [`NetBoxVirtualMachine`](netboxvirtualmachine.md#natural-key)'s `name__ie`.

There is no second candidate, because both halves are required fields: there is no state in
which one is missing and a different identity applies.

## `status`

Identical to every other kind. `virtualization.ComponentModel` mixes in both `TagsMixin` and
`CustomFieldsMixin`, so the provenance stamp applies in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the interface exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | one did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefKindUnavailable` |
| `ParentOwned` | the VM's owner reference is set | it cannot be | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Deleting an interface a `NetBoxIPAddress` is still assigned to reports
`Deleting=False, Reason=Protected` naming the blocker, and converges once the address is gone.
No force, no orphan ([deletion](../concepts/deletion.md)).

## Printer columns

```
NAME       VM    ENABLED   ID   READY   AGE
dns-eth0   dns   true      51   True    4m
```

| Column | JSONPath |
|---|---|
| `VM` | `.spec.virtualMachineRef.name` |
| `ENABLED` | `.spec.enabled` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |
