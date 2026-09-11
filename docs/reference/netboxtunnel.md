# `NetBoxTunnel`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxTunnel` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbtunnel` |
| Status subresource | yes |

A `NetBoxTunnel` is one `vpn.Tunnel` in NetBox: a point-to-point overlay between two places,
with an encapsulation, a lifecycle state and — for the encrypted ones — the
[`NetBoxIPSecProfile`](netboxipsecprofile.md) that protects it. It is written to `vpn/tunnels`.

Two things about this Kind are worth knowing before the field list.

## Its identity depends on whether you name a group

`vpn.Tunnel` carries **two** `UniqueConstraint`s
(`docs/netbox-schema.md` → `vpn.Tunnel.meta.constraints`):

```python
models.UniqueConstraint(fields=('group', 'name'), name='..._group_name')
models.UniqueConstraint(fields=('name',),         name='..._name',
                        condition=Q(group__isnull=True))
```

So a tunnel *in* a group is identified by `(group, name)`, and a tunnel in **no** group by `name`
alone — but only among the tunnels that are in no group. The lookup for the second one therefore
sends `?group_id=null` rather than omitting the filter, and
[declaring `groupRef`](#specgroupref) is what decides which of the two applies. That is the same
shape [`NetBoxRack`](netboxrack.md#natural-keys) has, on a model that is not a tree.

Unusually, both candidates are backed by the database *and* by a third fact: `name` also carries
a column-level `UNIQUE` at 4.6.8 (`docs/netbox-schema.md` → `vpn.Tunnel`,
`name CharField REQ UNIQUE len=100`; `hack/testdata/ir-4.6.8.json.gz` → `vpn.Tunnel.fields`,
`"sql": {"unique": true}`). That makes the pair strictly narrower than the column, so neither
candidate can match more than one row, and a tunnel renamed into a name another tunnel holds is
NetBox's own 409 rather than an adoption. See [Natural keys](#natural-keys).

## Terminations are not here

`vpn.TunnelTermination` is a separate NetBox model with its own endpoint, and it is **not** part
of this release — neither as its own Kind nor as an inline list on this one. Its identity is
`(termination_type, termination_id)` over a generic foreign key, which is a different piece of
machinery from anything on this spec.

Eight of the `vpn` app's ten models ship as Kinds. The two that do not are
`vpn.TunnelTermination` and `vpn.L2VPNTermination`, deferred by the pull request for #59 for
exactly that reason.

A tunnel declared here is a complete, adoptable `vpn.Tunnel`: what terminates on it is set in
NetBox until the termination Kind ships, and the operator neither writes nor removes those rows.
`terminations` is not a column on this model at all — it is the reverse accessor of
`TunnelTermination.tunnel`, which is where the write happens.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTunnel
metadata:
  name: rotterdam-hq
  namespace: default
spec:
  endpointRef: homelab
  name: rotterdam-hq
  # Required: NetBox declares no default, and an encapsulation invented here would land in
  # every tunnel the operator adopted.
  encapsulation: ipsec-tunnel
```

With no `groupRef`, this tunnel is looked up as `?name=rotterdam-hq&group_id=null` — the tunnel
of that name that is in no group.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTunnel
metadata:
  name: rotterdam-hq
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: rotterdam-hq
  status: active              # default
  encapsulation: ipsec-tunnel

  # Declaring this changes which natural key applies: with a group the tunnel is looked up as
  # `(group_id, name)`, without one as `name` with `?group_id=null` pinned.
  groupRef:
    name: branch-offices

  ipsecProfileRef:
    name: branch-esp
  tenantRef:
    name: acme

  tunnelId: 100

  description: Rotterdam branch to headquarters
  comments: |
    Terminations are set in NetBox: vpn.TunnelTermination is not a Kind yet (#59).
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `status` | `string` | no | `active` | enum: `planned`, `active`, `disabled` | `status CharField len=50` |
| `groupRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxTunnelGroup` | no | — | ref arity CEL | `group ForeignKey -> vpn.TunnelGroup on_delete=PROTECT` |
| `encapsulation` | `string` | yes | — | enum: `ipsec-transport`, `ipsec-tunnel`, `ip-ip`, `gre`, `wireguard`, `openvpn`, `l2tp`, `pptp` | `encapsulation CharField REQ len=50` |
| `ipsecProfileRef` | `ObjectRef` → `NetBoxIPSecProfile` | no | — | ref arity CEL | `ipsec_profile ForeignKey -> vpn.IPSecProfile on_delete=PROTECT` |
| `tenantRef` | `ObjectRef` → `NetBoxTenant` | no | — | ref arity CEL | `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT` |
| `tunnelId` | `integer` | no | — | ≥0 | `tunnel_id PositiveBigIntegerField` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

### `spec.name`

Required, 1–100 characters, and half of this kind's identity. Column-unique in NetBox on top of
both constraints, so a duplicate is a 409 rather than a second row.

Editing it does not rename the NetBox tunnel — see
[renaming, or moving groups, changes identity](#renaming-or-moving-groups-changes-identity).

### `spec.status`

The tunnel's lifecycle state, defaulted to NetBox's own default so the operator manages the field
from the first reconcile — a defaulted field that never reaches a payload is a field the operator
can never correct. NetBox returns it as `{"value":"active","label":"Active"}` and accepts the
bare value; the differ compares the value ([drift](../concepts/drift.md)).

Three members, read from `netbox/vpn/choices.py:10` in the 4.6.8 tree the digest was taken from:

- `planned` — designed but not yet configured;
- `active` — in service, and NetBox's own default;
- `disabled` — configured but administratively down.

**This `ChoiceSet` is extensible, and the enum here is not.** `TunnelStatusChoices` declares
`key = 'Tunnel.status'` (`hack/testdata/ir-4.6.8.json.gz` → `enums.TunnelStatusChoices`), so a
deployment can add values through `FIELD_CHOICES`; a value added there **needs this CRD's enum
widened**, because a CRD cannot read a NetBox setting. That is the same statement
[`NetBoxRack`](netboxrack.md#specstatus) makes about `Rack.status` and
[`NetBoxVLAN`](netboxvlan.md) makes about `VLAN.status`. The symptom is a `kubectl apply`
rejected on `status` naming a value your NetBox accepts in the UI.

Note that these are not [`NetBoxL2VPN`](netboxl2vpn.md#specstatus)'s three: `disabled` is not
`decommissioning`, and `planned` is the only value the two sets share. They are two separate
`ChoiceSet`s, which is why they are two enums.

### `spec.groupRef`

The tunnel group this tunnel is filed under, and **the most load-bearing optional field in this
spec**: declaring it moves the object from one natural-key candidate to the other.

- Declared and resolved → candidate 1, `?group_id=<id>&name=<name>`.
- Not declared → candidate 2, `?name=<name>&group_id=null`.
- Declared and **not** resolved → *neither*. `NaturalKey.Applicable` offers candidate 2 only
  while `groupRef` is undeclared, so a tunnel whose group has not been created yet waits rather
  than adopting a groupless tunnel of the same name and PATCHing this group onto it
  ([lookups](../concepts/lookups.md)). The condition is `Ready=False, Reason=WaitingForRef`, then
  `WaitingForKey` if the reference resolves but no candidate applies.

**If it is wrong.** `RefsResolved=False` with `RefNotFound`, `RefNotReady`, `RefDenied`,
`RefAmbiguous` or `RefTargetFailed` naming the field, and nothing written.

`PROTECT` rather than `CASCADE`, so this is an ordinary reference and not a containment parent —
see [no containment parent anywhere on this kind](#no-containment-parent-anywhere-on-this-kind).

A pointer to a typed alias, so it has **two** states rather than three: absent means unmanaged,
and a value claims the column. Moving a tunnel out of a group from a manifest is not expressible
today; clear the column in NetBox and stop declaring the field.

### `spec.encapsulation`

Required. How the tunnel wraps the traffic it carries. Eight members from
`netbox/vpn/choices.py:24` at 4.6.8: `ipsec-transport`, `ipsec-tunnel`, `ip-ip`, `gre`,
`wireguard`, `openvpn`, `l2tp`, `pptp`.

Closed — the class declares no `key`, so no deployment's `FIELD_CHOICES` can add a member this
enum would reject (`hack/testdata/ir-4.6.8.json.gz` → `enums.TunnelEncapsulationChoices`). Unlike
`status`, this one really cannot drift under you.

No `""` member and no default: the column is `REQ` with no Django default, and choosing one here
would put an encapsulation into every tunnel the operator adopted.

### `spec.ipsecProfileRef`

The IPSec profile this tunnel is protected by. Optional: a GRE or WireGuard tunnel has no IPSec
profile, and NetBox's own `clean()` is the authority on which encapsulations require one — this
schema models no crypto rules and no cross-field encapsulation rule.

`PROTECT`, so deleting a profile a tunnel still uses is refused on the *profile*.

**If it is wrong.** `RefsResolved=False` naming the field, `Ready=False, Reason=WaitingForRef`,
and nothing written — a declared reference that does not resolve withholds the whole payload
([references](../concepts/references.md#a-declared-reference-that-does-not-resolve-writes-nothing)).

### `spec.tenantRef`

The tenant this tunnel belongs to. Ordinary reference, `PROTECT`, and **not** part of the
identity: neither constraint names `tenant`, so two tenants cannot both hold a tunnel of the same
name and the lookup does not filter on it.

`tenantRef` does not cascade and contributes no owner reference — see
[`NetBoxTenant`](netboxtenant.md), and [references](../concepts/references.md) for why a namespace
does not imply a tenant.

### `spec.tunnelId`

The tunnel's numeric identifier as configured on the devices — a VNI, a key, an ifindex.

A pointer, so omitting it leaves NetBox's value alone rather than clearing it, and so that `0` is
distinguishable from unset. An `int64` because the column is a `PositiveBigIntegerField`:
NetBox's choice of column width says not to rely on a VNI fitting an `int32`. The only bound is
`minimum: 0`, from the column's own positivity.

**Not part of the identity, and deliberately**: no constraint names it, so two tunnels may
legitimately carry the same number, and adopting by it would rewrite the wrong tunnel's group and
encapsulation.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second — a `TextField` has no `max_length`. Both
inherited from `PrimaryModel`, and both clearable: omit one to leave NetBox's own value alone,
set it to `""` to clear it ([field ownership](../concepts/field-ownership.md)).

## Natural keys

Two candidates, and only ever **one** of them applicable to a given object, because they disagree
about whether `groupRef` is declared:

| # | Candidate | Query | Applicable when | Backed by |
|---|---|---|---|---|
| 1 | `(group, name)` | `?group_id=<id>&name=<name>` | `groupRef` is declared and resolved | `vpn_tunnel_group_name` |
| 2 | `name` with `group` null | `?name=<name>&group_id=null` | `groupRef` is **never declared** | `vpn_tunnel_name`, `condition=Q(group__isnull=True)` |

Both filters are registered on `TunnelFilterSet` (NetBox 4.6.8, `netbox/vpn/filtersets.py:40`):
`name` is in `Meta.fields` and `group_id` is declared as a `ModelMultipleChoiceFilter`.

**Candidate 2 pins `group_id=null` rather than leaving the filter out.** Candidates are tried in
order and the engine falls through when one matches nothing, so an unpinned name-only candidate
would match a tunnel of that name inside somebody else's group, adopt it, and the follow-up PATCH
would move it out (#206, #216). The pin makes candidate 2 the identity of a *different* object:
the tunnel of this name that is in no group. Same reasoning as a top-level
`dcim.Region` ([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

`?group_id=null` is the wire spelling of `registry.NullColumnRef`, and it is the spelling
django-filter's `null_value` sentinel accepts on a `ModelMultipleChoiceFilter` — the same
mechanism [`NetBoxLocation`](netboxlocation.md)'s `parent_id` and
[`NetBoxRack`](netboxrack.md)'s `location_id` are already pinned with (#216). The committed IR
marks this candidate *unusable* with a reason about a missing `__empty` suffix; that suffix is
not the spelling used here, and [coverage](../coverage.md) records the constraint as
`usable via #216`, one of the seventeen. Emitting `__empty` instead would be the #206 defect,
which is why the choice is made in one place.

**More than one match is impossible here**, which is the difference from
[`NetBoxRack`](netboxrack.md): `name` is column-unique at 4.6.8, so candidate 2 cannot return two
rows and candidate 1 is strictly narrower still. A `Conflict` on this Kind means another
namespace claimed the object, not that NetBox holds two.

`tunnel_id` is deliberately not a candidate — see [`spec.tunnelId`](#spectunnelid).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

`status.naturalKey` is the field to read first on this Kind: it records *which* candidate located
the object, filter by filter, so `{"name": "...", "group_id": "null"}` tells you the engine
treated the object as the groupless tunnel of that name.

`vpn.Tunnel` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is stamped in
full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the tunnel exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | every declared reference resolves | any does not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`WaitingForKey` is the one worth naming here — it is what a tunnel reports when its references
resolve but no candidate is applicable, which on this Kind means `groupRef` is declared and still
pending.

## Kind-specific behaviour

### No containment parent anywhere on this kind

Every foreign key on `vpn.Tunnel` is `PROTECT` — `group`, `ipsec_profile`, `tenant`
(`docs/netbox-schema.md` → `vpn.Tunnel`) — so none qualifies as a containment parent under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4, and this Kind takes **no owner
reference from anything**. `registry.ErrContainmentNotCascade` refuses such a descriptor at boot,
and rightly: NetBox refuses to delete a tunnel group that still has tunnels, so a cluster-side
cascade would delete the CR and leave the row.

The consequence is worth stating plainly: `kubectl delete netboxtunnelgroup branch-offices` does
**not** garbage-collect the tunnels in it. It fails on the group, with
`Deleting=False, Reason=Protected` naming the blocker. Delete the tunnels first.

### Renaming, or moving groups, changes identity

Both `name` and `groupRef` are in the lookup, so editing either does not rename or move the
NetBox tunnel — it changes what the CR is looking for. The next reconcile finds nothing and
creates a second tunnel, leaving the first behind. Adding a `groupRef` to a tunnel that had none
switches it from candidate 2 to candidate 1, which is the same thing.

`status`, `encapsulation`, `ipsecProfileRef`, `tenantRef`, `tunnelId`, `description` and
`comments` are safe to edit.

### `terminations_count` is never written

`terminations_count` is a counter NetBox maintains from the termination rows, it is in the
serializer's write path and read-only there
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.Tunnel.write_path`), and it is in the descriptor's
read-only list. NetBox **ignores** a write to it rather than refusing one, so an unguarded field
map would produce a difference the next reconcile finds again, and PATCH forever.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A tunnel is configuration a manifest recreates
verbatim; deleting one frees no resource anybody else can take. See
[deletion](../concepts/deletion.md).

Deleting a tunnel **does** cascade server-side to its terminations —
`vpn.TunnelTermination.tunnel` is `on_delete=CASCADE` — and those rows are ones no CR describes
yet. That is a reason to reach for `deletionPolicy: Retain` on a tunnel whose terminations were
set by hand.

### What is not here yet

- **`vpn.TunnelTermination`.** Not a Kind, not an inline list — see
  [terminations are not here](#terminations-are-not-here). Its `outside_ip` and its generic
  `(termination_type, termination_id)` pair are what make it a separate piece of work (#59).
- **`contacts`** is a `GenericRelation`, the far end of somebody else's foreign key.
- **`owner`.** `ForeignKey -> users.Owner`, and `users/*` is an excluded endpoint.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbtunnel
NAME           STATUS   ENCAPSULATION   ID    READY   AGE
rotterdam-hq   active   ipsec-tunnel    101   True    3m
lab-gre        planned  gre             102   True    3m
```

| Column | JSONPath |
|---|---|
| `STATUS` | `.spec.status` |
| `ENCAPSULATION` | `.spec.encapsulation` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

Both read the *spec*, so they show the intent even while a reference is unresolved and `ID` is
empty. There is no `GROUP` column: whether the field is set is visible in
`status.naturalKey`, which is the place that answers what it changed.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `encapsulation` | — | The field is required by the schema, because NetBox's column is `REQ` with no default. | Name one of the eight |
| `kubectl apply` rejected on `status` naming a value the NetBox UI accepts | — | `TunnelStatusChoices` declares `key = 'Tunnel.status'`, so your deployment extended it through `FIELD_CHOICES`. A CRD cannot read a NetBox setting. | Widen this CRD's enum, or use a listed value |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | A declared reference names an object that does not exist. | Create it, or fix the name |
| `Ready=False`, `Reason=WaitingForKey` | `RefsResolved=True` | `groupRef` is declared and still pending, so no candidate is applicable. The engine is waiting on purpose. | Wait, or drop `groupRef` |
| A second tunnel appeared after adding `groupRef` | — | Declaring a group switches the lookup from candidate 2 to candidate 1. | Move the tunnel into the group in NetBox first, or delete and re-create the CR |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this tunnel. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner |
| `Ready=False`, `Reason=Invalid`, NetBox message about `name` | `Ready` | A rename collided with the column-level `UNIQUE` on `name`, which is global. | Pick another name |
| Deleting the tunnel group is refused | on the *group*: `Deleting=False`, `Reason=Protected` | `Tunnel.group` is `PROTECT`, and this Kind takes no owner reference for exactly that reason. | Delete the tunnels first |
| Terminations vanished after `kubectl delete` | — | `vpn.TunnelTermination.tunnel` is `CASCADE`, and no CR describes those rows. | Use `deletionPolicy: Retain` on tunnels whose terminations are hand-made |
| `spec.terminations` rejected by `kubectl apply` | — | There is no such field. | See [terminations are not here](#terminations-are-not-here) |

## Related

- [`NetBoxTunnelGroup`](netboxtunnelgroup.md) — the label half of this kind's identity
- [`NetBoxIPSecProfile`](netboxipsecprofile.md) — what protects an IPSec tunnel, and what the crypto catalogue builds up to
- [`NetBoxL2VPN`](netboxl2vpn.md) — the other `vpn` kind that ships, and the other one whose terminations are deferred
- [`NetBoxRack`](netboxrack.md) — the same conditional-identity shape, with a convention key instead of a constraint
- [`NetBoxTenant`](netboxtenant.md) — the `PROTECT`ed tenant, and why it takes no owner reference
- [Lookups](../concepts/lookups.md) — candidate order, and why a null filter is pinned rather than omitted
- [Deletion](../concepts/deletion.md) — what `PROTECT` and `CASCADE` do to a delete
