# `NetBoxModuleTypeProfile`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxModuleTypeProfile` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbmoduletypeprofile` |
| Status subresource | yes |

A `NetBoxModuleTypeProfile` is one `dcim.ModuleTypeProfile` in NetBox: a *class* of module —
"Optic", "Line card", "Power supply" — together with the JSON Schema that every
[`NetBoxModuleType`](netboxmoduletype.md) claiming that class has its `attributes` document
validated against.

Two things about it are unusual and both come from the same place. It is a `PrimaryModel` with
exactly **two columns of its own**, `name` and `schema`
(`docs/netbox-schema.md` → `dcim.ModuleTypeProfile`); and it has **no `slug` column**, which in
NetBox's `dcim` catalogue only this Kind and [`NetBoxModuleType`](netboxmoduletype.md) manage --
`dcim.Manufacturer`, `dcim.DeviceType`, `dcim.RackType` and `dcim.Platform` all carry the usual
`name`/`slug` pair (`docs/netbox-schema.md`). So `name` is both the display string and the
lookup key, and a `slug`-mode [`ObjectRef`](../concepts/references.md) pointing at this Kind
matches nothing -- [`NetBoxASN`](netboxasn.md) is the other reference target in that position.

The schema is enforced by NetBox and by nobody else — see
[The operator does not validate the schema](#the-operator-does-not-validate-the-schema).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModuleTypeProfile
metadata:
  name: optic
  namespace: default
spec:
  endpointRef: homelab
  name: Optic
```

One required field. A profile with no `schema` is legal and useful: it classifies module types
without constraining what they may carry.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxModuleTypeProfile
metadata:
  name: optic
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly — Fail is the default
  deletionPolicy: Delete      # Delete | Retain — Delete is this kind's default

  name: Optic                 # the whole natural key: no slug column exists
  description: Pluggable optical transceivers
  comments: |
    Anything an SFP cage takes. Reach is the manufacturer's figure, not a measurement.

  # Opaque to the operator. NetBox applies it server-side to every module type that names this
  # profile, and returns a field-level 400 when a document does not match.
  schema:
    type: object
    properties:
      form_factor:
        type: string
        enum: [SFP, "SFP+", SFP28, QSFP+, QSFP28]
      reach_km:
        type: number
        minimum: 0
      wavelength_nm:
        type: integer
    required: [form_factor]
```

A runnable copy is [`../../config/samples/netbox_v1alpha1_netboxmoduletypeprofile.yaml`](../../config/samples/netbox_v1alpha1_netboxmoduletypeprofile.yaml),
and [`../examples/modules.yaml`](../examples/modules.yaml) applies one alongside the module type,
bay and module that use it.

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope and
behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | **yes** | — | `name CharField REQ UNIQUE len=100` |
| `schema` | JSON document | no | — | `schema JSONField` |
| `description` | `string`, ≤200 | no | — | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | `comments (PrimaryModel) TextField` |

Every column is from `docs/netbox-schema.md` → `dcim.ModuleTypeProfile`.

### `spec.name`

**Required**, 1–100 characters, and this Kind's entire natural key.

`name CharField REQ UNIQUE len=100` is a **column-level** unique — the whole of the uniqueness
this model has, because it declares no `meta.constraints` line at all. Uniqueness is therefore
global across the NetBox install and there is nothing to pin to null.

There is no `slug` beside it. That is worth saying twice, because every other catalogue Kind in
`dcim` has one and the absence changes how a reference to this Kind can be written: `name`, `id`
or `lookup` mode work, and `slug` mode resolves against a column that does not exist. See
[Natural key](#natural-key).

### `spec.schema`

Optional. A JSON Schema document, carried through to NetBox and back unchanged.

Typed `JSONDocument` in the Go API — an alias for `apiextensions.JSON`, so the CRD carries
`x-kubernetes-preserve-unknown-fields: true` and the API server prunes nothing out of the
document on the way in. Without that marker the structural schema would strip every key it does
not declare and the operator would faithfully write an empty object and report success.

A pointer with `omitempty`, so the three states stay distinguishable: absent means "do not manage
this column", `{}` means an empty document, and `null` means the column's own null. See
[field ownership](../concepts/field-ownership.md).

On the descriptor it is `registry.ClassJSON` rather than `ClassValue`, and the reason is specific
rather than tidy. The scalar comparison unwraps any JSON object carrying an `id` or a `value`
key, because that is how NetBox renders a foreign key and a choice on read — and a JSON Schema
document routinely contains a `value` property, so compared as a scalar it would never settle and
the operator would PATCH it forever (`internal/netbox/drift.go`, `netbox.FieldRules.JSON`).
`TestModuleTypeProfileSchemaRoundTripsAndDoesNotHotLoop`
(`internal/controller/dcim_modules_controller_test.go`) is the assertion, observed as an absence
of writes over four resync intervals.

### `spec.description`, `spec.comments`

Optional free text; `description` is capped at 200 characters and `comments` is a `TextField`,
so it has no length marker to derive.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

## Natural key

One candidate, and no conditional variant.

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | always |

**The committed IR reports no candidate for this Kind, and that is a gap in the extractor rather
than a gap in NetBox.** `dcim.ModuleTypeProfile` carries no `meta.constraints` line — its `Meta`
declares only `ordering: ('name',)` — so
`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleTypeProfile.natural_keys` is `[]`. The uniqueness
is real and is declared one level down, on the column itself
(`docs/netbox-schema.md` → `dcim.ModuleTypeProfile`, `name CharField REQ UNIQUE len=100`), which
is the same place [`NetBoxManufacturer`](netboxmanufacturer.md) and
[`NetBoxRackRole`](netboxrackrole.md) get theirs from — `name` here where those have `slug`. The
key is therefore hand-declared in `internal/registry/dcim_moduletypeprofile.go`, with the citation
next to it.

This is the extractor gap #274 records for the power kinds, arriving from the other direction.
There, a `UNIQUE` declared on an abstract base (`dcim.ComponentModel`, `dcim.ComponentTemplateModel`)
is not attributed to its subclasses; here, a `UNIQUE` declared on a *column* is not lifted into
`natural_keys` because the extractor reads `Meta.constraints`. Both produce the same symptom — a
Kind that looks as if it has no identity — and both need the same answer, which is to read the
digest and write the key down with its evidence rather than to trust the emitted list. A missing
adoption key is the class of defect behind #206 and #216.

The filter is registered: `name` is in `ModuleTypeProfileFilterSet`'s `meta_fields` as a
`MultiValueCharFilter` (`hack/testdata/ir-4.6.8.json.gz` → `dcim.ModuleTypeProfile.filters`).

The lookup is **exact**, not case-insensitive. Nothing in this model's `Meta` is declared over
`Lower('name')`, unlike [`NetBoxDevice`](netboxdevice.md)'s constraints, so `Optic` and `optic`
are two profiles to NetBox and must be two to the operator.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`dcim.ModuleTypeProfile` is a `PrimaryModel` (`docs/netbox-schema.md` → `dcim.ModuleTypeProfile`,
bases), so it mixes in both `TagsMixin` and `CustomFieldsMixin` and is stamped in full when the
endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

`status.naturalKey` is `{"name": "Optic"}` on a settled object — one filter, because there is one
candidate.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the profile exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Retry intervals are the endpoint's, not this kind's — see
[errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### The operator does not validate the schema

`spec.schema` is a JSON Schema document and the operator treats it as opaque bytes. It is not
parsed, not checked for well-formedness beyond being JSON, and never applied to a module type's
`attributes` on the way out.

That is a decision rather than an omission. NetBox validates `attribute_data` against the
profile's `schema` server-side on every module-type write and returns a field-level `400` naming
the offending property, which the engine surfaces verbatim in the `Ready` message and backs off
from rather than retrying hot. A client-side copy of the check would be a **second validator**,
and two validators over one document is one validator plus a source of disagreement: a NetBox
release that tightens or relaxes its own JSON Schema draft support would leave the operator
rejecting documents NetBox accepts, or accepting documents it does not.

The consequence to plan for is ordering. A [`NetBoxModuleType`](netboxmoduletype.md) whose
`profileRef` resolves to a profile whose `schema` has not been applied yet is validated against
whatever `schema` NetBox currently holds. Apply the profile and its module types in one manifest
and the engine's own reference ordering handles it; edit a live schema to be *stricter* and the
existing module types under it do not re-validate until something writes them again.

### Deleting one is refused while a module type uses it

`dcim.ModuleType.profile` is `on_delete=PROTECT` (`docs/netbox-schema.md` → `dcim.ModuleType`),
so NetBox refuses to delete a profile while any module type names it, and the CR reports
`Deleting=False, Reason=Protected` naming the blocker. Clear the module types' `profileRef`, or
delete them first.

### No containment parent, and `deletionPolicy` defaults to `Delete`

`dcim.ModuleTypeProfile` has **no foreign key at all** bar `owner`, so there is nothing that could
be a containment parent ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). The
reference pointing *at* it is `PROTECT`, as above, so nothing cascades in either direction.

`deletionPolicy` defaults to `Delete` (#176): a profile is configuration a manifest recreates
verbatim, and deleting one frees no resource anybody else can take. What protects the module
types under it is `PROTECT`, not `Retain`. See [deletion](../concepts/deletion.md).

### A hand-made profile is adopted, not duplicated

`name` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one profile in NetBox rather than two. Creating a second would be
refused by the unique index anyway, so adoption is the only outcome that works — which is why a
fresh operator pointed at a long-running NetBox needs no migration for this Kind.

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox profile — it changes what the
CR is looking for, and the next reconcile creates a second profile and leaves the first behind,
still `PROTECT`-ing whatever module types point at it. `description`, `comments` and `schema` are
safe to edit.

There is no second column to fall back on, unlike [`NetBoxRackType`](netboxracktype.md#natural-keys)
where a `model` rename is absorbed by the `slug` candidate.

### What is not here

- **`owner`** is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
  (`hack/coverage-exclusions.yaml`), so there is no Kind to point at. See
  [coverage](../coverage.md).
- **`bookmarks`, `journal_entries`, `subscriptions`** are `GenericRelation`s: the reverse of
  somebody else's foreign key, not columns.
- **`created`, `last_updated`, `url`, `display`** are the four `ChangeLoggedModel` columns the
  descriptor declares read-only. This model has no `CounterCacheField` and no underscore-prefixed
  cache, so the read-only list is those four and nothing else.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbmoduletypeprofile
NAME     NAME    ID   READY   AGE
optic    Optic   61   True    2m
psu      PSU     62   True    2m
```

| Column | JSONPath |
|---|---|
| `NAME` | `.spec.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

The first `NAME` is the CR's own `metadata.name` and the second is `spec.name`, the NetBox column.
They are allowed to differ and usually do: the CR name is a DNS label and the profile name is not.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `name` | — | The field is required by the schema, or is longer than 100 characters. | Provide a name within the bound. |
| A `profileRef` written in `slug` mode never resolves | `RefsResolved=False`, `Reason=RefNotFound` on the *referring* [`NetBoxModuleType`](netboxmoduletype.md) | This model has no `slug` column, so the filter matches nothing. | Use `name`, `id` or `lookup` mode. |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this `name`, and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Pick a different name, or set `onConflict: Adopt` in the namespace that should own it. |
| `Ready=False`, `Reason=Invalid`, message quotes a NetBox `400` on `schema` | `Ready` | NetBox rejected the document — it is not a schema it can compile. | Read the quoted message; the operator does not parse the document and has nothing to add to it. |
| A module type reports a `400` naming an attribute | on the [`NetBoxModuleType`](netboxmoduletype.md) | Its `attributes` do not satisfy this profile's `schema`. | Fix the module type's document, or relax the schema. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A module type still names this profile — `ModuleType.profile` is `PROTECT`. | Clear those `profileRef`s, or delete the module types. |
| A second profile appeared after an edit | — | `spec.name` was changed. | See [Renaming changes identity](#renaming-changes-identity). |

## Related

- [`NetBoxModuleType`](netboxmoduletype.md) — what a profile validates, and the only Kind that references one
- [`NetBoxModuleBay`](netboxmodulebay.md) and [`NetBoxModule`](netboxmodule.md) — the rest of the module block
- [`NetBoxASN`](netboxasn.md) — the other catalogue reference target with no `slug` column
- [`NetBoxManufacturer`](netboxmanufacturer.md) — the same "no `meta.constraints`, column-level unique" derivation
- [`NetBoxRackRole`](netboxrackrole.md) — the same derivation again, with `slug` where this has `name`
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Field ownership](../concepts/field-ownership.md) — absent versus empty versus set, and what a JSON document's three states mean
- [Deletion](../concepts/deletion.md) — why this kind defaults to `Delete`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
