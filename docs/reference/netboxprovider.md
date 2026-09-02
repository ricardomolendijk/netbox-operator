# `NetBoxProvider`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxProvider` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbprovider` |
| Status subresource | yes |

A `NetBoxProvider` is one `circuits.Provider` in NetBox: the company at the far end of a
circuit — a carrier, a transit provider, an exchange operator.

It is the root of the `circuits` app. Every other kind in that app reaches a provider either
directly ([`ProviderAccount.provider`](netboxprovideraccount.md),
[`ProviderNetwork.provider`](netboxprovidernetwork.md),
[`Circuit.provider`](netboxcircuit.md)) or through one of those, so it has to exist before any
of the rest can be declared by name.

It is a `PrimaryModel` with exactly **two columns of its own plus one many-to-many**
(`docs/netbox-schema.md` -> `circuits.Provider`): `name CharField REQ UNIQUE len=100`,
`slug SlugField REQ UNIQUE len=100` and `asns ManyToManyField -> ipam.ASN`. `description` and
`comments` are inherited. The identity that falls out of those two `UNIQUE`s is the interesting
part — see [the same key, a different route](#the-same-key-a-different-route).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxProvider
metadata:
  name: ntt
  namespace: default
spec:
  endpointRef: homelab
  name: NTT
  slug: ntt
```

Nothing else is required. `asns` omitted is not `asns: []` — see [`spec.asns`](#specasns).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxProvider
metadata:
  name: ntt
  namespace: netbox-catalog
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain
  name: NTT
  slug: ntt
  asns:                       # order-independent id set; reordering writes nothing
    - name: as2914            # a sibling NetBoxASN in this namespace
    - namespace: netbox-ipam  # another namespace, so a grant is needed there
      name: as2915
    - lookup:
        asn: "64512"          # an ASN the operator does not manage
  description: Tier 1 transit, contract 2024-118
  comments: |
    Escalation path and account manager are in the runbook.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1-100 | yes | — | `name`, `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1-100, `^[-a-zA-Z0-9_]+$` | yes | — | `slug`, `SlugField REQ UNIQUE len=100` |
| `asns` | `[]ASNRef`, up to 256 items | no | — | `asns`, `ManyToManyField -> ipam.ASN` |
| `description` | `string`, <=200 | no | — | `description` (`PrimaryModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`PrimaryModel`), `TextField` |

### `spec.name`

Required. The provider's name as NetBox displays it, up to 100 characters.

**Globally unique** (`name CharField REQ UNIQUE len=100`), and deliberately *not* this kind's
natural key: a kind gets one identity, `slug` is the stable one, and a rename that collides
comes back as NetBox's own 409 reported as `Ready=False, Reason=Invalid` rather than being
adopted under the other candidate.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and **globally unique** across the whole NetBox, so two namespaces claiming `ntt`
are claiming one provider and the second reports `Ready=False, Reason=Conflict`.

### `spec.asns`

| | |
|---|---|
| Type | `[]ASNRef` -> [`NetBoxASN`](netboxasn.md) |
| Required | no |
| Validation | `maxItems: 256` |

The autonomous system numbers this provider announces. `asns ManyToManyField -> ipam.ASN`.

Three states, like every optional list field: omitting it leaves NetBox's own list alone, `[]`
clears it, and a list replaces it. Absent and empty are different instructions, told apart from
`metadata.managedFields` ([field ownership](../concepts/field-ownership.md)).

The order is **not data** — NetBox does not preserve many-to-many order — so the ids are sent
sorted and deduplicated and the comparison is an order-independent id set. An unchanged list
produces zero writes however it is reordered in Git ([drift](../concepts/drift.md)).

**All or nothing.** If any element cannot be resolved the whole field is left out of the payload
and the object reports `RefsResolved=False` naming the element that failed. Writing only the
ones that resolved would be a full-list replacement with a shorter list — a deletion, reported
as a success.

`maxItems` is not a NetBox limit and is not decoration. `ObjectRef` carries five CEL rules and
the API server costs each at the list's *maximum* length, so an unbounded list of refs is refused
outright with `estimated rule cost exceeds budget`. 256 is the project's standard bound
([a list needs a bound](../concepts/references.md#a-list-needs-a-bound)).

One trap worth naming: **`slug` mode matches nothing on an `ASNRef`.** `ipam.ASN` is unique on
`asn` and declares no slug column, so `slug: as2914` reports `NotFound`. Use `name` for a
sibling CR, or `lookup: {asn: "64512"}` for an ASN the operator does not manage.

There is **no cascade**. `asns` is a `ManyToManyField` with no `on_delete` at all, so deleting
an ASN removes the row from the join table and leaves the provider standing; a to-many
reference cannot be a containment parent anyway
([ADR-0003](../decisions/0003-ownership-and-references.md)).

### `spec.description`

Optional free text, up to 200 characters. Omit the key to leave NetBox's own value alone; set it
to `""` to clear it. Absent, empty and set are three states — see
[field ownership](../concepts/field-ownership.md).

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`
(`docs/netbox-schema.md` -> `circuits.Provider`, `comments (PrimaryModel) TextField`), so it has
no `max_length` and there is no length marker to derive from one. Clearable on the same
three-state terms as `description`.

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

`circuits.Provider` declares **no `meta.constraints` at all** — its `meta` carries only
`ordering: ['name']` (`docs/netbox-schema.md` -> `circuits.Provider`), and the committed IR
agrees: `natural_keys: []` (`hack/testdata/ir-4.6.8.json.gz`). So the identity does not come
from a constraint list; it comes from the model's own *column-level* `UNIQUE`s. Uniqueness is
global and there is nothing to pin to null.

The filter is registered: `slug` is in `ProviderFilterSet.meta_fields` (NetBox 4.6.8,
`circuits/filtersets.py:38`), recorded in the IR as `from: meta.fields`. Checked rather than
assumed for the reason #206 exists — django-filter **drops a parameter it does not recognise
and answers with the unfiltered set**, so a guessed filter name is not a lookup that fails but
one that matches every provider in NetBox.

`name` is `UNIQUE` too and is deliberately **not** a second candidate, for the reason
[`spec.name`](#specname) gives.

## The same key, a different route

`slug` alone is the same key [`NetBoxManufacturer`](netboxmanufacturer.md),
[`NetBoxSite`](netboxsite.md) and [`NetBoxRackRole`](netboxrackrole.md) use, and it is derived
the same way — from column-level uniqueness rather than from a constraint list. But the two
kinds arrive there by different routes, and the route is what a reader has to check:

| | `circuits.Provider` | `dcim.RackRole` |
|---|---|---|
| Base classes | `ContactsMixin, PrimaryModel` | `OrganizationalModel` |
| Where `name`/`slug` are declared | **on the model itself** | on the base class |
| `meta.constraints` | none | none |
| `slug` uniqueness | column-level `UNIQUE` | column-level `UNIQUE` |
| Natural key | `slug` | `slug` |

`PrimaryModel` does not supply `name` or `slug` at all — it supplies `description` and
`comments`. So "it is a `PrimaryModel`" tells you nothing about this kind's identity, whereas
"it is an `OrganizationalModel`" tells you everything about `dcim.RackRole`'s. Two kinds in the
same shape, and only one of them could be answered from the base class.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`circuits.Provider` is a `PrimaryModel`, which mixes in both `TagsMixin` and `CustomFieldsMixin`,
so it carries the **whole provenance stamp** when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set
([provenance](../operations/provenance.md)). `ContactsMixin`, the other base, contributes only a
`GenericRelation` and nothing to stamp.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the provider exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRefs`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | every entry in `asns` resolved, or the field is absent | any entry did not | `AllResolved`, `RefNotFound`, `RefDenied`, `RefNotReady` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved` is a real condition on this kind, unlike on the reference-free organisational
kinds: `asns` is a reference list, so it can report `RefNotFound`.

## Kind-specific behaviour

### A hand-made provider is adopted, not duplicated

`slug` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one provider in NetBox rather than two. Creating a second one would be
refused by the unique index anyway, so adoption is the only outcome that works — which matters
more here than on most kinds, because a long-running NetBox already has its providers.

### Two namespaces claiming one slug is one provider

NetBox's uniqueness is a database constraint and a namespace boundary does not partition it. The
first CR to reconcile creates or adopts the provider; the second finds it already claimed and
reports `Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)).

### A provider is shared, so most references to it cross a namespace

A provider is shared infrastructure that circuits in many team namespaces point at, so the
catalogue shape is the normal one rather than the exception: the provider lives in
`netbox-catalog`, and a [`NetBoxCircuit`](netboxcircuit.md) in `team-a` names it with
`providerRef: {namespace: netbox-catalog, name: ntt}`. Crossing the boundary needs a
[`NetBoxRefGrant`](netboxrefgrant.md) **in the provider's namespace**; without one the circuit
sits at `RefsResolved=False, Reason=RefDenied` and writes nothing
([references](../concepts/references.md#crossing-a-namespace)). The same applies in the other
direction to an `asns` entry naming another namespace.

### No containment parent, and everything pointing here is `PROTECT`

`circuits.Provider` has **no foreign key at all bar `owner`**, which has no Kind, so there is
nothing that could be a containment parent
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

Every reference pointing *at* a provider is `on_delete=PROTECT` — `ProviderAccount.provider`,
`ProviderNetwork.provider` and `Circuit.provider`. Deleting a provider that anything uses is
therefore **refused** rather than cascading, and the CR reports `Deleting=False,
Reason=Protected` naming the blocker. Delete the accounts, networks and circuits first.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A provider is configuration a manifest recreates
verbatim, not allocated state; nothing about deleting one frees a resource somebody else can
take, which is what `Retain` was reserved for ([deletion](../concepts/deletion.md)).

### `circuit_count` is never written

The serializer returns `circuit_count`, a counter NetBox maintains from the circuits pointing
here. It is in the descriptor's read-only list alongside `created`, `last_updated`, `url` and
`display`. Writing it would not fail — NetBox drops it — which is precisely why it has to be
declared: a dropped write produces a difference the next reconcile finds again, and PATCHes
forever. A silent no-op is a loop, not an error.

### Renaming the slug changes identity

`slug` is the natural key, so editing it does not rename the NetBox provider — it changes what
the CR is looking for, and the next reconcile creates a second provider, leaving the first
behind, still holding all its circuits. `name`, `asns`, `description` and `comments` are safe to
edit.

### What is not here yet

This ships in the catalogue slice of the `circuits` app (#58), alongside
[`NetBoxProviderAccount`](netboxprovideraccount.md),
[`NetBoxProviderNetwork`](netboxprovidernetwork.md),
[`NetBoxCircuitType`](netboxcircuittype.md) and [`NetBoxCircuit`](netboxcircuit.md); circuit
terminations, circuit groups and the virtual-circuit kinds are deferred to a later slice.

Three of this model's fields are absent deliberately. **`owner`** is
`ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at: a field the CRD accepted and
the payload dropped would report success while writing nothing. **`contacts`** is a
`ContactsMixin` `GenericRelation` — the reverse of somebody else's foreign key, not a column --
so contact assignments are written from the assignment's own side by
[`NetBoxContactAssignment`](netboxcontactassignment.md). **`circuit_count`** is read-only, for
the reason above. `tags` and `customFields` are written by the provenance stamp, not by a user.

## Printer columns

```
$ kubectl get nbprovider
NAME     SLUG     ID   READY   AGE
ntt      ntt      14   True    6m
lumen    lumen    15   True    6m
ams-ix   ams-ix        False   40s
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
| `RefsResolved=False`, `Reason=RefNotFound`, message names an `asns` entry | `RefsResolved` | The [`NetBoxASN`](netboxasn.md) does not exist, or the ref used `slug` mode — `ipam.ASN` has no slug column. | Use `name` for a sibling CR or `lookup: {asn: "..."}`. **No part of `asns` is written until every entry resolves.** |
| `RefsResolved=False`, `Reason=RefDenied` | `RefsResolved` | An `asns` entry names another namespace with no grant there. | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in the ASN's namespace. |
| `asns` was reordered, or an ASN was deleted, and nothing happened | none | M2M order is not data, and `asns` has no `on_delete`. | Expected on both counts. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the provider re-enqueues off its event. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | An account, network or circuit still points here — all three are `PROTECT`. | Delete those first, or set `deletionPolicy: Retain`. |
| A second provider appeared after an edit | — | `spec.slug` was changed. | See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |

## Related

- [`NetBoxProviderAccount`](netboxprovideraccount.md) — an account number with this provider, keyed `(provider, account)`
- [`NetBoxProviderNetwork`](netboxprovidernetwork.md) — a network this provider operates, keyed `(provider, name)`
- [`NetBoxCircuit`](netboxcircuit.md) — the thing a provider provides
- [`NetBoxASN`](netboxasn.md) — what `asns` points at, and the one Kind with no `name` and no `slug`
- [`NetBoxRackRole`](netboxrackrole.md), [`NetBoxManufacturer`](netboxmanufacturer.md) — the same `slug`-alone derivation, reached from a base class instead
- [References](../concepts/references.md) — to-many resolution, bounds and crossing a namespace
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
