# `NetBoxDevice`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxDevice` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbdev` |
| Status subresource | yes |

A `NetBoxDevice` is one `dcim.Device` in NetBox: a named piece of hardware, of a type, in a
role, at a site.

> ### This kind has no containment parent, and that is deliberate
>
> Every other kind with a required reference gets an owner reference from it, so deleting the
> parent CR cascades. This one does not, and it is the first kind where the rule
> ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4, as amended by
> [#202](https://github.com/ricardomolendijk/netbox-operator/pull/202)) produces *no* parent.
>
> The rule is: the containment parent is whichever foreign key **the server cascades**. Every
> foreign key on `dcim.Device` is `PROTECT` or `SET_NULL`
> (`docs/netbox-schema.md` → `dcim.Device`):
>
> | Column | `on_delete` | Cascades? |
> |---|---|---|
> | `device_type` | `PROTECT` | no |
> | `role` | `PROTECT` | no |
> | `site` | `PROTECT` | no |
> | `tenant` | `PROTECT` | no |
> | `platform` | `SET_NULL` | no |
> | `cluster` | `SET_NULL` | no |
> | `primary_ip4`, `primary_ip6`, `oob_ip` | `SET_NULL` | no |
>
> So `kubectl delete netboxsite home` does **not** take its devices with it, and there is no
> `ParentOwned` condition on a `NetBoxDevice` at all. That is not a gap: NetBox refuses that
> deletion anyway, because `PROTECT` is what it means. An owner reference here would promise a
> cascade the server declines — garbage collection would remove the CR, the finalizer's
> `DELETE` would be refused, and the row would outlive the object that described it. The
> registry refuses such a descriptor at boot (`registry.ErrContainmentNotCascade`).
>
> `spec.clusterRef` is the reference that *looks* like a parent and is not, for the same
> reason from the other direction: `SET_NULL` leaves the device row alive with the column
> cleared. See [ownership](../concepts/ownership.md).
>
> The cascade that does exist runs the other way: deleting a `NetBoxDevice` takes its
> [`NetBoxInterface`s](netboxinterface.md) with it, because `dcim.Interface.device` **is**
> `CASCADE`.
>
> An interface the device *materialised* from [`spec.interfaces`](#specinterfaces) takes a
> **controller** owner reference instead of that containment one, and its addresses take one
> too — so `kubectl delete nbdev` removes a whole inline tree. That is not an exception to the
> rule above but the other half of it: the containment owner reference mirrors a foreign key
> NetBox cascades, and a controller owner reference records that this operator *created* the
> object. A device with no parent of its own is still a parent.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDevice
metadata:
  name: sw1
  namespace: default
spec:
  endpointRef: homelab
  name: sw1
  deviceTypeRef:
    slug: ex2200-48t
  roleRef:
    slug: access-switch
  siteRef:
    name: home
```

Four required fields and nothing else. `deviceTypeRef` and `roleRef` are in `slug` mode
because `NetBoxDeviceType` and `NetBoxDeviceRole` are NBO-027 — see
[what is deliberately absent](#what-is-deliberately-absent).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxDevice
metadata:
  name: rtmrpi0001
  namespace: default
spec:
  endpointRef: homelab
  deletionPolicy: Delete      # the envelope default, written out
  onConflict: Adopt           # the envelope default, written out

  name: rtmrpi0001

  deviceTypeRef:
    slug: raspberry-pi-4b
  roleRef:
    slug: server
  siteRef:
    name: home

  tenantRef:
    name: donkerslootstraat
  platformRef:
    slug: debian-12
  clusterRef:
    name: homelab-k3s        # a real Kind, and still no owner reference

  assetTag: RTM-0001
  serial: 10000000abcdef01

  status: active             # NetBox's own default, written out
  airflow: passive           # no default in NetBox

  latitude: "51.9244"
  longitude: "4.4777"

  primaryIP4Ref:             # all three are deferred, always
    name: rtmrpi0001-eth0-ip-10-0-20-10-24   # the inline address child, by its derived name
  primaryIP6Ref:
    name: rtmrpi0001-eth0-v6
  oobIPRef:
    name: rtmrpi0001-oob

  customFields:
    audit_ticket: OPS-4417

  description: Raspberry Pi 4B, house RTM
  comments: Managed by netbox-operator.

  interfaces:                # sugar: materialises NetBoxInterface + NetBoxIPAddress CRs
    - name: bond0
      type: lag
    - name: eth0
      type: 10gbase-t
      lag: bond0             # a sibling key, not a reference
      label: Port 1
      enabled: true
      mgmtOnly: false
      mtu: 9000
      speed: 1000000
      duplex: full
      mode: tagged
      poeMode: pd
      poeType: type2-ieee802.3at
      untaggedVLANRef:
        name: vlan-mgmt
      taggedVLANs:
        - name: vlan-guest
      vrfRef:
        name: vrf-home
      description: Uplink
      addresses:
        - address: 10.0.20.10/24
          status: active     # NetBox's own default, written out
          dnsName: rtmrpi0001.home.arpa
          vrfRef:
            name: vrf-home
```

`primaryIP4Ref` names the address child the inline `eth0` entry materialised, by its derived
name. That is the whole of how a device's primary address is set today: there is no `primary:
true` flag on an inline address of a *device*, and
[`spec.interfaces[].addresses`](#specinterfacesaddresses) says why. A
[`NetBoxVirtualMachine`](netboxvirtualmachine.md#primary-and-the-ring-it-closes) has one, and
what it uses is a general mechanism this Kind could adopt unchanged.

## `spec`

Envelope fields — `endpointRef`, `onConflict`, `deletionPolicy`, `customFields` — behave as on
every object kind. See [`NetBoxTag`](netboxtag.md#spec).

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Default | none |
| Validation | `minLength: 1`, `maxLength: 64` |

The device's name.

**Required here although NetBox's column is nullable.** `dcim.Device.name` is
`CharField len=64` with `null=True` (`docs/netbox-schema.md` → `dcim.Device`;
`netbox/dcim/models/devices.py` lines 45-50), so NetBox holds unnamed devices — blade chassis
members, unracked spares. An unnamed device has no natural key: every constraint on the model
is over `Lower('name')` or over a rack position, so it could not be looked up, adopted, or
reconciled idempotently, and an operator that lost `status.id` would create a second one on
the next pass. This is a deliberate, documented narrowing of NetBox's model. Manage an unnamed
device in the NetBox UI.

Matched **case-insensitively** — see [natural keys](#natural-keys).

**If it is wrong.** Absent or empty: rejected at admission, nothing is stored. Over 64
characters: rejected at admission. A name another device at this site already holds: the
lookup finds it and the device is *adopted* (`status.adopted: true`) rather than duplicated,
unless `onConflict: Fail`, which reports `Ready=False`, `Reason=Conflict`.

### `spec.deviceTypeRef`

| | |
|---|---|
| Type | `DeviceTypeRef` → [`NetBoxDeviceType`](../concepts/references.md) |
| Required | **yes** |
| Default | none |
| Validation | `ObjectRef`'s five CEL rules |

The model of hardware this device is. `device_type ForeignKey REQ -> dcim.DeviceType
on_delete=PROTECT`.

Not part of the identity, and not a containment parent: `PROTECT` means NetBox refuses to
delete a device type that still has devices.

**If it is wrong.** Malformed reference: rejected at admission. `name` mode today:
`RefsResolved=False`, `Reason=RefKindUnavailable` — `NetBoxDeviceType` is NBO-027, so use
`slug`, `lookup` or `id`, which resolve against NetBox directly. Unresolvable:
`Ready=False`, `Reason=WaitingForRef`, and **nothing is written** — a declared reference is a
precondition for the write.

### `spec.roleRef`

| | |
|---|---|
| Type | `DeviceRoleRef` → `NetBoxDeviceRole` |
| Required | **yes** |
| Default | none |

The functional role the device plays. `role ForeignKey REQ -> dcim.DeviceRole
on_delete=PROTECT`.

**`dcim.DeviceRole`, not `ipam.Role`.** They are separate models with separate endpoints
(`dcim/device-roles` against `ipam/roles`), which is why the field is typed `DeviceRoleRef`
and not the `RoleRef` a prefix or a VLAN carries. Pointing one at the other resolves against
the wrong CRs and writes an id that means something else.

**If it is wrong.** As `deviceTypeRef`.

### `spec.siteRef`

| | |
|---|---|
| Type | `SiteRef` → [`NetBoxSite`](netboxsite.md) |
| Required | **yes** |
| Default | none |

The site the device is installed at. `site ForeignKey REQ -> dcim.Site on_delete=PROTECT`.

**Half the identity.** Device names are unique *per site*, not globally, so `site_id` is never
omitted from a lookup — see [natural keys](#natural-keys). It is **not** the containment
parent; see [the box at the top](#this-kind-has-no-containment-parent-and-that-is-deliberate).

**If it is wrong.** Unresolvable: `Ready=False`, `Reason=WaitingForRef`, and **zero lookups as
well as zero writes** — without `site_id` the operator cannot tell whether this device exists,
and looking `sw1` up across every site would adopt the wrong one.

### `spec.tenantRef`

| | |
|---|---|
| Type | `*TenantRef` → [`NetBoxTenant`](netboxtenant.md) |
| Required | no |
| Default | none |

The tenant the device belongs to. `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`.

**Part of the identity when it is set.** `(Lower('name'), 'site', 'tenant')` is a constraint in
its own right, so two tenants sharing a site may each have an `sw1`.

**If it is wrong.** Declared and unresolved: `Ready=False`, `Reason=WaitingForRef`, nothing
written and **no lookup performed** — candidate 2 needs the tenant resolved and candidate 3
asserts it was never declared, so neither applies and the device waits rather than adopting
the tenant-less device of the same name. Do **not** remove `tenantRef` to unblock it: that
changes the object's identity.

### `spec.platformRef`

| | |
|---|---|
| Type | `*PlatformRef` → `NetBoxPlatform` |
| Required | no |

The operating system running on the device. `platform ForeignKey -> dcim.Platform
on_delete=SET_NULL`. `NetBoxPlatform` is NBO-027; `slug`, `lookup` and `id` work today.

### `spec.clusterRef`

| | |
|---|---|
| Type | `*ClusterRef` → [`NetBoxCluster`](netboxcluster.md) |
| Required | no |

The virtualization cluster this device is a host in. `cluster ForeignKey ->
virtualization.Cluster on_delete=SET_NULL`.

A containment-*shaped* reference that adds **no owner reference**. `SET_NULL` leaves the device
row alive with the column cleared, so an owner reference would garbage-collect a CR whose
NetBox object still exists. Two containment owners would have been wrong anyway: Kubernetes
garbage collection waits for *every* owner, so "delete the site **or** the cluster and the
device goes" is unrepresentable — it would become "delete both", silently.

### `spec.assetTag`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Default | none |
| Validation | `maxLength: 50` |

The tag used to identify this device, and the **only globally unique column on the model**:
`asset_tag CharField UNIQUE len=50` (`netbox/dcim/models/devices.py` lines 555-562,
`unique=True, null=True`).

That makes it the strongest natural key this kind has and **the first one tried**. It is also
the one field whose collision is cluster-wide rather than site-local: two CRs in two
namespaces claiming one asset tag are one device, and the loser reports `Conflict` naming the
winner — the same shape as [`NetBoxSite`](netboxsite.md)'s slug.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. Cleared as `null` and
not as `""`, because the column is unique *and* nullable: two devices with an empty-string tag
would collide where two with `null` do not (`registry.Field.EmptyIsNull`).

**If it is wrong.** Over 50 characters: rejected at admission. Held by another device: the
lookup finds that device and adopts it, which is usually what an asset tag is for. Two CRs
claiming it: one Ready, one `Ready=False`, `Reason=Conflict`.

### `spec.serial`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `maxLength: 50` |

Chassis serial number, assigned by the manufacturer. Not unique and not part of the identity.
Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.status`

| | |
|---|---|
| Type | `string` enum |
| Required | no |
| Default | `active` |
| Validation | `offline`, `active`, `planned`, `staged`, `failed`, `inventory`, `decommissioning` |

The device's lifecycle state. Values from `DeviceStatusChoices`
(`netbox/dcim/choices.py` lines 192-198, NetBox 4.6.8) — the digest records the choice *class*
and not its members.

Defaulted to NetBox's own default, so the operator manages the column from the first
reconcile: a defaulted field that never reaches a payload is a field the operator can never
correct.

**If it is wrong.** An unknown value is rejected at admission. There is no way to spell "clear
the status" — an enum has no empty member, and this one is defaulted besides.

### `spec.airflow`

| | |
|---|---|
| Type | `string` enum |
| Required | no |
| Default | none |
| Validation | `front-to-rear`, `rear-to-front`, `left-to-right`, `right-to-left`, `side-to-rear`, `rear-to-side`, `bottom-to-top`, `top-to-bottom`, `passive`, `mixed` |

Which way air moves through the chassis. Values from `DeviceAirflowChoices`
(`netbox/dcim/choices.py` lines 214-223).

**Not defaulted**, unlike `status`: the column carries no `def=`, and an unset airflow is a
real state in NetBox — a device type may declare one the device inherits — so defaulting it
would make every adopted device drift towards a value nobody chose.

### `spec.latitude` / `spec.longitude`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `latitude`: `^$\|^-?[0-9]{1,2}(\.[0-9]{1,6})?$` and `-90 ≤ x ≤ 90`; `longitude`: `^$\|^-?[0-9]{1,3}(\.[0-9]{1,6})?$` and `-180 ≤ x ≤ 180` |

GPS coordinates in decimal degrees, as strings.

Strings and not numbers, for the reason [`NetBoxSite`](netboxsite.md#latitude-longitude)
gives: NetBox stores them as `DecimalField`s and returns them as strings, and an OpenAPI
`number` round-trips through IEEE-754 on the way in and out of the API server — the sixth
decimal place is roughly a tenth of a metre. Compared numerically, so `"51.9244"` and NetBox's
`"51.924400"` produce no `PATCH`.

Digit counts off `latitude DecimalField decimal(8,6)` and `longitude DecimalField decimal(9,6)`
(`docs/netbox-schema.md` → `dcim.Device`). Omit either to leave NetBox's value alone; set it to
`""` to clear it, which is written as `null` — the empty string is not a number and DRF
rejects it.

### `spec.primaryIP4Ref` / `spec.primaryIP6Ref` / `spec.oobIPRef`

| | |
|---|---|
| Type | `*IPAddressRef` → `NetBoxIPAddress` |
| Required | no |
| Default | none |

The device's primary IPv4, primary IPv6 and out-of-band addresses. All three are
`OneToOneField -> ipam.IPAddress on_delete=SET_NULL`.

**All three are always deferred.** The ring is:

```
NetBoxDevice.primaryIP4Ref -> NetBoxIPAddress.assignedObject.interfaceRef
                           -> NetBoxInterface.deviceRef -> NetBoxDevice
```

No apply order satisfies it. So the three columns are stripped from the `POST` and applied by
a follow-up `PATCH` — `DeferAlways`, not `DeferIfUnresolved`, because there is no first pass in
which any of them could resolve and `IfUnresolved` would spend a reconcile finding that out
every time ([deferred fields](../concepts/references.md), NBO-015).

Between the two writes the device reports `Ready=False`, `Reason=DeferredFieldPending`, and
`status.deferredPending` names the fields. That is a legitimate, possibly long-lived state,
which is why it is a status field and not only a condition message.

Deferral is *legal* here because none of the three columns is matched on by any natural-key
candidate: stripping them from the create cannot change the identity the lookup decided on.
The registry enforces that (`validateDeferred`).

`NetBoxIPAddress` ships
([#199](https://github.com/ricardomolendijk/netbox-operator/pull/199)), so all four ref modes
resolve: `name` against a sibling CR, and `slug`/`lookup`/`id` against NetBox.

### `spec.description` / `spec.comments`

`description` is `maxLength: 200`; `comments` is a `TextField` with no cap. Both inherited from
`PrimaryModel`. Omit either to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.localContextData`

| | |
|---|---|
| Type | JSON object (`x-kubernetes-preserve-unknown-fields`) |
| Required | no |
| Default | none |
| Validation | `type: object`; the contents are NetBox's business |

This device's own slice of config context: the document NetBox merges **last** when it renders
the device's configuration, after every [`NetBoxConfigContext`](netboxconfigcontext.md) whose
selectors matched (`docs/netbox-schema.md` → `dcim.Device`,
`local_context_data (ConfigContextModel) JSONField`; `netbox/extras/models/configs.py`,
`ConfigContextModel.get_config_context()`).

```yaml
spec:
  localContextData:
    ntp:
      servers: ["10.0.0.1", "10.0.0.2"]
    bgp:
      asn: 64512
```

**A column on the device, not a reference to a context.** It is created, updated and deleted
with the row, so overrides that live here cannot be orphaned by somebody deleting a config
context — and being last in the merge, it wins every collision. That makes it the place for a
*single device's exception*. Shared policy belongs in a `NetBoxConfigContext`, which is
reviewed once and applies to everything it selects.

**Compared as a whole document** (`registry.ClassJSON`), not as a scalar. The scalar rule
unwraps any JSON object carrying an `id` or a `value` key, because that is how NetBox renders a
foreign key and a choice on read — so a local context that happens to carry an `id` key, which
is ordinary in inventory data, would differ from itself on every read and be PATCHed forever
([drift](../concepts/drift.md)).

**An object and not an arbitrary JSON value.** That is NetBox's rule rather than this
operator's: `ConfigContextModel.clean()` refuses a `local_context_data` that is not a mapping,
because rendering merges it into a dict. Declaring `type: object` turns that 400 into a
rejection at admission, where the message names the field.

Omit it to leave NetBox's own value alone; set it to `{}` to clear it. `{}` and not `null`,
although the column itself is nullable: the API server prunes a null under a schema that is not
marked nullable, before validation and before the operator reads the object back, so `null`
could not be told from an omitted field. An empty document merges to nothing, which is what
clearing it asks for.

### `spec.interfaces`

| | |
|---|---|
| Type | `[]InlineInterface` |
| Required | no |
| Default | none |
| Validation | `maxItems: 128`; `listType: map` keyed on `name` |

The device's interfaces, written inline and materialised as real
[`NetBoxInterface`](netboxinterface.md) CRs — with each entry's `addresses` materialised as real
[`NetBoxIPAddress`](netboxipaddress.md) CRs under them. This is
[ADR-0003 rule 5](../decisions/0003-ownership-and-references.md)'s inline sugar, and the whole of
it is documented on one page: [inline children](../concepts/inline-children.md).

**Not a NetBox column, and the only field on this spec that is not.** `dcim.Device` has no
`interfaces` column — the foreign key points the other way, `dcim.Interface.device` — so nothing
here reaches the device's own payload. Each child writes its own NetBox object; the device never
writes NetBox on a child's behalf.

**Sugar, and never required.** Every entry is equally expressible as a `NetBoxInterface` with a
`deviceRef` naming this device, and the longhand kind stays the complete one: an inline entry
offers a subset of its fields. The two coexist on one device — a hand-written `NetBoxInterface`
pointing here is never pruned, never adopted, and absent from `status.children`.

**Omitting it and writing `[]` are the same instruction**, unlike every other optional field on
this spec. There is no NetBox value to leave alone, so there is no third state: both mean
"declare no children", and both prune the children a previous spec declared.

```yaml
spec:
  interfaces:
    - name: bond0                 # the child key -> NetBoxInterface `<device>-bond0`
      type: lag
    - name: eth0
      type: 10gbase-t             # required; dcim.Interface.type is REQ
      lag: bond0                  # a SIBLING KEY, not a reference
      enabled: true
      mtu: 9000
      mode: tagged
      untaggedVLANRef: {name: vlan-mgmt}
      taggedVLANs: [{name: vlan-guest}]
      vrfRef: {name: vrf-home}
      addresses:
        - address: 10.0.20.10/24  # the child key, prefix length included
          dnsName: rtmrpi0001.home.arpa
```

`name` and `type` are the only required fields on an entry. The rest — `label`, `enabled`,
`mgmtOnly`, `mtu`, `speed`, `duplex`, `mode`, `poeMode`, `poeType`, `lag`, `parent`, `bridge`,
`untaggedVLANRef`, `taggedVLANs`, `vrfRef`, `description`, `addresses` — are optional and carry
the longhand kind's own types, so every enum, pattern and length limit is declared once.

`deviceRef` is **absent** from an entry, and so is an address's `assignedObject`: the
materialiser sets both, and a field the user cannot meaningfully set does not exist.

**An inline field has two states, not three.** Setting `description: ""` on an entry does *not*
clear NetBox's description: the child is written with server-side apply, `omitempty` drops the
empty string, no manager claims the field, and the child's own pass therefore reads it as
absent — which means "leave NetBox alone"
([field ownership](../concepts/field-ownership.md)). An inline entry can set a value and can
leave one alone; clearing one is a `NetBoxInterface`, where the field is yours and the
distinction survives.

**The bounds.** 128 interfaces, 128 tagged VLANs per interface, 16 addresses per interface —
narrower than the project's standard 256 for the two nested lists, because validation cost
multiplies through every level: the API server costs `interfaces[].taggedVLANs[]`'s five
`ObjectRef` rules at 128 × 128 = 16 384 items, not at either bound alone
([a list needs a bound](../concepts/references.md#a-list-needs-a-bound)). 128 interfaces is past
every fixed-form switch; a trunk enumerating more than 128 VLANs wants `mode: tagged-all`; an
interface with more than sixteen addresses is a `NetBoxIPAddress` each.

**If it is wrong.** Two entries with the same `name`, or two addresses with the same `address`
under one interface: rejected at admission by the map key, `Duplicate value`. An entry with no
`type`: rejected at admission. Two entries whose names differ only in case, or an entry whose
derived name is already taken by a CR the operator does not own: `ChildrenReady=False`,
`Reason=Conflict`, and **nothing is written at all** — see
[inline components](#inline-components-and-what-they-are-not).

#### `spec.interfaces[].lag` / `parent` / `bridge`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `minLength: 1`, `maxLength: 64` |

The **key of a sibling inline interface**, not a reference and not a CR name. `lag: bond0` means
the entry named `bond0` in this same list.

A key rather than an `ObjectRef` because an `ObjectRef` would make you write
`lagRef: {name: <device>-bond0}`, hardcoding the derived-name algorithm into your manifest — so a
device name long enough to take the truncate-and-hash path would silently point at a name that
does not exist. A key is stable because it is your own input.

From there it is an ordinary deferred self-reference on the child: `lag` is left out of the
child's `POST` when the sibling has no id yet and applied by a follow-up `PATCH`
(`DeferIfUnresolved`, see [`NetBoxInterface`](netboxinterface.md)). **The order the two entries
appear in the list does not matter.**

**If it is wrong.** A key that names no sibling is not silently dropped: the child is written with
a `lagRef` naming a CR that does not exist and reports `RefsResolved=False`,
`Reason=RefNotFound`, which leaves the device `ChildrenReady=False`, `Reason=PendingChildren`.
Two entries naming each other are a ring, reported as `RefCycle` on the children.

#### `spec.interfaces[].addresses`

| | |
|---|---|
| Type | `[]InlineIPAddress` |
| Required | no |
| Validation | `maxItems: 16`; `listType: map` keyed on `address` |

The addresses assigned to this interface. Four fields: `address` (required, the key, prefix
length included), `status`, `vrfRef` and `dnsName`.

The child is a `NetBoxIPAddress` whose `assignedObject.interfaceRef` names the **sibling
interface child**, so NetBox records `assigned_object_type: "dcim.interface"`. It is a child of
the *device* rather than of the interface, because a controller owner reference names the object
that created another; the interface still gets a non-controller containment owner reference on it
from its own pass, and the two coexist ([ownership](../concepts/ownership.md)).

Four fields the longhand kind has are deliberately absent here:

| Absent | Why |
|---|---|
| `primary` / `oob` | the mechanism exists as of NBO-033 -- a Kind states the references its sugar derives through `DerivedRefs()`, and they are folded into the spec the payload is built from without the materialiser ever writing a spec ([ADR-0005 §1](../decisions/0005-gitops-coexistence.md), [inline children](../concepts/inline-children.md#the-one-place-the-sugar-flows-upward)) -- and this Kind does not use it yet. `NetBoxVirtualMachine` does. Until it does, set [`spec.primaryIP4Ref`](#specprimaryip4ref--specprimaryip6ref--specoobipref) to the child's derived name -- `<device>-<interface>-ip-<slugified address>` |
| `claimFrom` | an inline address that names a pool instead of a literal materialises a `NetBoxIPAddressClaim`, which is [ADR-0004](../decisions/0004-claims-first-allocation.md)'s single allocation code path and NBO-036's ticket. `fromPrefixRef`, the spelling an older draft used, does not exist and never will |
| `allowDuplicate` | it makes the provenance stamp part of the address's identity, so a materialised child that lost `status.id` would create a **second** NetBox address rather than adopting its own. An anycast or VRRP address that needs it is written as its own `NetBoxIPAddress` |
| `tenantRef`, `role`, `natInsideRef`, `description`, `comments` | `NetBoxIPAddress` has no `tenantRef` to carry the first to, and the rest are the longhand kind's. Inline covers the common case; the standalone kind stays the complete one |

### What is deliberately absent

Each of these is a NetBox column this CRD does not offer, and each is absent rather than
accepted-and-ignored. NetBox drops a field name it does not know rather than rejecting it, so
a field that is accepted and silently discarded reports success while writing nothing.

| Column | Why | Arrives with |
|---|---|---|
| `location`, `rack`, `position`, `face` | [`NetBoxLocation`](netboxlocation.md) and [`NetBoxRack`](netboxrack.md) both ship now, so nothing external blocks these. What is left is the `('rack', 'position', 'face')` constraint, which is a three-column identity this Kind's natural key does not model — mounting a device in a rack is its own change, not a field addition | a ticket of its own |
| `virtual_chassis`, `vc_position`, `vc_priority` | `dcim.VirtualChassis` has no Kind, so the `('virtual_chassis', 'vc_position')` constraint is unreachable too | NBO-053 |
| `config_template` | rendering is its own feature, and it references a Kind this one cannot yet name. `local_context_data` was in this row until [#277](https://github.com/ricardomolendijk/netbox-operator/pull/277) and is now [`spec.localContextData`](#speclocalcontextdata): it references nothing, so nothing was blocking it | a ticket of its own |
| `tags` | written by the engine as the provenance stamp; a user-facing tag list needs `NetBoxTag` references on every kind at once | NBO-055 |
| inline `consolePorts`, `consoleServerPorts`, `powerPorts`, `powerOutlets` | the same sugar as [`spec.interfaces`](#specinterfaces), for components whose Kind does not exist yet. Declaring the field first would accept input the operator cannot honour | NBO-052 |
| inline `frontPorts`, `rearPorts`, `deviceBays`, `moduleBays`, `inventoryItems` | as above | NBO-053 |
| inline `services`, MAC addresses, cables | `ipam.Service` is NBO-055 and `NetBoxMACAddress` is NBO-048. A cable is not a component of one device — its terminations point at two, so neither could own it — and `NetBoxCable` (NBO-049) will stay a first-class kind referencing both ends | — |

## Natural keys

Three candidates, tried in order:

| # | Filters | From |
|---|---|---|
| 1 | `asset_tag=<tag>` | the **column**: `asset_tag CharField UNIQUE len=50` |
| 2 | `name__ie=<name>&site_id=<id>&tenant_id=<id>` | `UniqueConstraint(Lower('name'), 'site', 'tenant', name='…_unique_name_site_tenant')` |
| 3 | `name__ie=<name>&site_id=<id>&tenant_id__isnull=true` | `UniqueConstraint(Lower('name'), 'site', name='…_unique_name_site', condition=Q(tenant__isnull=True), violation_error_message=_('Device name must be unique per site.'))` |

`dcim.Device`'s full `meta.constraints` is four entries (`docs/netbox-schema.md` →
`dcim.Device`). The two above, plus:

- `UniqueConstraint(fields=('rack', 'position', 'face'), name='…_unique_rack_position_face')`
- `UniqueConstraint(fields=('virtual_chassis', 'vc_position'), name='…_unique_virtual_chassis_vc_position')`

Both are **unreachable**: all five columns are out of scope for this Kind, so no candidate can
be built from either. A candidate that can never be applicable is worse than none — the engine
would wait forever for an identity it cannot construct.

### Candidate 1 is not a `meta.constraints` entry

It is a *column-level* unique, which is as binding as a table-level one and is the only
single-column key `dcim.Device` has. It goes first because it is the strongest: one filter,
globally unique. If the asset tag is set and matches nothing, the engine falls through to
candidates 2 and 3 — which is the ordinary case of a device that exists in NetBox without an
asset tag and is about to be given one.

An **emptied** `assetTag` (the explicit clear) makes candidate 1 *inapplicable* rather than
matching every device without one: the engine treats the empty string as no value at all
(`filterValue`). Candidates 2 and 3 do not pin `asset_tag`, so the device stays identifiable.

### `name__ie`, not `name`

Both reachable constraints are over `Lower('name')`, so `SW1` and `sw1` at one site are the
**same device** to NetBox. An exact filter would report `sw1` absent while NetBox holds `SW1`,
and the create that followed would be answered with a `400` — a loop where the lookup and the
write disagree about what exists. `?name__ie=` is the difference between adopting the existing
device and failing forever
([lookups](../concepts/lookups.md#why-case-insensitive-lookup-exists)).

### `site_id` is never omitted

Device names are unique **per site**. A lookup without `site_id` finds the wrong device, or
several, and `sw1` is the most-reused device name there is. Ambiguity is a `Conflict` naming
the candidate ids, never a guess.

### The `tenant_id` pin is load-bearing

Candidate 3 carries the constraint's own `condition=Q(tenant__isnull=True)` as a pinned
`tenant_id__isnull=true`, not as an omitted filter. Omitted, a device whose tenant has not been
created yet would match by name and site, adopt the tenant-less device, and the follow-up
`PATCH` would move somebody else's device into this tenant. Pinned, such a device matches
nothing and the engine waits
([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

Two devices sharing a name at one site — one with a tenant, one without — are two objects to
NetBox and two to the operator: candidate 2 finds the first, candidate 3 finds the second, and
neither adopts the other.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each means and when it is
cleared. Nothing is cleared on failure: `status.id` in particular survives.

`status.provenance` is stamped in full: `dcim.Device` is a `PrimaryModel`, so it mixes in both
`TagsMixin` and `CustomFieldsMixin` ([provenance](../operations/provenance.md)).

`status.deferredPending` is the field to read on this kind. It lists `primaryIP4Ref`,
`primaryIP6Ref` and `oobIPRef` while their follow-up `PATCH` has not happened.

`status.children` is the other one, and it is populated only on a device that declares
[`spec.interfaces`](#specinterfaces): one entry per materialised child, with the key-based spec
path that declared it, its Kind, its derived name and its own readiness.

```yaml
status:
  children:
    - path: spec.interfaces[eth0]
      kind: NetBoxInterface
      name: rtmrpi0001-eth0
      ready: true
    - path: spec.interfaces[eth0].addresses[10.0.20.10/24]
      kind: NetBoxIPAddress
      name: rtmrpi0001-eth0-ip-10-0-20-10-24
      ready: true
```

It lists the **inline** set and not "every child of this device": a hand-written
`NetBoxInterface` pointing at this device never appears here. It is also what the device's own
finalizer reads while deleting, so it is not cleared on failure either.

`status.naturalKey` records which candidate ran, filter by filter, so
`{"name__ie": "sw1", "site_id": "12", "tenant_id__isnull": "true"}` tells you the engine
treated the object as a tenant-less device at site 12 — and `{"asset_tag": "RTM-0001"}` tells
you it never needed the name at all.

There is **no `ParentOwned` condition** on this kind, because there is no containment parent to
report on. See [the box at the top](#this-kind-has-no-containment-parent-and-that-is-deliberate).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the device exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded`, `RefKindUnavailable` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | **never set on this kind** | — | — |
| `ChildrenReady` | every child [`spec.interfaces`](#specinterfaces) declares exists and is Ready. `AllReady` over an empty set on a device that declares none | any declared child is missing, unready or blocked; or the device has no `status.id` yet | `AllReady`, `PendingChildren`, `Conflict`, `PruneBlocked`, `APIError`, `DryRunPending`, `ReportPending` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals are shared; see
[errors and retries](../concepts/errors-and-retries.md). The four that mean something
particular here:

- **`DeferredFieldPending`** on `Ready`: one of the three address references has not been
  written yet. Expected and transient on a create.
- **`WaitingForKey`** on `Ready`: `siteRef` has not resolved, so no candidate applies. **Zero
  lookups and zero writes** — the correct outcome, not a stall.
- **`Conflict`** on `Ready`: two devices matched, or one matched and `onConflict: Fail`. On
  `assetTag` this is cluster-wide: another namespace's CR holds it.
- **`Protected`** on `Deleting`: NetBox refuses to delete a device while something still
  references it — an interface holding an IP address is the usual one. The delete completes on
  its own once the blocker is gone, and `status.deletionAttempts` counts the tries.
- **`ChildrenReady` is set on every device**, not only on one with inline entries: the Kind
  implements the inline-parent capability, so a device with no `spec.interfaces` reports
  `AllReady` over an empty set. Before the first successful write it reports
  `PendingChildren` — no `status.id` means every child's reference back to the device would sit
  unresolved — which is expected and transient, and is why the reason exists on a device that
  declares nothing.
- **`Conflict`** on `ChildrenReady` is a different failure from `Conflict` on `Ready`: it is
  about a Kubernetes name, not a NetBox object, and it blocks **all** materialisation for this
  device rather than one entry. See
  [inline components](#inline-components-and-what-they-are-not).

## Kind-specific behaviour

### Three deferred writes, not one

A device with all three address references takes **two** requests either way: one `POST`
without any of them, then one `PATCH` carrying all three that resolved. They are not three
separate `PATCH`es. A device whose IPv6 address does not exist yet gets a `PATCH` with the two
that do and keeps `oobIPRef` in `status.deferredPending`.

### Every counter and `services` are excluded from the payload and the diff

`dcim.Device` carries ten `CounterCacheField`s — `console_port_count`,
`console_server_port_count`, `power_port_count`, `power_outlet_count`, `interface_count`,
`front_port_count`, `rear_port_count`, `device_bay_count`, `module_bay_count`,
`inventory_item_count` (`netbox/dcim/models/devices.py` lines 694-733) — plus a `services`
`GenericRelation`. NetBox maintains all of them and ignores an attempt to set one, so writing
one does not fail: it silently no-ops, the next reconcile finds the same difference, and the
operator `PATCH`es forever. All eleven are `ReadOnly` on the descriptor.

NBO-030's spec says "eleven `*_count` columns". There are ten; the eleventh in that count is
`dcim.DeviceType.device_count`, which lives on the other model.

### `SW1` is adopted, not duplicated

A CR named `sw1` against a NetBox that holds `SW1` at the same site adopts it and reports
`status.adopted: true`. The name in NetBox is **not** rewritten to lower case: `name` is in
the payload, so the next drift check compares `sw1` against `SW1`, finds a difference, and
`PATCH`es the case NetBox holds to the one the spec asks for. That is the spec winning, which
is what a declarative API is for — but it is a write, so it is worth knowing about before a
rollout renames a hundred devices' capitalisation.

### Renaming changes identity

`spec.name`, `spec.siteRef`, `spec.tenantRef` and `spec.assetTag` are all natural-key inputs.
Once `status.id` is set the natural key is not consulted again, so an edit to any of them
`PATCH`es the existing device rather than creating a second one. Editing them *before* the
first successful write, or after `status.id` is lost, changes which device the CR is about.

### Inline components, and what they are not

[`spec.interfaces`](#specinterfaces) is the whole of the inline component surface today, and
what follows is device-specific; the mechanism itself is
[inline children](../concepts/inline-children.md).

**One manifest, three objects, and nothing hidden.** A device with one inline interface carrying
one address produces a `NetBoxDevice`, a `NetBoxInterface` and a `NetBoxIPAddress`. All three
appear in `kubectl get`, each carries its own conditions, each is reconciled by its own
controller, and each writes its own NetBox object.

```console
$ kubectl get nbdev,nbif,nbip
NAME                                      SITE   TYPE              ID    READY
netboxdevice.../rtmrpi0001                home   raspberry-pi-4b   412   True

NAME                                      DEVICE       NAME   ID    READY
netboxinterface.../rtmrpi0001-eth0        rtmrpi0001   eth0   901   True

NAME                                             ADDRESS          ID    READY
netboxipaddress.../rtmrpi0001-eth0-ip-10-0-20-10-24   10.0.20.10/24   1204  True
```

**The names are derived and deterministic**: `slugify(<device>-<key>)` per level, with `ip` as
the addresses' discriminator. `metadata.name` and never `spec.name`, so renaming the device in
NetBox churns nothing in Kubernetes.

**The one case where the naming rule and NetBox disagree.** `dcim.Interface`'s uniqueness is
`('device', 'name')` with **no** `Lower()`, so `Eth0` and `eth0` are two interfaces on one device
([`NetBoxInterface`](netboxinterface.md#specname)) — while this kind's own name is matched with
`?name__ie=` and is one device either way. A derived child name is slugified, which lowercases,
so two entries differing only in case derive **one** CR name. The device reports:

```
ChildrenReady  False  Conflict  spec.interfaces[eth0] and spec.interfaces[Eth0] both derive
                                the NetBoxInterface name "sw1-eth0", so nothing was written:
                                give them different keys, or a different discriminator on one
                                of the two lists
```

and **nothing at all is written** — not even the entries that did not collide. That is
deliberate: two entries applying one name in turn would each overwrite the other on alternate
reconciles, forever, and there is no safe partial answer. A pair of interfaces differing only in
case is a shape the inline form cannot express; write at least one of them as a
`NetBoxInterface`.

**A pre-existing CR at a derived name is never hijacked.** If `sw1-eth0` already exists and does
not carry both the owner-uid label for this device and a controller owner reference to it,
nothing is written to it — no `PATCH`, no label, no owner reference — and the device reports
`ChildrenReady=False`, `Reason=Conflict` naming it. Two consequences worth knowing:

- The other entries' **CRs are still materialised**, but their NetBox side stalls: a
  `ChildrenReady=False` downgrades the device's `Ready`, and a child's `deviceRef` only resolves
  against a Ready target, so every sibling reports `RefsResolved=False`,
  `Reason=RefTargetFailed` carrying the device's own Conflict message and writes nothing. One
  occupied name therefore holds up the whole device until somebody renames or deletes the object
  in the way. Fail-closed, and legible: every stalled child names the cause.
- A hand-written `NetBoxInterface` at a name **no entry derives** is unaffected in every way. It
  is never pruned, it keeps its non-controller containment owner reference, and it is absent from
  `status.children`.

**The cascade runs one way, and only one.** `dcim.Device` has no containment parent
([the box at the top](#this-kind-has-no-containment-parent-and-that-is-deliberate)), so nothing
cascades *into* a device: deleting a site, a device type or a cluster leaves it alone, because
NetBox refuses those deletions anyway. Materialisation is the other direction and is a stronger
claim than containment — the operator *created* the child — so a materialised child gets a
**controller** owner reference from the device ([ADR-0003](../decisions/0003-ownership-and-references.md)
rule 3), and `kubectl delete nbdev` takes its interfaces and their addresses with it, parent
last. The ordering is the device's own finalizer waiting while owned children exist, not the
owner reference: the children's finalizers delete their NetBox objects first, so NetBox never
sees a `PROTECT`-refused device delete it could have avoided.

The chain is longer than a virtual machine's, and the failure surface with it: an address on this
device that is *another* device's `primary_ip4` stalls this whole cascade with
`Deleting=False`, `Reason=Protected` carrying NetBox's own message naming the other device. That
is correct, it is the server's opinion, and it completes with no manual step once the other
reference is removed.

**What inline deliberately cannot do.** No `primary` / `oob` flag, no `claimFrom`, no
`allowDuplicate`, no MAC address, no service, no cable, and none of the nine other component
kinds — [`spec.interfaces[].addresses`](#specinterfacesaddresses) and
[what is deliberately absent](#what-is-deliberately-absent) give the reason for each. Every one
of them is expressible as a longhand CR today.

### What NetBox validates and the operator does not

`dcim.Device.clean()` checks the site/location/rack combination, the device type's
subdevice role, and more (`netbox/dcim/models/devices.py`, `Device.clean`). None of it is
reimplemented here: a rejection comes back as `Ready=False`, `Reason=Invalid` carrying
NetBox's own message verbatim, with a long backoff, because retrying an unchanged payload
cannot succeed ([errors and retries](../concepts/errors-and-retries.md)).

## Printer columns

```console
$ kubectl get nbdev
NAME         SITE   TYPE              STATUS   PRIMARY-IP        ID    READY   AGE
rtmrpi0001   home   raspberry-pi-4b   active   rtmrpi0001-eth0   412   True    9m
sw1          home   ex2200-48t        active                     413   True    9m
sw1-lab      lab    ex2200-48t        staged                           False   2m
```

| Column | JSONPath |
|---|---|
| `SITE` | `.spec.siteRef.name` |
| `TYPE` | `.spec.deviceTypeRef.name` |
| `STATUS` | `.spec.status` |
| `PRIMARY-IP` | `.spec.primaryIP4Ref.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`SITE` reads the spec rather than the status because it is the half of the identity a human
needs when two devices share a name — `kubectl get nbdev` is where you see that `sw1` and
`sw1-lab` are two sites' switches. `TYPE` and `PRIMARY-IP` are blank when the reference is in
`slug`, `lookup` or `id` mode, which is expected: the column reads `.name`.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, `spec.name` required | admission, nothing stored | the field was omitted | Required here even though NetBox's column is nullable. See [`spec.name`](#specname) |
| `kubectl apply` rejected, `spec.deviceTypeRef`/`roleRef`/`siteRef` required | admission | one of the three was omitted | All three are `REQ` in NetBox |
| `kubectl apply` rejected, `spec.status` | admission | a label spelling, or `decommissioned` | The wire values are the seven in [`spec.status`](#specstatus); `decommissioning` has an `-ing` |
| `kubectl apply` rejected, `spec.latitude` | admission | more than two integer digits, or more than six decimals | `decimal(8,6)`. `longitude` allows three integer digits |
| `kubectl apply` rejected, `metadata.name` | admission | uppercase or an underscore copied from `spec.name` | `metadata.name` is a DNS-1123 label; `spec.name` keeps the literal string |
| `Ready=False`, `Reason=WaitingForKey` | reconcile, **zero lookups and zero writes** | `siteRef` has not resolved, so no candidate applies | Apply the site. The device re-enqueues on its own |
| `Ready=False`, `Reason=WaitingForRef` naming `tenantRef` | reconcile, zero writes | the tenant does not exist yet | Expected. Do **not** remove `tenantRef` to unblock it — that changes the object's identity |
| `Ready=False`, `Reason=WaitingForRef` with `RefKindUnavailable` | reconcile | `deviceTypeRef`, `roleRef`, `platformRef` or an address ref in `name` mode | Those Kinds are NBO-025 and NBO-027. Use `slug`, `lookup` or `id` meanwhile |
| `Ready=False`, `Reason=DeferredFieldPending` | reconcile | an address reference has not been PATCHed yet | Expected and transient. `status.deferredPending` names which |
| `Ready=False`, `Reason=Conflict` naming an `assetTag` | reconcile, zero writes | another CR, possibly in another namespace, claims that asset tag | The column is globally unique. The message names the winner |
| `Ready=False`, `Reason=Conflict`, two ids | reconcile, zero writes | two devices matched the candidate | Should not happen on candidates 2 and 3 — the constraints forbid it. Check `status.naturalKey` for what was searched |
| `Ready=False`, `Reason=Invalid` | reconcile, long backoff | NetBox's `clean()` refused the payload | The message is NetBox's own, verbatim. Fix the spec; retrying is pointless |
| The device's name changed case in NetBox | none | `name` is in the payload and the spec won | Expected. See [`SW1` is adopted, not duplicated](#sw1-is-adopted-not-duplicated) |
| `kubectl delete netboxsite` left the devices behind | none | `dcim.Device.site` is `PROTECT`, so there is no owner reference | Expected and correct. See [the box at the top](#this-kind-has-no-containment-parent-and-that-is-deliberate) |
| No `ParentOwned` condition at all | none | this kind has no containment parent | Expected. Every other kind's `ParentOwned` reports a cascade this one does not have |
| Terminating forever, `Deleting` `Reason=Protected` | finalizer | something still references the device — usually an interface holding an IP address | Delete the blocker; the device converges on its own. Or `deletionPolicy: Retain` to drop the finalizer without asking NetBox |
| Deleting the device deleted its interfaces too | none | `dcim.Interface.device` is `CASCADE`, so interfaces *do* take an owner reference from the device | Expected. See [`NetBoxInterface`](netboxinterface.md) |
| `kubectl apply` rejected, `spec.interfaces` `Duplicate value` | admission, nothing stored | two entries share a `name`, or two addresses under one interface share an `address` | The lists are keyed; give them different keys. The key is what the child's name and path are derived from |
| `ChildrenReady=False`, `Reason=Conflict`, "both derive" | reconcile, **zero writes of any kind** | two inline entries derive one child name — usually two interface names differing only in case | Rename one, or write one of them as a longhand `NetBoxInterface`. See [inline components](#inline-components-and-what-they-are-not) |
| `ChildrenReady=False`, `Reason=Conflict`, "already exists and is unowned" | reconcile, zero writes to that object | a CR the operator does not own already holds the derived name | Rename or delete it, or rename the inline entry. Nothing was written to it |
| Every inline child stuck at `RefTargetFailed` naming the device | reconcile, zero NetBox writes | the device is not `Ready`, and a child's `deviceRef` only resolves against a Ready target — a single `ChildrenReady` Conflict does this | Fix the Conflict the device reports; the whole tree converges on its own |
| `ChildrenReady=False`, `Reason=PendingChildren` on a device with no `spec.interfaces` | reconcile | the device has no `status.id` yet, so materialisation is skipped | Expected and transient. It clears on the first successful write |
| `ChildrenReady=False`, `Reason=PruneBlocked` | reconcile, **nothing deleted** | more children would be pruned than the device declares, plus a margin of 8 | Remove inline entries in smaller commits, or delete the device and let the cascade take them |
| An inline entry's child never appears | `ChildrenReady=False`, `Reason=PendingChildren` | the child exists but is not Ready — usually a `lag`, `parent` or `bridge` key naming no sibling | Read the child's own `RefsResolved`. A key must name another entry of the same list |

## Related

- [`NetBoxInterface`](netboxinterface.md) — this kind's children, and the one reference on this
  model whose cascade runs the other way
- [`NetBoxIPAddress`](netboxipaddress.md) — what an inline `addresses` entry becomes, and the
  complete kind an inline entry is a subset of
- [Inline children](../concepts/inline-children.md) — the derived name, the three prune cases,
  the blast-radius cap, and why a hand-written CR is never hijacked
- [`NetBoxSite`](netboxsite.md) — half the identity, and the parent that does *not* cascade
- [`NetBoxTenant`](netboxtenant.md) — the other half, when it is set
- [`NetBoxCluster`](netboxcluster.md) — the containment-shaped reference that is not a parent
- [Ownership](../concepts/ownership.md) — why a `PROTECT`-ed foreign key gets no owner
  reference, and what the absence of `ParentOwned` means
- [Lookups](../concepts/lookups.md) — `?name__ie=`, and why a null filter is pinned rather
  than omitted
- [References](../concepts/references.md) — the four ref modes, deferral, and cycle detection
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Drift detection](../concepts/drift.md) — how the decimals and the choice columns are
  compared
- [Deletion](../concepts/deletion.md) — the finalizer order, and what `PROTECT` looks like
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — rule 4,
  and the cascade rule this kind is the first counter-example to
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
