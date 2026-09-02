# `NetBoxInterface`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxInterface` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbif` |
| Status subresource | yes |

A `NetBoxInterface` is one `dcim.Interface` in NetBox: a named port on a
[device](netboxdevice.md), of a type, optionally in a LAG, optionally carrying VLANs.

> ### This is the kind `IPAssignment.interfaceRef` was waiting for
>
> `NetBoxIPAddress.assignedObject` is a
> [polymorphic reference](../concepts/generic-refs.md) with three members, and the
> `dcim.interface` one has named a Kind nothing registered since NBO-011. Every use of it
> reported `RefsResolved=False`, `Reason=RefKindUnavailable`.
>
> Registering this Kind is the whole of the fix. `Descriptor.ObjectType` is `dcim.interface`,
> `Registry.Add` builds the reverse index from it, and the resolver picks the Kind up as a
> watch target from there — no code, one field.
>
> | Member | Object type | Kind | Resolves? |
> |---|---|---|---|
> | `interfaceRef` | `dcim.interface` | [`NetBoxInterface`](netboxinterface.md) | yes |
> | `vmInterfaceRef` | `virtualization.vminterface` | [`NetBoxVMInterface`](netboxvminterface.md) | yes |
> | `fhrpGroupRef` | `ipam.fhrpgroup` | [`NetBoxFHRPGroup`](netboxfhrpgroup.md) | yes |
>
> This Kind closed the first of the three. All three resolve today: `NetBoxIPAddress` shipped
> with [#199](https://github.com/ricardomolendijk/netbox-operator/pull/199),
> `NetBoxVMInterface` with
> [#211](https://github.com/ricardomolendijk/netbox-operator/pull/211), and `NetBoxFHRPGroup`
> with the rest of the `ipam` remainder. `registry.ByObjectType` is the authority, and a unit
> test asserts all three states rather than the hoped-for one.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxInterface
metadata:
  name: sw1-ge-0-0-0
  namespace: default
spec:
  endpointRef: homelab
  deviceRef:
    name: sw1
  name: ge-0/0/0
  type: 1000base-t
```

Three required fields. `metadata.name` is a DNS-1123 label and `spec.name` keeps the literal
NetBox string, slashes included; the two are unrelated.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxInterface
metadata:
  name: rtmrpi0001-eth0
  namespace: default
spec:
  endpointRef: homelab
  deletionPolicy: Delete       # the envelope default, written out
  onConflict: Adopt            # the envelope default, written out

  deviceRef:
    name: rtmrpi0001
  name: eth0
  type: 1000base-t

  label: LAN
  enabled: true                # NetBox's own default, written out
  mgmtOnly: false              # NetBox's own default, written out
  markConnected: false         # NetBox's own default, written out

  mtu: 1500
  speed: 1000000               # kbps
  duplex: full
  wwn: "20:00:00:25:b5:00:00:01"

  mode: tagged
  untaggedVLANRef:
    name: vlan-1-mgmt-default
  taggedVLANs:
    - name: vlan-20-servers
    - name: vlan-30-clients
  vrfRef:
    name: house-rtm

  lagRef:                      # deferred when it does not resolve
    name: rtmrpi0001-bond0
  parentRef:                   # deferred when it does not resolve
    name: rtmrpi0001-eth0-phys
  bridgeRef:                   # deferred when it does not resolve
    name: rtmrpi0001-br0
  qinqSVLANRef:                # deferred when it does not resolve
    name: vlan-4000-svlan

  rfRole: ap
  rfChannel: 2.4g-1-2412-22
  rfChannelFrequency: "2412"
  rfChannelWidth: "22"
  txPower: 20
  poeMode: pse
  poeType: type2-ieee802.3at

  customFields:
    audit_ticket: OPS-4417

  description: Onboard gigabit Ethernet.
```

Every field at once, which is not a configuration anybody would ship: a `1000base-t` port with
wireless channel settings, a LAG, a parent *and* a bridge is exactly the combination NetBox's
own `clean()` rejects. It is here to show the shape, not the intent.

## `spec`

Envelope fields — `endpointRef`, `onConflict`, `deletionPolicy`, `customFields` — behave as on
every object kind. See [`NetBoxTag`](netboxtag.md#spec).

There is **no `comments`**: `dcim.ComponentModel` is a plain `NetBoxModel` rather than a
`PrimaryModel` (`docs/netbox-schema.md` → `dcim.ComponentModel`, bases), so the writable
envelope is smaller than a device's. It does mix in `TagsMixin` and `CustomFieldsMixin`, so the
provenance stamp applies in full.

### `spec.deviceRef`

| | |
|---|---|
| Type | `DeviceRef` → [`NetBoxDevice`](netboxdevice.md) |
| Required | **yes** |
| Default | none |
| Validation | `ObjectRef`'s five CEL rules |

The device this interface belongs to. `device ForeignKey REQ -> dcim.Device
on_delete=CASCADE`, declared on `dcim.ComponentModel`, which is where `dcim.Interface`
inherits it.

This one reference is three things at once:

1. **Required**, because NetBox's column is.
2. **Half the identity.** `(device, name)` is unique per device and not globally, and `eth0` is
   the most-reused interface name there is — see [natural keys](#natural-keys).
3. **The containment parent**, and the only foreign key on this model that could be.
   `on_delete=CASCADE` means NetBox deletes the interface when the device goes, so in one
   namespace `kubectl delete nbdev rtmrpi0001` takes this CR too
   ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). Every other foreign key
   here is `SET_NULL`, `RESTRICT` or `PROTECT` and none of them cascades, so the choice rule 4
   would otherwise leave open is closed by the schema.

Cross-namespace, the owner reference is **not** set — an owner reference may not cross a
namespace — and `ParentOwned=False`, `Reason=CascadeUnavailable` says so.

**If it is wrong.** Malformed reference: rejected at admission. Unresolvable: `Ready=False`,
`Reason=WaitingForKey`, and **zero lookups as well as zero writes** — without `device_id` the
operator cannot tell whether this interface exists, and looking `eth0` up across every device
in NetBox would adopt somebody else's.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Default | none |
| Validation | `minLength: 1`, `maxLength: 64` |

The interface's name. `name CharField REQ len=64` on `dcim.ComponentModel`.

Matched **case-sensitively**, unlike a [device's](netboxdevice.md#specname):
`UniqueConstraint(fields=('device', 'name'))` has no `Lower()`, so `Eth0` and `eth0` are two
interfaces on one device and the lookup must not merge them. This is the contrast worth
holding on to — the parent's name is case-insensitive and the child's is not, in the same
apply.

### `spec.type`

| | |
|---|---|
| Type | `string` enum |
| Required | **yes** |
| Default | none |
| Validation | one of 207 values from `InterfaceTypeChoices` |

The interface's physical or virtual form. `type CharField REQ len=50
choices=InterfaceTypeChoices`.

The values are read from `netbox/dcim/choices.py` lines 889-1508 (NetBox 4.6.8) — the schema
digest records the choice *class* and never its members, because the AST walk behind it cannot
evaluate a Django `ChoiceSet`. They are pinned by a golden file,
`api/v1alpha1/testdata/interface-types.txt`, compared against the generated CRD by
`TestInterfaceTypeEnumMatchesGolden`. A NetBox minor release that adds a transceiver type is a
regeneration of both.

The common ones: `virtual`, `bridge`, `lag`, `1000base-t`, `10gbase-t`, `10gbase-x-sfpp`,
`25gbase-x-sfp28`, `100gbase-x-qsfp28`, `ieee802.11ax`, `other`. `kubectl explain
netboxinterface.spec.type` lists all 207.

**No Go constants.** Nothing in the operator branches on an interface type — NetBox's own
`clean()` is what enforces that a LAG member is not itself a LAG — so 207 exported identifiers
would be 207 lines nobody reads. The list that matters is the one the API server enforces.

**If it is wrong.** An unknown value is **rejected at admission**, with no controller involved
and nothing stored. That is the point of the enum: a typo in a transceiver name is caught by
`kubectl apply` rather than surfacing as `Reason=Invalid` six seconds later.

### `spec.label`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `maxLength: 64` |

A physical label on the interface, distinct from its name — silkscreen against configuration.
Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.enabled` / `spec.mgmtOnly` / `spec.markConnected`

| | |
|---|---|
| Type | `*bool` |
| Required | no |
| Default | none in the CRD; `true`, `false`, `false` in NetBox |

Whether the interface is administratively up; whether it is out-of-band management only;
whether it counts as connected without a cable.

**Pointers, and that is the whole design.** A plain `bool` cannot tell "not managed" from
"managed as `false`", so adopting an interface a human had disabled would silently re-enable it
on the first reconcile. `nil` leaves NetBox's value alone; `false` writes `false`
([field ownership](../concepts/field-ownership.md)).

Not defaulted in the CRD either, for the same reason: a default makes the field never absent,
which is exactly the state that means "leave it alone".

### `spec.mtu`

| | |
|---|---|
| Type | `*int32` |
| Required | no |
| Validation | `1 ≤ mtu ≤ 65536` |

Bounds are NetBox's own validators, `INTERFACE_MTU_MIN` and `INTERFACE_MTU_MAX`
(`netbox/dcim/constants.py` lines 48-49).

### `spec.speed`

| | |
|---|---|
| Type | `*int64` |
| Required | no |
| Validation | `≥ 0` |

Configured speed in **kbps**: a gigabit port is `1000000`. `speed
PositiveBigIntegerField`.

An `int64` rather than an `int32` because the column is a `PositiveBigInteger`. A 400 GbE
interface is 400,000,000, which fits an `int32` — NetBox's choice of column width is the
statement not to rely on that staying true.

### `spec.duplex`

| | |
|---|---|
| Type | `string` enum |
| Required | no |
| Validation | `half`, `full`, `auto` |

From `InterfaceDuplexChoices` (`netbox/dcim/choices.py` lines 1531-1533).

### `spec.wwn`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `maxLength: 23` |

World Wide Name, an EUI-64. `wwn WWNField`.

**No pattern, deliberately.** NetBox's `WWNField` parses the value through netaddr's `EUI`
(`netbox/dcim/fields.py` lines 59-71), which accepts several separator conventions. A pattern
narrower than that would reject *at admission* a value NetBox stores happily, and an admission
rejection of a correct value is much harder to diagnose than NetBox's own `400` — which the
operator surfaces as `Reason=Invalid` carrying the server's message. The bound is the longest
spelling: eight hex octets and seven separators.

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.mode`

| | |
|---|---|
| Type | `string` enum |
| Required | no |
| Default | none |
| Validation | `access`, `tagged`, `tagged-all`, `q-in-q` |

How the interface treats 802.1Q tags. From `InterfaceModeChoices`
(`netbox/dcim/choices.py` lines 1544-1547). Declared on `dcim.BaseInterface`, so
[`NetBoxVMInterface`](../concepts/references.md) shares the type.

**Not defaulted**: the column carries no `def=`, and an unset mode is a real state in NetBox —
an interface with no VLAN semantics at all — so defaulting it would make every adopted
interface drift towards a mode nobody chose.

It carries **no tri-state note**, and that is not an omission. An enum has no empty member, so
`mode: ""` is rejected at admission and there is no way to spell "clear the mode NetBox holds"
through this field. Every choice column in the project is in the same position: the three
states of [field ownership](../concepts/field-ownership.md) are two for an enum, and the way
back to an unset mode is the NetBox UI.

### `spec.untaggedVLANRef`

| | |
|---|---|
| Type | `*VLANRef` → [`NetBoxVLAN`](netboxvlan.md) |
| Required | no |

The interface's native VLAN. `untagged_vlan (BaseInterface) ForeignKey -> ipam.VLAN
on_delete=SET_NULL`.

### `spec.taggedVLANs`

| | |
|---|---|
| Type | `[]VLANRef` → [`NetBoxVLAN`](netboxvlan.md) |
| Required | no |
| Validation | `maxItems: 256` |

The VLANs carried tagged on this interface. `tagged_vlans (BaseInterface) ManyToManyField ->
ipam.VLAN`.

Three states, like every optional field: omitting it leaves NetBox's own list alone, `[]`
clears it, and a list replaces it. The order is **not data** — NetBox does not preserve it — so
the ids are sent sorted and deduplicated and the comparison is order-independent. An unchanged
list produces zero writes however it is reordered in Git
([drift detection](../concepts/drift.md)).

**All or nothing.** If any element cannot be resolved the whole field is left out of the
payload and the object reports `RefsResolved=False` naming the element that failed. Writing
only the ones that resolved would be a full-list replacement with a shorter list — a deletion,
reported as a success.

`maxItems` is not a NetBox limit and is not decoration. `ObjectRef` carries five CEL rules and
the API server costs each one at the list's *maximum* length, so an unbounded list of refs is
refused outright with `estimated rule cost exceeds budget`. 256 is the project's standard bound
([references](../concepts/references.md), "A list needs a bound"). A trunk that needs more than
256 VLANs enumerated wants `mode: tagged-all`, which is one field instead of a list.

### `spec.lagRef` / `spec.parentRef` / `spec.bridgeRef`

| | |
|---|---|
| Type | `*InterfaceRef` → `NetBoxInterface` (this Kind) |
| Required | no |

Three **self-references**: the LAG this interface is a member of, the interface it is a
sub-interface of, and the interface it is bridged to.

| Field | NetBox column | `on_delete` |
|---|---|---|
| `lagRef` | `lag ForeignKey -> dcim.Interface` | `SET_NULL` |
| `parentRef` | `parent (BaseInterface) ForeignKey -> dcim.Interface` | `RESTRICT` |
| `bridgeRef` | `bridge (BaseInterface) ForeignKey -> dcim.Interface` | `SET_NULL` |

None of them cascades, so none is a containment parent — `RESTRICT` in particular means NetBox
*refuses* to delete an interface that still has children, so an owner reference on `parentRef`
would promise a cascade the server declines.

**Each is deferred when it does not resolve, and only then.** Two interfaces of one device
applied together cannot always both be created with the reference in place, so the field comes
out of the `POST` and goes in a follow-up `PATCH`. Conditionally rather than always: including
a reference that already resolves is one write, where unconditional deferral would turn every
ordinary sub-interface and every ordinary LAG member into two writes with a visible
intermediate state where it is briefly top-level or unbonded (NBO-015).

The three are independent. A sub-interface of a bonded parent defers `parent` and includes
`lag`, or the reverse, according to what has actually been created; `status.deferredPending`
names whichever are outstanding.

Deferral is *legal* here because none of the three is matched on by the natural key —
`(device, name)` is — so a deferred LAG cannot make the operator adopt the wrong row.

A pair that genuinely cannot be ordered (`a.parent = b`, `b.parent = a`) is a **cycle**:
`RefsResolved=False`, `Reason=RefCycle`, and **nothing is written** at all, on either object.
The walk is shared and covers all three fields together (NBO-016).

**What stays server-side.** NetBox's `clean()` refuses an interface that is its own
parent/LAG/bridge, and refuses a LAG member on a different device than its LAG unless the two
share a virtual chassis (`netbox/dcim/models/device_components.py`, `Interface.clean`). Neither
is reimplemented here: the operator knows nothing about virtual chassis and should not pretend
to. Both come back as `Ready=False`, `Reason=Invalid` with NetBox's own message, and a long
backoff.

### `spec.qinqSVLANRef`

| | |
|---|---|
| Type | `*VLANRef` → [`NetBoxVLAN`](netboxvlan.md) |
| Required | no |

The service VLAN of an 802.1ad double-tagged interface. `qinq_svlan (BaseInterface) ForeignKey
-> ipam.VLAN on_delete=SET_NULL`.

Deferred when it does not resolve, like `lagRef`, and for the neighbouring reason: a Q-in-Q
service VLAN is frequently created in the same apply as the interfaces that carry it, and
NetBox cross-validates it against `mode: q-in-q` — so a create that carried an unresolved
reference would fail where one that waits succeeds.

### `spec.vrfRef`

| | |
|---|---|
| Type | `*VRFRef` → [`NetBoxVRF`](netboxvrf.md) |
| Required | no |

The VRF the interface's addresses live in. `vrf ForeignKey -> ipam.VRF on_delete=SET_NULL`.
Declared on `dcim.Interface` itself, which is the one column here that is — `NetBoxVMInterface`
gets its `vrf` from the same place and `dcim.BaseInterface` has none.

### `spec.vlanTranslationPolicyRef`

| | |
|---|---|
| Type | `*VLANTranslationPolicyRef` → [`NetBoxVLANTranslationPolicy`](netboxvlantranslationpolicy.md) |
| Required | no |

The table of VLAN ID rewrites applied to this interface.
`vlan_translation_policy (BaseInterface) ForeignKey -> ipam.VLANTranslationPolicy
on_delete=PROTECT` (`docs/netbox-schema.md` → `dcim.Interface`).

Inherited from `dcim.BaseInterface`, so it is the same column
[`NetBoxVMInterface`](netboxvminterface.md#specvlantranslationpolicyref) carries: one policy can
be shared by a physical interface and a VM interface at once.

**Not deferred**, unlike `qinqSVLANRef`. A policy is a standalone object with no dependency on
the interface pointing at it, so there is no ordering problem to solve and NetBox
cross-validates nothing about it — a create can carry the reference.

`PROTECT`, so this is not a containment parent and never could be
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). Pointing at a policy is what
stops it being deleted, reported on the *policy* as `Deleting=False, Reason=Protected`.

The target has no `slug` column, so `slug` mode matches nothing and reports `NotFound`.

### `spec.rfRole` / `spec.rfChannel` / `spec.rfChannelFrequency` / `spec.rfChannelWidth` / `spec.txPower`

The wireless group. All optional, all unset on a wired interface.

| Field | Type | Validation | Source |
|---|---|---|---|
| `rfRole` | enum | `ap`, `station` | `netbox/wireless/choices.py` lines 6-7 |
| `rfChannel` | enum | one of 197 values | `netbox/wireless/choices.py` lines 34-236 |
| `rfChannelFrequency` | `string` | `^$\|^[0-9]{1,5}(\.[0-9]{1,3})?$` | `rf_channel_frequency DecimalField decimal(8,3)` |
| `rfChannelWidth` | `string` | `^$\|^[0-9]{1,4}(\.[0-9]{1,3})?$` | `rf_channel_width DecimalField decimal(7,3)` |
| `txPower` | `*int32` | `-40 ≤ x ≤ 127` | `netbox/dcim/models/device_components.py` lines 916-924 |

`rfChannel`'s values encode band, channel number, centre frequency and width:
`2.4g-1-2412-22` is channel 1 at 2412 MHz, 22 MHz wide. Not a name anybody guesses, which is
why it is an enum rather than free text — a wrong value is an admission rejection instead of a
`400` from NetBox.

The two frequencies are strings for the reason
[`NetBoxDevice`'s coordinates](netboxdevice.md#speclatitude--speclongitude) are: NetBox returns
a `DecimalField` as a string, and an OpenAPI `number` round-trips through IEEE-754. They are
cleared as `null`, which is what a nullable `DecimalField` takes. NetBox derives both from
`rfChannel` when that is set, so setting all three and disagreeing is NetBox's `clean()` to
resolve, not the operator's.

`txPower` is a pointer so that `0` dBm — 1 mW, a perfectly ordinary setting — is
distinguishable from unset.

### `spec.poeMode` / `spec.poeType`

| Field | Validation | Source |
|---|---|---|
| `poeMode` | `pd`, `pse` | `netbox/dcim/choices.py` lines 1559-1560 |
| `poeType` | `type1-ieee802.3af`, `type2-ieee802.3at`, `type3-ieee802.3bt`, `type4-ieee802.3bt`, `passive-24v-2pair`, `passive-24v-4pair`, `passive-48v-2pair`, `passive-48v-4pair` | `netbox/dcim/choices.py` lines 1570-1578 |

`pd` is a powered device, `pse` is power sourcing equipment. Note the **dots** in `poeType`:
`type1-ieee802.3af` is the value NetBox stores, not a typo for a dash.

### `spec.description`

`maxLength: 200`, inherited from `dcim.ComponentModel`. Omit it to leave NetBox's own value
alone; set it to `""` to clear it.

### What is deliberately absent

| Column | Why | Arrives with |
|---|---|---|
| `module` | `dcim.Module` has no Kind | NBO-053 |
| `vdcs` | `dcim.VirtualDeviceContext` has no Kind. The digest marks the M2M `REQ`, which is an artefact of how a `ManyToManyField` renders — an empty set satisfies it | NBO-060 audits it |
| `wireless_link`, `wireless_lans` | `wireless.WirelessLink` and `wireless.WirelessLAN` have no Kinds | NBO-050 |
| a MAC address of any kind | NetBox 4.2 moved the MAC to `dcim.MACAddress` behind a generic FK. This model's own entry lists `mac_addresses GenericRelation` — a reverse relation, never a column — and `dcim.BaseInterface` carries only `primary_mac_address -> dcim.MACAddress` | NBO-048 |
| `cable`, `cable_end`, `cable_connector`, `cable_positions` | writable columns that belong to another Kind: a cable is created from its own endpoints, not by an interface claiming one. **Read-only here**, so an interface that adopts a cabled peer does not `PATCH` the cable away | NBO-049 |
| `tags` | written by the engine as the provenance stamp | NBO-055 |

NetBox drops a field name it does not know rather than rejecting it, so every one of these,
offered and ignored, would report success while writing nothing.

## Natural keys

One candidate:

| # | Filters | From |
|---|---|---|
| 1 | `device_id=<id>&name=<name>` | `UniqueConstraint(fields=('device', 'name'), name='…_unique_device_name')` on **`dcim.ComponentModel`** |

`dcim.Interface` declares **no `meta.constraints` of its own**. Its only table-level line is
`meta.ordering: ('device', CollateAsChar('_name'))` (`docs/netbox-schema.md` →
`dcim.Interface`). The identity comes from the parent model, which is where every component —
console ports, power ports, front ports — gets the same one.

Two things follow.

**`device_id` is never omitted.** The pair is unique per device, so `?name=eth0` alone matches
every `eth0` in NetBox. Until `deviceRef` resolves there is no applicable candidate and the
engine waits: `Ready=False`, `Reason=WaitingForKey`, zero lookups.

**The name filter is exact.** There is no `Lower()` in that constraint, unlike *both* of
`dcim.Device`'s, so `Eth0` and `eth0` are two interfaces on one device and the operator must
not merge them. The parent uses `?name__ie=` and the child uses `?name=`, and the difference
is a real difference in the database rather than an inconsistency.

There is no second candidate, and there is nothing to fall back to: both halves are required
fields, so there is no state in which one is missing and a different identity applies.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status). Nothing is cleared on failure;
`status.id` in particular survives.

`status.provenance` is stamped in full: `dcim.ComponentModel` is a `NetBoxModel`, which mixes
in both `TagsMixin` and `CustomFieldsMixin` ([provenance](../operations/provenance.md)).

`status.deferredPending` is the field to read on this kind. It lists any of `lagRef`,
`parentRef`, `bridgeRef` and `qinqSVLANRef` whose follow-up `PATCH` has not happened.

`status.naturalKey` records the lookup that ran, so
`{"device_id": "412", "name": "eth0"}` is the whole of it — and the absence of `__ie` on the
name is the case-sensitivity, visible in the object.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the interface exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded`, `RefKindUnavailable` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | the device owns this CR | it cannot, or it was declined | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals are shared; see
[errors and retries](../concepts/errors-and-retries.md). The five that mean something
particular here:

- **`WaitingForKey`** on `Ready`: `deviceRef` has not resolved. **Zero lookups and zero
  writes** — the correct outcome, not a stall.
- **`DeferredFieldPending`** on `Ready`: a self-reference or the service VLAN has not been
  written yet. Expected and transient while a batch of sibling interfaces converges.
- **`RefCycle`** on `RefsResolved`: two interfaces name each other through `parentRef`,
  `lagRef` or `bridgeRef`. **Nothing is written**, and only a spec change retries it — no order
  of reconciles resolves a cycle.
- **`Invalid`** on `Ready`: NetBox's `clean()` refused the payload — a self-parent, or a LAG on
  another device. The message is NetBox's own, verbatim, and the backoff is long.
- **`CascadeUnavailable`** on `ParentOwned`: the device is in another namespace, or referenced
  by `slug`/`lookup`/`id`. An owner reference cannot cross a namespace, so deleting the device
  CR will not remove this one — NetBox will still delete the row, because the column is
  `CASCADE`.

## Kind-specific behaviour

### The largest spec in the catalogue, and no engine code

Twenty-eight spec fields, three self-references, seven choice columns, two decimals-as-strings,
one many-to-many and twenty read-only columns. Every one of them is data on a `Descriptor`
(`internal/registry/dcim_interface.go`); the controller file is one line plus its RBAC, and
nothing under `internal/reconciler` changed to ship this Kind. That is the claim the whole
registry design exists to make, and this is the hardest test of it so far
([the Descriptor](../concepts/descriptor.md)).

### `_name` and every reverse relation are excluded from the payload and the diff

Twenty columns are `ReadOnly`, in three groups:

- **Caches.** `_name` is a `NaturalOrderingField` NetBox derives from `name` so that `eth10`
  sorts after `eth9`. `_site`, `_location` and `_rack` are denormalised from the device
  (`dcim.ComponentModel`). `_path` is the cable path NetBox recomputes from the cable graph
  (`dcim.PathEndpoint`).
- **The cable.** `cable`, `cable_end`, `cable_connector`, `cable_positions` and
  `cable_terminations` — NetBoxCable's, not this Kind's.
- **Reverse relations.** `ip_addresses`, `mac_addresses`, `fhrp_group_assignments`,
  `tunnel_terminations`, `l2vpn_terminations` and `inventory_items` are `GenericRelation`s: the
  far end of somebody else's generic foreign key, which is to say a query rather than a column.

NetBox ignores every one of them on write, so a field-map entry for any would be a `PATCH` the
operator repeats forever.

`ip_addresses` is worth naming twice, because the direction is the point: **an address points
at an interface; an interface does not list its addresses.** That is why
`NetBoxIPAddress.assignedObject` exists and there is no `addresses` field here.

### The reference direction, in full

```
NetBoxDevice  <--deviceRef--  NetBoxInterface  <--assignedObject.interfaceRef--  NetBoxIPAddress
      ^                                                                                |
      +---------------------------- primaryIP4Ref (deferred) ---------------------------+
```

Applied in any order, this converges: the device is created first if it can be, the interface
waits for it, the address waits for the interface, and the device's `primaryIP4Ref` is
`PATCH`ed last because it is always deferred. Applied in reverse order it converges
identically — each object waits on the one it names and re-enqueues when that object's
`status.id` appears ([ref watches](../operations/stuck-references.md)).

### Deleting the device deletes this CR

Same namespace, `dcim.Interface.device` is `CASCADE`, so the device gets a **non-controller**
owner reference on every interface it owns. `kubectl delete nbdev rtmrpi0001` garbage-collects
the interface CRs, whose finalizers then find NetBox has already deleted the rows.

Non-controller and not controller, deliberately: an interface the operator *materialises* from
a device's [`spec.interfaces`](netboxdevice.md#specinterfaces) carries a **controller** owner
reference instead (NBO-034), and two controllers on one object is the one thing Kubernetes will
not allow. A hand-written interface keeps the non-controller one, so the two coexist on one
device — and the difference is exactly what pruning reads: a materialised interface whose inline
entry is removed is deleted, a hand-written one never is.

Set `netbox.kubeforge.org/parent-ownership: "false"` on the CR to opt out.

### Renaming changes identity

`spec.name` and `spec.deviceRef` are the natural key. Once `status.id` is set the key is not
consulted again, so an edit `PATCH`es the existing interface. Editing either *before* the first
successful write changes which interface the CR is about.

## Printer columns

```console
$ kubectl get nbif
NAME                   DEVICE       TYPE              ENABLED   ID    READY   AGE
rtmrpi0001-eth0        rtmrpi0001   1000base-t        true      901   True    7m
rtmrpi0001-bond0       rtmrpi0001   lag               true      902   True    7m
rtmrpi0001-eth0-101    rtmrpi0001   virtual           true      903   True    7m
sw1-ge-0-0-0           sw1          1000base-t        true      904   True    7m
sw1-ae0                sw1          lag                              False   1m
```

| Column | JSONPath |
|---|---|
| `DEVICE` | `.spec.deviceRef.name` |
| `TYPE` | `.spec.type` |
| `ENABLED` | `.spec.enabled` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`DEVICE` reads the spec rather than the status because it is half the identity, and a hundred
interfaces named `eth0` are only distinguishable by it. `ENABLED` is blank when the field is
omitted, which is the "not managed" state and worth seeing as blank rather than as `false`.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, `spec.deviceRef`/`name`/`type` required | admission, nothing stored | one of the three was omitted | All three are `REQ` in NetBox |
| `kubectl apply` rejected, `spec.type` not in enum | admission, **no controller involved** | a typo, or a NetBox 4.7 type against a 4.6.8 CRD | `kubectl explain netboxinterface.spec.type` lists the 207 accepted values |
| `kubectl apply` rejected, `spec.poeType` | admission | a dash where NetBox has a dot | `type1-ieee802.3af`, not `type1-ieee802-3af` |
| `kubectl apply` rejected, `spec.mtu` | admission | outside 1–65536 | NetBox's own validators |
| `kubectl apply` rejected, `spec.txPower` | admission | outside −40–127 | NetBox's own validators |
| `kubectl apply` rejected, `spec.taggedVLANs` too long | admission | over 256 items | Use `mode: tagged-all`. The bound exists because CEL cost is charged at the maximum |
| `kubectl apply` rejected, `metadata.name` | admission | a slash copied from `spec.name` (`ge-0/0/0`) | `metadata.name` is a DNS-1123 label; `spec.name` keeps the literal string |
| `Ready=False`, `Reason=WaitingForKey` | reconcile, **zero lookups and zero writes** | `deviceRef` has not resolved | Apply the device. The interface re-enqueues on its own |
| `Ready=False`, `Reason=DeferredFieldPending` | reconcile | `lagRef`, `parentRef`, `bridgeRef` or `qinqSVLANRef` has not been PATCHed yet | Expected while sibling interfaces converge. `status.deferredPending` names which |
| `RefsResolved=False`, `Reason=RefCycle` | reconcile, **zero writes on both objects** | two interfaces name each other through `parentRef`, `lagRef` or `bridgeRef` | Break the cycle in the manifests. Only a spec change retries it |
| `Ready=False`, `Reason=Invalid`, message mentions the LAG's device | reconcile, long backoff | the LAG is on another device and the two share no virtual chassis | NetBox's rule, not the operator's. Point `lagRef` at a LAG on this device |
| `Ready=False`, `Reason=Invalid`, message mentions the interface itself | reconcile, long backoff | `parentRef`, `lagRef` or `bridgeRef` points at this same interface | NetBox's `clean()` refuses a self-reference |
| `Ready=False`, `Reason=Invalid`, message mentions `qinq_svlan` and `mode` | reconcile | a service VLAN without `mode: q-in-q` | Set the mode, or drop the reference |
| Two interfaces where one was expected | none | `Eth0` and `eth0` on one device | Correct: the constraint has no `Lower()`, so they are two objects. Unlike a device's name |
| `taggedVLANs` was reordered and nothing happened | none | M2M order is not data | Expected. The comparison is order-independent and the ids are sent sorted |
| One VLAN in `taggedVLANs` does not exist and **none** was written | `RefsResolved=False` | all-or-nothing | Apply the missing VLAN. A partial write would be a full-list replacement with a shorter list — a deletion |
| The interface exists but has no cable | none | the cable columns are read-only here | Expected. `NetBoxCable` is NBO-049 |
| `ParentOwned=False`, `Reason=CascadeUnavailable` | reconcile | the device is in another namespace, or referenced by `slug`/`lookup`/`id` | Expected. NetBox still deletes the row on `CASCADE`; only the CR is left behind |
| Deleting the device deleted this CR | none | the device is the containment parent, and `dcim.Interface.device` is `CASCADE` | Expected. Opt out per object with `netbox.kubeforge.org/parent-ownership: "false"` |
| Terminating forever, `Deleting` `Reason=Protected` | finalizer | an IP address, cable termination or child interface still references this one | Delete the blocker; the interface converges on its own. Or `deletionPolicy: Retain` |
| `NetBoxIPAddress` with `assignedObject.vmInterfaceRef` reports `RefKindUnavailable` | reconcile | that member's Kind is NBO-029, which is not merged | Expected. This Kind closes only the `interfaceRef` member. See [the box at the top](#this-is-the-kind-ipassignmentinterfaceref-was-waiting-for) |

## Related

- [`NetBoxDevice`](netboxdevice.md) — the parent, the containment owner, and the kind whose own
  parent does *not* cascade
- [`NetBoxVLAN`](netboxvlan.md) — what `untaggedVLANRef`, `taggedVLANs` and `qinqSVLANRef`
  point at
- [`NetBoxVRF`](netboxvrf.md) — what `vrfRef` points at
- [Generic references](genericref.md) — the `IPAssignment` union this Kind is a
  member target of
- [Generic references (concept)](../concepts/generic-refs.md) — why the object-type spelling is
  written down exactly once
- [References](../concepts/references.md) — the four ref modes, deferral, cycles, and why a
  list needs a bound
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set, and why the
  booleans are pointers
- [Drift detection](../concepts/drift.md) — how the many-to-many and the decimals are compared
- [Ownership](../concepts/ownership.md) — the non-controller owner reference and the namespace
  rule
- [The Descriptor](../concepts/descriptor.md) — why twenty-eight fields took no engine code
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — rule 4
  and the cascade rule
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
