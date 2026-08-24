# Generic references: the union shape

The user-facing shape of a polymorphic NetBox foreign key. One spec field, one member per
legal target, at most one set.

For *why* it looks like this — the `app_label.model` spelling rule, the `REQ` trap in the
schema digest, how the pair is kept atomic and how a new union is added — see
[Generic references](../concepts/generic-refs.md).

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Go types | `IPAssignment` |
| Milestone | M3 (NBO-019) |
| Status | The mechanism is built. One union ships; the Kinds it targets arrive in M4. |

## The shape

A polymorphic reference is an object with one field per legal target. Each field is an
ordinary [`ObjectRef`](../concepts/references.md), so all four modes — `name`, `slug`,
`lookup`, `id` — work in each of them.

```yaml
spec:
  assignedObject:
    vmInterfaceRef:
      name: dns-eth0          # a CR in this namespace
```

```yaml
spec:
  assignedObject:
    interfaceRef:
      lookup:                 # an interface NetBox already has
        device: sw1
        name: Ethernet1/1
```

Set **at most one** member. Setting two is rejected by `kubectl apply`:

```
The GenericUnionFixture "x" is invalid: spec.assignedObject: Invalid value: "object":
at most one of interfaceRef, vmInterfaceRef or fhrpGroupRef may be set
```

## Empty, absent, and the difference between them

This is the one part of the shape that is not obvious, and it matters because the two look
alike in YAML:

| Written as | Means | What the operator does |
|---|---|---|
| field omitted entirely | *do not manage this reference* | leaves both NetBox columns exactly as they are |
| `assignedObject: {}` | *clear this reference* | writes both columns as `null` |
| one member set | *attach it here* | writes both columns together |

So removing the field from a manifest does **not** detach the object — it stops managing
the attachment. Writing the field empty detaches it.

## `<= 1` versus `== 1`

Some NetBox pairs are nullable and some are not, and the union follows the column:

| Union | Pair | Members | Empty union |
|---|---|---|---|
| `IPAssignment` (`ipam.IPAddress.assigned_object_*`) | nullable | at most one | legal — the address is unassigned |
| *a `REQ` pair, e.g. `ipam.Service.parent_object_*`* | `REQ` | **exactly** one | rejected at admission, and the field itself is required |

## The unions

### `IPAssignment`

What an IP address is attached to. `assigned_object_type` / `assigned_object_id` on
`ipam.IPAddress`, both nullable — an unassigned address is legal.

| Member | Target Kind | NetBox object type |
|---|---|---|
| `interfaceRef` | `NetBoxInterface` | `dcim.interface` |
| `vmInterfaceRef` | `NetBoxVMInterface` | `virtualization.vminterface` |
| `fhrpGroupRef` | `NetBoxFHRPGroup` | `ipam.fhrpgroup` |

None of the three Kinds exists before M4. Until each one's Descriptor is registered, a
member naming it reports:

```
RefsResolved  False  RefKindUnavailable
  assignedObject.interfaceRef -> netboxinterface/team-a/eth0: target kind unavailable
  (no descriptor is registered for netbox.kubeforge.org/v1alpha1, Kind=NetBoxInterface)
```

The reference is reported and **never silently dropped**, and no write is made for it.

### Unions that are not written yet

Deliberately absent rather than stubbed. Each is three lines of Descriptor data plus a
struct, and each lands with the Kind that needs it:

| Union | Pair | Shape | Lands with |
|---|---|---|---|
| `ScopeRef` | `ipam.Prefix` / `ipam.VLANGroup` `scope_*` | `<= 1` | NBO-018 |
| `ServiceParent` | `ipam.Service.parent_object_*` | `== 1` | `NetBoxService` |
| `FHRPInterfaceTarget` | `ipam.FHRPGroupAssignment.interface_*` | `== 1` | `NetBoxFHRPGroupAssignment` |
| `ContactAssignmentTarget` | `tenancy.ContactAssignment.object_*` | `== 1` | NBO-056 |
| `CableTerminationTarget` | `dcim.CableTermination.termination_*` | `== 1` | NBO-049 |

## Conditions

A polymorphic reference reports on `RefsResolved` like any other reference — see the
[condition vocabulary](../concepts/references.md#the-condition-vocabulary) — with one reason
of its own:

| Reason | Means | Retried |
|---|---|---|
| `RefTypeNotAllowed` | The member named is not one this union declares, or its object type is outside what the NetBox column accepts. | **No.** Terminal: only an edit clears it. |
| `RefKindUnavailable` | The member is declared and its target Kind is not registered in this build. | Every 10 minutes. |
| `RefNotReady`, `RefNotFound`, `RefAmbiguous`, `RefDenied`, `RefCycle` | Exactly as for a typed reference. | See [References](../concepts/references.md). |

The field named in the message is the **union member's path**, not the union:
`assignedObject.vmInterfaceRef`. That is the spelling to grep the manifest for.

A `RefTypeNotAllowed` message names both halves, because either alone leaves you guessing at
the other:

```
assignedObject: target type not allowed (siteRef resolves to object type "dcim.site",
and assigned_object_type accepts only [dcim.interface virtualization.vminterface ipam.fhrpgroup])
```

## Cross-namespace

A member's `namespace` works and defaults like any other reference's: it may be set only
together with `name`, and crossing a namespace needs a
[`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. There is nothing
polymorphic-specific about it — the member is resolved through the same grant check, at the
same point, before the target is read.
