# `NetBoxFHRPGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxFHRPGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbfhrp` |
| Status subresource | yes |
| Lands with | NBO-055 (M10) |

A `NetBoxFHRPGroup` is one `ipam.FHRPGroup` in NetBox: a VRRP, HSRP, GLBP or CARP group that
several interfaces share a virtual address through.

It is the Kind [`IPAssignment.fhrpGroupRef`](genericref.md) and
[`ServiceParent.fhrpGroupRef`](netboxservice.md#specparent) have been declared against since
NBO-025, so `ipam.fhrpgroup` becomes resolvable in `name` mode here.

## `spec.authKey` does not exist, and that is deliberate

`docs/netbox-schema.md` → `ipam.FHRPGroup` declares `auth_key CharField len=255`, and this API
does not expose it.

`plan.md` §15 permits the value only as `spec.authKeySecretRef`, never inline — a pre-shared key
inline in a CR is readable by anyone who can `get` the namespace. Reading a Secret into a NetBox
payload field is a capability the engine does not have: there is no `FieldClass` for it and
`internal/reconciler/payload.go` has nowhere to fetch one from. Adding one is a change to shared
logic, which [adding a Kind is not allowed to be](../concepts/descriptor.md).

So the column is **never written**, and therefore never cleared either. Set the key in the
NetBox UI or by API; the operator leaves it alone on every reconcile. `spec.authType` *is*
managed, because a choice column says nothing secret.

`auth_key` is in `internal/netbox/do.go`'s redaction set regardless of any of this, because
NetBox *returns* it on every read of this endpoint and a debug-level response log would
otherwise print it.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxFHRPGroup
metadata:
  name: vrrp-10
  namespace: default
spec:
  endpointRef: homelab
  protocol: vrrp3
  groupId: 10
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxFHRPGroup
metadata:
  name: vrrp-10
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  protocol: vrrp3             # vrrp2 | vrrp3 | carp | clusterxl | hsrp | glbp | other
  groupId: 10

  name: Gateway VRRP
  authType: md5               # plaintext | md5 -- the key itself is not a field here

  description: Redundant default gateway for the lab VLAN
  comments: Master is the core switch; the firewall is backup.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.protocol`

| | |
|---|---|
| Type | `FHRPGroupProtocol` |
| Required | **yes** |
| Validation | `Enum=vrrp2;vrrp3;carp;clusterxl;hsrp;glbp;other` |

`protocol CharField REQ len=50 choices=FHRPGroupProtocolChoices`, and half the lookup.

The seven values are read from `netbox/ipam/choices.py` lines 104–128
(`FHRPGroupProtocolChoices`) in the NetBox 4.6.8 tree — the digest records the choice *class*,
not its members. NetBox renders them in four option groups (`Standard`, `CheckPoint`, `Cisco`,
and a bare `Other`); that is presentation only, and the operator sends and compares the value
rather than the label ([drift](../concepts/drift.md)).

**A closed enum is safe here, and that is a fact about the source.** A `ChoiceSet` is extensible
through `FIELD_CHOICES` only when it declares a `key` (`netbox/utilities/choices.py` lines
23–35), and `FHRPGroupProtocolChoices` declares none — unlike `VLANStatusChoices`, which
declares `key = 'VLAN.status'`. So no deployment can add a protocol this enum would reject.

### `spec.groupId`

| | |
|---|---|
| Type | `int32` |
| Required | **yes** |
| Validation | `Minimum=0`, `Maximum=65535` |

`group_id PositiveSmallIntegerField REQ` — the protocol's own group number: the VRID for VRRP,
the group number for HSRP.

Not the Kubernetes object's name and not a NetBox id: it is the number that appears in the
device configuration. The other half of the lookup.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `MaxLength=100` |

`name CharField len=100` — nullable in NetBox, optional here.

**Not part of the identity**, and its nullability is why: a group with no name would have no
identity at all. `group_id` and `protocol` are what a device configuration keys on.

Omit it to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

### `spec.authType`

| | |
|---|---|
| Type | `FHRPGroupAuthType` |
| Required | no |
| Validation | `Enum=plaintext;md5` |

`auth_type CharField len=50 choices=FHRPGroupAuthTypeChoices`; the two values are read from
`netbox/ipam/choices.py` lines 131–139. That class declares no `key` either, so it cannot be
extended and the enum is closed.

Undefaulted, because the column is nullable with no Django default and a group with no
authentication is an ordinary group rather than a misconfigured one.

Setting it without a key set in NetBox is accepted and does nothing useful — see the section
above for why the key is not a field.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`.

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(protocol, group_id)` | `?protocol=&group_id=` | nothing — no `meta.constraints` on the model |

`ipam.FHRPGroup`'s only table-level lines are `meta.ordering: ['protocol', 'group_id', 'pk']`
and one non-unique index on `('protocol', 'group_id', 'id')`. So this is the ordering tuple
promoted to a convention, and two VRRP groups with VRID 10 on two unrelated segments are a
perfectly legal server state — reported as `Ready=False, Reason=Conflict` naming the candidate
ids, with nothing written.

If your NetBox really has several groups per `(protocol, group_id)`, this kind cannot tell them
apart. Manage them by hand, or use one namespace per segment and accept one `Ready` per group.

## Deletion

**`deletionPolicy` defaults to `Retain`** (decision #176), and this kind has a sharp reason
beyond the general one: `ipam.FHRPGroupAssignment.group` is `on_delete=CASCADE`, so deleting a
group takes **every assignment with it** — including assignments no CR describes.

## Ownership

**No containment parent.** `ipam.FHRPGroup` declares no foreign key of its own besides
`owner (OwnerMixin)`, which the operator does not map. The cascade in this family runs the other
way, from the group down to its
[assignments](netboxfhrpgroupassignment.md#ownership).

## `status`

Identical to every other kind. `ipam.FHRPGroup` is a `PrimaryModel`, so the provenance stamp
applies in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | always — this kind has no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`auth_key` appears in no condition and no status field, because it is in no payload.

## Printer columns

```
NAME      PROTOCOL   GROUP   ID   READY   AGE
vrrp-10   vrrp3      10      64   True    2m
```

| Column | JSONPath |
|---|---|
| `PROTOCOL` | `.spec.protocol` |
| `GROUP` | `.spec.groupId` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Stuck naming two ids | `Ready=False`, `Reason=Conflict` | two NetBox groups share `(protocol, group_id)`; nothing in the database prevents it | remove one in NetBox, or manage this group by hand |
| `protocol` rejected by `kubectl apply` | admission | a value outside the seven | the enum is closed and cannot be extended — check the spelling |
| The MD5 key never appears in NetBox | none; `Ready=True` | `auth_key` is not a field on this kind | set it in NetBox directly; the operator will not clear it |
| Deleting the group removed assignments the cluster did not own | none | `on_delete=CASCADE` on `ipam.FHRPGroupAssignment.group` | keep the default `deletionPolicy: Retain` |

## Related

- [`NetBoxFHRPGroupAssignment`](netboxfhrpgroupassignment.md) — the join row, and the `CASCADE`
  that makes this group its owner
- [`NetBoxIPAddress`](netboxipaddress.md) — whose `assignedObject` can be this group
- [`NetBoxService`](netboxservice.md) — whose `parent` can be this group
- [generic references](genericref.md) — the union shape both of those use
