# `NetBoxFHRPGroupAssignment`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxFHRPGroupAssignment` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbfhrpa` |
| Status subresource | yes |

A `NetBoxFHRPGroupAssignment` is one `ipam.FHRPGroupAssignment` in NetBox: the join row that
says an interface participates in a [first-hop-redundancy group](netboxfhrpgroup.md), at a
priority.

## The one object kind that carries no provenance stamp

`docs/netbox-schema.md` → `ipam.FHRPGroupAssignment` records `bases: ChangeLoggedModel`, and
everything unusual about this kind follows from that one line. A `ChangeLoggedModel` mixes in
neither `TagsMixin` nor `CustomFieldsMixin`, so:

- there is **no `tags` column** and **no `custom_fields` column** on the model;
- `Descriptor.Taggable` and `Descriptor.CustomFieldable` are both `false`;
- the object carries **no [provenance stamp](../operations/provenance.md)** — no
  `netbox.kubeforge.org/managed` tag, no UID custom field;
- `ipam.fhrpgroupassignment` is absent from the `object_types` list the provenance bootstrap
  declares its custom fields for.

Writing `tags` here would not fail. NetBox ignores a column it does not know, the value would
vanish, the next read would find it absent, and the operator would `PATCH` it again on every
resync forever. Which is why `Taggable` is a declaration rather than something the engine infers.

It also declares no `description` and no `comments`, which makes it the one object kind with **no
clearable field at all** — recorded as such in `noClearableFields`
(`internal/controller/manifests_test.go`), because the rule is otherwise that every object kind
has one.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxFHRPGroupAssignment
metadata:
  name: dns-eth0-vrrp-10
  namespace: default
spec:
  endpointRef: homelab
  interface:
    vmInterfaceRef:
      name: dns-eth0
  groupRef:
    name: vrrp-10
  priority: 200
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxFHRPGroupAssignment
metadata:
  name: dns-eth0-vrrp-10
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  # Exactly one member. interfaceRef -> dcim.interface is the other one.
  interface:
    vmInterfaceRef:
      name: dns-eth0

  groupRef:
    name: vrrp-10

  priority: 200
```

That is every field. Three, all of them `REQ` columns, which is why the full example is the
minimal one plus the envelope.

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.interface`

| | |
|---|---|
| Type | `FHRPInterface` (a [generic-FK union](genericref.md)) |
| Required | **yes** |
| Validation | CEL: exactly one of `interfaceRef` or `vmInterfaceRef` |

`interface_type ForeignKey REQ -> contenttypes.ContentType on_delete=CASCADE` and
`interface_id PositiveBigIntegerField REQ`.

Both columns are `REQ`, so the CEL shape is `== 1` rather than
[`IPAssignment`](genericref.md)'s `<= 1`: an assignment with no interface is not a thing NetBox
stores. The nullability is read off the two *columns* and never off the `interface
GenericForeignKey` row above them — that row is not a column, a `GenericForeignKey` takes no
`null=` kwarg, and the digest's extractor marks it `REQ` unconditionally.

| Member | NetBox object type |
|---|---|
| `interfaceRef` | `dcim.interface` |
| `vmInterfaceRef` | `virtualization.vminterface` |

`NetBoxInterface` has not shipped yet, so `interfaceRef` resolves in `slug`, `lookup` and `id`
mode today and reports `RefsResolved=False, Reason=RefKindUnavailable` in `name` mode.
[`NetBoxVMInterface`](netboxvminterface.md) exists, so `vmInterfaceRef` resolves in all four.

### `spec.groupRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxFHRPGroup`](netboxfhrpgroup.md) |
| Required | **yes** |

`group ForeignKey REQ -> ipam.FHRPGroup on_delete=CASCADE`.

**This kind's containment parent** — see [Ownership](#ownership).

### `spec.priority`

| | |
|---|---|
| Type | `int32` |
| Required | **yes** |
| Validation | `Minimum=0`, `Maximum=65535` |

`priority PositiveSmallIntegerField REQ` — which router is master.

Required, and therefore **not a pointer**: the three states an optional field has do not apply
to a column NetBox will not accept as null. `priority: 0` is legal and explicit; omitting the
key is rejected by the API server.

NetBox orders assignments by `-priority`, highest first.

## Natural key

**One candidate, and unlike three of the four other lookup-only kinds in this milestone it is a
real database constraint:**

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(interface_type, interface_id, group)` | `?interface_type=&interface_id=&group_id=` | `UniqueConstraint(fields=('interface_type', 'interface_id', 'group'))` |

So the lookup matches at most one row, and an ambiguous match is *impossible* rather than merely
reported.

The pair is named by its two **column** names rather than by the union's spec field, because a
lookup on a polymorphic pair needs two filters and the union has no single value to offer one.
`reconciler.applyGenericFK` writes the resolved pair back into the decoded spec under exactly
those names — the mechanism [`NetBoxVLANGroup`](netboxvlangroup.md)'s
`(scope_type, scope_id, slug)` identity is built on.

All three filters exist server-side:
`FHRPGroupAssignmentFilterSet.Meta.fields = ('id', 'group_id', 'interface_type', 'interface_id',
'priority')`, plus `interface_type = MultiValueContentTypeFilter()`
(`netbox/ipam/filtersets.py` lines 891–921).

There is no null variant and none is possible: all three columns are `REQ`.

## Deletion

**`deletionPolicy` defaults to `Retain`** (decision #176). The row is one end of a redundancy
pair — deleting it silently demotes an interface out of its group, which is a traffic change
rather than a bookkeeping one.

## Ownership

**Containment parent: `groupRef`.** And this is the one place in the milestone where the choice
had to be *made* rather than derived, so the reasoning is written down.

**Two candidates cascade:**

| Candidate | Mechanism |
|---|---|
| `groupRef` | `group ForeignKey on_delete=CASCADE`, declared on this model |
| `interface` (both members) | `dcim.Interface` and `virtualization.VMInterface` each declare an `fhrp_group_assignments` `GenericRelation` |

Either would satisfy [ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 and pass
`validateContainment`. **Exactly one is permitted**, because Kubernetes garbage collection waits
for *every* owner: a second owner would silently turn "delete the group or the interface and the
assignment goes" into "delete both".

`groupRef` wins on two grounds. It is the declared foreign key on this model with
`on_delete=CASCADE` written on it, which is the most direct evidence there is. And it is the
only member whose Kind ships — `NetBoxInterface` does not exist, so `interfaceRef` can only
resolve in `slug`, `lookup` or `id` mode, and a reference to an object with no CR cannot produce
an owner reference anyway.

**The consequence, stated rather than hidden:** deleting a
[`NetBoxVMInterface`](netboxvminterface.md) whose interface NetBox cascades leaves this CR
behind, and the engine recreates the row on the next pass. That is the cost of one containment
parent, and it is the same cost every polymorphic kind in this API pays.

## `status`

`status.id`, `status.observedGeneration` and `status.conditions` as on every kind. **No
provenance stamp**, per the section at the top — so `nbctl adopt` and the multi-writer checks
that read the stamp have nothing to read on this kind, and adoption here is by natural key
alone.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the assignment exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | the union and `groupRef` both resolved | either did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefKindUnavailable` |
| `ParentOwned` | the group's owner reference is set | it cannot be | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`ParentOwned=False, Reason=CascadeUnavailable` is the normal report when the group lives in
another namespace: an owner reference across namespaces is illegal in Kubernetes, so the
reference still resolves and the cascade is unavailable
([ownership](../concepts/ownership.md)).

## Printer columns

```
NAME               GROUP     PRIORITY   ID   READY   AGE
dns-eth0-vrrp-10   vrrp-10   200        71   True    1m
```

| Column | JSONPath |
|---|---|
| `GROUP` | `.spec.groupRef.name` |
| `PRIORITY` | `.spec.priority` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` naming `interfaceRef` | admission | the union has both members, or neither | exactly one; the pair's columns are both `REQ` |
| `interfaceRef: {name: ...}` never resolves | `RefsResolved=False`, `Reason=RefKindUnavailable` | `NetBoxInterface` has not shipped | use `slug`, `lookup` or `id` mode, or a `vmInterfaceRef` |
| Owner reference missing | `ParentOwned=False`, `Reason=CascadeUnavailable` | the group is in another namespace | move it, or accept no cascade |
| Deleted a VM interface and the CR came back | none | the containment parent is `groupRef`, not the interface | delete this CR too |
| No `netbox.kubeforge.org/managed` tag in NetBox | none; `Ready=True` | this model has no `tags` column | expected — see the top of this page |

## Related

- [`NetBoxFHRPGroup`](netboxfhrpgroup.md) — the containment parent
- [`NetBoxVMInterface`](netboxvminterface.md) — the union member that resolves today
- [generic references](genericref.md) — the `== 1` union shape
- [ownership](../concepts/ownership.md) — one containment parent, and why
