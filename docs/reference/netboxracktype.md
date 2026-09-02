# `NetBoxRackType`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRackType` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbracktype` |
| Status subresource | yes |

A `NetBoxRackType` is one `dcim.RackType` in NetBox: one make and model of rack, with its
height, width and weight.

It is a catalogue kind, and the reason it exists is that a rack can be built *from* it. Setting
`rackTypeRef` on a `NetBoxRack` makes NetBox copy this type's `RackBase` dimensions onto that
rack server-side — see [What a rack gets from a rack type](#what-a-rack-gets-from-a-rack-type).
Unlike `dcim.DeviceType.device_type` on a device, the reference is **optional**: a rack may carry
its own dimensions and name no type at all.

Its identity is the same shape as [`NetBoxDeviceType`](netboxdevicetype.md)'s — both
`meta.constraints` start at a required `manufacturer` — so most of that page's argument applies
here unchanged. The one place they differ is [`slug` is unique twice
over](#slug-is-unique-twice-over-and-the-key-still-carries-the-manufacturer).

## Start with the grant

A rack type is catalogue data and a rack is not, so this Kind sits on the same namespace boundary
[`NetBoxDeviceType`](netboxdevicetype.md#start-with-the-grant) does: a rack in `team-a` names a
`rackTypeRef` in `netbox-catalog`, and the rack type itself names a `manufacturerRef` there.
Both directions need a [`NetBoxRefGrant`](netboxrefgrant.md) **in the namespace being
referenced**. Without it the rack type sits at `RefsResolved=False, Reason=RefDenied` and writes
nothing.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readers
  namespace: netbox-catalog        # the namespace holding the catalogue
spec:
  from:
    namespaces: All                # or a selector; see NetBoxRefGrant
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackType
metadata:
  name: mcs-42u
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  manufacturerRef:
    name: minkels                  # same namespace, so no grant needed for this one
  model: MCS 42U 800x1000
  slug: mcs-42u
  formFactor: 4-post-cabinet
```

A team namespace then names it across the boundary:

```yaml
spec:
  rackTypeRef:
    namespace: netbox-catalog
    name: mcs-42u
```

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackType
metadata:
  name: mcs-42u
  namespace: default
spec:
  endpointRef: homelab
  manufacturerRef:
    name: minkels
  model: MCS 42U 800x1000
  slug: mcs-42u
  formFactor: 4-post-cabinet
```

Four required fields, and `formFactor` is the one that surprises people —
[see below](#specformfactor) for why it is required here and optional on a rack.
`manufacturerRef` is not optional either: the API server rejects a `NetBoxRackType` without one.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackType
metadata:
  name: mcs-42u
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly — Fail is the default
  deletionPolicy: Delete      # Delete | Retain — Delete is this kind's default

  manufacturerRef:
    name: minkels
  model: MCS 42U 800x1000
  slug: mcs-42u
  formFactor: 4-post-cabinet  # required, and may not be ""

  # RackBase dimensions. The three defaulted ones are NetBox's own defaults written out.
  width: 19                   # 10 | 19 | 21 | 23 inches; 19 is the default
  uHeight: 42                 # the default
  startingUnit: 1             # the default
  descUnits: false

  outerWidth: 800
  outerHeight: 2000
  outerDepth: 1000
  outerUnit: mm               # "" | mm | in

  mountingDepth: 900          # always millimetres, whatever outerUnit says

  maxWeight: 1200
  weight: "128.5"             # a string, not a number. See spec.weight.
  weightUnit: kg              # "" | kg | g | lb | oz

  description: 42U cabinet, 800 mm wide
  comments: |
    Ordered with the perforated doors.
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxracktype.yaml`](../../config/samples/netbox_v1alpha1_netboxracktype.yaml).

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope and
behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `manufacturerRef` | [`ObjectRef`](../concepts/references.md) | **yes** | — | `manufacturer ForeignKey REQ -> dcim.Manufacturer on_delete=PROTECT` |
| `model` | `string`, 1–100 | **yes** | — | `model CharField REQ len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | **yes** | — | `slug SlugField REQ UNIQUE len=100` |
| `formFactor` | enum, non-empty | **yes** | — | `form_factor CharField REQ len=50 choices=RackFormFactorChoices` |
| `width` | enum `10;19;21;23` | no | `19` | `width (RackBase) PositiveSmallIntegerField` |
| `uHeight` | `integer` ≥ 1 | no | `42` | `u_height (RackBase) PositiveSmallIntegerField` |
| `startingUnit` | `integer` ≥ 1 | no | `1` | `starting_unit (RackBase) PositiveSmallIntegerField` |
| `descUnits` | `boolean` | no | — | `desc_units (RackBase) BooleanField def=False` |
| `outerWidth` | `integer` ≥ 1 | no | — | `outer_width (RackBase) PositiveSmallIntegerField` |
| `outerHeight` | `integer` ≥ 1 | no | — | `outer_height (RackBase) PositiveSmallIntegerField` |
| `outerDepth` | `integer` ≥ 1 | no | — | `outer_depth (RackBase) PositiveSmallIntegerField` |
| `outerUnit` | enum `"";mm;in` | no | — | `outer_unit (RackBase) CharField len=50` |
| `mountingDepth` | `integer` ≥ 1 | no | — | `mounting_depth (RackBase) PositiveSmallIntegerField` |
| `maxWeight` | `integer` ≥ 1 | no | — | `max_weight (RackBase) PositiveIntegerField` |
| `weight` | `string`, `^([0-9]{1,6}(\.[0-9]{1,2})?)?$` | no | — | `weight (WeightMixin) DecimalField decimal(8,2)` |
| `weightUnit` | enum `"";kg;g;lb;oz` | no | — | `weight_unit (WeightMixin) CharField len=50` |
| `description` | `string`, ≤200 | no | — | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | `comments (PrimaryModel) TextField` |

Every column is from `docs/netbox-schema.md` → `dcim.RackType` and `dcim.RackBase`.

### `spec.manufacturerRef`

**Required.** An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxManufacturer`](netboxmanufacturer.md).

Required because NetBox's column is `manufacturer ForeignKey REQ -> dcim.Manufacturer
on_delete=PROTECT`. It is also half of *both* natural keys, so until it resolves the object
reports `RefsResolved=False` naming this field and makes **no NetBox write at all** — the
[`NetBoxLocation`](netboxlocation.md) shape rather than the [`NetBoxPrefix`](netboxprefix.md) one,
where the object is created without the field. There is nothing to create: a manufacturer-less
rack type is not a state NetBox has.

Written to NetBox as `manufacturer`, filtered as `manufacturer_id`.

`PROTECT`, so NetBox refuses to delete a manufacturer while any rack type points at it. That
surfaces on the *manufacturer* as `Deleting=False, Reason=Protected`.

### `spec.model`

Required. The model name as NetBox displays it, up to 100 characters.

Unique per manufacturer (`..._unique_manufacturer_model`), not globally. It is a candidate key
and the *second* one the engine tries — see [Natural keys](#natural-keys).

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. The leading
half of this kind's natural key, because it is the stable identifier: a marketing rename edits
`model`, and looking up by `slug` first keeps that a PATCH rather than a second object.

Unusually for a `(manufacturer, x)` constraint, it is *also* globally unique on its own —
[which changes nothing, and looks like it
should](#slug-is-unique-twice-over-and-the-key-still-carries-the-manufacturer).

### `spec.formFactor`

**Required, and may not be `""`.** One of `2-post-frame`, `4-post-frame`, `4-post-cabinet`,
`wall-frame`, `wall-frame-vertical`, `wall-cabinet`, `wall-cabinet-vertical`.

The same field is **optional and clearable** on a rack, and that asymmetry is the column's rather
than a choice. `docs/netbox-schema.md` → `dcim.RackType` reads `form_factor CharField REQ len=50`
while `dcim.Rack`'s is `blank=True, null=True`, so the shared Go enum (`v1alpha1.RackFormFactor`,
`api/v1alpha1/dcim_rackbase.go`) has to carry `""` as a member for the rack's sake. Requiring the
*field* here is therefore not enough on its own — `formFactor: ""` would satisfy the enum — so the
CRD carries a CEL rule as well:

```
formFactor is required on a rack type: dcim.RackType.form_factor is NOT NULL with no default
```

Why that matters rather than being belt-and-braces: the IR records a disagreement between the two
halves of NetBox it reads, and resolves it in the serializer's favour —

```json
{"fact": "required-on-create", "field": "form_factor", "kind": "dcim.RackType",
 "models.json": "NOT NULL, no default", "rest": "serializer declares required=False",
 "resolved_to": "not required"}
```

(`hack/testdata/ir-4.6.8.json.gz` → `conflicts`.) So DRF **accepts** a create without
`form_factor` and Postgres then refuses the `INSERT`. Rejecting it at admission turns that into a
message on `kubectl apply` instead of a `500` from NetBox.

`RackFormFactorChoices` declares no extension key (`netbox/dcim/choices.py:54`, NetBox 4.6.8), so
this set is closed and the CRD can enforce it without risking a legitimately configured value —
unlike [`NetBoxDeviceType`](netboxdevicetype.md#specairflow)'s `airflow`.

### `spec.width`, `spec.uHeight`, `spec.startingUnit`

The three defaulted dimensions, all defaulted to NetBox's own default so the operator manages
them from the first reconcile: a field NetBox fills in and the operator never sends is a field
the operator can never correct.

- `width` is an **integer** enum — `10`, `19`, `21` or `23` inches — because the column is
  `PositiveSmallIntegerField`, so NetBox stores and returns a number. Default `19`, from
  `def=UNRESOLVED:RackWidthChoices.WIDTH_19IN`.
- `uHeight` defaults to `42` and `startingUnit` to `1`. The digest records both defaults as
  unevaluated symbols (`def=UNRESOLVED:RACK_U_HEIGHT_DEFAULT`,
  `def=UNRESOLVED:RACK_STARTING_UNIT_DEFAULT`); the values are read from
  `netbox/dcim/constants.py` in the same 4.6.8 tree.

`uHeight`'s floor is `1` because `startingUnit` is 1-based and a rack of no units has no elevation
to mount anything in. There is deliberately **no** ceiling: NetBox applies a tighter model-level
validator than the column's own `PositiveSmallIntegerField` range and the digest does not carry
its bound, so restating one here would be a guess. An over-tall rack comes back as NetBox's own
`400`, reported as `Ready=False, Reason=Invalid`.

### `spec.descUnits`

Optional boolean, `false` in NetBox. Numbers the rack units from the top down.

A pointer in the API, so three states are possible: absent leaves NetBox's value alone, `true`
writes true, `false` writes false. That is what makes adopting a hand-made descending rack type a
no-op instead of silently renumbering every unit in it.

### `spec.outerWidth`, `spec.outerHeight`, `spec.outerDepth`, `spec.maxWeight`, `spec.mountingDepth`

Optional integers, minimum `1`. `outer*` are in `outerUnit`; `maxWeight` is in `weightUnit`.

These have **two** states rather than three, and that is a statement about the columns rather than
an omission here. Each is `blank=True, null=True` and every value it can hold is a real
measurement, so there is no empty *value* to write: nil leaves NetBox's own value alone, a number
claims and sets it. Clearing one back to `null` from a manifest is NBO-060's audit item, not a
state these fields can express — so none of them carries the
[field-ownership](../concepts/field-ownership.md) tri-state note.

`mountingDepth` is the odd one: it is **always millimetres**, whatever `outerUnit` says. NetBox
documents the column that way on the model itself, and it is the one measurement here that does
not follow the unit choice.

### `spec.outerUnit`, `spec.weightUnit`

Optional enums: `""`, `mm` or `in`; and `""`, `kg`, `g`, `lb` or `oz`.

Unset leaves NetBox's own value alone; `""` clears it, and is sent as JSON `null` rather than as
an empty string. NetBox's serializer returns `null` for an unset choice, so a payload of `""`
would differ from the value read back on every pass — a PATCH loop rather than an error. The
descriptor opts each column in with `registry.Field.EmptyIsNull`
(`internal/registry/dcim_rackbase.go`).

Neither `RackDimensionUnitChoices` (`netbox/dcim/choices.py:108`) nor `WeightUnitChoices`
(`netbox/netbox/choices.py:184`) declares an extension key, so both sets are closed.
`WeightUnitChoices` lives outside `dcim` because `WeightMixin` is a NetBox-wide mixin.

### `spec.weight`

Optional **string**, matching `^([0-9]{1,6}(\.[0-9]{1,2})?)?$`.

A string and not a number, the same decision
[`NetBoxDeviceType.uHeight`](netboxdevicetype.md#specuheight) documents. NetBox stores it as
`weight DecimalField decimal(8,2)` and returns it padded — `"18.50"` for a spec that said
`"18.5"` — while an OpenAPI `number` round-trips through IEEE-754 on its way in and out of the API
server. The engine compares two numeric strings numerically (`internal/netbox/drift.go`,
`scalarEqual`), so `"18.5"` and NetBox's `"18.50"` are the same value and produce **no PATCH** on
the second reconcile.

The pattern is read straight off `decimal(8,2)`: eight digits, two after the point, so `0` to
`999999.99` in hundredths. The empty alternative is what makes the field clearable.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it — and `""` leaves as
JSON `null`, because DRF parses the empty string as a number and rejects it on a nullable
`DecimalField`. See [field ownership](../concepts/field-ownership.md).

### `spec.description`, `spec.comments`

Optional free text; `description` is capped at 200 characters and `comments` has no limit.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural keys

Two candidates, tried in this order, from `dcim.RackType.meta.constraints`:

```
UniqueConstraint(fields=('manufacturer', 'model'), name='..._unique_manufacturer_model')
UniqueConstraint(fields=('manufacturer', 'slug'),  name='..._unique_manufacturer_slug')
```

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(manufacturer, slug)` | `?manufacturer_id=<id>&slug=<slug>` | `manufacturerRef` **resolves** |
| 2 | `(manufacturer, model)` | `?manufacturer_id=<id>&model=<model>` | `manufacturerRef` **resolves** |

Both filters are registered. `manufacturer_id` is declared on `RackTypeFilterSet` as a
`ModelMultipleChoiceFilter`, and `model` and `slug` are in its `Meta.fields` alongside
`u_height`, `starting_unit`, `desc_units`, `outer_width`, `outer_height`, `outer_depth`,
`outer_unit`, `mounting_depth`, `weight`, `max_weight`, `weight_unit`, `description` and
`rack_count` (NetBox 4.6.8, `netbox/dcim/filtersets.py:336`).

Neither constraint is conditional, so **there is no null pin here** — unlike on
[`NetBoxDeviceRole`](netboxdevicerole.md) or [`NetBoxPlatform`](netboxplatform.md). `manufacturer`
is `REQ`; a manufacturer-less rack type does not exist to be looked up.

This pair *is* a fallback chain, and the [`NetBoxDeviceType`](netboxdevicetype.md#natural-keys)
argument for it applies unchanged. Both `model` and `slug` are required, so both candidates apply
together and the second is reached only when the first matched nothing. That is safe because of
what the constraints say: an object candidate 2 finds is the same make and model the spec
describes, and creating a second one would be a 409 on the unique index rather than a duplicate —
so adopting it and PATCHing the slug is strictly better than failing every reconcile.

`manufacturerRef` is **not deferred and cannot be**: both candidates match on it, so stripping it
from a create would mean the lookup asked a different question from the create it decided on
(`registry.ErrDeferredNaturalKey`).

With `manufacturerRef` declared and unresolved, **no** candidate applies and the engine waits.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.RackType` derives from `RackBase`, which is a `PrimaryModel`
(`docs/netbox-schema.md` → `dcim.RackType`, `dcim.RackBase`, bases), so it carries both `tags` and
`custom_fields` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the rack type exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `manufacturerRef` resolves | it does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Retry intervals are the endpoint's, not this kind's — see
[errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### `slug` is unique twice over, and the key still carries the manufacturer

This is the one place `dcim.RackType` and `dcim.DeviceType` are not the same model with two names.
`dcim.RackType.slug` is `SlugField REQ UNIQUE len=100` — a **column-level** unique — *as well as*
being half of `..._unique_manufacturer_slug` (`docs/netbox-schema.md` → `dcim.RackType`).
`dcim.DeviceType.slug` carries no such column unique, which is what makes `ubiquiti/ucg-ultra` and
`mikrotik/ucg-ultra` two legitimate device types.

So `(manufacturer, slug)` here is *stricter than the database needs*: two manufacturers cannot in
fact share a rack-type slug, and `?slug=` alone would identify the object.

The natural key still sends `manufacturer_id`, and deliberately. A candidate that drops a filter
matches **more** objects rather than fewer, and the pair is what the constraint names — so
following the constraint costs one query parameter and cannot be wrong, while trusting the column
unique would be a second reading of the schema that a NetBox release could quietly invalidate.

The practical consequence: two namespaces claiming `mcs-42u` under *different* manufacturers is a
`Conflict` here where it would be two objects on a device type. The second CR reports
`Ready=False, Reason=Conflict`.

### What a rack gets from a rack type

Setting `rackTypeRef` on a `NetBoxRack` makes NetBox copy this type's `RackBase` dimensions —
`width`, `u_height`, `starting_unit`, `desc_units`, the three `outer*`, `outer_unit`,
`mounting_depth`, `max_weight`, `weight` and `weight_unit` — onto the rack, **server-side, on
create**. The operator does not re-send them and does not have to; a rack built from a catalogue
entry can leave every dimension field out of its own manifest.

The twelve fields are the same twelve on both Kinds, and that is enforced rather than intended:
they live on one inline Go struct, `v1alpha1.RackDimensions` (`api/v1alpha1/dcim_rackbase.go`),
and one shared descriptor table, `rackBaseFields()` (`internal/registry/dcim_rackbase.go`).
Inline, so the CR field paths stay flat — `spec.uHeight`, not `spec.dimensions.uHeight` — which is
what lets a descriptor address them by their own JSON names and keeps the base class invisible to
the engine. `TestRackBaseFieldsAreIdenticalOnBothKinds`
(`internal/registry/dcim_racks_test.go`) is what stops an edit to one Kind's table making the same
YAML field write a different column depending on which Kind carried it.

### An unresolved manufacturer writes nothing

Both candidates start at `manufacturer_id`, so there is no identity to look up and nothing to
create. The object reports `RefsResolved=False` naming `manufacturerRef` and performs **zero
NetBox writes** — the [`NetBoxLocation`](netboxlocation.md) shape.

### `rack_count` is never sent and never diffed

`dcim.RackType` declares one `CounterCacheField`, `rack_count`, plus `RackBase`'s two
`_`-prefixed weight caches, `_abs_max_weight` and `_abs_weight`.

`rack_count` is in the serializer's write path and the API refuses it, so a write silently no-ops:
the next reconcile finds the same difference and PATCHes forever. All three are in the
descriptor's read-only list, which turns a future field map that ever reaches for one into a boot
failure (`registry.ErrFieldReadOnly`) instead. The two `_abs_*` columns are absent from the write
path entirely per the IR, and are listed anyway for the same reason.

### Deleting one is refused while a rack uses it

`dcim.Rack.rack_type` is `on_delete=PROTECT` (`docs/netbox-schema.md` → `dcim.Rack`), so NetBox
refuses to delete a rack type while any rack is built from it, and the CR reports
`Deleting=False, Reason=Protected`. Delete the racks first, or clear their `rackTypeRef`.

### No containment parent, and `deletionPolicy` defaults to `Delete`

`manufacturer` is the required foreign key, but it is `on_delete=PROTECT`: nothing on the server
side disappears when a manufacturer is deleted, because NetBox refuses that deletion. So there is
no server-side cascade for an owner reference to mirror and this Kind takes none
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

`deletionPolicy` defaults to `Delete` rather than `Retain` (#176): a rack type is configuration a
manifest recreates verbatim, not allocated state whose deletion frees something for somebody else
to take. What protects the racks built from it is `PROTECT`, not `Retain`. See
[deletion](../concepts/deletion.md).

### What is not here

- **`rack_count`, `_abs_max_weight`, `_abs_weight`** — read-only, as above.
- **`owner`** is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint, so
  there is no Kind to point at. See [coverage](../coverage.md).
- **`images`** is an `ImageAttachmentsMixin` `GenericRelation`: the reverse of somebody else's
  foreign key, not a column, and uploaded as multipart form data rather than JSON. Manage rack
  elevation images in the UI.
- **Rack elevation rendering and utilisation figures** are read-only computed views, not columns.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbracktype
NAME      MANUFACTURER   MODEL              U    ID   READY   AGE
mcs-42u   minkels        MCS 42U 800x1000   42   93   True    4m
apc-24u   apc            NetShelter 24U     24   94   True    4m
```

| Column | JSONPath |
|---|---|
| `MANUFACTURER` | `.spec.manufacturerRef.name` |
| `MODEL` | `.spec.model` |
| `U` | `.spec.uHeight` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`MANUFACTURER` reads `.spec.manufacturerRef.name`, so it shows the *intent* even while the
reference is unresolved and `ID` is empty — and it is blank for a reference given by `id`, `slug`
or `lookup`.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `manufacturerRef` | — | The field is required by the schema, because NetBox's column is `REQ`. | Name a manufacturer. |
| `kubectl apply` rejected: `formFactor is required on a rack type` | — | `formFactor` was `""`. The enum carries `""` for `NetBoxRack`'s sake; a rack type's column is `NOT NULL` with no default. | Pick a form factor. |
| `kubectl apply` rejected on `width` | — | The value is not `10`, `19`, `21` or `23`. `RackWidthChoices` is closed. | Use one of the four. |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved=False`, `Reason=RefNotFound` | `manufacturerRef` names a CR that does not exist. Nothing was written. | Create the [`NetBoxManufacturer`](netboxmanufacturer.md), or fix the name. |
| `RefsResolved=False`, `Reason=RefNotReady` | | The manufacturer CR exists but has no `status.id` yet. | Wait; check the manufacturer's own conditions. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace ref with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | See [Start with the grant](#start-with-the-grant). |
| `Ready=False`, `Reason=Conflict` | | Another namespace already owns this `(manufacturer, slug)` — or this `slug` under *any* manufacturer, since `slug` is column-unique here — and `onConflict` is `Fail`. | Set `onConflict: Adopt`, or resolve the duplicate declaration. |
| `Ready=False`, `Reason=Invalid`, message quotes a NetBox `400` on `u_height` | | Above NetBox's own model-level ceiling, which this CRD deliberately does not restate. | Lower it. |
| A PATCH on every resync | `Synced=False`, `Reason=DriftCorrected` repeatedly | Not expected for `weight` — it is compared numerically. | Read the `Synced` message for the field that differs; see [drift detection](../concepts/drift.md). |
| `Deleting=False`, `Reason=Protected` | | A rack is still built from this type. | Delete the racks, or clear their `rackTypeRef`. |

## Related

- [`NetBoxManufacturer`](netboxmanufacturer.md) — the required reference this kind's identity needs
- [`NetBoxDeviceType`](netboxdevicetype.md) — the same identity shape, and the `slug` that is *not* column-unique
- [`NetBoxRefGrant`](netboxrefgrant.md) — what a cross-namespace `manufacturerRef` or `rackTypeRef` needs
- [Lookups](../concepts/lookups.md) — candidate order, and why a null filter is pinned
- [Field ownership](../concepts/field-ownership.md) — absent versus empty versus set, and the two-state dimensions
- [Deletion](../concepts/deletion.md) — why this kind defaults to `Delete`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
