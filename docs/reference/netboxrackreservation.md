# `NetBoxRackReservation`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxRackReservation` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbrackres` |
| Status subresource | yes |
| Lands with | NBO-051 (M9–M10) |

A `NetBoxRackReservation` is one `dcim.RackReservation`: a claim on a run of rack units, so
nobody mounts a device in them.

It is the odd Kind in NBO-051 three times over, and each is worth reading before the field list.

## Its identity is backed by nothing at all

`dcim.RackReservation` declares **no `meta.constraints` and no column-level `UNIQUE`**
(`docs/netbox-schema.md` → `dcim.RackReservation`). Its `meta.ordering` is `['created', 'pk']`
and its `meta.indexes` is `(models.Index(fields=('created', 'id')),)`, so neither offers a column
that identifies anything — this is a step weaker than [`NetBoxPrefix`](netboxprefix.md), whose
ordering at least names `prefix` and `vrf`, and a step weaker than
[`NetBoxContact`](netboxcontact.md), whose lookup column at least has an index behind it.

The lookup key is therefore a pure convention over the two required columns a filter can carry,
`(rack, description)`, and a second object matching both is reported as `Conflict` rather than
adopted. See [Natural key](#natural-key).

## `units` cannot be part of that key

NBO-051's ticket proposes `(rack, sorted(units))`. It is not expressible, and the reason is
structural rather than a gap in this Kind's descriptor:

- A natural-key filter carries **one scalar value**. `reconciler.filterValue` renders strings,
  bools and numbers only (`internal/reconciler/payload.go`), so a JSON list never reaches
  `SpecState.Resolved` and a candidate matching on `units` would never be *applicable* — the
  object would wait forever for an identity it cannot build, which is the worst of the three
  outcomes.
- NetBox has no equality filter for it either. `RackReservationFilterSet` declares
  `unit = NumericArrayFilter(field_name='units', lookup_expr='contains')` (NetBox 4.6.8,
  `netbox/dcim/filtersets.py:519`), which asks whether the array *contains* one unit — not
  whether it equals a set.

Reported on the pull request as a divergence rather than worked around.

## Its `user` is a required foreign key with no Kind to point at

`user ForeignKey REQ -> settings.AUTH_USER_MODEL on_delete=PROTECT`, and the whole `users` app
is out of scope — `users/users` is an excluded endpoint in
[coverage](../coverage.md) — so there is no `NetBoxUser` for a reference to resolve against.
`spec.userID` is therefore a **literal NetBox primary key in a plain value field**, which is the
one place in this API that happens. [Why it is not `userRef`](#why-userid-is-not-a-reference) has
the full argument.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackReservation
metadata:
  name: cage-3-network
  namespace: default
spec:
  endpointRef: homelab
  rackRef:
    name: r1
  # Sorted, and the order is data: NetBox stores the array as given -- see spec.units.
  units: [20, 21, 22]
  # A literal NetBox user id: there is no NetBoxUser Kind. Find it with
  #   GET /api/users/users/?username=<name>
  userID: 4
  description: Reserved for the network team
```

`rackRef` needs a `NetBoxRack` named `r1` in this namespace, or a
[grant](netboxrefgrant.md) and a `namespace:` to reach one elsewhere.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRackReservation
metadata:
  name: cage-3-network
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail          # default
  deletionPolicy: Delete    # default: the reservation is recreated verbatim from this manifest

  rackRef:
    name: r1
  units: [20, 21, 22]
  userID: 4
  # Required, unusually: dcim.RackReservation shadows PrimaryModel's own `description` to make
  # it so -- and it is half of this Kind's lookup key, so two reservations on one rack should
  # not share one.
  description: Reserved for the network team
  status: active            # default
  tenantRef:
    name: acme
  comments: |
    Held until the Q3 switch refresh.
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `customFields`.
See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `rackRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxRack` | yes | — | ref arity CEL | `rack ForeignKey REQ -> dcim.Rack on_delete=CASCADE` |
| `units` | `integer[]` | yes | — | `minItems: 1`, `maxItems: 100`, each `≥ 1` | `units ArrayField REQ` |
| `userID` | `integer` | yes | — | `≥ 1` | `user ForeignKey REQ -> settings.AUTH_USER_MODEL on_delete=PROTECT` |
| `description` | `string` | yes | — | 1–200 | `description CharField REQ len=200` |
| `status` | `string` | no | `active` | enum: `pending`, `active`, `stale` | `status CharField len=50` |
| `tenantRef` | `ObjectRef` → `NetBoxTenant` | no | — | | `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

Four required fields out of seven, which is the most of any Kind in the catalogue and a direct
reading of the model: `rack`, `units`, `user` and `description` are all `REQ`.

### `spec.rackRef`

The rack whose units are reserved. Required, because NetBox's column is, and because it is half
of the lookup key — until it resolves the object reports `RefsResolved=False` naming this field
and makes no NetBox write at all.

It is also this Kind's **containment reference**, and the only one in NBO-051: `rack` is
`on_delete=CASCADE`, so NetBox deletes a rack's reservations with the rack. That makes the rack
the containment parent under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4, and
`kubectl delete netboxrack` garbage-collects the reservation CRs in the same namespace. See
[The one cascade in the rack hierarchy](#the-one-cascade-in-the-rack-hierarchy).

**If it is wrong.** `RefsResolved=False` with `RefNotFound`, `RefNotReady`, `RefDenied`,
`RefAmbiguous` or `RefTargetFailed` naming the field, and `Ready=False, Reason=WaitingForRef`,
with zero NetBox writes. An absent `rackRef` is rejected by the API server. A cross-namespace
`rackRef` with no grant is `RefDenied`, and gets no owner reference either — an owner reference
may never cross a namespace, and the `ParentOwned` condition says which happened
([ownership](../concepts/ownership.md)).

### `spec.units`

The rack units this reservation covers, numbered in the rack's own scheme — so
`startingUnit` and `descUnits` on the rack decide what `1` means.

**Order is data here, not incidental.** `units` is a Postgres `ArrayField`, NetBox stores it as
given and returns it in stored order, so the field is `registry.ClassArray` and the comparison
is order-sensitive — the same rule [`NetBoxVLANGroup`](netboxvlangroup.md)'s `vidRanges` follows.
The opposite of a many-to-many, where NetBox does not preserve order and comparing
order-sensitively would PATCH forever (`registry.FieldClass`, `internal/netbox/drift.go`).

So **write the units sorted**. Unchanged input produces no write at all
(`TestReservationUnitsRoundTripInOrder`), but reordering them *is* a change to the column and the
operator PATCHes it. NBO-051's ticket expected a set; the array semantics are what ship, because
comparing the column order-independently would report two genuinely different server states as
equal.

Bounded at 100 items rather than at the 256 a reference list gets
([a list needs a bound](../concepts/references.md#a-list-needs-a-bound)). The elements are
integers, so they carry no CEL rules to cost, and a reservation cannot cover more units than a
rack has — `uHeight`'s own ceiling. `minItems: 1`, because a reservation of no units reserves
nothing.

**If it is wrong.** Fewer than one item, more than 100, or a unit below 1 is rejected by the API
server. Units the rack does not have, or units another reservation already holds, are NetBox's
own `400` from `RackReservation.clean()`, reported as `Ready=False, Reason=Invalid` with
NetBox's message.

### `spec.userID`

The NetBox user the reservation is booked to, as a literal primary key.

Find it with `GET /api/users/users/?username=<name>` and pin it in the manifest; NetBox user ids
are stable.

Required, and it must be. NetBox refuses the create without it, and the operator must never
guess the token's own user — that would silently attribute every reservation to the operator's
service account. Refusing at admission is what turns that into a `kubectl apply` error rather
than an object that sits at `Ready=False` forever.

#### Why `userID` is not a reference

A `userRef ObjectRef` would not work today, in any of its four modes.
`internal/resolver`'s `Resolve` dispatches every mode — `name`, `slug`, `lookup` **and `id`** —
through `Descriptors.Get(Field.Target)`, because that is where the endpoint to query comes from:

```go
target, ok := r.kinds().Get(req.Field.Target)
if !ok {
    return Result{}, req.blocked(ErrRefKindUnavailable, req.unavailableDetail())
}
```

(`internal/resolver/resolver.go`.) With no `NetBoxUser` Descriptor there is no endpoint, so
`spec.userRef.id` would report `RefsResolved=False, Reason=RefKindUnavailable` forever, and
resolving `spec.username` through `users/users?username=` needs the same missing fact. The
ticket's escape hatch is not expressible against today's engine; closing the gap means teaching
a `Field` to name an endpoint for a Kind-less reference, which is a change to shared logic and
belongs in its own issue. Reported on the pull request.

`registry.TestReservationUserIsAValueNotAReference` pins the current answer, and is the test that
should change if that fact does.

**If it is wrong.** An id no user has is NetBox's own `400` on the foreign key, reported as
`Ready=False, Reason=Invalid`. There is no `RefNotFound` for it, because nothing resolves it —
which is the honest consequence of the escape hatch.

### `spec.description`

What the units are reserved for. **Required**, unusually for a `description`:
`dcim.RackReservation` shadows `PrimaryModel`'s own to make it so
(`docs/netbox-schema.md` → `dcim.RackReservation`, "shadows inherited: description
(PrimaryModel)", `description CharField REQ len=200`).

Two consequences. It is **not clearable**, so it carries none of the
absent-versus-empty-versus-set language the other Kinds' descriptions do
([field ownership](../concepts/field-ownership.md)). And it is the other half of this Kind's
lookup key, which is only possible *because* it is required — so two reservations on one rack
should not share one.

**If it is wrong.** Absent or `""` is rejected by the API server. A duplicate against another
reservation on the same rack is a `Conflict` at reconcile, not at admission.

### `spec.status`

Three members, read from `netbox/dcim/choices.py:146` in the 4.6.8 tree the digest was taken
from. Defaulted to NetBox's own default so the operator manages the field from the first
reconcile — a defaulted field that never reaches a payload is a field the operator can never
correct.

Its own set and deliberately not `NetBoxRack`'s five, which share not one member with it.
`RackReservationStatusChoices` declares `key = 'RackReservation.status'`, so a deployment can add
values through `FIELD_CHOICES`; a value added there needs this CRD's enum widened.

**If it is wrong.** A value outside the enum is rejected by the API server, naming the field.

### `spec.tenantRef`

Who the reservation is on behalf of. `PROTECT`, so a reservation holding this reference **blocks
deletion of that tenant in NetBox**, reported on the *tenant* as
`Deleting=False, Reason=Protected` naming this object. Not a containment reference: a reservation
outliving its tenant is a normal state.

**If it is wrong.** `RefsResolved=False` naming the field, and nothing is written.

### `spec.comments`

The only clearable field on this Kind, because `description` is required. Omit it to leave
NetBox's own value alone; set it to `""` to clear the value in NetBox
([field ownership](../concepts/field-ownership.md)). A `TextField`, so no `maxLength`.

## Natural key

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(rack, description)` | `?rack_id=<id>&description=<text>` | **nothing** — a convention |

One candidate, and both halves of it are required columns, so it is applicable as soon as
`rackRef` resolves and there is nothing to fall back to.

Both filters are registered: `rack_id` is declared on `RackReservationFilterSet`
(`ModelMultipleChoiceFilter`) and `description` is in its `meta_fields`
(`('id', 'created', 'description')`, NetBox 4.6.8 `netbox/dcim/filtersets.py:519`).

`description` can be in a key at all only because the model shadows the inherited column to make
it `REQ`. That is the same derivation [`NetBoxContact`](netboxcontact.md) uses for its `name`,
one step weaker: there, at least, an index exists.

### More than one match is a `Conflict`

Nothing on the server side stops two reservations on one rack with one description. The lookup
then matches both, and the engine reports `Ready=False, Reason=Conflict` naming the matches
rather than adopting one ([errors and retries](../concepts/errors-and-retries.md)). The fix is to
describe them differently — which is also what makes them legible in the NetBox UI.

There is deliberately **no `duplicateSpec`**. That is
[`NetBoxIPAddress`](netboxipaddress.md#allowduplicate)'s `allowDuplicate`, where NetBox's
data model *requires* a duplicate and the provenance stamp decides which row is the CR's own.
Here a second matching row means somebody declared the same reservation twice, and the honest
answer is `Conflict`.

Changing `userID` on an existing reservation is a PATCH rather than a duplicate, because `user`
is not in the key — a handover does not create a second reservation.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`provenance`, `conflict`, `deferredPending`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.naturalKey` is the field to read first on this Kind. Its identity is a convention, so
"which object did this adopt, and on what" is a question the recorded lookup answers and the
schema cannot.

`dcim.RackReservation` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the reservation exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `rackRef` resolves and `tenantRef` is unset or resolves | either does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `ParentOwned` | the owner reference on the rack is set | it cannot be — the rack is in another namespace, or somebody else set an owner reference | `Owned`, `CrossNamespace`, `Foreign` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`ParentOwned` appears here and on no other Kind in NBO-051, because this is the only one with a
containment parent. Reason glossary and retry intervals:
[errors and retries](../concepts/errors-and-retries.md) and
[ownership](../concepts/ownership.md).

## Kind-specific behaviour

### The one cascade in the rack hierarchy

`dcim.RackReservation.rack` is `on_delete=CASCADE`. Every other foreign key in NBO-051 —
`Rack.site`, `Rack.group`, `Rack.role`, `Rack.rack_type`, `Rack.tenant`,
`RackType.manufacturer`, `RackReservation.tenant` — is `PROTECT`, and `Rack.location` is
`SET_NULL` (`docs/netbox-schema.md`). So this is the only Kind here that takes an owner
reference, and it takes it on `rackRef`.

What that buys: NetBox deletes a rack's reservations when the rack goes, so the CRs have to go
with them, or the engine's create-if-absent step resurrects rows NetBox deliberately deleted.
`kubectl delete netboxrack r1` therefore garbage-collects its reservation CRs.

The owner reference is **non-controller** and is set only for a `rackRef` in `name` mode within
one namespace: an id- or slug-mode reference names no CR to own the object, and an owner
reference may never cross a namespace. `ParentOwned` says which of those happened. See
[ownership](../concepts/ownership.md) and
[ADR-0003](../decisions/0003-ownership-and-references.md).

### `units` is written in spec order, and reordering it is a write

Stated once more here because it is the acceptance criterion NBO-051 words the other way round.
Unchanged units produce no PATCH; reordered units produce one, because the column really is
different afterwards. Sort them in the manifest and the question does not arise.

### `unit_count` is never sent and never diffed

NetBox derives `unit_count` from `units` and refuses it on write
(`hack/testdata/ir-4.6.8.json.gz` → `dcim.RackReservation.write_path`), so it is in the
descriptor's read-only list. An ignored write produces a difference the next reconcile finds
again, and PATCHes forever.

### `deletionPolicy` defaults to `Delete`, and a reservation looks like the exception

A reservation reads like allocated state — it holds units the way an
[`NetBoxIPAddress`](netboxipaddress.md) holds an address — so it is worth saying why it is not.
What it holds is a promise about rack units, recorded nowhere else and recreated verbatim from
this manifest. Deleting an `ipam.IPAddress` frees an address for somebody else to allocate and
destroys the record of who had it; deleting a reservation frees units the manifest can re-claim
with no loss. So `Delete` -- which is now every kind's default
([#304](https://github.com/ricardomolendijk/netbox-operator/issues/304)), so this is no longer
a per-kind decision at all. See [deletion](../concepts/deletion.md#the-two-policies).

### What is not here yet

- **`owner`.** `ForeignKey -> users.Owner`, the same `users/*` deferral that makes `userID` a raw
  id. Nothing will ever write it.
- **A `username` field.** It needs an engine fact this operator does not have — see
  [Why `userID` is not a reference](#why-userid-is-not-a-reference).
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbrackres
NAME              RACK   UNITS        STATUS   ID    READY   AGE
cage-3-network    r1     [20 21 22]   active   204   True    3m
cage-3-storage    r1     [30 31]      pending  205   True    3m
```

| Column | JSONPath |
|---|---|
| `RACK` | `.spec.rackRef.name` |
| `UNITS` | `.spec.units` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`RACK` reads the *spec*, so it shows the intent even while the reference is unresolved and `ID`
is empty. `UNITS` prints the array as `kubectl` renders one, so a long run is truncated in the
column and readable in `-o yaml`.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `userID` | — | The field is required, because NetBox's column is `REQ` and the operator will not guess the token's own user. | `GET /api/users/users/?username=<name>` and pin the id. |
| `kubectl apply` rejected, message names `description` | — | Required: the model shadows `PrimaryModel.description` to make it so, and it is half the lookup key. | Give it one. |
| `kubectl apply` rejected, message names `units` | — | Empty, over 100 items, or a unit below 1. | Fix the list. |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | `rackRef` or `tenantRef` names an object that does not exist. | Create it, or fix the name. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace reference with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant. |
| `Ready=False`, `Reason=Conflict` naming two matches | | Two reservations on this rack share this description — a state no constraint prevents. | Describe them differently. |
| `Ready=False`, `Reason=Invalid`, message about units | | NetBox's own `clean()`: the units are outside the rack, or already reserved. | Pick free units, or check the rack's `startingUnit` and `descUnits`. |
| `Ready=False`, `Reason=Invalid`, message about `user` | | `userID` is not a NetBox user id. There is no `RefNotFound` for it, because nothing resolves it. | Look the id up again. |
| A PATCH on every resync, `units` in the message | `Synced=False` | Somebody reordered the array in NetBox, or the manifest lists it unsorted and a tool re-sorts it. | Sort the manifest and leave it sorted. |
| `ParentOwned=False`, `Reason=CrossNamespace` | | The rack is in another namespace, so no owner reference is possible. Everything else works. | Nothing, or co-locate them. |
| The reservation CR vanished on its own | | The rack CR was deleted, and `rack` is `CASCADE`, so the owner reference collected it. | Expected. |
| `Deleting=False`, `Reason=Protected` | | Not expected here: nothing points at a reservation. Read the message. | — |

## Related

- `NetBoxRack` — the containment parent, and the Kind whose `startingUnit` and `descUnits` decide
  what a unit number means
- [`NetBoxTenant`](netboxtenant.md) — what `tenantRef` points at, and why it does not cascade
- [`NetBoxContact`](netboxcontact.md) and [`NetBoxIPAddress`](netboxipaddress.md) — the other
  Kinds whose identity no constraint backs
- [`NetBoxVLANGroup`](netboxvlangroup.md) — the other Kind with a Postgres array whose order is
  data
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Ownership](../concepts/ownership.md) and
  [ADR-0003](../decisions/0003-ownership-and-references.md) — the `CASCADE` rule this Kind is the
  only user of in NBO-051
- [Drift detection](../concepts/drift.md) — the eight comparison rules, including the array one
- [Deletion](../concepts/deletion.md) — why this is `Delete` and not `Retain`
- [Coverage](../coverage.md) — where `users/users` is recorded as an excluded endpoint
