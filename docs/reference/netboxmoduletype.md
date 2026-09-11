# `NetBoxModuleType`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxModuleType` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbmoduletype` |
| Status subresource | yes |

A `NetBoxModuleType` is one `dcim.ModuleType` in NetBox: one make and model of *module* — a line
card, a power supply, an optic — in the hardware catalogue, so that
[`NetBoxModule.moduleTypeRef`](netboxmodule.md) has something to point at.

It is [`NetBoxDeviceType`](netboxdevicetype.md)'s and [`NetBoxRackType`](netboxracktype.md)'s
identity shape with **one constraint instead of two**, and the missing one is the whole of what
makes this page worth reading rather than a diff against those. `dcim.ModuleType` has no `slug`
column, so there is no `(manufacturer, slug)` candidate to fall back to and a model rename is a
new object rather than a PATCH — see [Natural key](#natural-key).

The other thing to know before writing one is that `spec.attributes` is not the column it looks
like: the model calls it `attribute_data` and the API calls it `attributes`, and NetBox drops a
field name it does not recognise rather than rejecting it. See
[`attributes` is `attribute_data` under another name](#attributes-is-attribute_data-under-another-name).

## Start with the grant

A module type is catalogue data and a module is not, so this Kind sits on the same namespace
boundary [`NetBoxDeviceType`](netboxdevicetype.md#start-with-the-grant) does: a module in `team-a`
names a `moduleTypeRef` in `netbox-catalog`, and the module type itself names a `manufacturerRef`
and a `profileRef` there. Every direction that crosses a namespace needs a
[`NetBoxRefGrant`](netboxrefgrant.md) **in the namespace being referenced**. Without it the
object sits at `RefsResolved=False, Reason=RefDenied` and writes nothing.

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
kind: NetBoxModuleType
metadata:
  name: sfp-10g-lr
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  manufacturerRef:
    name: cisco                    # same namespace, so no grant needed for this one
  model: SFP-10G-LR
```

A team namespace then names it across the boundary:

```yaml
spec:
  moduleTypeRef:
    namespace: netbox-catalog
    name: sfp-10g-lr
```

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModuleType
metadata:
  name: sfp-10g-lr
  namespace: default
spec:
  endpointRef: homelab
  manufacturerRef:
    name: cisco
  model: SFP-10G-LR
```

Two required fields, and both are the natural key. `manufacturerRef` is not optional: the API
server rejects a `NetBoxModuleType` without one, because NetBox's column is `REQ` and the only
candidate starts at it.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModuleType
metadata:
  name: sfp-10g-lr
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly — Fail is the default
  deletionPolicy: Delete      # Delete | Retain — Delete is this kind's default

  manufacturerRef:
    name: cisco
  model: SFP-10G-LR           # the other half of the only natural key

  profileRef:                 # optional, and deliberately not part of the identity
    name: optic

  partNumber: SFP-10G-LR=
  airflow: passive            # "" | front-to-rear | rear-to-front | left-to-right
                              # | right-to-left | side-to-rear | passive

  # Validated by NetBox against profileRef's schema, and by nothing on this side.
  attributes:
    form_factor: "SFP+"
    reach_km: 10
    wavelength_nm: 1310

  weight: "0.08"              # a string, not a number. See spec.weight.
  weightUnit: kg              # "" | kg | g | lb | oz

  description: 10GBASE-LR SFP+ transceiver, 1310 nm, 10 km
  comments: |
    Ordered in trays of ten.
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxmoduletype.yaml`](../../config/samples/netbox_v1alpha1_netboxmoduletype.yaml),
and [`../examples/modules.yaml`](../examples/modules.yaml) applies one end to end with the
profile it validates against and the bay and module that use it.

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope and
behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `manufacturerRef` | [`ObjectRef`](../concepts/references.md) | **yes** | — | `manufacturer ForeignKey REQ -> dcim.Manufacturer on_delete=PROTECT` |
| `model` | `string`, 1–100 | **yes** | — | `model CharField REQ len=100` |
| `profileRef` | [`ObjectRef`](../concepts/references.md) | no | — | `profile ForeignKey -> dcim.ModuleTypeProfile on_delete=PROTECT` |
| `partNumber` | `string`, ≤50 | no | — | `part_number CharField len=50` |
| `airflow` | enum `"";front-to-rear;rear-to-front;left-to-right;right-to-left;side-to-rear;passive` | no | — | `airflow CharField len=50 choices=ModuleAirflowChoices` |
| `attributes` | JSON document | no | — | `attribute_data JSONField`, written as `attributes` |
| `weight` | `string`, `^([0-9]{1,6}(\.[0-9]{1,2})?)?$` | no | — | `weight (WeightMixin) DecimalField decimal(8,2)` |
| `weightUnit` | enum `"";kg;g;lb;oz` | no | — | `weight_unit (WeightMixin) CharField len=50` |
| `description` | `string`, ≤200 | no | — | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | `comments (PrimaryModel) TextField` |

Every column is from `docs/netbox-schema.md` → `dcim.ModuleType`.

### `spec.manufacturerRef`

**Required.** An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxManufacturer`](netboxmanufacturer.md).

Required because NetBox's column is `manufacturer ForeignKey REQ -> dcim.Manufacturer
on_delete=PROTECT`. It is also half of the only natural key, so until it resolves the object
reports `RefsResolved=False` naming this field and makes **no NetBox write at all** — not a
create without the field, which would put the module type under whichever manufacturer NetBox
defaulted to. There is nothing to create: a manufacturer-less module type is not a state NetBox
has.

Written to NetBox as `manufacturer`, filtered as `manufacturer_id`.

`PROTECT`, so NetBox refuses to delete a manufacturer while any module type points at it. That
surfaces on the *manufacturer* as `Deleting=False, Reason=Protected`.

### `spec.model`

**Required**, 1–100 characters. The model name as NetBox displays it, and the other half of the
natural key.

Unique per manufacturer (`..._unique_manufacturer_model`), not globally, so two manufacturers may
both sell an `SFP-10G-LR`.

Unlike [`NetBoxRackType.model`](netboxracktype.md#specmodel) this is the *only* value-bearing half
of the identity, because there is no `slug` beside it. Editing it therefore changes what the CR
is looking for — see
[A model rename is a new object](#a-model-rename-is-a-new-object-and-there-is-no-slug-to-absorb-it).

### `spec.profileRef`

Optional. An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md), which is what
[`attributes`](#specattributes) is validated against.

**Deliberately not part of the natural key**, and that is worth stating because the schema
gestures the other way. `dcim.ModuleType.meta.ordering` is `('profile', 'manufacturer', 'model')`
and its `meta.indexes` is `(models.Index(fields=('profile', 'manufacturer', 'model')),)` — the
profile leads both. The *unique constraint* names `(manufacturer, model)` and nothing else, so
two module types of one manufacturer and model cannot exist under different profiles, and adding
`profile_id` to the lookup would narrow the query below what the database enforces. A candidate
that adds a filter matches fewer objects than the constraint allows, which is how a settled
object stops being found and gets duplicated. Ordering and indexes are performance facts; the
constraint is the identity.

`PROTECT`, so a profile cannot be deleted while a module type claims it, and there is no
containment parent here for the same reason.

The reference resolves in `name`, `id` or `lookup` mode. **Not `slug`** —
`dcim.ModuleTypeProfile` has no `slug` column, so that filter matches nothing.

### `spec.partNumber`

Optional, up to 50 characters. The manufacturer's ordering part number.

Not unique in NetBox and therefore not a lookup candidate, however much it looks like one: two
module types may carry the same part number, and `?part_number=` would match both.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it — see
[field ownership](../concepts/field-ownership.md).

### `spec.airflow`

Optional enum: `""`, `front-to-rear`, `rear-to-front`, `left-to-right`, `right-to-left`,
`side-to-rear` or `passive`. The direction air moves through a module of this type.

Six values, read from `ModuleAirflowChoices` (`netbox/dcim/choices.py:264`, NetBox 4.6.8), plus
`""` because the column is `blank=True, null=True`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleType.airflow`), so "unspecified" is a real state.

It is a **different Go type** from `RackAirflow` and `DeviceAirflow`, and that separation is
load-bearing rather than fussy. The three are separate `ChoiceSet`s in NetBox and this one
declares `key = 'Module.airflow'`
(`hack/testdata/api-schema-4.6.8.json.gz` → `choices.ModuleAirflowChoices`), so a deployment can
extend it through `FIELD_CHOICES` **independently of the other two**. One shared Go enum would
make a value added to a rack's airflow silently legal on a module type, and the reverse.

The set is enumerated in the CRD anyway, following [`NetBoxRack`](netboxrack.md)'s `status`: a
typo caught by `kubectl apply` is worth more than an extension nobody has made, and widening the
enum when somebody does is a one-line change.

Unset leaves NetBox's own value alone; `""` clears it, and is sent as JSON `null` rather than as
an empty string. NetBox's serializer returns `null` for an unset choice, so a payload of `""`
would differ from the value read back on every pass — a PATCH loop rather than an error. The
descriptor opts the column in with `registry.Field.EmptyIsNull` (#170).

### `spec.attributes`

Optional. The profile-specific attribute document, carried through to NetBox and back unchanged.

**The spec field is `attributes` and the model column is `attribute_data`.** That is not a
convenience rename by the operator; it is what NetBox's own API calls the column, and writing the
model's name would silently do nothing. See
[`attributes` is `attribute_data` under another name](#attributes-is-attribute_data-under-another-name)
for the evidence and the failure mode.

Validated **server-side** against [`profileRef`](#specprofileref)'s `schema`, and nowhere else.
The operator sends the document opaquely; an invalid one is NetBox's own field-level `400`,
surfaced verbatim in the `Ready` message with `Reason=Invalid`, and the engine backs off on the
endpoint's schedule rather than retrying hot. There is deliberately no client-side JSON-Schema
check — see
[The operator does not validate the schema](netboxmoduletypeprofile.md#the-operator-does-not-validate-the-schema).

A document with no profile behind it is legal: NetBox stores whatever JSON it is given when
`profile` is null.

Typed `JSONDocument` in the Go API — an alias for `apiextensions.JSON`, so the CRD carries
`x-kubernetes-preserve-unknown-fields: true` and the API server prunes nothing out of the
document. It is `registry.ClassJSON` on the descriptor rather than `ClassValue`, because the
scalar comparison unwraps any JSON object carrying an `id` or a `value` key — that is how NetBox
renders a foreign key and a choice on read — so an attribute document containing a `value`
property would never settle and the operator would PATCH it forever
(`internal/netbox/drift.go`, `netbox.FieldRules.JSON`).

A pointer with `omitempty`, so absent, `{}` and `null` stay three distinguishable states — see
[field ownership](../concepts/field-ownership.md).

### `spec.weight`

Optional **string**, matching `^([0-9]{1,6}(\.[0-9]{1,2})?)?$`.

A string and not a number, the same decision
[`NetBoxRackType.weight`](netboxracktype.md#specweight) documents. NetBox stores it as
`weight DecimalField decimal(8,2)` and returns it padded — `"0.80"` for a spec that said `"0.8"` —
while an OpenAPI `number` round-trips through IEEE-754 on its way in and out of the API server.
The engine compares two numeric strings numerically (`internal/netbox/drift.go`, `scalarEqual`),
so the two are the same value and produce **no PATCH** on the second reconcile.

The pattern is read straight off `decimal(8,2)`: eight digits, two after the point, so `0` to
`999999.99` in hundredths. The empty alternative is what makes the field clearable.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it — and `""` leaves as
JSON `null`, because DRF parses the empty string as a number and rejects it on a nullable
`DecimalField`.

### `spec.weightUnit`

Optional enum: `""`, `kg`, `g`, `lb` or `oz`. The unit `weight` is given in.

This is the **same Go type** [`NetBoxRackType`](netboxracktype.md) uses, and the sharing is
correct here where sharing an airflow enum was not. `WeightUnitChoices` is one `ChoiceSet`
declared once in `netbox/choices.py:184`, it declares **no `key`** and so cannot be extended
through `FIELD_CHOICES`, and both models mix in the same `WeightMixin`
(`docs/netbox-schema.md` → `dcim.ModuleType`, bases). One `ChoiceSet` is one Go type; three
separate extensible ones are three.

Unset leaves NetBox's own value alone; `""` clears it, and is sent as JSON `null` for the reason
`airflow` gives.

### `spec.description`, `spec.comments`

Optional free text; `description` is capped at 200 characters and `comments` is a `TextField`,
so it has no length marker to derive.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural key

One candidate, and there is no second one to fall back to.

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(manufacturer, model)` | `?manufacturer_id=<id>&model=<model>` | `manufacturerRef` **resolves** |

It comes from `dcim.ModuleType.meta.constraints`, which is exactly one line.

```
UniqueConstraint(fields=('manufacturer', 'model'), name='..._unique_manufacturer_model')
```

**This is the one Kind in #54's table the committed IR supplies directly.**
`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleType.natural_keys` carries the pair, with
`manufacturer_id` and `model` as its filters and no null pin, so nothing here is hand-derived —
unlike [`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md#natural-key), whose key had to be
read off a column-level `UNIQUE`, and unlike the four power kinds #274 records, whose constraints
sit on an abstract base the extractor does not attribute to subclasses.

Both filters are registered: `manufacturer_id` is declared on `ModuleTypeFilterSet` as a
`ModelMultipleChoiceFilter`, and `model` is in its `meta_fields` as a `MultiValueCharFilter`
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleType.filters`).

The constraint is unconditional and `manufacturer` is `REQ`, so **there is no null pin** — unlike
[`NetBoxRack`](netboxrack.md#natural-keys), whose constraints are keyed on an optional column, or
[`NetBoxModuleBay`](netboxmodulebay.md), whose third column is nullable. With
`manufacturerRef` declared and unresolved, no candidate applies and the engine waits.

`manufacturerRef` is **not deferred and cannot be**: the candidate matches on it, so stripping it
from a create would mean the lookup asked a different question from the create it decided on
(`registry.ErrDeferredNaturalKey`).

### There is no second candidate, and that is the schema's doing

[`NetBoxRackType`](netboxracktype.md#natural-keys) and
[`NetBoxDeviceType`](netboxdevicetype.md#natural-keys) each have two candidates —
`(manufacturer, slug)` then `(manufacturer, model)` — and the pair works as a fallback chain: a
marketing rename edits `model`, the `slug` candidate still finds the object, and the rename stays
a PATCH.

`dcim.ModuleType` **has no `slug` column at all**
(`docs/netbox-schema.md` → `dcim.ModuleType`; the model's own columns are `profile`,
`manufacturer`, `model`, `part_number`, `airflow`, `attribute_data` and nine counters). So there
is nothing to fall back to, and the operator cannot invent one: a candidate over `part_number`
would be a key NetBox does not enforce, and adopting on an unverified uniqueness claim is the
class of defect behind #206 and #216.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.ModuleType` is a `PrimaryModel` (`docs/netbox-schema.md` → `dcim.ModuleType`, bases), so it
mixes in both `TagsMixin` and `CustomFieldsMixin` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. `ImageAttachmentsMixin` and
`WeightMixin`, the other two bases, contribute a `GenericRelation` and three columns
respectively. See [provenance](../operations/provenance.md).

`status.naturalKey` is `{"manufacturer_id": "41", "model": "SFP-10G-LR"}` on a settled object —
two filters, because there is one candidate and it has two halves.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the module type exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `manufacturerRef` and any `profileRef` resolve | either does not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Retry intervals are the endpoint's, not this kind's — see
[errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### `attributes` is `attribute_data` under another name

This is the one mistake on this Kind that fails **silently**, so it is worth the paragraph.

The Django model declares `attribute_data JSONField`
(`docs/netbox-schema.md` → `dcim.ModuleType`). The REST serializer does not expose that name: it
declares `attributes` with an `AttributesField`
(`hack/testdata/api-schema-4.6.8.json.gz` → `ModuleTypeSerializer`, `declared.attributes`), and
the IR agrees from the other side — `attribute_data` is recorded as **not in the write path**
while `attributes` **is**
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleType.write_path`).

NetBox drops a field name it does not know rather than rejecting it. So a payload carrying
`attribute_data` would come back `201`/`200` with the attributes unset, the next reconcile would
find the same difference, and the operator would PATCH forever with nothing in the conditions to
say why. The descriptor maps `attributes` → `attributes`
(`internal/registry/dcim_moduletype.go`), and two tests hold it there:
`TestModuleTypeWritesAttributesAndNotAttributeData` (`internal/registry/dcim_modules_test.go`)
asserts the field map, and `TestModuleTypeWritesAttributesUnderTheSerializerName`
(`internal/controller/dcim_modules_controller_test.go`) asserts that `attribute_data` appears in
**no request** the operator makes.

### An unresolved manufacturer writes nothing

The only candidate starts at `manufacturer_id`, so there is no identity to look up and nothing to
create. The object reports `RefsResolved=False` naming `manufacturerRef` and performs **zero
NetBox writes** — the [`NetBoxLocation`](netboxlocation.md) shape rather than the
[`NetBoxPrefix`](netboxprefix.md) one.

A `profileRef` that does not resolve blocks the write too, but for the ordinary reason: it is a
declared reference the engine waits on, not part of the identity.

### A model rename is a new object, and there is no `slug` to absorb it

`(manufacturer, model)` is the whole identity, so editing `spec.model` does not rename the NetBox
module type — it changes what the CR is looking for, and the next reconcile creates a second
module type and leaves the first behind, still `PROTECT`-ing whatever modules point at it.

On [`NetBoxRackType`](netboxracktype.md) and [`NetBoxDeviceType`](netboxdevicetype.md) the `slug`
candidate absorbs exactly this edit. Here there is no such column, so the safe move is to treat
`model` as immutable: create the new module type, repoint the modules, delete the old one.

`profileRef`, `partNumber`, `airflow`, `attributes`, `weight`, `weightUnit`, `description` and
`comments` are all safe to edit.

### The nine counters are never sent and never diffed

`dcim.ModuleType` declares **nine** `CounterCacheField`s — `module_count`, and one
`*_template_count` for each component template kind: `console_port_template_count`,
`console_server_port_template_count`, `power_port_template_count`,
`power_outlet_template_count`, `interface_template_count`, `front_port_template_count`,
`rear_port_template_count` and `module_bay_template_count`. NetBox maintains each from the rows
pointing at this type (`docs/netbox-schema.md`, preamble on every `CounterCacheField`).

All nine are in the serializer's write path and the API refuses them, so a write silently no-ops:
the next reconcile finds the same difference and PATCHes forever. They are in the descriptor's
read-only list, together with `WeightMixin`'s `_abs_weight` cache, which turns a future field map
that ever reaches for one into a boot failure (`registry.ErrFieldReadOnly`) instead.
`TestModuleCountersAndDerivedColumnsAreReadOnly` (`internal/registry/dcim_modules_test.go`)
asserts the list against the digest rather than against a count.

### Deleting one is refused while a module uses it

`dcim.Module.module_type` is `on_delete=PROTECT` (`docs/netbox-schema.md` → `dcim.Module`), so
NetBox refuses to delete a module type while any module is an instance of it, and the CR reports
`Deleting=False, Reason=Protected`. Delete the modules first.

### No containment parent, and `deletionPolicy` defaults to `Delete`

Both foreign keys on this model — `manufacturer` and `profile` — are `on_delete=PROTECT`. Nothing
on the server side disappears when either parent is deleted, because NetBox refuses that
deletion, so there is no server-side cascade for an owner reference to mirror and this Kind takes
none ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). An owner reference on a
`PROTECT`ed foreign key would promise a cluster-side cascade NetBox will not perform, which
deletes the CR and leaves the row.

`deletionPolicy` defaults to `Delete` (#176): a module type is configuration a manifest recreates
verbatim, not allocated state whose deletion frees something for somebody else to take. See
[deletion](../concepts/deletion.md).

### What is deferred

- **The ten `*Template` inline lists.** #54 asks for `consolePortTemplates`,
  `consoleServerPortTemplates`, `powerPortTemplates`, `powerOutletTemplates`,
  `interfaceTemplates`, `frontPortTemplates`, `rearPortTemplates` and `moduleBayTemplates` inline
  on this Kind. **None of those Kinds exists yet**, so there is nothing to inline: an
  `InlineChildSet` materialises child CRs of a Kind the registry has to know about. They land with
  the template kinds, not before them.
- **`moduleRef` on the shipped component Kinds.** `dcim.ModularComponentModel.module` is a
  nullable cascading foreign key on every component subclass, and it is now unblocked rather than
  written — see [coverage](../coverage.md) and the note in `hack/coverage-exclusions.yaml`.

### What this Kind unblocks

#53's `*Template` Kinds can now carry a `moduleTypeRef`, and could not before this Descriptor
existed. `internal/resolver` dispatches **every** reference mode — `name`, `slug`, `lookup` and
`id` alike — through `Descriptors.Get(Field.Target)` to learn which endpoint to query, so a
reference whose target Kind has no Descriptor cannot resolve in any mode and reports
`RefsResolved=False, Reason=RefKindUnavailable` for ever. That was the hard blocker #53's
investigation found, and registering `dcim.moduletype` is the whole of the fix: the reverse index
is built from the Descriptor's `ObjectType` in `Registry.Add`, and the resolver picks the Kind up
as a watch target from there.

### What is not here

- **The nine counters and `_abs_weight`** — read-only, as above.
- **`owner`** is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
  (`hack/coverage-exclusions.yaml`), so there is no Kind to point at.
- **`images`** is an `ImageAttachmentsMixin` `GenericRelation`: the reverse of somebody else's
  foreign key, not a column, and uploaded as multipart form data rather than JSON.
- **`bookmarks`, `journal_entries`, `subscriptions`** are `GenericRelation`s too.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbmoduletype
NAME         MANUFACTURER   MODEL        PROFILE   ID   READY   AGE
sfp-10g-lr   cisco          SFP-10G-LR   optic     71   True    3m
c9300-nm-8x  cisco          C9300-NM-8X            72   True    3m
```

| Column | JSONPath |
|---|---|
| `MANUFACTURER` | `.spec.manufacturerRef.name` |
| `MODEL` | `.spec.model` |
| `PROFILE` | `.spec.profileRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`MANUFACTURER` and `PROFILE` read `.spec.<ref>.name`, so they show the *intent* even while the
reference is unresolved and `ID` is empty — and both are blank for a reference given by `id` or
`lookup`. `PROFILE` is blank for a module type that names no profile, which is legal.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `manufacturerRef` | — | The field is required by the schema, because NetBox's column is `REQ` and the only candidate starts at it. | Name a manufacturer. |
| `kubectl apply` rejected on `airflow` | — | The value is not one of the seven members. | Use one of them, or `""` to clear. If your deployment extended `ModuleAirflowChoices` through `FIELD_CHOICES`, the enum needs widening — file it. |
| `kubectl apply` rejected on `weight` | — | The value does not match `^([0-9]{1,6}(\.[0-9]{1,2})?)?$` — most often it was written as a number rather than a quoted string. | Quote it: `weight: "0.08"`. |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved=False`, `Reason=RefNotFound` | `manufacturerRef` or `profileRef` names a CR that does not exist. Nothing was written. | Create the [`NetBoxManufacturer`](netboxmanufacturer.md) or [`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md), or fix the name. |
| `RefsResolved=False`, `Reason=RefNotReady` | | The referenced CR exists but has no `status.id` yet. | Wait; check that object's own conditions. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace ref with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | See [Start with the grant](#start-with-the-grant). |
| A `profileRef` in `slug` mode never resolves | `RefsResolved=False`, `Reason=RefNotFound` | `dcim.ModuleTypeProfile` has no `slug` column, so the filter matches nothing. | Use `name`, `id` or `lookup` mode. |
| `Ready=False`, `Reason=Conflict` | | Another namespace already owns this `(manufacturer, model)` and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Set `onConflict: Adopt` in the namespace that should own it, or resolve the duplicate declaration. |
| `Ready=False`, `Reason=Invalid`, message quotes a `400` naming a key inside `attributes` | | The document does not satisfy `profileRef`'s JSON Schema. NetBox is the only validator. | Fix the document, or relax the [profile's schema](netboxmoduletypeprofile.md#specschema). |
| `attributes` set in the spec and unset in NetBox, with no error | | Very unlikely, and would mean the payload used `attribute_data`. | Not reachable through the shipped descriptor; see [`attributes` is `attribute_data` under another name](#attributes-is-attribute_data-under-another-name) and file it. |
| A PATCH on every resync | `Synced=False`, `Reason=DriftCorrected` repeatedly | Not expected for `weight` or `attributes` — the first is compared numerically and the second as a whole document. | Read the `Synced` message for the field that differs; see [drift detection](../concepts/drift.md). |
| A second module type appeared after an edit | — | `spec.model` was changed. | See [A model rename is a new object](#a-model-rename-is-a-new-object-and-there-is-no-slug-to-absorb-it). |
| `Deleting=False`, `Reason=Protected` | | A module is still an instance of this type. | Delete the modules first. |

## Related

- [`NetBoxModuleTypeProfile`](netboxmoduletypeprofile.md) — the JSON Schema `attributes` is validated against
- [`NetBoxModule`](netboxmodule.md) — the instance to this catalogue entry
- [`NetBoxModuleBay`](netboxmodulebay.md) — the slot a module goes into
- [`NetBoxManufacturer`](netboxmanufacturer.md) — the required reference this kind's identity needs
- [`NetBoxDeviceType`](netboxdevicetype.md) and [`NetBoxRackType`](netboxracktype.md) — the same identity shape, each with the `slug` candidate this Kind has no column for
- [`NetBoxRefGrant`](netboxrefgrant.md) — what a cross-namespace `manufacturerRef`, `profileRef` or `moduleTypeRef` needs
- [Lookups](../concepts/lookups.md) — candidate order, and why a filter is never dropped
- [Field ownership](../concepts/field-ownership.md) — absent versus empty versus set
- [Deletion](../concepts/deletion.md) — why this kind defaults to `Delete`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
