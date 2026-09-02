# `NetBoxRack`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRack` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrack` |
| Status subresource | yes |

A `NetBoxRack` is one `dcim.Rack` in NetBox: a physical cabinet or frame in a site, with a
height in rack units and a set of things mounted in it.

Two things about this Kind are worth knowing before the field list.

## It is *not* the kind NetBox 4.2's scope change broke

[`NetBoxPrefix`](netboxprefix.md) and [`NetBoxCluster`](netboxcluster.md) both lost their
`site` column in NetBox 4.2 and gained a `(scope_type, scope_id)` pair with cached
`site`/`location` columns behind it. Writing the cached `site` on those models returns `201`
and sets nothing — which is the trap `NetBoxCluster`'s page calls the one it broke *silently*.

`dcim.Rack` was not part of that change. The digest reads:

```
site      ForeignKey  REQ  -> dcim.Site      on_delete=PROTECT
location  ForeignKey       -> dcim.Location  on_delete=SET_NULL
```

(`docs/netbox-schema.md` → `dcim.Rack`), and the serializer's write path carries `site` and
`location` and neither `scope_type` nor `scope_id`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.Rack.write_path`). So there is **no `ScopeRef` union
on this Kind**, `spec.siteRef` is an ordinary foreign key the operator really does write, and
the mistake available here is the mirror image: sending a scope pair a rack has no columns for,
which DRF drops rather than rejecting, leaving a rack with no site and no drift ever.
`TestRackWritesSiteAsAForeignKeyAndNoScopePair` asserts the negative on every recorded request.

## NetBox does not enforce this Kind's identity

Both `meta.constraints` on `dcim.Rack` are keyed on `location`:

```python
models.UniqueConstraint(fields=('location', 'name'),        name='..._unique_location_name')
models.UniqueConstraint(fields=('location', 'facility_id'), name='..._unique_location_facility_id')
```

`location` is optional, so a rack with no location satisfies **neither** — Postgres treats
`NULL`s as distinct — and two identically named location-less racks in one site are legal server
state. This is the third Kind in that position, after [`NetBoxIPAddress`](netboxipaddress.md)
and [`NetBoxContact`](netboxcontact.md), and it is handled the same way: a convention key with
the null pinned, and `Conflict` rather than a guess when more than one object matches. See
[Natural keys](#natural-keys).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRack
metadata:
  name: r1
  namespace: default
spec:
  endpointRef: homelab
  name: R1
  # Required: dcim.Rack.site is a REQ foreign key, and every natural-key candidate reads
  # `site_id` or `location_id`. Until this resolves the object writes nothing at all.
  siteRef:
    name: home
```

`siteRef` needs a [`NetBoxSite`](netboxsite.md) named `home` in this namespace, or a
[grant](netboxrefgrant.md) and a `namespace:` to reach one elsewhere.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRack
metadata:
  name: r1
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail          # default
  deletionPolicy: Delete    # default: a rack is configuration, not allocated state

  name: R1
  siteRef:
    name: home

  # Setting locationRef is what gives this rack a database-backed identity, `(location, name)`.
  # Leaving it unset gets the `(site, name)` convention instead -- see Natural keys.
  locationRef:
    name: ground-floor
  groupRef:
    name: cage-1
  roleRef:
    name: compute
  # NetBox copies this type's RackBase dimensions onto the rack server-side on create, so the
  # dimension fields below need not be restated for a rack built from the catalogue.
  rackTypeRef:
    name: mcs-42u
  tenantRef:
    name: acme

  status: active            # default
  formFactor: 4-post-cabinet
  airflow: front-to-rear
  facilityID: C3.14
  serial: MC2024-0007
  assetTag: ASSET-0001

  # The twelve dcim.RackBase dimensions. The first three are NetBox's own defaults written
  # out; the rest are unset by default.
  width: 19                 # default
  uHeight: 42               # default
  startingUnit: 1           # default
  descUnits: false
  outerWidth: 600
  outerHeight: 2000
  outerDepth: 1200
  outerUnit: mm
  mountingDepth: 1000       # always millimetres, whatever outerUnit says
  maxWeight: 1200
  # A string, not a number: NetBox returns `weight` padded to two places, and the engine
  # compares the two numerically, so this produces no PATCH on the second reconcile.
  weight: "18.5"
  weightUnit: kg

  description: Compute cabinet, ground floor
  comments: |
    Replaces the 2019 frame. PDU feeds A and B.
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `customFields`.
See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ len=100` |
| `siteRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxSite` | yes | — | ref arity CEL | `site ForeignKey REQ -> dcim.Site on_delete=PROTECT` |
| `locationRef` | `ObjectRef` → `NetBoxLocation` | no | — | | `location ForeignKey -> dcim.Location on_delete=SET_NULL` |
| `groupRef` | `ObjectRef` → `NetBoxRackGroup` | no | — | | `group ForeignKey -> dcim.RackGroup on_delete=PROTECT` |
| `rackTypeRef` | `ObjectRef` → `NetBoxRackType` | no | — | | `rack_type ForeignKey -> dcim.RackType on_delete=PROTECT` |
| `roleRef` | `ObjectRef` → `NetBoxRackRole` | no | — | | `role ForeignKey -> dcim.RackRole on_delete=PROTECT` |
| `tenantRef` | `ObjectRef` → `NetBoxTenant` | no | — | | `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT` |
| `status` | `string` | no | `active` | enum: `reserved`, `available`, `planned`, `active`, `deprecated` | `status CharField len=50` |
| `formFactor` | `string` | no | — | enum: `""`, `2-post-frame`, `4-post-frame`, `4-post-cabinet`, `wall-frame`, `wall-frame-vertical`, `wall-cabinet`, `wall-cabinet-vertical` | `form_factor CharField len=50` |
| `airflow` | `string` | no | — | enum: `""`, `front-to-rear`, `rear-to-front` | `airflow CharField len=50` |
| `facilityID` | `string` | no | — | ≤50 | `facility_id CharField len=50` |
| `serial` | `string` | no | — | ≤50 | `serial CharField len=50` |
| `assetTag` | `string` | no | — | ≤50 | `asset_tag CharField UNIQUE len=50` |
| `width` | `integer` | no | `19` | enum: `10`, `19`, `21`, `23` | `width (RackBase) PositiveSmallIntegerField` |
| `uHeight` | `integer` | no | `42` | ≥1 | `u_height (RackBase) PositiveSmallIntegerField` |
| `startingUnit` | `integer` | no | `1` | ≥1 | `starting_unit (RackBase) PositiveSmallIntegerField` |
| `descUnits` | `boolean` | no | — | | `desc_units (RackBase) BooleanField def=False` |
| `outerWidth` | `integer` | no | — | ≥1 | `outer_width (RackBase) PositiveSmallIntegerField` |
| `outerHeight` | `integer` | no | — | ≥1 | `outer_height (RackBase) PositiveSmallIntegerField` |
| `outerDepth` | `integer` | no | — | ≥1 | `outer_depth (RackBase) PositiveSmallIntegerField` |
| `outerUnit` | `string` | no | — | enum: `""`, `mm`, `in` | `outer_unit (RackBase) CharField len=50` |
| `mountingDepth` | `integer` | no | — | ≥1 | `mounting_depth (RackBase) PositiveSmallIntegerField` |
| `maxWeight` | `integer` | no | — | ≥1 | `max_weight (RackBase) PositiveIntegerField` |
| `weight` | `string` | no | — | `^([0-9]{1,6}(\.[0-9]{1,2})?)?$` | `weight (WeightMixin) DecimalField decimal(8,2)` |
| `weightUnit` | `string` | no | — | enum: `""`, `kg`, `g`, `lb`, `oz` | `weight_unit (WeightMixin) CharField len=50` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

The twelve dimension fields come from NetBox's abstract `dcim.RackBase`
(`docs/netbox-schema.md` → `dcim.RackBase`) and are declared once, on the inline Go struct
`v1alpha1.RackDimensions` (`api/v1alpha1/dcim_rackbase.go`). `NetBoxRackType` embeds the same
struct, so the two Kinds cannot drift apart on a bound, and the field paths stay flat —
`spec.uHeight`, not `spec.dimensions.uHeight`.

### `spec.siteRef`

The site the rack stands in. Required, because NetBox's column is, and because every
natural-key candidate reads either `site_id` or `location_id`.

**If it is wrong.** An unresolvable `siteRef` gives `RefsResolved=False` with `RefNotFound`,
`RefNotReady`, `RefDenied`, `RefAmbiguous` or `RefTargetFailed` naming the field, and
`Ready=False, Reason=WaitingForRef` — and **zero NetBox writes**, because no candidate is
applicable without it. That is asserted on the recorded traffic
(`TestRackWithAnUnresolvableSiteWritesNothing`), not only on the status: a version that
reported the reference and created the rack anyway would look identical in the conditions.
An absent `siteRef` is rejected by the API server before the operator sees the object.

`siteRef` is **not** the containment parent — see
[No containment parent, in either direction](#no-containment-parent-in-either-direction).

### `spec.locationRef`

The room or row within the site. The single most load-bearing optional field in this spec,
because it is what both of NetBox's unique constraints are keyed on: setting it moves this
object from a convention identity to a database-backed one.

**If it is wrong.** Same conditions as `siteRef`, and the same "nothing is written": with
`locationRef` declared and unresolved, candidate 1 is not applicable (the value does not
resolve) and candidate 2 is not applicable either (the field *is* declared), so the engine
waits. It must: candidate 2 would otherwise find a location-less rack of this name in this site
and adopt it, and the follow-up PATCH would move somebody else's rack into a room
([NBO-015](../concepts/lookups.md)).

A pointer to a typed alias, so it has **two** states rather than three: absent means unmanaged,
and a value claims the column. `SET_NULL` means NetBox *can* hold a rack in no particular room,
but clearing the column from a manifest needs `registry.Field.EmptyIsNull` and a
`v1alpha1.OptionalRef` — a third state no shipped Kind uses yet. Until then, move a rack out of
a location by clearing it in NetBox and no longer declaring the field.

### `spec.rackTypeRef`

The catalogue entry the rack is built from. Setting it makes NetBox copy the type's `RackBase`
dimensions onto this rack **server-side on create** — the operator does not re-send them, and
does not have to. `NetBoxRackType`'s page has the other end of it.

**If it is wrong.** `RefsResolved=False` naming the field, and nothing is written: an
unresolved reference on this Kind blocks the write rather than being deferred, because the
dimensions the type supplies are part of what the rack is. `PROTECT`, so deleting a rack type
still in use is refused on the *type*.

### `spec.groupRef`, `spec.roleRef`, `spec.tenantRef`

Three ordinary references, all `PROTECT`, none in any natural-key candidate. `roleRef` points at
`dcim.RackRole` and not at the `dcim.DeviceRole` a [`NetBoxDevice`](netboxdevice.md)'s
identically named field uses, nor at `ipam.Role` — three separate NetBox models spell "role".

`groupRef` points at a flat label rather than a position in a hierarchy: `dcim.RackGroup` is an
`OrganizationalModel` with no `parent` column at all
(`docs/netbox-schema.md` → `dcim.RackGroup`), which is not what its name suggests.

`tenantRef` does not cascade and contributes no owner reference — see
[`NetBoxTenant`](netboxtenant.md) for why, and
[references](../concepts/references.md) for why a namespace does not imply a tenant.

**If any is wrong.** `RefsResolved=False` naming the field, `Ready=False, Reason=WaitingForRef`.
A `PROTECT`-refused deletion of the *target* surfaces on the target as
`Deleting=False, Reason=Protected` naming this rack.

### `spec.status`

The rack's lifecycle state, defaulted to NetBox's own default so the operator manages the field
from the first reconcile — a defaulted field that never reaches a payload is a field the
operator can never correct.

Five members, read from `netbox/dcim/choices.py:90` in the 4.6.8 tree the digest was taken from.
Note that they are **not** [`NetBoxSite`](netboxsite.md)'s five: `reserved` and `available` are
rack-specific, and `staging`, `decommissioning` and `retired` are absent. `RackStatusChoices`
declares `key = 'Rack.status'`, so a deployment can add values through `FIELD_CHOICES`; a value
added there needs this CRD's enum widened.

**If it is wrong.** A value outside the enum is rejected by the API server, naming the field.

### `spec.formFactor`

The rack's physical construction. Seven members from `netbox/dcim/choices.py:54`, plus `""`.

Optional here and **required on `NetBoxRackType`**, and the asymmetry is the column's rather
than a choice: this one is `blank=True, null=True` and `dcim.RackType.form_factor` is `REQ` with
no default (`docs/netbox-schema.md`). Unset leaves NetBox's own value alone; `""` clears it,
which is how NetBox spells "unspecified". Those are two different instructions and the operator
tells them apart from `metadata.managedFields`
([field ownership](../concepts/field-ownership.md)).

Cleared as JSON `null` rather than as `""`, because NetBox's serializer returns `null` for an
unset choice and a payload of `""` would differ from the value read back on every pass — a
PATCH loop rather than an error (`registry.Field.EmptyIsNull`; asserted by
`TestRackAirflowIsClearedWithNull`).

### `spec.airflow`

Two members from `netbox/dcim/choices.py:130`, plus `""`, cleared as `null` for the reason
`formFactor` gives.

A different set from [`NetBoxDeviceType`](netboxdevicetype.md)'s `airflow`, whose ten members
include `passive`, `mixed` and six side-to-side directions. They are two separate `ChoiceSet`s
in NetBox, both extensible through `FIELD_CHOICES`, so sharing one enum would make a value added
to one silently legal on the other.

### `spec.facilityID`

The rack's designation in the facility's own numbering. Setting it gives the rack a
database-backed *second* identity, `(location, facility_id)`, which is what lets the operator
adopt a rack the facility renamed — see candidate 3 below.

`facility_id` and not a reference: it is free text the data centre assigns, not an id of
anything in NetBox.

Clearable: omit it to leave NetBox's own value alone, set it to `""` to clear it.

### `spec.assetTag`

The organisation's own inventory tag, and the one column on this model with a global `UNIQUE`
(`asset_tag CharField UNIQUE len=50`).

It is deliberately **not** a natural-key candidate. The asset tag identifies a chassis and this
CR describes a rack position, so adopting by asset tag would let moving a chassis silently
rewrite the site and location of somebody else's rack.

**If it is wrong.** A duplicate is NetBox's own `409` on the unique index, reported as
`Ready=False, Reason=Invalid` with NetBox's message.

### `spec.weight`

A string and not a number, the same decision [`NetBoxDeviceType`](netboxdevicetype.md#specuheight)
documents for `uHeight`. NetBox stores it as `weight DecimalField decimal(8,2)` and returns it
padded — `"18.50"` for a spec that said `"18.5"` — and an OpenAPI `number` round-trips through
IEEE-754 on the way in and out of the API server. The engine compares two numeric strings
numerically (`internal/netbox/drift.go`, `scalarEqual`), so `"18.5"` and `"18.50"` produce no
PATCH; `TestRackDoesNotHotLoopOnItsDecimalWeight` holds that from the outside.

The pattern is read off `decimal(8,2)`: eight digits, two after the point. Clearable, and the
empty string is sent as `null` because DRF parses `""` as a number and rejects it on a nullable
`DecimalField`.

### `spec.outerWidth`, `spec.outerHeight`, `spec.outerDepth`, `spec.mountingDepth`, `spec.maxWeight`

Five `*int32`s with **two** states rather than three, and that is a statement about the columns:
each is `blank=True, null=True` and every value it can hold is a real measurement, so there is
no empty *value* to write. Nil leaves NetBox's own value alone; a number claims and sets it.
Clearing one back to `null` is NBO-060's audit item.

`mountingDepth` is in millimetres regardless of `outerUnit` — NetBox documents that on the model
itself, and it is the one measurement here that does not follow the unit choice.

### `spec.uHeight`, `spec.startingUnit`, `spec.width`, `spec.descUnits`

All defaulted or pointer-typed so the three states behave. `width`'s four members are
`netbox/dcim/choices.py:75` at 4.6.8 — 10, 19, 21 and 23 inches — and an integer rather than a
string because the column is `PositiveSmallIntegerField`.

`uHeight` defaults to 42 and `startingUnit` to 1: the digest records both as
`def=UNRESOLVED:RACK_U_HEIGHT_DEFAULT` and `def=UNRESOLVED:RACK_STARTING_UNIT_DEFAULT`, symbols
the AST walk does not evaluate, and `netbox/dcim/constants.py` in the same 4.6.8 tree resolves
them to 42 and 1. `uHeight` carries no `maximum`: NetBox applies a tighter model-level validator
than the column's own range and the digest does not carry its bound, so restating one here would
be a guess — an over-tall rack comes back as NetBox's own `400`.

`descUnits` is a `*bool`, and the reason is the column's default. A plain `bool` cannot tell
"not managed" from "managed as false", so adopting a hand-made descending rack would silently
renumber every unit in it.

### `spec.description`, `spec.comments`

Both clearable: omit one to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)). `comments` is a `TextField` and so carries
no `maxLength`.

## Natural keys

Three candidates, and only ever two applicable to one object, because the first two disagree
about whether `locationRef` is declared.

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(location, name)` | `?location_id=<id>&name=<name>` | `dcim_rack_unique_location_name` |
| 2 | `(site, name)` with `location` null | `?site_id=<id>&name=<name>&location_id=null` | **nothing** — a convention |
| 3 | `(location, facility_id)` | `?location_id=<id>&facility_id=<facility>` | `dcim_rack_unique_location_facility_id` |

Every filter is registered: `RackFilterSet` declares `site_id` (`ModelMultipleChoiceFilter`) and
`location_id` (`TreeNodeMultipleChoiceFilter`), and `name` and `facility_id` are in its
`meta_fields` (NetBox 4.6.8, `netbox/dcim/filtersets.py`).

**Candidate 1** is the database constraint, and the one a rack that names a location uses.

**Candidate 2** is the convention for a rack that names none, and the null pin is what makes it
safe. Without it the lookup would match a rack of that name in a room somebody else declared and
adopt it. `NaturalKey.Applicable` offers this candidate only while `locationRef` is
*undeclared*, so a rack whose location has not been created yet waits rather than falling
through — see [`spec.locationRef`](#speclocationref).

`?location_id=null` is the wire spelling of `registry.NullColumnRef`. `location_id` is a
`TreeNodeMultipleChoiceFilter`, which subclasses the `ModelMultipleChoiceFilter` that
[`NetBoxLocation`](netboxlocation.md)'s own `parent_id` is already pinned this way (#216), so
the pin goes over the wire through django-filter's `null_value` rather than through an
`__isnull` suffix `BaseFilterSet` would drop.

**Candidate 3** is NetBox's second constraint, reached only when candidate 1 matched nothing and
both halves are set. It is a fallback chain in the
[`NetBoxDeviceType`](netboxdevicetype.md#natural-keys) sense and safe for the same reason: the
pair is unique in the database, so the rack it finds *is* the facility slot the spec describes
and creating a second one would be a `409`. Adopting it and PATCHing the name beats failing
every reconcile, which is what a facility rename would otherwise cause.

`name` leads rather than `facility_id` because it is the identity NetBox itself orders and
indexes on: `meta.ordering: ('site', 'location', 'name', 'pk')` and
`meta.indexes: (models.Index(fields=('site', 'location', 'name', 'id')),)`.

There is deliberately **no** `(site, facility_id)` variant to match candidate 2. No constraint
backs it and neither does `meta.ordering`, so it would be a second guess layered on the first —
a location-less rack that has only a facility id gets no candidate at all rather than an
invented one.

### More than one match is a `Conflict`

Because candidate 2 is a convention, two location-less racks called `R1` in one site are a state
NetBox will happily hold. The lookup then matches both, and the engine reports
`Ready=False, Reason=Conflict` naming the matches rather than adopting one
([errors and retries](../concepts/errors-and-retries.md)). The fix is to give the racks
locations, or distinct names.

Give one of them a `locationRef` and it moves to candidate 1 and becomes unambiguous — which is
the same shape as [`NetBoxClusterGroup`](netboxclustergroup.md)'s "setting a cluster's group is
what makes that cluster's lookup unambiguous".

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`provenance`, `conflict`, `deferredPending`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.naturalKey` is the field to read first on this Kind: it records *which* candidate
located the object, filter by filter, so "why was this rack adopted" and "why was it not" are
answerable without re-deriving the candidate list.

`dcim.Rack` derives from `RackBase`, which is a `PrimaryModel`, so it carries both `tags` and
`custom_fields` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the rack exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `siteRef` resolves and every optional reference is unset or resolves | any does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals: [errors and retries](../concepts/errors-and-retries.md).
`WaitingForKey` is the one worth naming here — it is what a rack reports when its references
resolve but no candidate is applicable, which on this Kind means `locationRef` is declared and
still pending.

## Kind-specific behaviour

### No containment parent, in either direction

Every foreign key on `dcim.Rack` is `PROTECT` except `location`, which is `SET_NULL`. Neither
qualifies as a containment parent under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4, so this Kind takes **no owner
reference from anything** — the [`NetBoxDevice`](netboxdevice.md) shape rather than the
[`NetBoxLocation`](netboxlocation.md) one.

That is a consequence rather than a gap, and the alternative is worse in both directions:

- An owner reference on `siteRef` would promise a cluster-side cascade NetBox refuses to
  perform. `kubectl delete netboxsite` would garbage-collect the rack CRs, their finalizers
  would issue `DELETE`s NetBox rejects with `PROTECT`, and the racks would be gone from
  Kubernetes and still in NetBox. `registry.ErrContainmentNotCascade` refuses that descriptor at
  boot.
- An owner reference on `locationRef` would delete the rack CR over a column NetBox merely
  *cleared*.

So deleting a site that still has racks is **refused by NetBox**, and surfaces on the *site* as
`Deleting=False, Reason=Protected` naming the blocker. Delete the racks first. NBO-051's ticket
asks for the cascade from `siteRef`; the schema does not support it, and this is the divergence.

The one cascading foreign key in the whole rack hierarchy is
`dcim.RackReservation.rack`, so a rack reservation *does* take an owner reference on its rack —
`NetBoxRackReservation`'s page has it.

### A rack type's dimensions arrive without the operator sending them

Set `rackTypeRef` and leave the dimension fields alone: NetBox copies the type's `RackBase`
columns onto the rack on create. The three defaulted fields (`width`, `uHeight`,
`startingUnit`) are NetBox's own defaults, so a rack that names a type and restates nothing is
written with the values NetBox would have used anyway.

Restating a dimension that disagrees with the type is legal and wins — it is an ordinary managed
field. What it is not is a way to *change* the type.

### Two counter caches are never sent and never diffed

`device_count` and `powerfeed_count` are in the serializer's write path and read-only there
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.Rack.write_path`), and `RackBase` adds
`_abs_max_weight` and `_abs_weight` — `_`-prefixed caches NetBox maintains from `maxWeight`,
`weight` and `weightUnit`. All four are in the descriptor's read-only list. NetBox **ignores** a
write to any of them rather than refusing it, so an unguarded field map would produce a
difference the next reconcile finds again, and PATCH forever.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A rack is configuration a manifest recreates
verbatim; nothing about deleting one frees a resource for somebody else, which is what `Retain`
exists for. See [deletion](../concepts/deletion.md).

Deleting a rack CR does not, in practice, delete much: `dcim.Device.rack` is `PROTECT`, so a
rack with devices in it is refused.

### What is not here yet

- **`Device.rack`, `position` and `face`.** This Kind makes the target real; mounting a device
  in it does not arrive with it. `(rack, position, face)` is one of `dcim.Device`'s
  `UniqueConstraint`s, so the three columns arrive together and re-derive that Kind's natural
  keys. Recorded as a note in [coverage](../coverage.md) rather than left to be re-derived.
- **Elevation rendering and utilisation figures.** Read-only, computed views
  (`dcim/racks/<id>/elevation/`), not columns.
- **A `ContactAssignment` on a rack.** A rack is already a legal target of the union on
  [`NetBoxContactAssignment`](netboxcontactassignment.md); the assignment is written from that
  object, not from here.
- **`vlanGroups`, `contacts` and `images`** are `GenericRelation`s — the far end of somebody
  else's foreign key. A rack is a legal [`NetBoxVLANGroup`](netboxvlangroup.md) scope, and again
  that is written from the other object.
- **`owner`.** `ForeignKey -> users.Owner`, and `users/*` is an excluded endpoint, so nothing
  will ever write it.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbrack
NAME   SITE   LOCATION       U    STATUS   ID    READY   AGE
r1     home   ground-floor   42   active   140   True    4m
r2     home                  24   planned  141   True    4m
```

| Column | JSONPath |
|---|---|
| `SITE` | `.spec.siteRef.name` |
| `LOCATION` | `.spec.locationRef.name` |
| `U` | `.spec.uHeight` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`SITE` and `LOCATION` read the *spec*, so they show the intent even while the reference is
unresolved and `ID` is empty. `LOCATION` is blank for a rack using candidate 2, which is exactly
the set of racks whose identity is a convention.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `siteRef` | — | The field is required by the schema, because NetBox's column is `REQ`. | Name a site. |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | A reference names an object that does not exist. | Create it, or fix the name. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace reference with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant. |
| `Ready=False`, `Reason=WaitingForKey` | `RefsResolved=True` | `locationRef` is declared and still pending, so no candidate is applicable. The engine is waiting on purpose. | Wait, or drop `locationRef`. |
| `Ready=False`, `Reason=Conflict` naming two matches | | Two location-less racks of this name in this site — a state no constraint prevents. | Give them locations, or distinct names. |
| `Ready=False`, `Reason=Conflict` naming a namespace | | Another namespace already owns this rack. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner. |
| `Ready=False`, `Reason=Invalid`, message about `asset_tag` | | The asset tag is already on another rack; the column is globally `UNIQUE`. | Change it, or clear it on the other rack. |
| `Ready=False`, `Reason=Invalid`, message about `u_height` | | Above NetBox's own model validator, which this CRD deliberately does not restate. | Lower it. |
| A PATCH on every resync | `Synced=False` | Not expected for `weight` — it is compared numerically. | Read the `Synced` message for the field that differs, and `status.lastAppliedHash`. |
| `kubectl apply` rejected on `status`, `formFactor` or `airflow` | — | The value is not in the enum. A NetBox that extended the `ChoiceSet` through `FIELD_CHOICES` needs this CRD's enum widened. | Use a listed value. |
| Deleting the site is refused | on the *site*: `Deleting=False`, `Reason=Protected` | `dcim.Rack.site` is `PROTECT`, and this Kind takes no owner reference for exactly that reason. | Delete the racks first. |
| `Deleting=False`, `Reason=Protected` on the rack | | A device, power feed or reservation still points at it. | Delete those first. |

## Related

- `NetBoxRackType` — the catalogue entry `rackTypeRef` points at, and where the dimensions come
  from
- `NetBoxRackRole` and `NetBoxRackGroup` — the two label Kinds a rack points at
- `NetBoxRackReservation` — the one Kind in this family with a containment parent, which is this
  one
- [`NetBoxSite`](netboxsite.md) and [`NetBoxLocation`](netboxlocation.md) — the required
  reference and the optional one its identity turns on
- [`NetBoxCluster`](netboxcluster.md) and [`NetBoxPrefix`](netboxprefix.md) — the two Kinds
  NetBox 4.2's scope change *did* break, for contrast
- [`NetBoxIPAddress`](netboxipaddress.md) and [`NetBoxContact`](netboxcontact.md) — the other two
  Kinds whose identity no constraint backs
- [Lookups](../concepts/lookups.md) — candidate order, why a null filter is pinned, and why more
  than one match is an error
- [Ownership](../concepts/ownership.md) and
  [ADR-0003](../decisions/0003-ownership-and-references.md) — why a `PROTECT` foreign key gets no
  owner reference
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
