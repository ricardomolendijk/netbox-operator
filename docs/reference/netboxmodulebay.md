# `NetBoxModuleBay`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxModuleBay` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbmodulebay` |
| Status subresource | yes |

A `NetBoxModuleBay` is one `dcim.ModuleBay`: the slot a module goes into. A bay belongs to a
device directly — a front-panel slot on a switch — or to a module already installed in that
device, which is how NetBox represents a line card that provides slots of its own.

Three things about this Kind are worth knowing before the field list.

## NetBox declares this Kind's identity and does not enforce it

`dcim.ModuleBay` has a `meta.constraints` line, unlike [`NetBoxRack`](netboxrack.md)'s two and
[`NetBoxRackReservation`](netboxrackreservation.md)'s none, and it names three columns:

```python
models.UniqueConstraint(fields=('device', 'module', 'name'),
                        name='%(app_label)s_%(class)s_unique_device_module_name')
```

(`docs/netbox-schema.md` → `dcim.ModuleBay.meta.constraints`.) `module` in it is **nullable** —
`module ForeignKey -> dcim.Module on_delete=CASCADE`, no `REQ` — and Postgres treats `NULL`s as
distinct. So the constraint holds for a bay *on a module* and does nothing at all for a bay on
the chassis, which is the common case: two identically named `Slot 1`s on one switch are legal
server state.

This is the fourth Kind in that position, after [`NetBoxIPAddress`](netboxipaddress.md),
[`NetBoxContact`](netboxcontact.md) and [`NetBoxRack`](netboxrack.md), and it arrives there by a
route none of them takes. On a rack the constraints exist and are keyed on an optional *column
the CR may not set*; here the constraint exists, the CR always sets two of its three columns,
and the third is nullable. The handling is the same in both cases — a convention key with the
null pinned, and `Conflict` rather than a guess when more than one object matches. See
[Natural keys](#natural-keys).

## `moduleRef` is not the module in the bay

The single most confusable pair in the module block. `spec.moduleRef` is the module that
**provides** this bay; the module **installed in** it is the other direction entirely and is
[`NetBoxModule`](netboxmodule.md)'s own `spec.moduleBayRef`. NetBox spells the difference in the
schema, and the operator follows it exactly — see
[Which way round `module` points](#which-way-round-module-points).

## Its MPTT parent is derived, not written

`dcim.ModuleBay` is an `MPTTModel` and carries `parent TreeForeignKey -> dcim.ModuleBay`
(`docs/netbox-schema.md` → `dcim.ModuleBay`), and there is deliberately no `parentRef` on this
spec. The column is absent from the serializer's write path, because NetBox derives it. See
[`parent` is a consequence of `moduleRef`](#parent-is-a-consequence-of-moduleref).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModuleBay
metadata:
  name: slot-1
  namespace: default
spec:
  endpointRef: homelab
  # Required: dcim.ComponentModel.device is a REQ foreign key, and both natural-key candidates
  # read `device_id`. Until this resolves the object writes nothing at all.
  deviceRef:
    name: sw1
  name: Slot 1
```

`deviceRef` needs a [`NetBoxDevice`](netboxdevice.md) named `sw1` in this namespace, or a
[grant](netboxrefgrant.md) and a `namespace:` to reach one elsewhere.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModuleBay
metadata:
  name: slot-1
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail          # default
  deletionPolicy: Delete    # default: a bay is configuration, not allocated state

  deviceRef:
    name: sw1
  name: Slot 1

  # moduleRef is deliberately unset here. A bay on the chassis has `module IS NULL`, which is
  # the state the null-pinned (device, name) candidate covers. Setting it would mean the bay is
  # a slot on a line card, and would add module_id to the lookup -- see Natural keys.

  # The {module} token NetBox substitutes into the names of the components a module installed
  # here provides. A string, because the column is a CharField.
  position: "1"

  enabled: true
  label: SLOT1
  description: Front-panel module slot 1
```

A bay on a line card is the same object with one field more:

```yaml
spec:
  deviceRef:
    name: sw1
  name: Port 1
  # The module that PROVIDES this bay: the line card in slot-1, not whatever optic is plugged
  # into Port 1. The third column of the lookup, and the only way to declare a nested bay.
  moduleRef:
    name: slot-1-linecard
  position: "1/1"
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `customFields`.
See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `deviceRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxDevice` | yes | — | ref arity CEL | `device (ComponentModel) ForeignKey REQ -> dcim.Device on_delete=CASCADE` |
| `name` | `string` | yes | — | 1–64 | `name (ComponentModel) CharField REQ len=64` |
| `moduleRef` | `ObjectRef` → `NetBoxModule` | no | — | ref arity CEL | `module (ModularComponentModel) ForeignKey -> dcim.Module on_delete=CASCADE` |
| `label` | `string` | no | — | ≤64 | `label (ComponentModel) CharField len=64` |
| `position` | `string` | no | — | ≤30 | `position CharField len=30` |
| `enabled` | `boolean` | no | — | | `enabled BooleanField def=True` |
| `description` | `string` | no | — | ≤200 | `description (ComponentModel) CharField len=200` |

Seven fields, and **no `comments`**. `dcim.ComponentModel` is a plain `NetBoxModel` rather than a
`PrimaryModel` (`docs/netbox-schema.md` → `dcim.ComponentModel`, bases), so the long-form column
[`NetBoxModuleType`](netboxmoduletype.md) has does not exist here. `NetBoxModel` still mixes in
`TagsMixin` and `CustomFieldsMixin`, so the object is stamped in full — the same reading
[`NetBoxInterface`](netboxinterface.md) documents.

### `spec.deviceRef`

The device the bay is on. Required, because NetBox's column is, and because **both** natural-key
candidates read `device_id` — until it resolves no candidate applies and the object writes
nothing at all.

It is also this Kind's **containment reference**: `device` is `on_delete=CASCADE`, so NetBox
deletes a device's module bays with the device, and `kubectl delete nbdev sw1`
garbage-collects the bay CRs in the same namespace
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). See
[Two cascades, one containment parent](#two-cascades-one-containment-parent).

**If it is wrong.** `RefsResolved=False` with `RefNotFound`, `RefNotReady`, `RefDenied`,
`RefAmbiguous` or `RefTargetFailed` naming the field, and `Ready=False, Reason=WaitingForRef`,
with zero NetBox writes. An absent `deviceRef` is rejected by the API server before the operator
sees the object. A cross-namespace `deviceRef` with no grant is `RefDenied`, and gets no owner
reference either — an owner reference may never cross a namespace, and the `ParentOwned`
condition says which happened ([ownership](../concepts/ownership.md)).

### `spec.name`

The bay's name, unique per `(device, module)` in the database and per `(device)` by convention.

Matched **exactly**, not case-insensitively. [`NetBoxDevice`](netboxdevice.md)'s own constraints
are declared over `Lower('name')` and `dcim.ComponentModel`'s is a plain column, so `Slot1` and
`slot1` are two bays to NetBox and must be two to the operator. `registry.LookupIExact` exists
for the Kinds whose constraint really is case-folded, and this is not one of them.

Renaming the field changes the object's identity: the lookup finds nothing under the new name
and the engine creates a second bay. Rename in NetBox and in the manifest together, or accept
the new object and delete the old one — the same rule
[`NetBoxInterface`](netboxinterface.md#renaming-changes-identity) states.

### `spec.moduleRef`

The module that **provides** this bay, when the bay is a slot on a line card rather than on the
chassis. Optional, and the most load-bearing optional field in this spec, because it is the
third column of NetBox's unique constraint: setting it moves this object from a convention
identity to a database-backed one.

It is never the module installed *in* this bay. See
[Which way round `module` points](#which-way-round-module-points).

**If it is wrong.** Same conditions as `deviceRef`, and the same "nothing is written": with
`moduleRef` declared and unresolved, candidate 1 is not applicable (the value does not resolve)
and candidate 2 is not applicable either (the field *is* declared), so the engine waits. It must
— candidate 2 would otherwise find the chassis bay of this name on this device and adopt it, and
the follow-up PATCH would move it off the card ([NBO-015](../concepts/lookups.md)).
`TestModuleBayWhoseModuleIsUnresolvableWritesNothing` asserts zero writes in exactly that state,
with the chassis bay seeded so the wrong answer would have something to find. This is the
failure class behind #206 and #216.

`module` is `CASCADE` as well as `device`, and it is declared as such on the descriptor
(`Field.CascadeOnDelete`) even though it is not the containment parent — see
[Two cascades, one containment parent](#two-cascades-one-containment-parent).

A pointer to a typed alias, so it has **two** states rather than three: absent means unmanaged,
and a value claims the column. Moving a bay from a module back to the chassis needs
`registry.Field.EmptyIsNull` and a `v1alpha1.OptionalRef`, a third state no shipped Kind uses
yet; until then, clear it in NetBox and stop declaring the field.

### `spec.position`

The identifier NetBox substitutes for the `{module}` token in the names of the components a
module installed here provides. Install a card whose module type declares an interface template
named `Ethernet{module}/1` into a bay with `position: "3"` and NetBox creates `Ethernet3/1`.

A **string** and not a number, because NetBox's column is `position CharField len=30`: slot
identifiers are routinely `A1` or `0/1`, and a numeric field could not hold either. Quote it in
YAML — `position: 1` is an integer to the parser and the API server rejects it.

Clearable: omit it to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

### `spec.enabled`

Whether the bay may take a module (`enabled BooleanField def=True`).

A `*bool`, and the reason is the column's default. A plain `bool` cannot tell "not managed" from
"managed as false", so adopting a bay a human had disabled would silently re-enable it on the
first reconcile. Nil leaves NetBox's value alone; `false` writes false. The same shape as
[`NetBoxInterface`](netboxinterface.md#specenabled--specmgmtonly--specmarkconnected)'s three
booleans.

### `spec.label`, `spec.description`

Both clearable: omit one to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)). `label` is the physical marking on the
chassis, distinct from `name`, and neither is in any natural-key candidate but `name`.

## Natural keys

Two candidates, and never both applicable to one object, because they disagree about whether
`moduleRef` is declared.

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(device, module, name)` | `?device_id=<id>&module_id=<id>&name=<name>` | `dcim_modulebay_unique_device_module_name` |
| 2 | `(device, name)` with `module` null | `?device_id=<id>&name=<name>&module_id=null` | **nothing** — a convention |

Every filter is registered: `device_id` is declared on `DeviceComponentFilterSet`, `module_id` on
`ModularDeviceComponentFilterSet` — both `ModelMultipleChoiceFilter` — and `name` is in
`ModuleBayFilterSet`'s `meta_fields`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleBay.filters`, NetBox 4.6.8
`netbox/dcim/filtersets.py`).

**Candidate 1** is the database constraint verbatim, and the one the committed IR supplies
directly: `hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleBay.natural_keys` carries `device_id`,
`module_id` and `name` with no null fields. Nothing about it is hand-derived — which makes this
Kind the exception in the module block rather than the rule; the other three all needed a
reading. See [`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md#natural-key) for the
opposite case.

**Candidate 2** is the convention for a bay on the chassis, and the null pin is what makes it
safe. Without it the lookup would also match a bay of that name on *some module of the same
device* and adopt it, and the follow-up PATCH would move it off the card.
`NaturalKey.Applicable` offers this candidate only while `moduleRef` is *undeclared*, so a bay
whose module has not been created yet waits rather than falling through — see
[`spec.moduleRef`](#specmoduleref).

`?module_id=null` is the wire spelling of `registry.NullColumnRef`. `module_id` is a
`ModelMultipleChoiceFilter`, which is the filter class #216 established the spelling against
(the same class [`NetBoxLocation`](netboxlocation.md)'s `parent_id` is pinned through), so the
pin travels through django-filter's `null_value` rather than through an `__isnull` suffix
`BaseFilterSet` would drop in silence.

`device_id` is never omitted from either candidate. The pair is unique per device and `Slot 1` is
the most-reused bay name there is, so a lookup without it would adopt another switch's bay on
the first reconcile — the [`NetBoxInterface`](netboxinterface.md#natural-keys) argument, and the
reason a dropped filter is a *wider* query rather than a narrower one.

`TestModuleCandidatesByState` is the table above as assertions: a chassis bay gets candidate 2
only, a bay on a module gets candidate 1 only, and a bay whose module or device is declared and
unresolved gets none at all.

### More than one match is a `Conflict`

Because candidate 2 is a convention, two chassis bays called `Slot 1` on one switch are a state
NetBox will happily hold. The lookup then matches both, and the engine reports
`Ready=False, Reason=Conflict` naming the matches rather than adopting one
([errors and retries](../concepts/errors-and-retries.md)). The fix is to name them distinctly,
which is also what makes them legible in the NetBox UI.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`provenance`, `conflict`, `deferredPending`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.naturalKey` is the field to read first on this Kind: it records *which* candidate
located the object, filter by filter, so `module_id: "null"` in it is the visible difference
between a chassis bay and a bay on a card. `TestChassisModuleBayPinsModuleIDToNull` asserts the
recorded key exactly, which is the only way the pin's presence can be observed from outside the
engine.

`dcim.ModuleBay` derives from `ComponentModel`, which is a `NetBoxModel`, so it carries both
`tags` and `custom_fields` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the bay exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `deviceRef` resolves and `moduleRef` is unset or resolves | either does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `ParentOwned` | the owner reference on the device is set | it cannot be — the device is in another namespace, or somebody else set an owner reference | `Owned`, `CrossNamespace`, `Foreign` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals: [errors and retries](../concepts/errors-and-retries.md).
`WaitingForKey` is the one worth naming here — it is what a bay reports when its references
resolve but no candidate is applicable, which on this Kind means `moduleRef` is declared and
still pending.

## Kind-specific behaviour

### Which way round `module` points

`dcim.ModuleBay` and `dcim.Module` point at each other, and the two directions mean opposite
things.

| Direction | Column | Spec field | Meaning |
|---|---|---|---|
| bay → module | `ModuleBay.module` (forward `ForeignKey`) | `spec.moduleRef` **here** | the module that *provides* this bay |
| module → bay | `Module.module_bay` (`OneToOneField`) | `spec.moduleBayRef` on [`NetBoxModule`](netboxmodule.md) | the bay a module *occupies* |
| bay ← module | `ModuleBay.installed_module` | none, and never | the reverse accessor of the row above |

`installed_module` is not a column. It is the `related_name` on `Module.module_bay`
(`docs/netbox-schema.md` → `dcim.Module`), so the writable half of that one-to-one lives on
[`NetBoxModule`](netboxmodule.md) and only there. It is in the serializer's write path
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleBay.write_path`) and the descriptor declares it
**read-only** rather than merely leaving it out, for the reason
[`NetBoxInterface`](netboxinterface.md) gives about `cable`: a bay that adopted an occupied slot
must not PATCH the module out of it.

Two writers for one relation is the mistake this table exists to prevent, and here the schema
already says which side owns it.

### `parent` is a consequence of `moduleRef`

`dcim.ModuleBay` is an `MPTTModel` with `parent TreeForeignKey -> dcim.ModuleBay
on_delete=CASCADE`, and this spec has no `parentRef`. The column is absent from the serializer's
write path entirely — `hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleBay.write_path` lists
`device`, `module`, `name`, `label`, `position`, `enabled`, `description`, `installed_module`
and `_occupied`, and no `parent` — because NetBox derives it from `module.module_bay`: a bay's
tree parent is the bay its providing module sits in.

Three things follow, and all three are absences rather than gaps.

- There is no `parentRef` to declare. A field for it would be a key NetBox drops in silence,
  which reports success and sets nothing.
- There is no cycle check to webhook, unlike every `NestedGroupModel` Kind
  ([`NetBoxRegion`](netboxregion.md), [`NetBoxLocation`](netboxlocation.md)). The tree edge is
  not user input, so a user cannot write a cycle into it.
- There is no `parent IS NULL` variant among the candidates. The nesting question is asked
  through `module_id`, which is the column the constraint names.

`parent` is in the descriptor's read-only list rather than merely unmapped, so a later edit that
reaches for it fails the boot with `registry.ErrFieldReadOnly` instead of shipping a write NetBox
ignores. `TestModuleCountersAndDerivedColumnsAreReadOnly` holds the list.

### Two cascades, one containment parent

Both foreign keys on this model cascade. `dcim.ComponentModel.device` is `REQ` and
`on_delete=CASCADE`, and `dcim.ModularComponentModel.module` is optional and `on_delete=CASCADE`
(`docs/netbox-schema.md`). So the choice here is between two qualifying keys rather than between
cascade and no cascade, which is unusual — on most Kinds
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 leaves at most one candidate.

Exactly one containment parent is permitted, because Kubernetes garbage collection waits for
*every* owner and two would turn "delete the device or the module" into "delete both". `device`
wins because it is the required one: every bay has a device and only a nested bay has a module,
so naming `moduleRef` would leave the common case with no containment parent at all. That is the
same choice [`NetBoxInterface`](netboxinterface.md#deleting-the-device-deletes-this-cr) makes,
which records the identical pair.

`module` is still declared `CascadeOnDelete: true` on the descriptor, truthfully rather than
conveniently: the flag records what NetBox does, and `validateContainment` reads it to decide
whether a containment parent is legal at all. A Kind whose only cascading foreign key was left
undeclared would silently get no containment parent.
`TestModuleContainmentFollowsTheCascade` asserts both flags and the choice between them.

The owner reference is **non-controller** and is set only for a `deviceRef` in `name` mode
within one namespace: an id- or slug-mode reference names no CR to own the object, and an owner
reference may never cross a namespace. `ParentOwned` says which of those happened. See
[ownership](../concepts/ownership.md).

### A device created from a device type already has its bays

This is the behaviour most likely to produce a duplicate, and the reason `onConflict` matters
here more than on a catalogue Kind. NetBox instantiates a device type's `ModuleBayTemplate` rows
into real `dcim.ModuleBay` rows when a device is created from that type — server-side, without
the operator asking. So the bays a manifest describes routinely **already exist**, and they are
unmanaged NetBox rows rather than CRs.

The rule is the ordinary one: a bay CR whose natural key matches an existing row **adopts** it
rather than creating a second. With `onConflict: Adopt` the pre-existing row is taken over,
`status.adopted` is set and one `Slot 3` exists rather than two;
`TestChassisModuleBayIsAdoptedNotDuplicated` holds that from the outside. With the default
`onConflict: Fail` the match is reported as `Ready=False, Reason=AdoptOnly` and nothing is
written, which is the safe answer when the row's provenance is unknown. See
[lookups](../concepts/lookups.md) and [object lifecycle](../concepts/object-lifecycle.md).

The templates themselves are not yet a Kind — see [What is not here yet](#what-is-not-here-yet).

### Six columns are never sent and never diffed

`parent` and `installed_module` have their own sections above. Beside them, `_occupied` is a
`BooleanField` the serializer computes and declares `read_only`
(`hack/testdata/api-schema-4.6.8.json.gz` → `ModuleBaySerializer`, `declared._occupied`), and
`_site`, `_location` and `_rack` are the `_`-prefixed `ComponentModel` caches NetBox
denormalises from the device (`docs/netbox-schema.md` → `dcim.ComponentModel`) — the IR records
all three as absent from the write path. `inventory_items` beside them is a `GenericRelation`,
the far end of somebody else's foreign key rather than a column.

All are in the descriptor's read-only list, and
`TestChassisModuleBayPinsModuleIDToNull` asserts that none of the six appears in any recorded
request. NetBox **ignores** a write to one rather than refusing it, so an unguarded field map
would produce a difference the next reconcile finds again, and PATCH forever.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A bay is a slot on a chassis, described in full by
the manifest that creates it; nothing about deleting one frees a resource for somebody else,
which is what `Retain` exists for. See [deletion](../concepts/deletion.md).

Deleting a bay CR does delete the row, and `Module.module_bay` is `CASCADE`, so a module
installed in it goes with it — which is why [`NetBoxModule`](netboxmodule.md) takes its owner
reference here rather than on the device.

### What is not here yet

- **`dcim.ModuleBayTemplate`.** The template that makes NetBox create these rows on device
  creation. It is one of the `*Template` Kinds still owed by #54, and until it ships a
  template-borne bay is adopted rather than declared.
- **`moduleRef` on the other component Kinds.** `module` is a column on *every*
  `ModularComponentModel` subclass, and this is the only subclass that carries it so far —
  because `module` is one of the three columns of this Kind's unique constraint. Adding it to
  [`NetBoxInterface`](netboxinterface.md) and the rest is one change across all of them, and it
  is recorded as a note in [coverage](../coverage.md) rather than left to be re-derived.
- **`owner`.** `ForeignKey -> users.Owner`, and `users/*` is an excluded endpoint, so nothing
  will ever write it.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbmodulebay
NAME     DEVICE   NAME     POSITION   ID    READY   AGE
slot-1   sw1      Slot 1   1          311   True    2m
port-1   sw1      Port 1   1/1        312   True    2m
```

| Column | JSONPath |
|---|---|
| `DEVICE` | `.spec.deviceRef.name` |
| `NAME` | `.spec.name` |
| `POSITION` | `.spec.position` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`DEVICE` reads the *spec*, so it shows the intent even while the reference is unresolved and
`ID` is empty. `POSITION` is the `{module}` token rather than a rack unit, which is worth
remembering next to [`NetBoxRack`](netboxrack.md) in the same output.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `deviceRef` | — | The field is required by the schema, because NetBox's column is `REQ`. | Name a device. |
| `kubectl apply` rejected, message names `position` | — | `position: 1` is an integer to the YAML parser; the column is a `CharField`. | Quote it: `position: "1"`. |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | `deviceRef` or `moduleRef` names an object that does not exist. | Create it, or fix the name. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace reference with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant. |
| `Ready=False`, `Reason=WaitingForKey` | `RefsResolved=True` | `moduleRef` is declared and still pending, so no candidate is applicable. The engine is waiting on purpose. | Wait, or drop `moduleRef` if the bay is on the chassis. |
| `Ready=False`, `Reason=Conflict` naming two matches | | Two chassis bays of this name on this device — a state no constraint prevents. | Name them distinctly. |
| `Ready=False`, `Reason=AdoptOnly` | | A bay of this name already exists, created by NetBox from the device type's templates, and `onConflict` is `Fail`. | Set `onConflict: Adopt` once the row is known to be the right one. |
| Two bays with the same name in NetBox | | The lookup ran before the name was final, or `moduleRef` was added after the object converged. | Delete the stray row; the CR re-adopts the survivor. |
| A PATCH on every resync | `Synced=False` | Not expected. `parent`, `installed_module`, `_occupied` and the three caches are read-only and never sent. | Read the `Synced` message for the field that differs, and `status.lastAppliedHash`. |
| The bay CR vanished on its own | | The device CR was deleted, and `device` is `CASCADE`, so the owner reference collected it. | Expected. |
| `ParentOwned=False`, `Reason=CrossNamespace` | | The device is in another namespace, so no owner reference is possible. Everything else works. | Nothing, or co-locate them. |
| `Deleting=False`, `Reason=Protected` | | Not expected here: every foreign key pointing at a bay cascades. Read the message. | — |

## Related

- [`NetBoxModule`](netboxmodule.md) — the other half of the one-to-one, and the Kind this one
  owns
- [`NetBoxModuleType`](netboxmoduletype.md) — the catalogue entry a module installed here is
  built from
- [`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md) — the schema behind a module type's
  attributes
- [`NetBoxDevice`](netboxdevice.md) — the required reference and the containment parent
- [`NetBoxInterface`](netboxinterface.md) — the other shipped `ComponentModel`, with the same
  `(device, name)` derivation and the same cascade
- [`NetBoxRack`](netboxrack.md), [`NetBoxIPAddress`](netboxipaddress.md) and
  [`NetBoxContact`](netboxcontact.md) — the other Kinds whose identity NetBox does not enforce
- [Lookups](../concepts/lookups.md) — candidate order, why a null filter is pinned, and why more
  than one match is an error
- [Ownership](../concepts/ownership.md) and
  [ADR-0003](../decisions/0003-ownership-and-references.md) — the `CASCADE` rule, and why only
  one parent
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
