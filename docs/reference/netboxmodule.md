# `NetBoxModule`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxModule` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbmodule` |
| Status subresource | yes |

A `NetBoxModule` is one `dcim.Module`: a physical module installed in a bay — a line card, a
power supply, an optic. It is the instance to [`NetBoxModuleType`](netboxmoduletype.md)'s
catalogue entry, the way [`NetBoxDevice`](netboxdevice.md) is the instance to
[`NetBoxDeviceType`](netboxdevicetype.md).

Three things about this Kind are worth knowing before the field list.

## The bay is the identity, and a `OneToOneField` is why

`dcim.Module` declares **no `meta.constraints`** — the committed IR's `natural_keys` for it is
`[]` (`hack/testdata/ir-4.6.8.json.gz` → `dcim.Module`) — and it does not need one:

```
module_bay  OneToOneField  REQ  -> dcim.ModuleBay  on_delete=CASCADE
```

(`docs/netbox-schema.md` → `dcim.Module`.) A `OneToOneField` is a `ForeignKey` Django declares
`unique=True` on, so `module_bay_id` carries a `UNIQUE` index and the database already holds at
most one module per bay. That is a uniqueness guarantee of exactly the kind a natural key needs,
read off a committed artefact, and it makes this the first Kind in the catalogue whose identity
comes from a column type rather than from a `UniqueConstraint`
([`NetBoxRackType`](netboxracktype.md)) or from a convention
([`NetBoxRackReservation`](netboxrackreservation.md)).

One column, and only one. See [Natural key](#natural-key).

## The filter behind that column is wider than the column

`module_bay_id` is a `TreeNodeMultipleChoiceFilter`, so the query the operator sends matches more
rows than the key describes. It never adopts the wrong object, and on one shape of topology it
refuses to adopt at all. This is the one limitation of the Kind and it has its own section:
[The lookup follows the bay tree](#the-lookup-follows-the-bay-tree).

## Creating one instantiates components the operator does not manage

`ModuleSerializer` declares a `write_only` `replicate_components` field that defaults to
**true**, so NetBox creates the module type's component templates as real rows on this module
when the module is created. Those rows are unmanaged NetBox objects, not CRs. See
[`replicate_components` and `adopt_components`](#replicate_components-and-adopt_components).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModule
metadata:
  name: slot-1-optic
  namespace: default
spec:
  endpointRef: homelab
  # All three are REQ columns on dcim.Module, so all three are required here.
  deviceRef:
    name: sw1
  moduleBayRef:
    name: slot-1
  moduleTypeRef:
    name: sfp-10g-lr
```

`moduleBayRef` needs a [`NetBoxModuleBay`](netboxmodulebay.md) named `slot-1` in this namespace,
or a [grant](netboxrefgrant.md) and a `namespace:` to reach one elsewhere. The same for
`deviceRef` and `moduleTypeRef`.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModule
metadata:
  name: slot-1-optic
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail          # default
  deletionPolicy: Delete    # default: a module is configuration, not allocated state

  # Redundant with the bay in the data and required by NetBox anyway, which denormalises it so
  # a module can be filtered by device without a join. Not part of the key.
  deviceRef:
    name: sw1

  # The whole natural key, and the owner reference: deleting the bay's CR deletes this one,
  # and deleting the device's CR deletes the bay's.
  moduleBayRef:
    name: slot-1

  moduleTypeRef:
    name: sfp-10g-lr

  # Omitting it leaves NetBox's server-side default (`active`) on create and leaves an adopted
  # module's own status alone. There is no "" member: the column is NOT NULL with a default.
  status: active
  serial: FNS12345678

  # Globally UNIQUE in NetBox and deliberately not a lookup candidate: an asset tag identifies
  # the hardware, and this CR describes the slot it is installed in.
  assetTag: ASSET-SFP-0001

  description: Uplink optic in slot 1
  comments: |
    Spare in the top drawer of the network cabinet.
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `customFields`.
See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `deviceRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxDevice` | yes | — | ref arity CEL | `device ForeignKey REQ -> dcim.Device on_delete=CASCADE` |
| `moduleBayRef` | `ObjectRef` → `NetBoxModuleBay` | yes | — | ref arity CEL | `module_bay OneToOneField REQ -> dcim.ModuleBay on_delete=CASCADE` |
| `moduleTypeRef` | `ObjectRef` → `NetBoxModuleType` | yes | — | ref arity CEL | `module_type ForeignKey REQ -> dcim.ModuleType on_delete=PROTECT` |
| `status` | `string` | no | — (NetBox's own `active`) | enum: `offline`, `active`, `planned`, `staged`, `failed`, `decommissioning` | `status CharField len=50` |
| `serial` | `string` | no | — | ≤50 | `serial CharField len=50` |
| `assetTag` | `string` | no | — | ≤50 | `asset_tag CharField UNIQUE len=50` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

Three required references out of eight fields, which is the most references any Kind in the
catalogue requires — a direct reading of the model, where `device`, `module_bay` and
`module_type` are all `REQ`.
`TestModuleWithoutItsRequiredReferencesIsRejectedByTheAPIServer` applies the object with each
one missing in turn and asserts the API server names the field.

### `spec.deviceRef`

The device the module is installed in. Required, because NetBox's column is.

It is **redundant with `moduleBayRef` in the data** — a bay already names its device — and NetBox
requires it anyway, because it denormalises the column so a module can be filtered by device
without a join. The operator writes what the column asks for rather than deriving it from the
bay: a derived value that disagreed with the bay's device would be a `400` whose cause the user
cannot see in their own manifest.

It is **not** part of the natural key. `module_bay` alone is unique, so adding `device_id` to the
lookup would narrow it below what the database enforces — and a narrower key adopts less, not
more safely. `TestModuleIsKeyedOnItsBayAlone` asserts the recorded lookup carries `module_bay_id`
and nothing else.

It is not the containment parent either, though it cascades — see
[Why the bay owns this CR and not the device](#why-the-bay-owns-this-cr-and-not-the-device).

**If it is wrong.** `RefsResolved=False` with `RefNotFound`, `RefNotReady`, `RefDenied`,
`RefAmbiguous` or `RefTargetFailed` naming the field, and `Ready=False, Reason=WaitingForRef`,
with zero NetBox writes. An absent `deviceRef` is rejected by the API server.

### `spec.moduleBayRef`

The bay the module occupies. Required, and this Kind's whole identity: see
[Natural key](#natural-key).

**One module per bay, enforced by the database.** Installing a second module into an occupied bay
is NetBox's own `400` on the `UNIQUE` index behind the `OneToOneField`, surfaced verbatim as
`Ready=False, Reason=Invalid` with NetBox's message naming the field and the occupant. The
operator does not pre-check it — NetBox is the authority on whether the slot is free, and a
client-side check would be a second copy of that rule racing the first.

It is also the **containment reference**: `module_bay` is `on_delete=CASCADE`, so NetBox deletes
a bay's module with the bay, and `kubectl delete nbmodulebay slot-1` garbage-collects this CR
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

It points at the bay this module *occupies*. The other direction — the module that *provides* a
bay — is [`NetBoxModuleBay`](netboxmodulebay.md)'s own `spec.moduleRef`, and
[that page's table](netboxmodulebay.md#which-way-round-module-points) is the one to read if the
two ever look interchangeable.

**If it is wrong.** With `moduleBayRef` declared and unresolved there is **no applicable
candidate at all**, so the engine waits and writes nothing rather than creating a module into a
slot it has not identified. That is asserted on the recorded traffic, not only on the status.

### `spec.moduleTypeRef`

The catalogue entry this module is an instance of. Required, because NetBox's column is.

`PROTECT`, so NetBox refuses to delete a module type while any module points at it; that surfaces
on the *module type* as `Deleting=False, Reason=Protected` naming this module. No cascade, so it
contributes no owner reference.

Writing it is also what makes NetBox instantiate the type's component templates into this module
— see
[`replicate_components` and `adopt_components`](#replicate_components-and-adopt_components).

**If it is wrong.** `RefsResolved=False` naming the field, and nothing is written: the type is
not in the key, but a create without a `REQ` column would be a `400` and the engine does not
issue one.

### `spec.status`

The module's operational status. Six members, read from `netbox/dcim/choices.py:244` in the 4.6.8
tree the digest was taken from — the digest records the choice *class* and not its members,
because the AST walk cannot evaluate one.

Its own Go type and deliberately not [`NetBoxRack`](netboxrack.md)'s five, which share only
`active` and `planned` with it: `decommissioning` is here and `reserved`, `available` and
`deprecated` are not. `ModuleStatusChoices` declares `key = 'Module.status'`
(`hack/testdata/api-schema-4.6.8.json.gz` → `choices.ModuleStatusChoices`), so a deployment can
add values through `FIELD_CHOICES`; a value added there needs this CRD's enum widened.

**Not defaulted by the CRD**, unlike `NetBoxRack.status`, and the column is why: it is NOT NULL
*with a server-side default*, so omitting the field lets NetBox choose `active` on create and
leaves an adopted module's own status alone. There is no `""` member for the same reason —
"unspecified" is not a state this column has.

**If it is wrong.** A value outside the enum is rejected by the API server, naming the field.

### `spec.serial`

The manufacturer's serial number. Not unique in NetBox and so not a lookup candidate — two
modules may legitimately carry the same string, and NetBox does not stop them.

Clearable: omit it to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

### `spec.assetTag`

The deployment's own inventory tag, and the one column on this model with a global `UNIQUE`
(`asset_tag CharField UNIQUE len=50`).

It is deliberately **not** a natural-key candidate — the [`NetBoxRack`](netboxrack.md#specassettag)
argument unchanged. An asset tag identifies the piece of hardware and this CR describes the slot
it is installed in, so adopting by asset tag would let moving a card between chassis rewrite the
device and bay of somebody else's module. `TestModuleAssetTagIsClearedWithNullAndIsNotAKey`
asserts no candidate carries the filter.

Cleared as JSON `null` rather than as `""`, and here that flag is load-bearing rather than tidy:
the column is `UNIQUE` **and** `null=True`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.Module.asset_tag`), so two modules whose tag was
cleared to the empty string would collide on the unique index where two `NULL`s do not
(`registry.Field.EmptyIsNull`; asserted end to end by
`TestModuleAssetTagIsClearedWithNullOnTheWire`).

**If it is wrong.** A duplicate is NetBox's own `400` on the unique index, reported as
`Ready=False, Reason=Invalid` with NetBox's message.

### `spec.description`, `spec.comments`

Both clearable: omit one to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)). `dcim.Module` is a `PrimaryModel`, so
`comments` exists here where it does not on [`NetBoxModuleBay`](netboxmodulebay.md); it is a
`TextField` and carries no `maxLength`.

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(module_bay)` | `?module_bay_id=<id>` | the `UNIQUE` index behind `module_bay OneToOneField` |

One candidate and one filter, which is the shortest key in the catalogue. It is applicable as
soon as `moduleBayRef` resolves, and there is nothing to fall back to: the column is `REQ`, so
there is no state in which it is missing and a different identity applies.

`module_bay_id` is declared on `ModuleFilterSet`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.Module.filters.module_bay_id`, NetBox 4.6.8
`netbox/dcim/filtersets.py:1609`).

Two columns are deliberately absent from it. `device_id` would narrow the query below what the
database enforces, for no gain — see [`spec.deviceRef`](#specdeviceref). `asset_tag` is globally
unique and describes the hardware rather than the slot — see
[`spec.assetTag`](#specassettag).

### The lookup follows the bay tree

The one limitation of this Kind, and it is a refusal rather than a corruption.

`module_bay_id` is not an exact-match filter. `ModuleFilterSet` declares it as a
`TreeNodeMultipleChoiceFilter` with `lookup_expr='in'`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.Module.filters.module_bay_id`), because
`dcim.ModuleBay` is an `MPTTModel`. So `?module_bay_id=N` matches modules in bay `N` **and in
every bay descended from it** — and a bay's descendants are the bays provided by the module
installed in it (`ModuleBay.parent` is derived from `module.module_bay`; see
[that page](netboxmodulebay.md#parent-is-a-consequence-of-moduleref)).

Three cases, and **none of them adopts the wrong object**.

| Bay `N` | Descendants | Matches | Outcome |
|---|---|--:|---|
| empty | none are possible — a descendant needs a module in `N` | 0 | the engine creates |
| holds a module, no sub-modules | none occupied | 1 | the right object, adopted or updated |
| holds a module that has modules in its sub-bays | occupied | 2+ | `netbox.AmbiguousError` → `Ready=False, Reason=Conflict` naming every id, and zero writes |

The first row is what makes the whole thing safe. A lookup can only over-match when the bay it
names is *already occupied*, and in that case the correct object is among the matches — so the
engine never sees exactly one wrong row and never silently adopts one. It sees either the right
row alone, or an ambiguity it refuses ([lookups](../concepts/lookups.md)).

Two things narrow the blast radius further. The engine consults the natural key only while
`status.id` is unset (`reconciler.pass.find`), so a module that converged before its sub-modules
existed keeps working — a chassis populated in dependency order never hits this. What does hit
it is a **fresh cluster adopting an already-populated chassis**: the outer module's CR has no
`status.id` yet, its bay has occupied descendants, and the lookup is ambiguous from the first
reconcile.

**There is no client-side fix.** Using `spec.moduleBayRef.id` instead of `name` does not help:
the reference mode only changes how the bay's id is *found*, and that id is exactly what the
over-wide filter is then given. Nor does declaring the inner modules first — each of those
resolves unambiguously, because its own bay has no occupied descendants, but none of them
changes what `?module_bay_id=N` returns for the outer one.

What the operator does instead is say so. The `Conflict` message names the endpoint, the query
and every matching id, so the ambiguity is a report a human can act on rather than a guess the
engine makes — which is the whole of [`docs/concepts/lookups.md`](../concepts/lookups.md)'s
rule. Adopting an already-populated chassis therefore needs either the inner modules removed
from NetBox for the one reconcile that adopts the outer one, or the outer module's id supplied
out of band.

The real fix is upstream: an exact-match parameter for `module_bay` that
`TreeNodeMultipleChoiceFilter` does not provide. NetBox 4.6.8 offers none, and the descriptor
records the reasoning at the declaration
(`internal/registry/dcim_module.go`) rather than hiding it behind a key that looks exact.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`provenance`, `conflict`, `deferredPending`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.naturalKey` is a single entry on this Kind — `{"module_bay_id": "<id>"}` — which is
worth reading anyway: it is the id the over-wide filter was given, and therefore the first thing
to check against a `Conflict`.

`dcim.Module` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is stamped
in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the module exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | all three references resolve | any does not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `ParentOwned` | the owner reference on the module bay is set | it cannot be — the bay is in another namespace, or somebody else set an owner reference | `Owned`, `CrossNamespace`, `Foreign` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals: [errors and retries](../concepts/errors-and-retries.md).
`Conflict` carries more weight on this Kind than on most, because it is the outcome of
[the tree-following lookup](#the-lookup-follows-the-bay-tree) as well as of an ordinary
double-declaration.

## Kind-specific behaviour

### Why the bay owns this CR and not the device

Both `device` and `module_bay` are `REQ` and `on_delete=CASCADE`
(`docs/netbox-schema.md` → `dcim.Module`), and `module_type` is `PROTECT`. So two foreign keys
qualify as a containment parent under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4, and exactly one is permitted —
Kubernetes garbage collection waits for *every* owner, so two would turn "delete the device or
the bay" into "delete both".

`moduleBayRef` wins, and the reason is that the chain already reaches the device:
[`NetBoxModuleBay`](netboxmodulebay.md#two-cascades-one-containment-parent)'s own containment
parent is its device, so deleting the device CR collects the bay CRs and deleting those collects
their modules. Naming `deviceRef` here would give the same reach with less precision — removing a
single bay would leave its module's CR behind, to be recreated into a slot that no longer exists.

It also keeps two facts in agreement: the object the CR is identified by is the object whose
deletion removes it. `TestModuleContainmentFollowsTheCascade` asserts the choice, that the chosen
field really is `CascadeOnDelete`, and that `ContainmentTargets()` names `NetBoxModuleBay` alone.

The owner reference is **non-controller** and is set only for a `moduleBayRef` in `name` mode
within one namespace: an id- or slug-mode reference names no CR to own the object, and an owner
reference may never cross a namespace. `ParentOwned` says which of those happened. See
[ownership](../concepts/ownership.md).

### `replicate_components` and `adopt_components`

Two fields the API accepts and this spec does not have.

`ModuleSerializer` declares both as `write_only` `BooleanField`s rather than columns on the model
(`hack/testdata/api-schema-4.6.8.json.gz` → `ModuleSerializer`, `declared`), with
`replicate_components` defaulting to **true** and `adopt_components` to **false**. They are not
in the field map, for two independent reasons.

- A write-only field cannot be read back. Mapping one would put a key in every payload that never
  appears in any response, and the drift comparison would never settle — a PATCH loop rather than
  an error. `TestModuleDoesNotWriteTheWriteOnlyActionFlags` asserts neither reaches a payload.
- They are *actions* taken once at write time, not state an object holds. Nothing about a module
  that exists records whether its components were replicated when it was created, so a
  declarative field for it would describe a moment rather than a fact.

The consequence a reader needs. Because `replicate_components` defaults to true **server-side**,
creating a module through this operator *does* instantiate its module type's component templates
in NetBox — interfaces, power ports and the rest appear on the module without the operator
asking. Those rows are unmanaged NetBox objects rather than CRs, exactly as the components NetBox
creates from a device type's templates are
([`NetBoxModuleBay`](netboxmodulebay.md#a-device-created-from-a-device-type-already-has-its-bays)
has that case). Declaring one of them later adopts the existing row rather than creating a
second, which is the ordinary rule.

The flags themselves, and the component Kinds they act on, arrive with the rest of #54.

### Renaming is not possible, and moving is a rewrite

There is no `name` on `dcim.Module` — a module is identified by where it is, not by what it is
called. So the rename hazard other component Kinds carry does not exist here.

Moving a module to a different bay is the analogous change, and it is a genuine one: point
`moduleBayRef` at another bay and the lookup finds nothing under the new id, so the engine creates
a second module and the old row stays behind. Physically moving a card is best modelled as
deleting the CR and declaring a new one, which is also what happened to the hardware.

### Four columns are never sent, and there are no counters

`created`, `last_updated`, `url` and `display` are the envelope every `ChangeLoggedModel`
carries, and they are the whole of this Kind's read-only list. `dcim.Module` declares no
`CounterCacheField` and no `_`-prefixed cache
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.Module.fields`), which makes it unusual next to
[`NetBoxModuleType`](netboxmoduletype.md) and its nine counters.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A module is a record of what is installed in a
slot, described in full by the manifest that creates it; nothing about deleting one frees a
resource for somebody else, which is what `Retain` exists for. See
[deletion](../concepts/deletion.md).

Deleting a module CR deletes the row, and any component NetBox replicated onto it goes with it —
`ModularComponentModel.module` is `CASCADE`. That is NetBox's own behaviour, not the operator's.

### What is not here yet

- **`moduleRef` on the components.** A module can own interfaces, power ports and the rest
  through `dcim.ModularComponentModel.module`, and
  [`NetBoxModuleBay`](netboxmodulebay.md) is the only subclass that carries the column so far.
  Recorded as a note in [coverage](../coverage.md).
- **The `*Template` Kinds.** `dcim.ModuleBayTemplate` and its seven siblings are what make
  `replicate_components` produce anything, and none of them ships yet. Owed by #54.
- **`owner`.** `ForeignKey -> users.Owner`, and `users/*` is an excluded endpoint, so nothing
  will ever write it.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbmodule
NAME           DEVICE   BAY      TYPE         STATUS   ID    READY   AGE
slot-1-optic   sw1      slot-1   sfp-10g-lr   active   402   True    5m
slot-2-optic   sw1      slot-2   sfp-10g-lr   planned  403   True    5m
```

| Column | JSONPath |
|---|---|
| `DEVICE` | `.spec.deviceRef.name` |
| `BAY` | `.spec.moduleBayRef.name` |
| `TYPE` | `.spec.moduleTypeRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

All three reference columns read the *spec*, so they show the intent even while a reference is
unresolved and `ID` is empty. `STATUS` is blank for a module that leaves the field to NetBox's
own default, which is the recommended way to write one.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, naming `deviceRef`, `moduleBayRef` or `moduleTypeRef` | — | All three are required by the schema, because all three columns are `REQ`. | Name them. |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | A reference names an object that does not exist. | Create it, or fix the name. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace reference with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant. |
| `Ready=False`, `Reason=WaitingForKey` | `RefsResolved=True` | Not expected: the only candidate needs only `moduleBayRef`, which is also a required reference. Read the message. | — |
| `Ready=False`, `Reason=Invalid`, message names `module_bay` | | The bay already holds a module. `module_bay` is a `OneToOneField`, so NetBox refuses the second. | Free the bay, or point at another one. |
| `Ready=False`, `Reason=Invalid`, message names `asset_tag` | | The asset tag is already on another module; the column is globally `UNIQUE`. | Change it, or clear it on the other module. |
| `Ready=False`, `Reason=Conflict` naming two or more ids | | The bay has occupied sub-bays, and `module_bay_id` follows the MPTT tree. | [The lookup follows the bay tree](#the-lookup-follows-the-bay-tree) — adopt the inner modules first. |
| `Ready=False`, `Reason=AdoptOnly` | | A module already occupies this bay and `onConflict` is `Fail`. | Set `onConflict: Adopt` once the row is known to be the right one. |
| Interfaces appeared on the module nobody declared | | `replicate_components` defaults to true server-side, so NetBox instantiated the module type's templates. | Expected; declare them later and they are adopted, not duplicated. |
| A PATCH on every resync | `Synced=False` | Not expected. The two write-only flags are never sent, and the read-only list is only the envelope. | Read the `Synced` message for the field that differs, and `status.lastAppliedHash`. |
| The module CR vanished on its own | | The bay CR was deleted, or the device CR was — `module_bay` is `CASCADE` and the bay is owned by the device. | Expected. |
| `ParentOwned=False`, `Reason=CrossNamespace` | | The bay is in another namespace, so no owner reference is possible. Everything else works. | Nothing, or co-locate them. |
| Deleting the module type is refused | on the *type*: `Deleting=False`, `Reason=Protected` | `dcim.Module.module_type` is `PROTECT`. | Delete the modules first. |

## Related

- [`NetBoxModuleBay`](netboxmodulebay.md) — the identity, the containment parent, and the other
  half of the one-to-one
- [`NetBoxModuleType`](netboxmoduletype.md) — the catalogue entry, and where the component
  templates `replicate_components` instantiates would live
- [`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md) — the JSON Schema a module type's
  attributes are validated against
- [`NetBoxDevice`](netboxdevice.md) — the required, denormalised reference that is not the owner
- [`NetBoxRack`](netboxrack.md) — the other Kind with a globally unique `assetTag` that is
  deliberately not a lookup candidate
- [`NetBoxRackType`](netboxracktype.md) and
  [`NetBoxRackReservation`](netboxrackreservation.md) — a constraint-backed identity and a
  convention one, for contrast with this Kind's column-backed one
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Ownership](../concepts/ownership.md) and
  [ADR-0003](../decisions/0003-ownership-and-references.md) — the `CASCADE` rule, and why only
  one parent
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Deletion](../concepts/deletion.md) — why this is `Delete` and not `Retain`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
