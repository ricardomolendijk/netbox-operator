# Generic references: the union shape

The user-facing shape of a polymorphic NetBox foreign key. One spec field, one member per
legal target, at most one set.

For *why* it looks like this — the `app_label.model` spelling rule, the `REQ` trap in the
schema digest, how the pair is kept atomic and how a new union is added — see
[Generic references](../concepts/generic-refs.md).

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Go types | `IPAssignment`, `ScopeRef`, `ContactAssignmentTarget`, `FHRPInterface`, `ServiceParent`, `CableTerminationTarget`, `MACAssignment` |
| Milestone | M3 (NBO-018, NBO-019); `ContactAssignmentTarget` M10 (NBO-056); `FHRPInterface` and `ServiceParent` M10 (NBO-055); `CableTerminationTarget` M10 (NBO-049); `MACAssignment` M9 (NBO-048) |
| Status | The mechanism is built. Seven unions ship: four with the `== 1` shape, and one of those **to-many**. |

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
| `ScopeRef` (`CachedScopeMixin.scope_*`) | nullable | at most one | legal — the object is globally scoped |
| `ContactAssignmentTarget` (`tenancy.ContactAssignment.object_*`) | `REQ` | **exactly** one | rejected at admission, and the field itself is required |
| `FHRPInterface` (`ipam.FHRPGroupAssignment.interface_*`) | `REQ` | **exactly** one | rejected at admission, and the field itself is required |
| `ServiceParent` (`ipam.Service.parent_object_*`) | `REQ` | **exactly** one | rejected at admission, and the field itself is required |
| `CableTerminationTarget` (`dcim.CableTermination.termination_*`) | `REQ` | **exactly** one | rejected at admission; the *list* it sits in is required and `MinItems=1` |
| `MACAssignment` (`dcim.MACAddress.assigned_object_*`) | nullable | at most one | legal — the address is unattached |

## The unions

### `IPAssignment`

What an IP address is attached to. `assigned_object_type` / `assigned_object_id` on
`ipam.IPAddress`, both nullable — an unassigned address is legal. Carried by
[`NetBoxIPAddress.spec.assignedObject`](netboxipaddress.md#assignedobject), the first shipped
CRD to embed a union.

| Member | Target Kind | NetBox object type |
|---|---|---|
| `interfaceRef` | `NetBoxInterface` | `dcim.interface` |
| `vmInterfaceRef` | `NetBoxVMInterface` | `virtualization.vminterface` |
| `fhrpGroupRef` | `NetBoxFHRPGroup` | `ipam.fhrpgroup` |

Two of the three Kinds ship; `NetBoxFHRPGroup` arrives with NBO-055. Until a Kind's
Descriptor is registered, a member naming it reports:

```
RefsResolved  False  RefKindUnavailable
  assignedObject.fhrpGroupRef -> netboxfhrpgroup/team-a/vrrp-1: target kind unavailable
  (no descriptor is registered for netbox.kubeforge.org/v1alpha1, Kind=NetBoxFHRPGroup)
```

The reference is reported and **never silently dropped**, and no write is made for it.

### `ScopeRef`

Which Region, SiteGroup, Site or Location a scoped NetBox object hangs off. `scope_type` /
`scope_id` from `CachedScopeMixin`, both nullable — an unscoped object is legal and common.

**"Scope" here is NetBox's, not Kubernetes'.** `spec.scope` has no effect on where the CR
lives; every kind in `v1alpha1` is namespaced regardless
([ADR-0002](../decisions/0002-crd-scoping.md)).

| Member | Target Kind | NetBox object type |
|---|---|---|
| `regionRef` | `NetBoxRegion` | `dcim.region` |
| `siteGroupRef` | `NetBoxSiteGroup` | `dcim.sitegroup` |
| `siteRef` | `NetBoxSite` | `dcim.site` |
| `locationRef` | `NetBoxLocation` | `dcim.location` |

All four members have a Descriptor as of
[NBO-066 (#79)](https://github.com/ricardomolendijk/netbox-operator/issues/79), so the union
is resolvable end to end: `siteGroupRef` and `locationRef` used to report
`RefKindUnavailable` in **all four** modes, because `slug`, `lookup` and `id` each need the
target's REST endpoint and only a Descriptor holds it.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata:
  name: hq-lan
  namespace: team-a
spec:
  endpointRef: homelab
  prefix: 10.0.0.0/24
  scope:
    # At most one of regionRef, siteGroupRef, siteRef or locationRef.
    siteRef:
      # The mode to prefer: a sibling CR, which is the only mode the operator can wait on.
      name: hq
      # Defaults to this object's own namespace. Crossing one needs a NetBoxRefGrant.
      namespace: netbox-catalog
```

Omit `scope` entirely and the operator does not manage it. To *clear* a scope, empty the union
rather than deleting the field:

```yaml
spec:
  scope: {}          # sends scope_type: null, scope_id: null
```

#### What never appears in a request body

| Key | Why |
|---|---|
| `site`, `site_id` | not a column on a scoped model since NetBox 4.2. NetBox ignores an unknown key rather than rejecting it, so writing it returns `201` and sets nothing |
| `_region`, `_site_group`, `_site`, `_location` | read-only caches NetBox maintains from the pair. Writing one is dropped, so the operator would PATCH it again every resync, forever |

There is deliberately no `siteRef` shortcut on a scoped kind, not even sugar that expands into
`scope.siteRef` — a field by that name would read as the foreign key NetBox no longer has. A
descriptor listing a cache column without marking it read-only fails the manager's boot
(`ErrCachedNotReadOnly`), and a test asserts none of these keys reaches a request body.

#### Migrating from `netbox-populator`

| netbox-populator | here |
|---|---|
| `site: <name>` on a prefix | `scope: {siteRef: {name: <name>}}` |
| pruning keyed on the object's `site` | identity is the natural key; the scope is not a prune key |
| a `site` that appeared to work | it never did — check NetBox for prefixes with no scope |

`GET /api/ipam/prefixes/?scope_id__empty=true` is the query that tells you how much of it
silently did nothing.

### `ContactAssignmentTarget`

`spec.objectRef` on [`NetBoxContactAssignment`](netboxcontactassignment.md) — what a contact is
assigned to. The widest union in the catalogue, and the first on a `REQ` pair.

```yaml
spec:
  objectRef:
    siteRef:
      name: rtm1
```

| | |
|---|---|
| Pair | `object_type` / `object_id`, both `REQ` |
| Rule | `== 1`, and the field itself is required |
| Members | `regionRef`, `siteGroupRef`, `siteRef`, `locationRef`, `deviceRef`, `prefixRef`, `ipAddressRef`, `tenantRef`, `clusterRef`, `clusterGroupRef`, `virtualMachineRef` |
| Allowed types | 25 — every model mixing in `ContactsMixin` in NetBox 4.6.8 |
| Cached columns | none |
| Containment | yes; every member cascades |

Two things about it are unlike the other two unions.

**`AllowedTypes` is wider than the member list, and deliberately so.** NetBox accepts 25 object
types in this column and the CRD offers 11 — the ones whose target Kind has a typed alias to
write it down on. The two lists are independent statements, which is what gives the boot
cross-check something to say: a member pointing at a type NetBox would reject fails the boot
rather than shipping. `IPAssignment` and `ScopeRef` happen to have the two lists coincide because
their columns are narrow, not because either is derived from the other. The remaining fourteen
arrive one `Members` entry at a time, with the Kinds they name.

**The empty union is not an instruction.** On a nullable pair `assignedObject: {}` writes both
columns `null`. Here the columns are `NOT NULL`, so there is nothing to clear and the `== 1` rule
rejects `{}` at admission. The same is true of
[`CableTerminationTarget`](#cableterminationtarget), the second `REQ` pair to ship.

`deviceRef` is the union's one unresolvable member: `dcim.device` carries `ContactsMixin` and
`NetBoxDevice` lands in M4, so until then the member is admissible and reported as
[`RefKindUnavailable`](#conditions) in all four modes.

### `FHRPInterface`

Which interface an `ipam.FHRPGroupAssignment` enrols in a group. `interface_type` /
`interface_id`, **both `REQ`**, so `== 1` — an assignment with no interface is not a thing
NetBox stores.

| Member | Target Kind | NetBox object type |
|---|---|---|
| `interfaceRef` | `NetBoxInterface` | `dcim.interface` |
| `vmInterfaceRef` | `NetBoxVMInterface` | `virtualization.vminterface` |

Both members resolve end to end: `NetBoxInterface` and `NetBoxVMInterface` both have
Descriptors, so this is the first `== 1` union with no unresolvable member.

**Both members cascade**, through an `fhrp_group_assignments` `GenericRelation` on each target
model — and the union is still *not* that Kind's containment parent, because its `groupRef` is a
declared `on_delete=CASCADE` foreign key and only one slot exists. See
[`netboxfhrpgroupassignment.md`](netboxfhrpgroupassignment.md#ownership).

### `ServiceParent`

What an `ipam.Service` runs on. `parent_object_type` / `parent_object_id`, both `REQ`, so `== 1`.

| Member | Target Kind | NetBox object type |
|---|---|---|
| `deviceRef` | `NetBoxDevice` | `dcim.device` |
| `virtualMachineRef` | `NetBoxVirtualMachine` | `virtualization.virtualmachine` |
| `fhrpGroupRef` | `NetBoxFHRPGroup` | `ipam.fhrpgroup` |

The FHRP group is the member worth noticing: a service can be parented to a *redundancy group*
rather than to a box. All three members resolve end to end.

**Every member cascades** — `dcim.Device`, `virtualization.VirtualMachine` and `ipam.FHRPGroup`
each declare a `services` `GenericRelation` — so this union *is*
[`NetBoxService`](netboxservice.md#ownership)'s containment parent. Note that
`parent_object_type` is `on_delete=PROTECT`: that is about the ContentType row, not about the
parent object, and it is not the cascade that decides ownership.

### `CableTerminationTarget`

`spec.aTerminations` and `spec.bTerminations` on [`NetBoxCable`](netboxcable.md) — what each
end of a cable is plugged into. The **first to-many union**, and the one that is *used*
differently rather than *shaped* differently.

```yaml
spec:
  aTerminations:
    - interfaceRef:
        name: sw1-eth0
  bTerminations:
    - interfaceRef:
        name: sw2-eth0
```

| | |
|---|---|
| Pair | `termination_type` / `termination_id`, both `REQ` |
| Written as | `a_terminations` / `b_terminations`, one field per end carrying a list of `{object_type, object_id}` |
| Rule | `== 1` per element; the lists are required, `MinItems=1`, `MaxItems=16` |
| Members | `interfaceRef`, `consolePortRef`, `consoleServerPortRef`, `powerPortRef`, `powerOutletRef`, `frontPortRef`, `rearPortRef`, `powerFeedRef`, `circuitTerminationRef` |
| Allowed types | 9 — every model mixing in `dcim.CabledObjectModel` in NetBox 4.6.8 |
| Cached columns | none *reachable* — see below |
| Containment | **no**; every member is `SET_NULL` in the direction that matters |

Three things about it are unlike the other three unions.

**It is to-many, and that is a fact about the field rather than about the union.** The struct
is an ordinary one-of-N union with the `== 1` rule; the spec field is a bounded list of it,
because NetBox 4.x permits several terminations per cable end. Order inside a list is **not
data**: the operator sorts and deduplicates before writing and compares as a set, so
reordering entries produces zero API writes. See [a to-many
pair](../concepts/generic-refs.md#a-to-many-pair) for what that cost on the Descriptor side.

**Its pair is not two columns of the referring object.** It is two keys *inside* a list
element, named by `GenericObjectSerializer`
(`netbox/netbox/api/serializers/generic.py:15`) rather than by the model — `object_type` and
`object_id`, not `termination_type` and `termination_id`. The columns of those names live on
`dcim.CableTermination`, whose whole serializer is read-only
(`netbox/dcim/api/serializers_/cables.py:71`), which is why there is no
`NetBoxCableTermination` Kind.

**`AllowedTypes` and `Members` coincide, and both are nine.** Unlike
`ContactAssignmentTarget`'s 25-against-11: the column is narrow enough that every legal target
has a typed alias. `interfaceRef` is the one member whose Kind exists today; the other eight
report [`RefKindUnavailable`](#conditions) in all four modes until their Kinds land.

### What never appears in a cable's request body

| Key | Why |
|---|---|
| `termination_a_type`, `termination_a_id`, `termination_b_type`, `termination_b_id` | `CableFilterSet` parameters, not columns. They are how the cable is *looked up* (`dcim/filtersets.py:2637`); NetBox would ignore them in a body |
| `termination_type`, `termination_id`, `cable_end`, `cable` | columns of `dcim.CableTermination`, which the API refuses on write |
| `connector`, `positions` | real columns of `dcim.CableTermination`, and **unreachable from the 4.6.8 REST API by any route**: the termination endpoint is read-only and `GenericObjectSerializer` carries only the pair |
| `_abs_length` | a cache NetBox maintains from `length` and `length_unit` |

### `MACAssignment`

`spec.assignedObject` on [`NetBoxMACAddress`](netboxmacaddress.md) — what a MAC address is
attached to. `assigned_object_type` / `assigned_object_id` on `dcim.MACAddress`, both nullable:
an unattached MAC is legal.

```yaml
spec:
  assignedObject:
    vmInterfaceRef:
      name: dns-eth0
```

| Member | Target Kind | NetBox object type |
|---|---|---|
| `interfaceRef` | [`NetBoxInterface`](netboxinterface.md) | `dcim.interface` |
| `vmInterfaceRef` | [`NetBoxVMInterface`](netboxvminterface.md) | `virtualization.vminterface` |

**The narrowest union here, and the one that is deliberately not a reuse.** It is
`IPAssignment` minus `fhrpGroupRef`, over the same two typed ref aliases — and it is its own Go
type because NetBox restricts this column to the two models deriving from `dcim.BaseInterface`:

```python
MACADDRESS_ASSIGNMENT_MODELS = Q(app_label='dcim', model='interface') | \
                               Q(app_label='virtualization', model='vminterface')
```

(`netbox/dcim/constants.py:156-159`, applied to the serializer's `assigned_object_type` queryset
at `netbox/dcim/api/serializers_/devices.py:318`.) `ipam.fhrpgroup` is legal for an IP address
and illegal for a MAC.

Sharing `IPAssignment` and narrowing only the pair's `AllowedTypes` would be a **boot failure
waiting to happen**, not a wider `kubectl explain`: `validateUnionTypes` cross-checks every
member whose Kind is registered against `AllowedTypes` and returns `ErrMemberTypeNotAllowed`,
which fails the manager for *every* kind. `NetBoxFHRPGroup` (NBO-055) is registered today, and
`ipam.fhrpgroup` is legal for an `IPAssignment` member and illegal for `MACAssignment` — sharing
the type would already fail the manager on boot rather than merely "the day NBO-055 adds it".

It is also the first union that is **part of its kind's identity**: `(assigned_object_type,
assigned_object_id, mac_address)` is the natural key, so a member declared and not yet resolved
means no applicable candidate and **no write at all** — unlike on `NetBoxIPAddress`, where the
row is created and the assignment follows. See
[`NetBoxMACAddress`](netboxmacaddress.md#a-declared-but-unresolved-union-writes-nothing).
### Unions that are not written yet

There are none left. `ServiceParent` and `FHRPInterface` landed with NBO-055 and
`CableTerminationTarget` with NBO-049, so every polymorphic pair NetBox 4.6.8 declares on a
model this operator carries a Kind for now has a union. A pair on a model with no Kind yet gets
its union with that Kind — three lines of Descriptor data plus a struct — rather than being
stubbed ahead of it.

## Conditions

A polymorphic reference reports on `RefsResolved` like any other reference — see the
[condition vocabulary](../concepts/references.md#what-happens-when-it-does-not-resolve) — with one reason
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

## Related

- [Generic references](../concepts/generic-refs.md) — the mechanism, the spelling rule, and
  how a union is added
- [References](../concepts/references.md) — the four modes, grants, cycles, watches
- [The Descriptor](../concepts/descriptor.md) — `GenericFKSpec`, `Cached`, `registry.ScopeFK`
- [`NetBoxRefGrant`](netboxrefgrant.md) — authorising a cross-namespace member
- [`NetBoxContactAssignment`](netboxcontactassignment.md) — the `REQ` pair in use, and an
  identity built from one
- [`NetBoxCable`](netboxcable.md) — the to-many pair in use, and an identity built from a
  *representative element* of one- [`NetBoxMACAddress`](netboxmacaddress.md) — the narrowest union, and why narrowing a shared
  one instead would fail the manager's boot