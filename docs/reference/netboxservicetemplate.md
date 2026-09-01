# `NetBoxServiceTemplate`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxServiceTemplate` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbsvctpl` |
| Status subresource | yes |
| Lands with | NBO-055 (M10) |

A `NetBoxServiceTemplate` is one `ipam.ServiceTemplate` in NetBox: a reusable
protocol-and-ports definition a [service](netboxservice.md) can be stamped from.

It is [`NetBoxService`](netboxservice.md) minus the parent and the addresses, and the pair is
worth reading side by side. The same `protocol` and `ports` columns, from the same abstract base
`ipam.ServiceBase` — and an identity of the **opposite kind**: `name CharField REQ UNIQUE
len=100` here is a database guarantee, where `ipam.Service` has no `meta.constraints` at all and
a convention.

**NetBox keeps no link from a service back to the template it was stamped from.** The values are
copied at creation time, so this kind has no reverse relation and editing a template changes
nothing that already exists.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxServiceTemplate
metadata:
  name: ssh
  namespace: default
spec:
  endpointRef: homelab
  name: ssh
  protocol: tcp
  ports:
    - 22
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxServiceTemplate
metadata:
  name: ssh
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # Delete | Retain -- Retain is this kind's default

  name: ssh
  protocol: tcp               # tcp | udp | sctp
  ports:
    - 22
    - 2222

  description: Secure shell
  comments: Port 2222 is the jump host's alternate listener.
```

That is every field.

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope — see
[`NetBoxTag`](netboxtag.md#specendpointref).

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` |

`name CharField REQ UNIQUE len=100`, and this kind's natural key.

Globally unique in NetBox over namespaced CRDs: two namespaces cannot both own `ssh`, and the
loser reports `Ready=False, Reason=Conflict` naming the id it found.

**Matched exactly rather than case-insensitively**, and the distinction is load-bearing. The
constraint is a plain `unique=True` on the column, **not** a `UniqueConstraint` over
`Lower('name')` the way [`dcim.Device`](netboxvirtualmachine.md#natural-key)'s and
`virtualization.VirtualMachine`'s are. So `SSH` and `ssh` are two legal rows in NetBox, and a
`name__ie` lookup would adopt one for the other and `PATCH` somebody else's template.

### `spec.protocol`

| | |
|---|---|
| Type | `ServiceProtocol` |
| Required | **yes** |
| Validation | `Enum=tcp;udp;sctp` |

`protocol (ServiceBase) CharField REQ len=50 choices=ServiceProtocolChoices`.

The same enum type [`NetBoxService.spec.protocol`](netboxservice.md#specprotocol) uses, because
it is the same column on the same abstract base. The three values are read from
`netbox/ipam/choices.py` lines 175–185; that class declares no `key`, so the enum is closed and
cannot reject a value a deployment added.

### `spec.ports`

| | |
|---|---|
| Type | `[]int32` |
| Required | **yes** |
| Validation | `MinItems=1`, `MaxItems=256`, per item `Minimum=1`, `Maximum=65535` |

`ports (ServiceBase) ArrayField REQ`, bounded per element by `SERVICE_PORT_MIN = 1` and
`SERVICE_PORT_MAX = 65535` (`netbox/ipam/constants.py` lines 92–93).

Order is data, exactly as on [`NetBoxService`](netboxservice.md#specports) — a reorder is one
`PATCH` that converges, never a second object — and **not** part of the identity here: `name`
alone is.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`. Omit either
to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(name)` | `?name=` | `name CharField REQ UNIQUE len=100` |

`ipam.ServiceTemplate`'s only other table-level line is `meta.ordering: ('name',)`.

## Read-only columns

`_ports_lowest` is a cache NetBox recomputes from `ports` on every save
(`netbox/ipam/models/services.py` lines 41–47), so it is in `Descriptor.ReadOnly`.

## Deletion

**`deletionPolicy` defaults to `Retain`** (decision #176). The whole app takes the same default
rather than a per-kind judgement about how expensive each row is to lose — and a template is
genuinely cheap to recreate, which is worth saying out loud: this is the one kind in the group
where `deletionPolicy: Delete` is a reasonable thing to write explicitly.

## Ownership

**No containment parent.** `ipam.ServiceTemplate` declares no foreign key besides
`owner (OwnerMixin)`, which the operator does not map
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

## `status`

Identical to every other kind. `ipam.ServiceTemplate` is a `PrimaryModel`, so the provenance
stamp applies in full.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the template exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | always — this kind has no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Printer columns

```
NAME   NAME   PROTOCOL   PORTS       ID   READY   AGE
ssh    ssh    tcp        [22 2222]   93   True    1m
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
| Stuck on a name NetBox already has | `Ready=False`, `Reason=Conflict` | `name` is globally unique | pick another, or `onConflict: Adopt` |
| Two templates `ssh` and `SSH` both `Ready` | none | the constraint is `unique=True`, not `Lower('name')` | expected in NetBox; pick one convention in your manifests |
| Editing the template changed no service | none; `Ready=True` | NetBox copies the values and keeps no link | edit the services, or restamp them by hand |

## Related

- [`NetBoxService`](netboxservice.md) — the kind stamped from this one, with the opposite kind of
  identity
- [descriptor](../concepts/descriptor.md) — why two kinds sharing a base class are still two
  descriptors
