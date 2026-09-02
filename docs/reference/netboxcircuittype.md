# `NetBoxCircuitType`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCircuitType` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcircuittype` |
| Status subresource | yes |
| Endpoint | `circuits/circuit-types` |
| Object type | `circuits.circuittype` |

A `NetBoxCircuitType` is one `circuits.CircuitType` in NetBox: the classification of a circuit
-- `Transit`, `Peering`, `DIA`, `Dark Fibre`. It is the catalogue kind
[`NetBoxCircuit`](netboxcircuit.md)'s `typeRef` points at, and the reason a circuit's required
`type` column can be written by name at all.

It is the flattest kind in the catalogue: `circuits.CircuitType` declares **no columns and no
`meta.constraints` of its own**, so every field below, and the identity, arrives from two base
classes up — see [a model that declares nothing of its
own](#a-model-that-declares-nothing-of-its-own). Read against
[`NetBoxRackRole`](netboxrackrole.md) it is the same shape — an `OrganizationalModel` plus a
colour — with one difference that matters: [`spec.color`](#speccolor) is **not defaulted**.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCircuitType
metadata:
  name: transit
  namespace: default
spec:
  endpointRef: homelab
  name: Transit
  slug: transit
```

`color` is absent from that manifest and stays absent in the payload. Unlike
[`NetBoxRackRole`](netboxrackrole.md), nothing is filled in for you.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCircuitType
metadata:
  name: transit
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Transit
  slug: transit
  color: 2196f3               # six lowercase hex digits, no leading '#'
  description: Full-table IP transit from an upstream
  comments: |
    Circuits of this type are billed on 95th percentile.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

Every column below is inherited. The `NetBox column` cells name the class each one comes from,
because none of them come from `circuits.CircuitType`.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1-100 | yes | — | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1-100, `^[-a-zA-Z0-9_]+$` | yes | — | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |
| `color` | `string`, `^([0-9a-f]{6})?$` | no | **none** | `color` (`BaseCircuitType`), `ColorField` |
| `description` | `string`, <=200 | no | — | `description` (`OrganizationalModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`OrganizationalModel`), `TextField` |

### `spec.name`

Required. The type's name, as NetBox displays it, up to 100 characters.

**Column-unique** (`name (OrganizationalModel) CharField REQ UNIQUE len=100`). It is a
candidate key and deliberately not the lookup key: a kind gets one identity, `slug` is the
stable one, and a rename that collides comes back as NetBox's own 409 reported as
`Ready=False, Reason=Invalid` rather than being adopted under the other candidate.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and **globally unique** across the whole NetBox.

### `spec.color`

Optional, six lowercase hexadecimal digits without a leading `#`, or `""`. **Not defaulted**,
and that is the one place this kind departs from its twin.

`dcim.RackRole.color` reads `def=UNRESOLVED:ColorChoices.COLOR_GREY` in the digest, so
[`NetBoxRackRole`](netboxrackrole.md#speccolor) defaults its CRD field to `9e9e9e` and always
sends a value. `circuits.BaseCircuitType.color` carries `blank=True` and **no Django default at
all** — the committed IR records the field's `sql` as `{"blank": true}` and nothing else.
Defaulting it here would invent a value NetBox does not have and PATCH it onto every adopted
circuit type. See [the colour that is not
defaulted](#color-is-not-defaulted-and-that-is-a-statement-about-the-column).

So `color` has the ordinary three states. Omit the key to leave NetBox's own value alone; set it
to `""` to clear it. The pattern `^([0-9a-f]{6})?$` admits the empty string for exactly that
reason, and the field needs no `EmptyIsNull`: the column is a `CharField` that takes the empty
string, so `""` is what clears it and a `null` would be rejected. The operator tells absent from
empty using `metadata.managedFields` — see [field
ownership](../concepts/field-ownership.md).

### `spec.description`

Optional free text, up to 200 characters. Clearable on the same three-state terms as `color`.

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`
(`docs/netbox-schema.md` -> `circuits.CircuitType`, `comments (OrganizationalModel) TextField`),
so it has no `max_length` and there is no length marker to derive from one.

Clearable on the same three-state terms as `description`.

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

`circuits.CircuitType` declares **no `meta.constraints` at all** — its `Meta` carries only
`ordering: ('name',)` (`docs/netbox-schema.md` -> `circuits.CircuitType`), and the committed IR
(`hack/testdata/ir-4.6.8.json.gz`) records `natural_keys: []`. The identity therefore does not
come from a constraint list; it comes from `OrganizationalModel`'s *column-level* `UNIQUE` on
`slug`, two base classes up. Uniqueness is global and there is nothing to pin to null.

`name` is `UNIQUE` too and is deliberately **not** a second candidate, for the reason
[`spec.name`](#specname) gives.

The filter is registered: `slug` is in `CircuitTypeFilterSet.meta_fields`
(NetBox 4.6.8, `circuits/filtersets.py:163`). That check is not a formality. django-filter drops
a parameter it does not recognise and answers with the *unfiltered* set, so a lookup on an
unregistered field would return every circuit type and read as ambiguous rather than as an error
(#206).

## A model that declares nothing of its own

Most kinds in this reference have at least one column to call theirs.
`circuits.CircuitType` has none. The digest records it literally:

```
## circuits.CircuitType   (circuits/models/circuits.py)
   bases: BaseCircuitType
   (no own columns — every field is inherited from BaseCircuitType)
```

The inheritance is two deep:

| Class | What it contributes |
|---|---|
| `circuits.CircuitType` | nothing — no columns, no `meta.constraints`, only `ordering: ('name',)` |
| `circuits.BaseCircuitType` | exactly one column, `color ColorField` |
| `OrganizationalModel` | `name`, `slug`, `description`, `comments`, and the `UNIQUE` on both `name` and `slug` |

Two consequences. The five spec fields are wholly inherited, which is why the `spec` table names
a class for every row; and the natural key is inherited too, so the base class decides it rather
than the app. That is the same derivation as [`NetBoxRackRole`](netboxrackrole.md),
[`NetBoxManufacturer`](netboxmanufacturer.md) and [`NetBoxContactRole`](netboxcontactrole.md),
and the contrast that proves it is the base class doing the work is
[`NetBoxDeviceRole`](netboxdevicerole.md): `NestedGroupModel.slug` carries no `UNIQUE`, which is
why every nested-group kind has a `(parent, name)` key instead.

One correction, since the ticket says otherwise. Issue #58 records that
`circuits.CircuitType` and `circuits.VirtualCircuitType` have "no model entry in the schema".
They do. `docs/netbox-schema.md` carries both, and the committed IR carries both as full kinds
with endpoints, filtersets and write paths. Every field, filter and read-only column on this
page is read from those two artefacts, not inferred from `BaseCircuitType`.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`circuits.CircuitType` is an `OrganizationalModel` through `BaseCircuitType`, so it carries both
`tags` and `custom_fields` and is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the type exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references at all | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### `color` is not defaulted, and that is a statement about the column

The two kinds are otherwise the same shape, so the difference is worth reading side by side:

| | `dcim.RackRole.color` | `circuits.BaseCircuitType.color` |
|---|---|---|
| Digest | `ColorField def=UNRESOLVED:ColorChoices.COLOR_GREY` | `ColorField` |
| IR `sql` | a Django default | `{"blank": true}`, and nothing else |
| CRD default | `9e9e9e` | none |
| Absent from the manifest means | still sent — the default is | not sent |

[`NetBoxRackRole`](netboxrackrole.md#speccolor) defaults because NetBox itself does: a value
NetBox fills in and the operator never sends is a value the operator can never correct, so a
colour changed in the UI would stay changed. Here there is no value to mirror. Defaulting would
invent one, and every adopted circuit type would get a PATCH setting a colour nobody chose.

### A hand-made type is adopted, not duplicated

`slug` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one circuit type in NetBox rather than two — a second one would be
refused by the unique index anyway, so adoption is the only outcome that works.

### Two namespaces claiming one slug is one type

NetBox's uniqueness is a database constraint and a namespace boundary does not partition it. The
first CR to reconcile creates or adopts the type; the second finds it already claimed and reports
`Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)).

This is a catalogue kind, so the usual arrangement is one shared namespace holding the types and
[`NetBoxProvider`](netboxprovider.md)s, with team namespaces pointing at them. A `typeRef`
crossing a namespace boundary needs a [`NetBoxRefGrant`](netboxrefgrant.md) in the catalogue
namespace.

### No containment parent, in either direction

`circuits.CircuitType` has **no foreign key at all** bar `owner`, which has no Kind, so there is
nothing that could be a containment parent
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

The reference pointing *at* it is `Circuit.type ForeignKey REQ -> circuits.CircuitType
on_delete=PROTECT` (`docs/netbox-schema.md` -> `circuits.Circuit`), and it is *required*, so
every circuit in NetBox holds one of these down. Deleting a type in use is **refused** rather
than cascading: the CR reports `Deleting=False, Reason=Protected` naming the blocker. Delete the
circuits, or move them to another type, first.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A circuit type is configuration a manifest
recreates verbatim; nothing about deleting one frees a resource somebody else can take, which is
what `Retain` was reserved for. See [deletion](../concepts/deletion.md).

### `circuit_count` is never written

The serializer returns `circuit_count`, a counter NetBox maintains from the circuits pointing at
the type. It is in the descriptor's read-only list alongside `created`, `last_updated`, `url` and
`display`. Writing it silently no-ops rather than failing, which is precisely why it has to be
declared: a dropped write produces a difference the next reconcile finds again, and PATCHes
forever.

### Renaming the slug changes identity

`slug` is the natural key, so editing it does not rename the NetBox type — it changes what the
CR is looking for, and the next reconcile creates a second type, leaving the first behind (and
the circuits still attached to it). `name`, `color`, `description` and `comments` are safe to
edit.

### What is not here yet

`NetBoxVirtualCircuitType` is a **separate Kind** and is not shipped. `circuits.CircuitType` and
`circuits.VirtualCircuitType` are two models over one `BaseCircuitType`, with two endpoints and
two tables, so one CRD could not serve both — a shared base class is not a shared object type.
The virtual one is deferred.

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at: a field the CRD accepted and
the payload dropped would report success while writing nothing. `tags` and `customFields` are
written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbcircuittype
NAME         SLUG         ID   READY   AGE
transit      transit      14   True    6m
peering      peering      15   True    6m
dark-fibre   dark-fibre   16   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this slug, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Pick a different slug, or set `onConflict: Adopt` in the namespace that should own it. |
| `Ready=False`, `Reason=Invalid`, message names `name` | `Ready` | A rename collided with the column-level `UNIQUE` on `name`. | Pick another name; `slug` is what identifies the object. |
| `Ready=False`, `Reason=Invalid`, message names `color` | `Ready` | `color` was given with a leading `#`, or in upper case. | Six lowercase hex digits, no `#`, or `""` to clear. Admission rejects most of these first. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the type re-enqueues off its event. |
| The colour in NetBox is empty and the manifest has no `color` | — | Expected. The field is not defaulted. | Set `spec.color` if you want one. See [`spec.color`](#speccolor). |
| A `NetBoxCircuit` reports `RefsResolved=False` naming `typeRef` | — | The type is in another namespace and there is no grant. | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in the catalogue namespace. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A circuit still has this type — `Circuit.type` is `REQ` and `PROTECT`. | Move or delete those circuits, or set `deletionPolicy: Retain`. |
| A second type appeared after an edit | — | `spec.slug` was changed. | See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |

## Related

- [`NetBoxCircuit`](netboxcircuit.md) — the kind that points here, through a required `typeRef`
- [`NetBoxProvider`](netboxprovider.md) — the other catalogue kind in the `circuits` slice
- [`NetBoxRackRole`](netboxrackrole.md) — the same shape, with the colour defaulted
- [`NetBoxManufacturer`](netboxmanufacturer.md) — the same `slug`-alone derivation, no `meta.constraints` either
- [`NetBoxContactRole`](netboxcontactrole.md) — the same derivation, argued from the other side
- [`NetBoxRefGrant`](netboxrefgrant.md) — what a cross-namespace `typeRef` needs
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live

This kind ships in the catalogue slice of the `circuits` app (issue #58), alongside
[`NetBoxProvider`](netboxprovider.md) and [`NetBoxCircuit`](netboxcircuit.md).
</content>
</invoke>
