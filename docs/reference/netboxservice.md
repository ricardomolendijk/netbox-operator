# `NetBoxService`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxService` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbsvc` |
| Status subresource | yes |

A `NetBoxService` is one `ipam.Service` in NetBox: a layer-four service running on a device, a
virtual machine or a [first-hop-redundancy group](netboxfhrpgroup.md).

It is the only kind so far that carries a **polymorphic pair**, a **many-to-many** and an
**ordered array** on one object — and it is still three small files and a one-line controller,
which is the [descriptor](../concepts/descriptor.md) claim made concrete.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxService
metadata:
  name: dns-ssh
  namespace: default
spec:
  endpointRef: homelab
  parent:
    virtualMachineRef:
      name: dns
  name: ssh
  protocol: tcp
  ports:
    - 22
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxService
metadata:
  name: dns-ssh
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  # Exactly one member: deviceRef | virtualMachineRef | fhrpGroupRef.
  parent:
    virtualMachineRef:
      name: dns

  name: ssh
  protocol: tcp               # tcp | udp | sctp
  ports:
    - 22
    - 2222

  # A many-to-many. Omit to leave NetBox's list alone; [] to clear it.
  ipAddresses:
    - name: dns-v4
    - lookup:
        address: 10.0.10.53/24

  description: Secure shell
  comments: Restricted to the management VLAN by the firewall, not by NetBox.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.parent`

| | |
|---|---|
| Type | `ServiceParent` (a [generic-FK union](genericref.md)) |
| Required | **yes** |
| Validation | CEL: exactly one of `deviceRef`, `virtualMachineRef` or `fhrpGroupRef` |

`parent_object_type ForeignKey REQ -> contenttypes.ContentType on_delete=PROTECT` and
`parent_object_id PositiveBigIntegerField REQ`.

Both columns are `REQ`, so the CEL shape is `== 1`: a service with no parent is not a thing
NetBox stores.

| Member | NetBox object type | Kind ships? |
|---|---|---|
| `deviceRef` | `dcim.device` | not yet (`NetBoxDevice`) |
| `virtualMachineRef` | `virtualization.virtualmachine` | yes |
| `fhrpGroupRef` | `ipam.fhrpgroup` | yes, [here](netboxfhrpgroup.md) |

The FHRP group is the member worth noticing: a service can be parented to a *redundancy group*
rather than to a box, which is how the listeners on a virtual address are recorded.

**This kind's containment parent** — see [Ownership](#ownership).

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` |

`name CharField REQ len=100`. Part of the lookup convention.

Not globally unique, and not close to it: `ssh` on every device is the normal shape. That is
exactly why the parent is pinned in the lookup rather than omitted.

### `spec.protocol`

| | |
|---|---|
| Type | `ServiceProtocol` |
| Required | **yes** |
| Validation | `Enum=tcp;udp;sctp` |

`protocol (ServiceBase) CharField REQ len=50 choices=ServiceProtocolChoices`.

The three values are read from `netbox/ipam/choices.py` lines 175–185
(`ServiceProtocolChoices`) in the NetBox 4.6.8 tree. That class declares no `key`, so it cannot
be extended through `FIELD_CHOICES` (`netbox/utilities/choices.py` lines 23–35) and a closed
enum cannot reject a legitimate value.

`ServiceProtocol` is **one enum type shared with**
[`NetBoxServiceTemplate`](netboxservicetemplate.md), which is the opposite of `VLANStatus` and
`PrefixStatus`: those are two nearly-identical *different* choice classes and a shared enum
would have papered over the near-miss. Here one abstract base, `ipam.ServiceBase`, really does
declare the column for both models.

### `spec.ports`

| | |
|---|---|
| Type | `[]int32` |
| Required | **yes** |
| Validation | `MinItems=1`, `MaxItems=256`, per item `Minimum=1`, `Maximum=65535` |

`ports (ServiceBase) ArrayField REQ`, bounded per element by `SERVICE_PORT_MIN = 1` and
`SERVICE_PORT_MAX = 65535` (`netbox/ipam/constants.py` lines 92–93).

**Order is data.** A Postgres `ArrayField` preserves the order it is given, NetBox does not sort
it on save (`netbox/ipam/models/services.py` lines 41–47 recompute only the `_ports_lowest`
cache), and the operator compares it order-sensitively — the rule `internal/netbox/drift.go`
already names `Service.ports` under.

So **reordering `spec.ports` produces one `PATCH`**, which then converges. NBO-055's acceptance
criterion asks for zero writes; that is not what ships, and the reason is deliberate. An
unordered-array comparison would need a new `FieldClass` and a new rule in the differ — changes
to shared logic, which [adding a kind is not allowed to be](../concepts/descriptor.md). What the
descriptor does guarantee is the half that matters for correctness: **`ports` is not in the
natural key**, so a reorder finds the same row and becomes a `PATCH` instead of a duplicate. See
[Natural key](#natural-key).

`maxItems` is not a NetBox limit. It bounds the API server's cost estimate for the list, the same
256 every bounded list in this API uses.

### `spec.ipAddresses`

| | |
|---|---|
| Type | `[]ObjectRef` → [`NetBoxIPAddress`](netboxipaddress.md) |
| Required | no |
| Validation | `MaxItems=256` |

`ipaddresses ManyToManyField -> ipam.IPAddress` — which of the parent's addresses the service
listens on. Empty means all of them, which is NetBox's own semantics.

The API name has **no separator**: `ipaddresses`, not `ip_addresses`. A camelCase-to-snake_case
convention would produce the wrong one, NetBox would ignore it rather than reject it, and the
field would write nothing while reporting success. Which is why
[`Descriptor.Fields`](../concepts/descriptor.md) is an explicit table.

A many-to-many, so NetBox does not preserve the order the spec lists them in and the operator
compares it as an order-independent id set: **reordering this list writes nothing**, unlike
`ports`. Absent, empty and set are three different instructions — omit it to leave NetBox's own
list alone, write `[]` to clear it ([field ownership](../concepts/field-ownership.md)).

`maxItems` is not decoration: `ObjectRef` carries five CEL rules and the API server costs them at
the list's maximum length, so an unbounded list makes the whole CRD uninstallable (#185).

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`.

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(parent, name, protocol)` | `?parent_object_type=&parent_object_id=&name=&protocol=` | nothing — no `meta.constraints` on the model |

`ipam.Service`'s table-level lines are `meta.ordering: ('protocol', '_ports_lowest', 'id')` and
two non-unique indexes. So this is a convention, and two services agreeing on all three are a
legal server state reported as `Ready=False, Reason=Conflict` naming the candidate ids.

**The parent halves are what make the candidate safe at all.** `?name=ssh&protocol=tcp` alone
matches the SSH service on *every* device in the NetBox, so the first reconcile would adopt
somebody else's row and the follow-up `PATCH` would reparent it.
`reconciler.applyGenericFK` writes the resolved pair into the decoded spec under the two column
names, which is the mechanism [`NetBoxVLANGroup`](netboxvlangroup.md)'s identity uses. Both
columns are `REQ`, so there is no null variant and none is possible.

Server-side these filters exist:
`ServiceFilterSet.Meta.fields = ('id', 'name', 'protocol', 'description', 'parent_object_type',
'parent_object_id')`, with `parent_object_type = MultiValueContentTypeFilter()`
(`netbox/ipam/filtersets.py` lines 1239–1289).

**`ports` is deliberately not a filter.** A query parameter carries one value, and NetBox's only
port filter is `port = NumericArrayFilter(field_name='ports', lookup_expr='contains')`
(`netbox/ipam/filtersets.py` lines 1282–1285) — a single-value containment test that cannot
express "these ports and no others".

## Read-only columns

`_ports_lowest` is a cache NetBox recomputes from `ports` on every save, so it is in
`Descriptor.ReadOnly`. Writing it does not fail — it silently no-ops, which is a `PATCH` loop
rather than an error.

## Deletion

**`deletionPolicy` defaults to `Delete`**, like every kind, since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) reversed decision #176. The reasoning that used to put `Retain` here still describes a real cost.

## Ownership

**Containment parent: `parent`**, and every member of the union cascades.

`dcim.Device`, `virtualization.VirtualMachine` and `ipam.FHRPGroup` each declare a `services`
`GenericRelation` (`docs/netbox-schema.md`), so deleting any of the three deletes the service
server-side, and the owner reference is what makes the CR go too
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). The cascade is stated per
member (#214) rather than per pair, because that is where the fact lives — and read back per
member, since which one an object used is not known until its reference resolves.

`parent_object_type` being `on_delete=PROTECT` is about the **ContentType row**, not about the
parent object: content types are not deleted in normal operation, and the cascade that matters
comes from the `GenericRelation` on the far side.

`ipAddresses` is ruled out by cardinality rather than by taste. A to-many containment ref is
`ErrContainmentToMany`: garbage collection waits for every owner, and a list of parents is that
mistake with no upper bound.

## `status`

Identical to every other kind. `ipam.Service` is a `PrimaryModel`, so the provenance stamp
applies in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the service exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | the union and every listed address resolved | one did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable` |
| `ParentOwned` | the parent's owner reference is set | it cannot be | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

A **partially resolvable** `ipAddresses` writes nothing at all, exactly as
[`NetBoxVRF`](netboxvrf.md#specimporttargets-and-specexporttargets)'s target lists do: half a
many-to-many is a different set, not a smaller one.

## Printer columns

```
NAME      NAME  PROTOCOL   PORTS       ID   READY   AGE
dns-ssh   ssh   tcp        [22 2222]   88   True    3m
```

| Column | JSONPath |
|---|---|
| `NAME` | `.spec.name` |
| `PROTOCOL` | `.spec.protocol` |
| `PORTS` | `.spec.ports` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` naming `deviceRef` | admission | the union is empty, or has two members | exactly one; both columns are `REQ` |
| `deviceRef: {name: ...}` never resolves | `RefsResolved=False`, `Reason=RefKindUnavailable` | `NetBoxDevice` has not shipped | use `slug`, `lookup` or `id` mode |
| One `PATCH` after reordering `ports` | `Synced=True` after it | an `ArrayField`'s order is data | expected; see `spec.ports`. Never a second object |
| Stuck naming two ids | `Ready=False`, `Reason=Conflict` | two NetBox services share `(parent, name, protocol)`; nothing prevents it | remove one in NetBox |
| `ipAddresses` never reaches NetBox | `RefsResolved=False` on one entry | a partially resolvable list writes nothing | fix or remove the failing entry |
| Nothing written and no ref error | `Ready=False`, `Reason=Invalid` | NetBox refused an address that does not belong to the parent | list only the parent's own addresses |

## Related

- [`NetBoxServiceTemplate`](netboxservicetemplate.md) — the same two columns with a
  database-backed identity instead of a convention
- [`NetBoxFHRPGroup`](netboxfhrpgroup.md) — one of the three parents
- [generic references](genericref.md) — the `== 1` union shape
- [drift](../concepts/drift.md) — why an array and a many-to-many compare by opposite rules
