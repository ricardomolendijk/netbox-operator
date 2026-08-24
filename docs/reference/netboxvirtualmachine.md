# `NetBoxVirtualMachine`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVirtualMachine` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvm` |
| Status subresource | yes |
| Lands with | NBO-029 (M4) |

A `NetBoxVirtualMachine` is one `virtualization.VirtualMachine` in NetBox. It is the kind with
the most intricate identity in the catalogue — four `UniqueConstraint`s, three of them
conditional, all four over `Lower('name')` — and the first whose primary IP addresses can only
be written by a second pass.

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
- **`interfaces` / `addresses` / `disks` inline** — that sugar is NBO-033, on top of NBO-032.
  This kind is the longhand it expands into.

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
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

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
cannot answer it. An empty column means nothing is outstanding.
