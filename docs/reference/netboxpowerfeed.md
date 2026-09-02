# `NetBoxPowerFeed`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxPowerFeed` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbpowerfeed` |
| Status subresource | yes |

A `NetBoxPowerFeed` is one `dcim.PowerFeed` in NetBox: one circuit from a
[`NetBoxPowerPanel`](netboxpowerpanel.md), optionally serving a rack.

**It is the first kind in the catalogue whose defaults are not the model's own.** `voltage`,
`amperage` and `max_utilization` default to `ConfigItem('POWERFEED_DEFAULT_VOLTAGE')` and
friends — read from the *target NetBox's* configuration at write time, not from the model. So
those three fields carry **no CRD default**, and omitting one means "whatever this NetBox is
configured for" rather than "whatever the operator guessed". That is the whole of
[server-side defaults](#server-side-defaults), and it is the only genuinely new behaviour this
Kind introduces.

It is also the first power object that can terminate a cable. See
[cable terminations](#a-feed-is-a-legal-cable-termination).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPowerFeed
metadata:
  name: feed-a1
  namespace: default
spec:
  endpointRef: homelab
  name: Feed A1
  powerPanelRef:
    name: panel-a
```

Four enums default to NetBox's own values (`active`, `primary`, `ac`, `single-phase`) and the
three electrical figures are left to the installation.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPowerFeed
metadata:
  name: feed-a1
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Feed A1
  powerPanelRef:
    name: panel-a
  rackRef:
    name: a1
  tenantRef:
    name: platform

  status: active              # offline | active | planned | failed
  type: primary               # primary | redundant
  supply: ac                  # ac | dc
  phase: single-phase         # single-phase | three-phase

  # Set these only when this installation's own configured defaults are wrong for
  # *this* feed. See "Server-side defaults" below before you do.
  voltage: 230
  amperage: 32
  maxUtilization: 75

  markConnected: false
  description: A feed for rack A1
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | yes | — | `name`, `CharField REQ len=100` |
| `powerPanelRef` | [reference](../concepts/references.md) → [`NetBoxPowerPanel`](netboxpowerpanel.md) | yes | — | `power_panel`, `ForeignKey REQ -> dcim.PowerPanel on_delete=PROTECT` |
| `rackRef` | reference → [`NetBoxRack`](netboxrack.md) | no | — | `rack`, `ForeignKey -> dcim.Rack on_delete=PROTECT` |
| `tenantRef` | reference → [`NetBoxTenant`](netboxtenant.md) | no | — | `tenant`, `ForeignKey -> tenancy.Tenant on_delete=PROTECT` |
| `status` | enum | no | `active` | `status`, `CharField len=50 choices=PowerFeedStatusChoices` |
| `type` | enum | no | `primary` | `type`, `CharField len=50 choices=PowerFeedTypeChoices` |
| `supply` | enum | no | `ac` | `supply`, `CharField len=50 choices=PowerFeedSupplyChoices` |
| `phase` | enum | no | `single-phase` | `phase`, `CharField len=50 choices=PowerFeedPhaseChoices` |
| `voltage` | `int32`, −32768…32767 | no | **none — see below** | `voltage`, `SmallIntegerField def=ConfigItem('POWERFEED_DEFAULT_VOLTAGE')` |
| `amperage` | `int32`, 0…32767 | no | **none — see below** | `amperage`, `PositiveSmallIntegerField def=ConfigItem('POWERFEED_DEFAULT_AMPERAGE')` |
| `maxUtilization` | `int32`, 0…32767 | no | **none — see below** | `max_utilization`, `PositiveSmallIntegerField def=ConfigItem('POWERFEED_DEFAULT_MAX_UTILIZATION')` |
| `markConnected` | `bool` | no | — | `mark_connected` (`CabledObjectModel`), `BooleanField def=False` |
| `description` | `string`, ≤200 | no | — | `description` (`PrimaryModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`PrimaryModel`), `TextField` |

### `spec.name`

Required, up to 100 characters. Unique **per power panel**
(`docs/netbox-schema.md` → `dcim.PowerFeed`, `meta.constraints`), so `Feed A1` on two panels is
legitimate NetBox state and two on one panel is not.

### `spec.powerPanelRef`

Required, because NetBox's column is `REQ`.

It is half the natural key, so until it resolves the object reports `RefsResolved=False` naming
this field and **makes no NetBox write at all**. That matters more here than on most kinds:
`name` alone is unique nowhere in `dcim/power-feeds`, so a lookup with the panel dropped would
match every feed of that name in the whole NetBox and adopt one
([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206),
[#216](https://github.com/ricardomolendijk/netbox-operator/issues/216)).

**Not a containment reference.** `PROTECT`, so NetBox refuses to delete a panel while a feed
points at it and there is no server-side cascade for an owner reference to mirror
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). Deleting the
[`NetBoxPowerPanel`](netboxpowerpanel.md) CR reports `Deleting=False, Reason=Protected` on the
*panel*; delete the feeds first.

### `spec.rackRef`

Optional, and in no natural-key candidate: NetBox constrains nothing on it, so two feeds of one
name on one panel are refused however their racks differ. `PROTECT`, so not a containment parent
either.

Two states, as [`NetBoxPowerPanel`'s `locationRef`](netboxpowerpanel.md#speclocationref)
explains: absent means unmanaged, and a value claims the column.

### `spec.tenantRef`

Optional. Not a containment parent and never a cascade — see
[`NetBoxTenant`](netboxtenant.md) on why `tenantRef` does not cascade, and
[references](../concepts/references.md) on why a namespace does not imply a tenant.

### `spec.status`, `spec.type`, `spec.supply`, `spec.phase`

Four closed enums, and the deliberate contrast with the three numbers below: **these are
defaulted and those are not.**

| Field | Values | NetBox default | Extensible? |
|---|---|---|---|
| `status` | `offline`, `active`, `planned`, `failed` | `active` | yes — `key = 'PowerFeed.status'` |
| `type` | `primary`, `redundant` | `primary` | no |
| `supply` | `ac`, `dc` | `ac` | no |
| `phase` | `single-phase`, `three-phase` | `single-phase` | no |

Their column defaults are model-level *constants* rather than `ConfigItem` lookups, so the value
is the same on every NetBox and there is nothing to guess. Restating each at NetBox's own default
means the operator manages the field from the first reconcile — a defaulted field that never
reaches a payload is a field the operator can never correct.

Three of the four ChoiceSets are `extendable: false`, so a deployment cannot widen them at all
(`hack/testdata/ir-4.6.8.json.gz` → `enums`). `PowerFeedStatusChoices` can be widened through
`FIELD_CHOICES`, and is enumerated anyway on the same reasoning as
[`NetBoxSite`](netboxsite.md)'s and [`NetBoxRack`](netboxrack.md)'s status: a typo caught by
`kubectl apply` is worth more than an extension nobody has made, and widening the enum is a
one-line change.

None of the four has an `""` member. All four columns are `NOT NULL` with a default
(`nullable: false` on each in the IR), so "unspecified" is not a state NetBox can hold. Combined
with `omitempty`, writing `status: ""` therefore drops the key and the API server's own default
fills it — the opposite of [`NetBoxRack`'s `airflow`](netboxrack.md), where `""` is a real state
cleared as `null`.

`supply` is worth one more line: it is what makes a negative `voltage` meaningful, which is why
that column is a `SmallIntegerField` and not a `PositiveSmallIntegerField` like its two
neighbours.

### `spec.voltage`, `spec.amperage`, `spec.maxUtilization`

**Optional, and deliberately carrying no default.** See
[server-side defaults](#server-side-defaults) for the whole argument; in short, omitting one
means "whatever this NetBox is configured for" and setting one writes and drift-corrects it like
any other column.

Removing a value from a manifest **stops managing** the field; it does not clear it. Pointer
fields are exempt from the empty-value restoration that makes `description: ""` clear a
description, because a nil pointer is already a state of its own
(`internal/reconciler/ownership.go`, `restoreEmpty`).

The bounds are the Django field's own — a `SmallIntegerField` is a Postgres `smallint`, and
`PositiveSmallIntegerField` is the unsigned half of the same range. NetBox's model carries
validators beyond the column type; **none of them are recorded in any committed artefact**
(`hack/testdata/ir-4.6.8.json.gz` records `sql` and `api` metadata and no `validators` key at
all), so they are not restated here as CRD bounds that could be wrong. `maxUtilization` is the
visible consequence: it means a percentage and the CRD lets you write 500, because 500 is
outside NetBox's range and *not* outside the column's, and only the second is checkable. NetBox
answers 400 and the operator reports `Ready=False, Reason=Invalid`.

### `spec.markConnected`

Optional. Marks the feed as connected without a cable.

A pointer, and for a different reason from `voltage`'s: the default here is the model's own
literal `False`, so the risk is not a wrong default but an unmanaged one. A plain bool cannot
tell "not managed" from "managed as false", so adopting a feed a human had marked connected would
silently unmark it on the first reconcile. Nil leaves NetBox's value alone; `false` writes false.
The same choice [`NetBoxInterface`](netboxinterface.md) makes for the same column on the same
base class.

### `spec.description`, `spec.comments`

Optional free text; `description` is capped at 200 characters and `comments` is a `TextField`
with no `max_length` to derive one from.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Server-side defaults

The design note this Kind exists to get right.

Three of NetBox's own columns do not default to a value in the model:

```
voltage          SmallIntegerField          def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_VOLTAGE')
amperage         PositiveSmallIntegerField  def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_AMPERAGE')
max_utilization  PositiveSmallIntegerField  def=UNRESOLVED:ConfigItem('POWERFEED_DEFAULT_MAX_UTILIZATION')
```

(`docs/netbox-schema.md` → `dcim.PowerFeed`. All three carry `default_unresolved: true` in
`hack/testdata/ir-4.6.8.json.gz`, which is the extractor saying "the AST walk could not evaluate
this default" — because it is not a value, it is a lookup.) A `ConfigItem` is read from the
*target NetBox's* configuration at write time. NetBox ships 120 V; an installation in Europe is
configured for 230. There is no value the CRD could carry that would be right on both.

So the rule is:

- **Omit the field** → the request body carries no such key at all, NetBox applies its own
  configured value, and no later reconcile reports drift against it.
- **Set the field** → it is written on create and corrected on every pass, like any other column.

Baking `120`/`15`/`80` into the CRD instead would silently reconfigure every feed on an
installation configured for anything else — and it would do it *on every resync*, because the
operator would find the same difference each time. That is the hot loop
[drift](../concepts/drift.md) opens by warning about, with a real-world consequence attached.

Nothing in the engine implements this. It is four existing properties lining up, and each one
matters:

1. the CRD carries no `+kubebuilder:default`, so the API server adds nothing on create;
2. the Go fields are `*int32`, so a nil marshals to nothing and the key never enters the spec map;
3. `payload.desired` skips a spec key with no value, so it never reaches a request body;
4. `netbox.Drift` considers only fields present in the desired object, so an unset field is never
   compared against what NetBox holds;

and, holding it all up, `specFields.restoreEmpty` deliberately has no empty form for a pointer
type — so a *claimed but unset* `voltage` is not restored as `0`, which is a value NetBox would
store. `internal/reconciler/dcim_powerfeed_test.go` pins each of the five separately rather than
only the end-to-end result, because any one of them changing would break this silently.

### `available_power` is not reported

NetBox derives `available_power` from `voltage`, `amperage` and `phase` in `clean()`, and
NBO-052 asks for it in `status`. It is **not** shipped, and the reason is a gap in the evidence
rather than a decision about the field:

- it is absent from `PowerFeedSerializer.fields` at 4.6.8
  (`hack/testdata/api-schema-4.6.8.json.gz` → `serializers.PowerFeedSerializer`);
- it is absent from `dcim.PowerFeed.write_path` (`hack/testdata/ir-4.6.8.json.gz`).

So there is no committed evidence the REST API returns it at all, and a `status.availablePower`
promising a value the API never sends would report `0` forever. Confirming it needs a read
against a live NetBox; until then the column is in the descriptor's read-only list, which is
where "this column exists and the operator must never touch it" is recorded, and
`TestPowerFeedAvailablePowerIsNeverWritten` is the test that should change if the answer turns
out to be yes.

## Natural keys

One candidate, no fallback and no null pin:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(power_panel, name)` | `?power_panel_id=<id>&name=<name>` | `powerPanelRef` has resolved |

A real database constraint, checkable against two committed artefacts:

```
meta.constraints: (models.UniqueConstraint(fields=('power_panel', 'name'),
   name='%(app_label)s_%(class)s_unique_power_panel_name'),)
```

(`docs/netbox-schema.md` → `dcim.PowerFeed`.) `hack/testdata/ir-4.6.8.json.gz` →
`dcim.PowerFeed.natural_keys` resolves it to `{column: power_panel, filter: power_panel_id}` and
`{column: name, filter: name}`, with `null_fields: []` and `unusable: null`; both filters are
registered on `PowerFeedFilterSet`.

`rack` and `tenant` are optional and unconstrained, so neither is a candidate and neither is a
pin. `power_panel` is `REQ`, so every feed satisfies the constraint and an ambiguous match is
impossible rather than merely reported — the same shape as
[`NetBoxPowerPanel`](netboxpowerpanel.md#natural-keys), and the opposite of
[`NetBoxRack`](netboxrack.md#natural-keys).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

There is no `availablePower` — see [above](#available_power-is-not-reported).

`dcim.PowerFeed` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the feed exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForRef`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `powerPanelRef` and any `rackRef`/`tenantRef` resolved | any has not | `AllResolved`, `WaitingForRef`, `RefNotFound`, `RefNotReady`, `RefKindUnavailable`, `RefForbidden`, `RefCycle` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### A feed is a legal cable termination

`dcim.PowerFeed` mixes in `CabledObjectModel`, so a [`NetBoxCable`](netboxcable.md) may terminate
on one through the `powerFeedRef` member of its termination union.

Every part of that was already in the tree before this Kind: `dcim.powerfeed` is in
`registry.cabledObjectTypes()`, `powerFeedRef` is a member of both ends of the union, and the
`PowerFeedRef` alias already named the target Kind — all declared ahead of the Kind by NBO-049,
deliberately. What was missing was the Descriptor those declarations point at.
`internal/resolver` dispatches **every** mode — `name`, `slug`, `lookup` and `id` alike — through
`Descriptors.Get(Field.Target)` to learn the endpoint, so until this Kind was registered a cable
terminating on a feed reported `RefKindUnavailable` in every mode except `id`.

Registering this Kind is therefore the whole of that change; nothing in `dcim_cable.go` moved.
`dcim.powerport` and `dcim.poweroutlet` are still in the same position and still `id`-only —
they land with the second power PR.

The cascade runs the *other way*, and the union declares no cascade for that reason:
`CabledObjectModel.cable` is `SET_NULL`, so deleting the feed clears the feed's own `cable`
column and leaves the cable standing.

### The cable columns belong to the cable

`cable`, `cable_end`, `cable_connector`, `cable_positions` and `cable_terminations` are writable
columns of this model that are nevertheless read-only here:
[`NetBoxCable`](netboxcable.md) owns the cable graph, and a cable is created from its own
endpoints rather than by a feed claiming one. A feed that adopted an already-cabled row must not
PATCH the cable away. `_path`, `_occupied`, `link_peers` and `connected_endpoints` are computed
over that graph rather than columns at all. The same treatment
[`NetBoxInterface`](netboxinterface.md) gives the same columns off the same base class.

### No containment parent

Every foreign key on the model is `PROTECT` bar `owner`, so nothing on the server side disappears
when a parent does ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

### `deletionPolicy` defaults to `Delete`

As on every Kind since
[#304](https://github.com/ricardomolendijk/netbox-operator/issues/304). A feed is configuration
a manifest recreates: deleting one frees no allocation and destroys no record of who held one —
the circuit is still there, and the manifest is what says so. See
[deletion](../concepts/deletion.md).

### Renaming changes identity

`name` and `powerPanelRef` are the natural key, so editing either does not rename or move the
NetBox feed — it changes what the CR is looking for, and the next reconcile creates a second
feed. Everything else is safe to edit.

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the `users` app is an excluded endpoint. `tags` and
`customFields` are written by the provenance stamp. `available_power` is
[discussed above](#available_power-is-not-reported).

## Printer columns

```
$ kubectl get nbpowerfeed
NAME      PANEL     STATUS   TYPE        ID    READY   AGE
feed-a1   panel-a   active   primary     201   True    2m
feed-b1   panel-b   active   redundant   202   True    2m
```

| Column | JSONPath |
|---|---|
| `PANEL` | `.spec.powerPanelRef.name` |
| `STATUS` | `.spec.status` |
| `TYPE` | `.spec.type` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `RefsResolved=False`, `Reason=RefNotFound`, message names `powerPanelRef` | `RefsResolved` | No [`NetBoxPowerPanel`](netboxpowerpanel.md) of that name in the namespace. Nothing was written to NetBox. | Apply the panel; the feed re-enqueues off its event. |
| The voltage in NetBox is not what the manifest expected | — | The manifest does not set `voltage`, so NetBox applied `POWERFEED_DEFAULT_VOLTAGE` from *its own* configuration. | Set `spec.voltage` explicitly, or change the installation's configuration. See [server-side defaults](#server-side-defaults). |
| `Ready=False`, `Reason=Invalid`, message names `max_utilization` | `Ready` | The value is inside the CRD's column-derived bound and outside NetBox's own validator range. | Use a percentage NetBox accepts. See [`spec.maxUtilization`](#specvoltage-specamperage-specmaxutilization). |
| `Ready=False`, `Reason=Conflict` | `Ready` | A feed with this `(power_panel, name)` already exists and `onConflict` is `Fail`, or another namespace owns it. | Set `onConflict: Adopt` in the owning namespace, or rename. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | Something downstream still points at the feed. | Remove the dependent objects, or set `deletionPolicy: Retain`. |
| A cable naming this feed reports `RefKindUnavailable` | — | Should no longer happen; this Kind is registered. If it does, the cable is naming `powerPortRef` or `powerOutletRef` instead, which are still `id`-only. | Use `id` mode for ports and outlets until they ship. |
| No `status.availablePower` | — | Deliberate; the field is not in the 4.6.8 serializer. | See [above](#available_power-is-not-reported). |

## Related

- [`NetBoxPowerPanel`](netboxpowerpanel.md) — the required upstream, and where a refused delete is reported
- [`NetBoxRack`](netboxrack.md) — the optional `rackRef` target
- [`NetBoxCable`](netboxcable.md) — the termination union this Kind completes one member of
- [`NetBoxInterface`](netboxinterface.md) — the other `CabledObjectModel` kind, and the same read-only cable columns
- [Field ownership](../concepts/field-ownership.md) — why absent, empty and set are three states, and why a pointer only has two
- [Drift](../concepts/drift.md) — why an unset field is never compared
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
