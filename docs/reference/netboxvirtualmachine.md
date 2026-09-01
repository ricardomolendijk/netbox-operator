# `NetBoxVirtualMachine`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVirtualMachine` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvm` |
| Status subresource | yes |
| Lands with | NBO-029 (M4); inline children NBO-033 (M5) |

A `NetBoxVirtualMachine` is one `virtualization.VirtualMachine` in NetBox. It is the kind with
the most intricate identity in the catalogue — four `UniqueConstraint`s, three of them
conditional, all four over `Lower('name')` — and the first whose primary IP addresses can only
be written by a second pass.

It also carries **inline children**, as [`NetBoxDevice`](netboxdevice.md) does — and one thing
that kind does not, the `primary` back-patch below: `spec.interfaces`,
`spec.interfaces[].addresses` and `spec.disks` materialise real `NetBoxVMInterface`,
`NetBoxIPAddress`, `NetBoxIPAddressClaim` and `NetBoxVirtualDisk` CRs owned by it
([inline children](../concepts/inline-children.md),
[ADR-0003 rule 5](../decisions/0003-ownership-and-references.md)). Every inline field is
optional and every one of those kinds is fully usable on its own; nothing is expressible only
through the sugar.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVirtualMachine
metadata:
  name: dns
  namespace: default
spec:
  endpointRef: homelab
  name: dns
  siteRef:
    name: home
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVirtualMachine
metadata:
  name: dns
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: dns

  # At least one of clusterRef / siteRef / deviceRef, enforced by CEL.
  clusterRef:
    name: proxmox-home
  siteRef:
    name: home

  # Optional, and none of them is a containment parent.
  tenantRef:
    name: acme
  roleRef:                    # dcim.DeviceRole, not ipam.Role
    slug: server
  platformRef:
    slug: debian-12

  status: active              # offline | active | planned | staged | failed |
                              # decommissioning | paused
  startOnBoot: "off"          # on | off | laststate

  vcpus: "2"                  # a decimal as a string; NetBox returns "2.00"
  memory: 2048                # MB or MiB, per the instance's RAM_BASE_UNIT
  disk: 20480                 # omit when NetBoxVirtualDisk children carry the sizes
  serial: VM-0001

  # Both deferred: stripped from the create and PATCHed once they resolve.
  primaryIP4Ref:
    name: dns-v4
  primaryIP6Ref:
    name: dns-v6

  description: Recursive resolver
  comments: Managed by netbox-operator.

  # Inline children. Every field is optional and every child kind is fully usable on its
  # own; this is the same set of objects written shorter. See spec.interfaces below.
  interfaces:
    - name: eth0
      enabled: true
      mtu: 1500
      mode: tagged              # access | tagged | tagged-all | q-in-q
      vrfRef:
        name: vrf-home
      untaggedVLANRef:
        name: vlan-mgmt
      taggedVLANs:
        - name: vlan-guest
      description: mgmt
      addresses:
        - address: 10.20.0.10/24
          primary: true         # -> this VM's primary_ip4
          status: active
          dnsName: dns.home.arpa
        - claimFrom:            # -> a NetBoxIPAddressClaim child
            prefixRef:
              name: mgmt-net
  disks:
    - name: scsi0
      size: 20                  # required; the unit is the instance's DISK_BASE_UNIT
      description: root
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Validation | `MinLength=1`, `MaxLength=64` |

The VM's name (`docs/netbox-schema.md` → `virtualization.VirtualMachine`,
`name CharField REQ len=64`).

**Matched case-insensitively.** All four unique constraints are over `Lower('name')`, so the
lookup filter is `name__ie` and not `name`. A NetBox holding `DNS` in this VM's cluster is
therefore *adopted* rather than duplicated. With an exact filter the operator would report
`dns` absent, create it, and NetBox would answer `400` — a loop where the read and the write
disagree about what exists ([lookups](../concepts/lookups.md)).

### `spec.clusterRef`, `spec.siteRef`, `spec.deviceRef`

| | |
|---|---|
| Type | `ObjectRef` |
| Required | at least one of the three |
| Validation | CEL: `has(self.clusterRef) \|\| has(self.siteRef) \|\| has(self.deviceRef)` |

All three columns are nullable in the database, and NetBox's `clean()` requires at least one
of them (`netbox/virtualization/models/virtualmachines.py` lines 291–295, NetBox 4.6.8). That
is a validation rule no schema digest can show, so it is enforced at admission — a VM with no
host is rejected by `kubectl apply` rather than failing on every write forever.

Three, not two. NBO-029's spec table names `clusterRef` and `siteRef`; the source adds
`device`, and a VM pinned to a standalone host device with no cluster and no site is legal in
NetBox. Rejecting it at admission would make a working manifest un-appliable.

**What stays server-side.** Three further rules need rows only NetBox has, so they surface as
`Ready=False, Reason=Invalid` carrying NetBox's own message:

- a site that does not match the cluster's site (`clean()` lines 297–303);
- a site that does not match the device's site (lines 305–311);
- a device that already belongs to a cluster, with no `clusterRef` set, or one in a different
  cluster (lines 313–327).

**Kinds that do not exist yet.** `NetBoxCluster` lands with NBO-028 and `NetBoxDevice` with
NBO-030. A `name`-mode reference to either reports
`RefsResolved=False, Reason=RefKindUnavailable` — the manifest is correct and the fix is an
operator upgrade. An `id`-mode reference works today, because an id needs the target's
endpoint rather than its CR.

`clusterRef` is this kind's **containment parent**: deleting the `NetBoxCluster` in the same
namespace takes its VMs with it, and their interfaces and disks after them
([ADR-0003](../decisions/0003-ownership-and-references.md) §4). Exactly one, because garbage
collection waits for *every* owner — so a site-only VM takes no owner reference and reports
`ParentOwned=False, Reason=CascadeUnavailable`.

### `spec.roleRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxDeviceRole` |
| Required | no |

`dcim.DeviceRole`, not a virtualization-specific role model and not `ipam.Role`
(`docs/netbox-schema.md` → `virtualization.VirtualMachine`, `role ForeignKey ->
dcim.DeviceRole`). There is no virtualization role model at all, which is the easy thing to
assume and the reason `DeviceRole.vm_role` exists and defaults to true. Kind lands with
NBO-027.

### `spec.tenantRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxTenant` |
| Required | no |

Part of the identity, and the reason the candidate list has four constraint-backed entries
rather than two. It is not a containment parent: re-assigning a tenant is normal and only one
containment owner is allowed.

### `spec.status`, `spec.startOnBoot`

| | `status` | `startOnBoot` |
|---|---|---|
| Type | enum | enum |
| Default | `active` | `off` |
| Values | `offline`, `active`, `planned`, `staged`, `failed`, `decommissioning`, `paused` | `on`, `off`, `laststate` |

Both columns are `CharField`s whose `def=` the schema digest could not evaluate, so the values
are read from `netbox/virtualization/choices.py` — lines 32–51 for
`VirtualMachineStatusChoices` and 54–65 for `VirtualMachineStartOnBootChoices`, in the same
4.6.8 tree the digest was taken from.

`startOnBoot` is deliberately **not a boolean**: `laststate` — resume whatever the VM was
doing when the host went down — is a hypervisor's most common setting and a bool could not
express it.

Both are defaulted to NetBox's own defaults, so the operator manages them from the first
reconcile. A defaulted field that never reaches a payload is a field the operator can never
correct. The cost is that neither can be *cleared* through this API: an enum has no empty
member, so the three states of [field ownership](../concepts/field-ownership.md) are two for a
choice column.

### `spec.vcpus`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `MaxLength=9`, CEL: `^[0-9]{1,4}(\.[0-9]{1,2})?$` or `""` |

A `DecimalField decimal(6,2)` (`docs/netbox-schema.md` → `virtualization.VirtualMachine`), and
a **string** rather than a `float64`. NetBox returns a decimal as a canonicalised JSON string,
so `"2"` comes back as `"2.00"`. Those are the same number and different strings; the drift
comparison is numeric, so neither spelling produces a `PATCH`
([drift](../concepts/drift.md)). A float would round-trip through binary and produce a third
spelling of the same value, and a `PATCH` on every resync.

Cleared with `null` rather than with `""`, like `dcim.Site`'s latitude: DRF parses an empty
string as a number and rejects it. Write `vcpus: ""` and the operator sends JSON `null`.

### `spec.memory`, `spec.disk`

| | |
|---|---|
| Type | `*int32` |
| Required | no |
| Validation | `Minimum=0`, `Maximum=2147483647` |

Both are `PositiveIntegerField`s, and both are **pointers**: a plain int cannot tell "not
managed" from "managed as zero", so adopting a VM whose memory a human had set would silently
clear it. Nil leaves NetBox's value alone; `0` writes zero.

Neither column carries a unit. NetBox's own form derives the label from the instance's
`RAM_BASE_UNIT` and `DISK_BASE_UNIT` settings — MB or MiB
(`netbox/virtualization/forms/model_forms.py` line 292) — so the operator writes the integer
it is given and does no conversion. NBO-029's spec table says gigabytes for `disk`; it is the
same unit as `memory`.

**`disk` versus [`NetBoxVirtualDisk`](netboxvirtualdisk.md).** Use one or the other. NetBox
fills `disk` from the aggregate of a VM's virtual disks when it is null, and **rejects** a
value that disagrees with that aggregate
(`netbox/virtualization/models/virtualmachines.py` lines 330–341). So the contradiction is a
`400` reported as `Ready=False, Reason=Invalid` naming both numbers — not a `PATCH` loop, and
not something the operator has to guard against. NBO-029's spec proposed a guard clause on the
hypothesis that NetBox recomputes the value silently; the source says it complains, so no
guard ships.

### `spec.primaryIP4Ref`, `spec.primaryIP6Ref`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxIPAddress` |
| Required | no |
| Deferred | always |

`OneToOneField -> ipam.IPAddress on_delete=SET_NULL`, one per family, resolved and written
independently.

**Both are deferred unconditionally**, because the dependency ring has no satisfiable order:
the address is assigned to a `NetBoxVMInterface`, the interface belongs to this VM, and this
VM points back at the address. So the fields are stripped from the create and applied by a
follow-up `PATCH` once the reference resolves. In between the VM reports
`Ready=False, Reason=DeferredFieldPending` and names the fields in
`status.deferredPending`, which is what makes `kubectl wait --for=condition=Ready` on a VM
mean something ([object lifecycle](../concepts/object-lifecycle.md)).

`NetBoxIPAddress` lands with NBO-025.

### `spec.interfaces`

| | |
|---|---|
| Type | list of inline interface entries |
| Required | no |
| Validation | `MaxItems=32`, `listType=map` keyed on `name` |
| Materialises | `NetBoxVMInterface`, one per entry |

Each entry becomes a real `NetBoxVMInterface` CR owned by this VM, and each of its
`addresses` a real `NetBoxIPAddress` or `NetBoxIPAddressClaim`. Nothing is hidden: the
children appear in `kubectl get`, carry their own conditions, are reconciled by their own
controllers and each writes its own NetBox object. `kubectl delete` on the VM takes all of
them with it. The mechanism, the derived names and the pruning rules are in
[inline children](../concepts/inline-children.md).

| Field | Type | Notes |
|---|---|---|
| `name` | string, **required** | `MinLength=1`, `MaxLength=64`. The entry's key: it makes the derived child name and the `owned-by-path`. Case-sensitive in NetBox, lowercased in the derived CR name. |
| `enabled` | `*bool` | Nil leaves NetBox's own value alone; `false` writes false. |
| `mtu` | `*int32` | `Minimum=1`, `Maximum=65536` — NetBox's own `INTERFACE_MTU_MIN`/`MAX`. |
| `mode` | enum | `access`, `tagged`, `tagged-all`, `q-in-q`. Not defaulted: an unset mode is a real state. |
| `vrfRef` | `ObjectRef` → `NetBoxVRF` | |
| `untaggedVLANRef` | `ObjectRef` → `NetBoxVLAN` | |
| `taggedVLANs` | list of `ObjectRef` → `NetBoxVLAN` | `MaxItems=32`, not the 256 the longhand kind allows — see below. |
| `description` | string | `MaxLength=200`. Omit it to leave NetBox's own value alone; set it to `""` to clear it. |
| `addresses` | list of inline address entries | `MaxItems=16`, `listType=atomic`. |

**`virtualMachineRef` is absent, and that is the rule rather than an omission.** The
materialiser sets it from this VM, so a field the user could not meaningfully set does not
exist here instead of existing and being ignored. The same applies to `assignedObject` on an
inline address: it is set to the interface child the entry is nested under.

**A map list, so two entries named `eth0` are rejected by the API server**, not discovered at
reconcile time. The key is what makes the derived name and the path unique, so a duplicate is
not a state the operator can be in. It also gives server-side apply per-entry ownership, so
two writers editing different interfaces of one VM do not fight.

**`MaxItems=32` here and `MaxItems=32` on `taggedVLANs` are two different arguments.** 32
interfaces is a real-world maximum: VMware caps a VM at 10 vNICs and a virtio guest at ~31
slots. The nested `taggedVLANs` bound is a **CEL cost** bound: the API server costs a rule at
the *product* of both lists' maxima, so a bound that is fine on its own can make the whole
CRD unloadable one level down ([references](../concepts/references.md#a-list-needs-a-bound)).
A trunk enumerating more than 32 VLANs wants `mode: tagged-all`, or the interface written as
its own `NetBoxVMInterface`.

**Mixing inline and longhand on one VM is supported, and only the inline subset is owned.**
A hand-written `NetBoxVMInterface` whose `virtualMachineRef` names this VM works, appears in
NetBox, is **never pruned** and never appears in `status.children` — it gets a
*non-controller* containment owner reference instead
([ADR-0003 rule 4](../decisions/0003-ownership-and-references.md)), so
`kubectl delete nbvm` still cascades to it while the VM never claims authorship. "Why did my
interface survive the prune" and "why did my interface get deleted" are the two questions
this feature generates, and that is the answer to both.

#### If it is wrong

- Two entries with one `name`: rejected at admission, `Duplicate value`.
- Two entries deriving one CR name — `eth0/1` and `eth0.1` slugify identically:
  `ChildrenReady=False, Reason=Conflict` naming both paths, and **nothing is written at all**,
  not even the children that did not collide.
- A CR already at a derived name that the operator does not own:
  `ChildrenReady=False, Reason=Conflict` naming it, nothing written to it, and the other
  entries still materialise.
- Renaming a key (`eth0` → `eth1`) prunes `dns-eth0` and its address children and
  materialises `dns-eth1` with new ones. **In NetBox that is a delete and a create**, so the
  interface and its IP are destroyed and reissued with new ids. It is documented rather than
  prevented, because the alternative is heuristic rename detection — a guess about data.
  `deletionPolicy: Retain` does not help and makes it worse: the children are separate
  objects that inherit the policy, so `Retain` plus a key rename **leaks** the old NetBox
  objects as orphans. `NetBoxSweep` (NBO-046) is the cleanup.

### `spec.interfaces[].addresses`

| | |
|---|---|
| Type | list of inline address entries |
| Required | no |
| Validation | `MaxItems=16`, `listType=atomic`, exactly one of `address` / `claimFrom` |
| Materialises | `NetBoxIPAddress` or `NetBoxIPAddressClaim`, one per entry |

| Field | Type | Notes |
|---|---|---|
| `address` | string | `Pattern` on `<address>/<0-128>`, `MinLength=4`, `MaxLength=43`. The entry's key **including the mask**: `/24` and `/25` of one host are two NetBox objects, two CRs and two keys. |
| `claimFrom.prefixRef` | `ObjectRef` → `NetBoxPrefix`, **required inside `claimFrom`** | Allocates instead of stating. The entry's key is the pool. |
| `primary` | bool | Makes this the VM's `primary_ip4` or `primary_ip6`, by family. Not a NetBox column on the address — see below. |
| `status` | enum | `active`, `reserved`, `deprecated`, `dhcp`, `slaac`. Not defaulted here, unlike the longhand kind. |
| `role` | enum | `""`, `loopback`, `secondary`, `anycast`, `vip`, `vrrp`, `hsrp`, `glbp`, `carp`. |
| `vrfRef` | `ObjectRef` → `NetBoxVRF` | Unset means the global table, which is a different identity rather than a missing filter. |
| `dnsName` | string | `MaxLength=255`. Omit it to leave NetBox's own value alone; set it to `""` to clear it. |
| `description` | string | `MaxLength=200`. Same two states. |

**Not a map list**, unlike `interfaces` and `disks`: an entry's key is its `address` when it
states one and its pool when it says `claimFrom`, so there is no single property the API
server could key on. Two entries deriving one key are caught by the materialiser instead,
which reports `Conflict` and writes nothing.

**`claimFrom` and not `fromPrefixRef`.** The key is nested so that allocating out of an
ip-range later is another member of one union rather than a second mutually exclusive sibling
field ([ADR-0004](../decisions/0004-claims-first-allocation.md#the-inline-key-is-claimfrom-and-it-is-nested)).
`fromPrefixRef` does not exist: a manifest using it is rejected by a server-side apply's
strict field validation. `claimFrom: {ipRangeRef: …}` is NBO-064.

**An inline `claimFrom` allocates an address and does not yet attach it to the interface it is
written under**, because `NetBoxIPAddressClaim` carries no `assignedObject` and does not yet
materialise a `NetBoxIPAddress` of its own (NBO-036). That is exactly as complete as a
standalone claim, which is the property that keeps the sugar equivalent to the longhand it
stands for rather than a better version of it. An address that has to be attached today is
written as a literal `address`.

**`allowDuplicate` is absent, and this one is a safety property rather than a scope
decision.** The flag makes the provenance stamp part of an address's identity; a stamped child
that loses `status.id` — a status write lost to a restart, a restore — then matches nothing it
can claim and the engine **creates a second address**
([#167](https://github.com/ricardomolendijk/netbox-operator/issues/167)). A materialised child
is the object most exposed to that, because it is re-created from an unchanged manifest by
design. So the field is not in the inline schema, and the admission webhook additionally
refuses it on any object the operator materialised. An address that legitimately exists twice
— anycast, a VRRP virtual address — is written as its own `NetBoxIPAddress`, where a human has
said so deliberately.

**`tenantRef` is absent for a different reason:** `NetBoxIPAddress` has none either.
`ipam.IPAddress.tenant` waits on NBO-021, and an inline field the child CR could not carry
would be accepted and silently dropped.

#### `primary`, and the ring it closes

`primary: true` makes the materialised address this VM's `primary_ip4` or `primary_ip6`,
chosen by whether the literal contains a colon. It is **not a column on the address**: the
column is `virtualization.VirtualMachine.primary_ip4`, and the value is the address child's
id. So it reaches NetBox exactly as [`spec.primaryIP4Ref`](#specprimaryip4ref-specprimaryip6ref)
does — as a deferred field on the VM, stripped from the create and applied by one follow-up
`PATCH` — and never as a write to the VM's spec, which ADR-0005 §1 forbids and which Argo CD
would revert on its next sync.

Which is how the `VM → IPAddress → VMInterface → VM` ring closes in one convergence:

| Pass | What happens |
|---|---|
| 1 | The VM is created without `primary_ip4`. It has an id, so the children are materialised. `status.deferredPending: [primaryIP4Ref]`, `ChildrenReady=False, Reason=PendingChildren`. |
| 2 | The interface child has an id, so the address child resolves its `assignedObject` and is written. |
| 3 | The address child has an id, so the VM's derived reference resolves and one `PATCH` sets `primary_ip4`. `ChildrenReady=True/AllReady`, `Ready=True`. |

**Each pass is triggered by an event, not by a timer**, and that falls out of folding the
derived reference into the same spec map every other reader of a spec goes through: the
reference index is built from it, so the address child becoming Ready re-enqueues the VM
immediately, exactly as a hand-written `primaryIP4Ref` would
([references](../concepts/references.md#ordering-and-convergence)). The materialiser's
15-second interval is the backstop underneath that, and the endpoint's resync the backstop
under *that* — so the whole chain is sub-second in practice and bounded by seconds in the worst
case, never by the ten-minute resync.

Exactly one `PATCH` is issued and none afterwards. A second would mean the differ was comparing
a column the create never sent, which is the loop [drift detection](../concepts/drift.md) opens
by warning about; `TestVirtualMachineInlinePrimaryLandsInOneDeferredPatch` counts them.

**At most one `primary` per family per VM, across every interface rather than per interface**,
and an explicit `spec.primaryIP4Ref` beside an inline IPv4 `primary` is the same refusal. Two
sources of truth for one column is not something to resolve by precedence. Enforced twice: CEL
counts the sources at admission, and the controller counts them again and reports
`Ready=False, Reason=Conflict` naming both declarations with zero writes. Defence in depth
because the CEL half is a nested list comprehension, whose cost the API server charges at the
product of both maxima — and a rule that grows out of its budget stops being installed.

A `claimFrom` entry may not set `primary`: there is no address CR for the VM to point at until
NBO-036 lands.

### `spec.disks`

| | |
|---|---|
| Type | list of inline disk entries |
| Required | no |
| Validation | `MaxItems=32`, `listType=map` keyed on `name` |
| Materialises | `NetBoxVirtualDisk`, one per entry |

| Field | Type | Notes |
|---|---|---|
| `name` | string, **required** | `MinLength=1`, `MaxLength=64`. The entry's key. |
| `size` | int32, **required** | `Minimum=0`, `Maximum=2147483647`. Required because `virtualization.VirtualDisk.size` is; `size: 0` is legal and explicit. |
| `description` | string | `MaxLength=200`. Omit it to leave NetBox's own value alone; set it to `""` to clear it. |

**`spec.disk` beside these is legal and is not a `Conflict`.** NetBox fills the VM's `disk`
column from the aggregate of its virtual disks when it is null and **rejects** a value that
disagrees with the aggregate (`netbox/virtualization/models/virtualmachines.py` lines 330-341,
NetBox 4.6.8) — a loud `400` reported as `Ready=False, Reason=Invalid` naming both numbers,
not a `PATCH` loop. Setting both consistently works, so nothing refuses the combination.

### Deleting a VM with inline children

`kubectl delete netboxvirtualmachine dns` removes the children and their NetBox objects,
parent last, under both propagation policies. What orders it is the **VM's own finalizer**: it
waits while owned child CRs exist, so the children's finalizers delete their NetBox objects
first and the VM's last ([deletion](../concepts/deletion.md)).

The chain for an address is the sharpest edge in the feature, and it is the point rather than
an accident: the address child inherits the VM's `deletionPolicy`, so a VM deleted with the
default `Delete` frees its addresses in NetBox. The same is true of an inline `claimFrom` — the
claim child inherits `Delete` and frees the address it was handed, which is what stops one
leaked address per VM deletion from exhausting a pool in a CI-driven cluster
([ADR-0004](../decisions/0004-claims-first-allocation.md#deleting-a-claim-frees-its-address)).

If anything in NetBox still points at one of those addresses — a `nat_inside`, a service,
another device's `primary_ip4` — NetBox `PROTECT`s the delete and the chain **stalls** with
`Deleting=False, Reason=Protected` naming the blocker, rather than cascading. Nothing is
forced, no finalizer is dropped, and when the blocker goes away the chain completes with no
manual step.

**Delete the VM, re-apply the same manifest, get the same objects.** Child names are derived
from `metadata.name` and the entry's key, and a claim's allocation identity is derived from
`(endpoint, namespace, kind, name)`
([ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git)).
Composed: a VM re-applied from unchanged Git materialises children with the same names, which
compute the same identities, which adopt the same NetBox objects — and hand back the same
addresses, provided nothing has taken them meanwhile.

### `spec.serial`, `spec.description`, `spec.comments`

Free text, `MaxLength=50` / `200` / none. Omit any of them to leave NetBox's own value alone;
set one to `""` to clear it. Those are different instructions
([field ownership](../concepts/field-ownership.md)).

### What is deliberately absent

- **`macAddress` / `primaryMACAddressRef`.** NetBox 4.2 moved the MAC to `dcim.MACAddress`
  behind a generic FK. `dcim.BaseInterface` carries `primary_mac_address`, and
  `virtualization.VMInterface` lists only `mac_addresses GenericRelation` — a reverse
  relation, never a column. `NetBoxMACAddress` is NBO-048.
- **`virtualMachineTypeRef`** — `virtualization.VirtualMachineType` has no ticket (NBO-060).
- **`configTemplateRef` and `localContextData`** — NBO-059.
- **`oobIPRef`** — `VirtualMachine` has no such column; only `dcim.Device` does.
- **`services` inline** — `ipam.Service` is NBO-055, so there is no child kind to
  materialise yet.
- **`interfaces[].parentRef` / `bridgeRef` / `qinqSVLANRef`, and an inline address's
  `natInsideRef`, `comments`, `tags` and `customFields`.** The inline forms deliberately do
  not mirror the longhand specs: an interface or address that needs one of those is written as
  its own CR. Inline covers the common case; the standalone kind stays the complete one, which
  is what keeps the sugar from growing into a copy of three other specs that has to be kept in
  step with them.

## Natural key

Five candidates, tried in order. Four are read straight off `meta.constraints`
(`docs/netbox-schema.md` → `virtualization.VirtualMachine`):

| # | Constraint | Query |
|---|---|---|
| 1 | `UniqueConstraint(Lower('name'), 'cluster', 'tenant')` | `?name__ie=&cluster_id=&tenant_id=` |
| 2 | `UniqueConstraint(Lower('name'), 'cluster')` where `tenant IS NULL` | `?name__ie=&cluster_id=&tenant_id__isnull=true` |
| 3 | `UniqueConstraint(Lower('name'), 'device', 'tenant')` where `cluster IS NULL AND device IS NOT NULL` | `?name__ie=&device_id=&tenant_id=&cluster_id__isnull=true` |
| 4 | `UniqueConstraint(Lower('name'), 'device')` where `cluster IS NULL AND device IS NOT NULL AND tenant IS NULL` | `?name__ie=&device_id=&cluster_id__isnull=true&tenant_id__isnull=true` |
| 5 | *convention*: `(name, site)` | `?name__ie=&site_id=&cluster_id__isnull=true&device_id__isnull=true` |

**Every condition becomes a pinned null filter, never an omitted one.** That is what makes
this a list of *identities* rather than a fallback chain. Candidate 2 asserts `tenantRef` was
never declared, so a VM whose tenant has not been created yet matches nothing and the engine
waits — falling through would adopt the tenant-less VM of that name and then `PATCH` a tenant
onto somebody else's row
([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)). Candidates
3 and 4 carry the constraints' own `cluster__isnull=True` for the same reason; their
`device__isnull=False` needs no pin, because a candidate is only applicable once `deviceRef`
has resolved.

**Candidate 5 is not a constraint.** `clean()` accepts a VM with a site and neither cluster
nor device, and no unique constraint covers that shape — so without a candidate such a VM
could never establish identity and would sit at `WaitingForKey` forever, which is the worst of
the available outcomes. It does not make `(name, site)` unique: two site-only VMs sharing a
name still match, and that is reported as `Ready=False, Reason=Conflict` naming both ids
rather than resolved by taking the first row. Tenant is deliberately not in it — there is no
constraint to derive a tenant-qualified variant from.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `deferredPending`,
`lastAppliedHash`, `lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each means.

`virtualization.VirtualMachine` is a `PrimaryModel`, so it carries both `tags` and
`custom_fields` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the VM exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | one did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefKindUnavailable` |
| `ParentOwned` | the cluster's owner reference is set | it cannot be | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `ChildrenReady` | every child the inline lists declare exists and is Ready | anything else | `AllReady`, `PendingChildren`, `Conflict`, `PruneBlocked`, `APIError`, `DryRunPending`, `ReportPending` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`ChildrenReady` is absent on a VM with no inline lists, rather than `True` over nothing. **The
VM is not `Ready=True` while any declared child is not**, because `kubectl wait` on a VM has to
mean the VM *and* its interfaces, addresses and disks — but a `Ready=False` the VM already set
for its own reason is left alone, since that is the more specific answer.

`status.children` lists what was materialised, one entry per declared child, with its path,
Kind, name and readiness. It is also what the finalizer reads to order the cascade, and what
the pruner reads to know which Kinds to look for once every inline entry has been removed.

`DeferredFieldPending` is the state a VM with a primary address spends its first passes in,
and it is not a failure — it is the operator saying it has the value and has not sent it yet.
`WaitingForRef` is the neighbouring state where it does not have the value at all.

## `deletionPolicy` defaults to `Delete`

Unlike [`NetBoxPrefix`](netboxprefix.md), and that is deliberate. Issue #176 decided that
IPAM kinds holding allocated state default to `Retain`, and enumerated them; a VM is not one.
Deleting a VM record destroys no allocation, and re-creating it from the manifest that
described it gives back the same object. Its interfaces and disks go with it, in NetBox by
`on_delete=CASCADE` and in Kubernetes by the owner reference.

Set `deletionPolicy: Retain` on a VM the operator should stop managing without removing.

## Printer columns

```
NAME   CLUSTER        STATUS   PRIMARY-IP        ID   READY   AGE
dns    proxmox-home   active                     41   True    4m
web    proxmox-home   active   ["primaryIP4Ref"] 42   False   9s
```

| Column | JSONPath |
|---|---|
| `CLUSTER` | `.spec.clusterRef.name` |
| `STATUS` | `.spec.status` |
| `PRIMARY-IP` | `.status.deferredPending` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`PRIMARY-IP` reads the status rather than the spec on purpose: the question a VM's primary
address raises is not "what did you ask for" but "has it been written yet", and the spec
cannot answer it. An empty column means nothing is outstanding. A VM whose primary address is
declared *inline* uses the same column, because `primary: true` reaches NetBox through the same
deferred field.

## Related

- [Inline children](../concepts/inline-children.md) — the derived names, the three cases
  pruning tells apart, and what each of the three "renames" does
- [Ownership](../concepts/ownership.md) — the two owner references and the cascade
- [Deletion](../concepts/deletion.md) — the finalizer ordering and a `PROTECT`-blocked delete
- [ADR-0003 rule 5](../decisions/0003-ownership-and-references.md) — why the sugar is in
  `v1alpha1` at all, and the two constraints that let `v1beta1` drop it
- [ADR-0004](../decisions/0004-claims-first-allocation.md) — why `claimFrom` materialises a
  real claim, and why deleting one frees its address
- [`NetBoxVMInterface`](netboxvminterface.md), [`NetBoxIPAddress`](netboxipaddress.md),
  [`NetBoxVirtualDisk`](netboxvirtualdisk.md),
  [`NetBoxIPAddressClaim`](netboxipaddressclaim.md) — the four longhand kinds the sugar
  expands into
