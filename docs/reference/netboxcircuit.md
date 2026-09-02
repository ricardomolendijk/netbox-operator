# `NetBoxCircuit`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCircuit` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcircuit` |
| Status subresource | yes |

A `NetBoxCircuit` is one `circuits.Circuit` in NetBox: a single service bought from a provider,
identified by the circuit ID that provider gave you.

It is the kind the whole `circuits` app is arranged around. `CircuitTermination` hangs off it,
`CircuitGroupAssignment` points at it, and [`NetBoxProvider`](netboxprovider.md),
[`NetBoxProviderAccount`](netboxprovideraccount.md) and
[`NetBoxCircuitType`](netboxcircuittype.md) exist so that it can be declared by name. Endpoint
`circuits/circuits`, object type `circuits.circuit`.

Its bases are `ContactsMixin, ImageAttachmentsMixin, DistanceMixin, PrimaryModel`
(`docs/netbox-schema.md` -> `circuits.Circuit`). `PrimaryModel` mixes in both `TagsMixin` and
`CustomFieldsMixin`, so the kind is taggable and custom-fieldable and carries the whole
provenance stamp. `DistanceMixin` contributes two real columns, mapped below; `ContactsMixin`
and `ImageAttachmentsMixin` contribute a `GenericRelation` each and nothing writable.

Two decisions on this kind are worth reading before the field list, because neither is derivable
from the schema alone: [why only one of its two unique constraints is a natural-key
candidate](#why-only-one-natural-key-candidate), and [why `terminationA` and `terminationZ` are
not fields](#terminationa-and-terminationz-are-not-fields).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCircuit
metadata:
  name: ntt-100000123
  namespace: team-a
spec:
  endpointRef: homelab
  cid: "100000123"
  providerRef:
    namespace: netbox-catalog
    name: ntt
  typeRef:
    namespace: netbox-catalog
    name: transit
```

All three are required and the API server rejects a manifest missing any of them, because `cid`,
`provider` and `type` are all `REQ` on NetBox's model. Both references cross a namespace here,
which is the ordinary shape for a shared catalogue: each needs a
[`NetBoxRefGrant`](netboxrefgrant.md) **in the target namespace**
([references](../concepts/references.md#crossing-a-namespace)). `cid` is quoted because a circuit
ID that looks like a number is still a string, and an unquoted `100000123` is rejected rather
than coerced.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCircuit
metadata:
  name: ntt-100000123
  namespace: team-a
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  cid: "100000123"
  providerRef:
    namespace: netbox-catalog
    name: ntt
  typeRef:
    namespace: netbox-catalog
    name: transit
  # Optional, and deliberately not part of this kind's identity.
  providerAccountRef:
    namespace: netbox-catalog
    name: ntt-eu
  tenantRef:
    name: acme

  status: active              # default
  installDate: "2026-01-15"
  terminationDate: "2029-01-14"
  commitRate: 1000000         # Kbps
  # A string, not a number: NetBox returns it padded to two places and the engine compares the
  # two numerically, so this produces no PATCH on the second reconcile.
  distance: "12.5"
  distanceUnit: km

  description: Primary transit into AMS
  comments: |
    Ordered under MSA 2024-11. Renewal notice is 90 days.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `cid` | `string` | yes | — | 1-100 | `cid CharField REQ len=100` |
| `providerRef` | [`ObjectRef`](../concepts/references.md) -> `NetBoxProvider` | yes | — | ref arity CEL | `provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT` |
| `typeRef` | `ObjectRef` -> `NetBoxCircuitType` | yes | — | ref arity CEL | `type ForeignKey REQ -> circuits.CircuitType on_delete=PROTECT` |
| `providerAccountRef` | `ObjectRef` -> `NetBoxProviderAccount` | no | — | | `provider_account ForeignKey -> circuits.ProviderAccount on_delete=PROTECT` |
| `status` | `string` | no | `active` | enum: `planned`, `provisioning`, `active`, `offline`, `deprovisioning`, `decommissioned` | `status CharField len=50 def='CircuitStatusChoices.STATUS_ACTIVE'` |
| `tenantRef` | `ObjectRef` -> `NetBoxTenant` | no | — | | `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT` |
| `installDate` | `string` | no | — | `^(\d{4}-\d{2}-\d{2})?$` | `install_date DateField` |
| `terminationDate` | `string` | no | — | `^(\d{4}-\d{2}-\d{2})?$` | `termination_date DateField` |
| `commitRate` | `integer` | no | — | 0 to 2147483647 | `commit_rate PositiveIntegerField` |
| `distance` | `string` | no | — | `^$\|^[0-9]{1,6}(\.[0-9]{1,2})?$` | `distance (DistanceMixin) DecimalField decimal(8,2)` |
| `distanceUnit` | `string` | no | — | enum: `km`, `m`, `mi`, `ft` | `distance_unit (DistanceMixin) CharField len=50` |
| `description` | `string` | no | — | <=200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

`providerRef` and `typeRef` are **values rather than pointers** on the Go type, because both
columns are `REQ`. That is why a manifest omitting one is refused at admission rather than
becoming a `400` from NetBox three steps later.

### `spec.cid`

Required. The provider's circuit ID, up to 100 characters, and the trailing half of this kind's
natural key.

**Not unique on its own.** `cid` carries no column-level `UNIQUE`, and two providers may both
sell you a circuit numbered `100000123` — which is exactly why NetBox's constraint is a pair,
and why the lookup filters on `provider_id` as well. A key on `cid` alone would find the wrong
circuit, and would find it silently.

It is also one of the few NetBox column names that is not what a Go field would naturally be
called, which is why the descriptor's field map is an explicit table rather than a naming
convention: a payload key of `circuitId` would be dropped by DRF without complaint.

### `spec.providerRef`

Required. Who sells the circuit, and the leading half of the natural key.

**If it is unresolved.** `RefsResolved=False` with `RefNotFound` (or `RefNotReady`, `RefDenied`,
`RefAmbiguous`, `RefTargetFailed`) naming the field, `Ready=False, Reason=WaitingForRef`, and
**no NetBox write at all**. That is not politeness: with `providerRef` unresolved there is no
applicable natural-key candidate, so the engine has nothing to look the circuit up by, and
creating one anyway would file it against whichever provider NetBox happened to default to.

`PROTECT`, so NetBox refuses to delete a provider while a circuit points at it. That surfaces on
the *provider* as `Deleting=False, Reason=Protected`, and it is why `providerRef` is not a
containment parent — see [No containment parent, in either
direction](#no-containment-parent-in-either-direction).

### `spec.typeRef`

Required, because NetBox's column is. The circuit's classification — transit, peering, dark
fibre — pointing at a [`NetBoxCircuitType`](netboxcircuittype.md).

**Not part of the identity.** Neither constraint names `type`, so unlike `providerRef` an
unresolved `typeRef` does not make the lookup impossible. It still blocks the write: the column
is `REQ`, so a create without it is a `400` from DRF or a `NOT NULL` violation from Postgres, and
the engine reports `RefsResolved=False` rather than sending a body it knows NetBox will refuse.
The distinction shows in what is *knowable*: an unresolved `typeRef` leaves an existing circuit
alone, whereas an unresolved `providerRef` means the operator cannot tell whether one exists.

### `spec.providerAccountRef`

Optional. The billing account the circuit is bought under.

`(provider_account, cid)` is a real `UniqueConstraint` in NetBox and is deliberately **not** a
second natural-key candidate here. The whole argument is
[below](#why-only-one-natural-key-candidate); the short version is that the two constraints are
keyed on different references, so a second candidate could only ever match a circuit sold by a
*different* provider.

`PROTECT`, like every other foreign key on this kind.

### `spec.status`

The circuit's lifecycle state: `planned` (designed but not ordered), `provisioning` (the provider
is turning it up), `active` (carrying traffic), `offline` (exists but is down), `deprovisioning`
(being torn down) and `decommissioned` (cancelled).

Six members, read from `circuits/choices.py:10` in the NetBox 4.6.8 tree the digest was taken
from; the digest itself records the choice *class* and not its members, because the AST walk
cannot evaluate one, so the members are taken from the committed IR
(`hack/testdata/ir-4.6.8.json.gz` -> `enums` -> `CircuitStatusChoices`) rather than from memory.

**Defaulted to `active`**, NetBox's own default, so the operator manages the field from the first
reconcile. A defaulted field that never reaches a payload is a field the operator can never
correct, so a status changed in the UI would stay changed. The column is not nullable and
carries a default, so there is no empty member and no third state.

Its own Go type rather than a reuse of `RackStatus` or `SiteStatus`, even though `planned` and
`active` appear in all three: NetBox extends each set independently, so one shared enum would
make a value added to a rack legal on a circuit.

`CircuitStatusChoices` declares `key = 'Circuit.status'`, so a deployment *can* add members
through `FIELD_CHOICES`, and this closed CRD enum would reject the new value at admission.
Enumerated anyway, following [`NetBoxSite`](netboxsite.md), [`NetBoxPrefix`](netboxprefix.md) and
[`NetBoxRack`](netboxrack.md): a typo caught by `kubectl apply` is worth more than an extension
nobody has made, and widening the enum is a one-line change.

### `spec.tenantRef`

Optional. The tenant the circuit is assigned to.

**Not part of the natural key.** This kind has a real uniqueness constraint and `(provider, cid)`
is the whole of it, so there is nothing for a tenant term to disambiguate — the same reading
[`NetBoxWirelessLink`](netboxwirelesslink.md) makes, and the opposite of the kinds whose identity
is only a convention.

`PROTECT`, so a circuit holding this reference blocks that tenant's deletion, reported on the
[`NetBoxTenant`](netboxtenant.md) as `Deleting=False, Reason=Protected` naming this object. A
namespace does not imply a tenant ([references](../concepts/references.md)).

### `spec.installDate`, `spec.terminationDate`

Optional. When the circuit was turned up, and when it is due to be cancelled, both as
`YYYY-MM-DD`.

The pattern admits the **empty string** on purpose. Both columns are nullable `DateField`s, and
NetBox rejects `""` for a `DateField` outright, so an emptied value has to go over the wire as
`null` to clear rather than to fail. That is `registry.Field.EmptyIsNull` (#170) — the same
handling [`NetBoxAggregate.dateAdded`](netboxaggregate.md#specdateadded) gets.

Omit either to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states, and the operator tells them apart from `metadata.managedFields`
([field ownership](../concepts/field-ownership.md)).

The two are deliberately **not validated against each other**. NetBox does not order them, and a
CEL rule requiring `terminationDate` to follow `installDate` would reject data NetBox holds
today — a renewal recorded before the original turn-up, or a placeholder date on a circuit
inherited with an acquisition.

### `spec.commitRate`

Optional. The committed information rate, in **Kbps**. The bound is Django's
`PositiveIntegerField`: a 32-bit signed column with a non-negative check, hence 0 to
2147483647.

A pointer with **two** states rather than three, and that is a statement about the column rather
than an omission here. It is `blank=True, null=True`, and every value it can hold is a real
rate, so there is no empty *value* to write. Nil leaves NetBox's own value alone; a number claims
and sets it. Clearing the column back to null is not a state this field can express — the same
two-state shape [`NetBoxRackType`](netboxracktype.md)'s `outerWidth` has.

It needs no `EmptyIsNull`: the only two states an `*int32` has are absent and a number, so there
is no empty value for the flag to translate.

### `spec.distance`, `spec.distanceUnit`

The circuit's length, from `DistanceMixin` — the same mixin
[`NetBoxWirelessLink`](netboxwirelesslink.md#specdistance-specdistanceunit) takes its two columns
from.

**A string and not a `float64`**, for the reason `dcim.Site.latitude` is one: NetBox stores it as
`DecimalField(max_digits=8, decimal_places=2)` and returns it as a string, and an OpenAPI
`number` round-trips through IEEE-754 on its way in and out of the API server. The engine
compares two numeric strings **numerically** (`internal/netbox/drift.go`, `scalarEqual`), so
`"12.5"` and NetBox's `"12.50"` are the same value and produce no PATCH. That is held from the
outside by a test that rewrites the stored value to the padded form, lets several resync
intervals pass, and asserts that no further write was recorded
([drift detection](../concepts/drift.md)).

The pattern is `decimal(8,2)` written out: at most six integer digits and two fractional, and no
sign.

`distance` is cleared as `null` and not as an empty string (`registry.Field.EmptyIsNull`),
because DRF parses `""` as a number and rejects it on a nullable `DecimalField`. `distance_unit`
is a char column and takes the empty string, so it needs no flag.

`distanceUnit` uses the **same Go enum** `NetBoxWirelessLink` does, shared on purpose: both
columns come from the one `DistanceMixin`, and the IR records `DistanceUnitChoices` as
`extendable: false`, so there is no `FIELD_CHOICES` divergence for two separate enums to protect
against. Undefaulted — the column is nullable with no Django default, and there is no unit that
is right by default. NetBox clears `distance_unit` by itself on save whenever `distance` is null,
so a unit that disappears after the distance is cleared is expected rather than drift.

### `spec.description`, `spec.comments`

Both inherited from `PrimaryModel`. `description` is capped at 200 characters; `comments` is a
`TextField` and so carries no `maxLength`.

Both clearable: omit one to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural keys

One candidate, applicable whenever `providerRef` resolves:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `(provider, cid)` | `?provider_id=<id>&cid=<cid>` | `circuits_circuit_unique_provider_cid` |

`meta.constraints` on `circuits.Circuit` carries **two** entries, verbatim
(`docs/netbox-schema.md` -> `circuits.Circuit`):

```python
models.UniqueConstraint(fields=('provider', 'cid'),         name='%(app_label)s_%(class)s_unique_provider_cid')
models.UniqueConstraint(fields=('provider_account', 'cid'), name='%(app_label)s_%(class)s_unique_provideraccount_cid')
```

Both are unconditional, and only the first is a candidate. That narrowing is the one judgement
call on this kind, and the next section argues it.

Every filter is registered (NetBox 4.6.8, `circuits/filtersets.py:171`): `provider_id` is
declared on `CircuitFilterSet`, and `cid` is in its `meta_fields`. `provider_account_id` is
declared there **too**, over the `provider_account` column — so the discarded candidate is
discarded on the argument below and explicitly **not** for want of a filter.

Because the candidate is a database constraint rather than a convention, a pre-existing row with
the same pair *is* the same circuit, and the lookup cannot return more than one match. This kind
cannot report the ambiguity `Conflict` that
[`NetBoxRack`](netboxrack.md#more-than-one-match-is-a-conflict) can.

## Why only one natural-key candidate

Nothing forced this. The committed IR records **both** constraints as usable — `unusable: null`
on each (`hack/testdata/ir-4.6.8.json.gz`) — so unlike `circuits.ProviderAccount`'s second
constraint, the second one here could have been declared. It is a decision.

What decides it is that candidates are **tried in order**, and the two constraints are keyed on
*different* references.

Candidate 1 is `(provider, cid)`, and `provider` is `REQ`, so it is applicable on every reconcile
where `providerRef` resolves. A second candidate on `(provider_account, cid)` could therefore
only ever fire in one situation: NetBox holds **no** circuit with this provider and this cid, and
**does** hold one with this provider *account* and this cid. Because `ProviderAccount.provider`
is itself a foreign key, that object is by construction a circuit sold by a **different
provider**. Adopting it means PATCHing `provider` — silently repointing somebody else's circuit
at yours.

Declining means the create returns NetBox's own `409` naming `..._unique_provideraccount_cid`,
which says exactly what is wrong. A permanent `409` is a worse *loop* than an adoption — that is
precisely the argument
[`NetBoxRackType`](netboxracktype.md#natural-keys)'s fallback candidate makes, and it is a good
argument there. It is not, however, a worse *outcome* than repointing the wrong object, which is
the class of defect behind #206 and #216.

The asymmetry is what settles it:

| | `dcim.RackType` | `circuits.Circuit` |
|---|---|---|
| Candidate 1 | `(manufacturer, model)` | `(provider, cid)` |
| Candidate 2 | `(manufacturer, slug)` | `(provider_account, cid)` — **not declared** |
| Leading term | shared | **different references** |
| What the fallback can find | only an object with the same manufacturer | only an object with another provider |

`NetBoxRackType`'s two candidates share their leading `manufacturer` term, so its fallback can
only ever find an object that already belongs where the spec says it belongs, and PATCHing the
trailing term is a rename. These two candidates share nothing but `cid`.

The decision is also **reversible in the direction that matters**. Natural keys are Descriptor
data rather than CRD shape, so adding the second candidate later is not a breaking change to a
shipped `v1alpha1`. Shipping it now and discovering it adopts wrongly would not be reversible for
the objects it touched.

The behaviour is pinned from both ends. The descriptor is asserted to declare exactly one
candidate, never to key on `providerAccountRef`, and to offer no candidate at all while
`providerRef` is unresolved — including for a circuit that names an account. And a controller
test seeds a NetBox row sharing the account and the cid but differing on the provider, then
asserts that the reconcile **creates** rather than adopts, and that the object it creates carries
its own provider.

## `terminationA` and `terminationZ` are not fields

`termination_a` and `termination_z` are real foreign keys on `circuits.Circuit`, back to
`circuits.CircuitTermination` with `on_delete=SET_NULL` (`docs/netbox-schema.md` ->
`circuits.Circuit`). They are absent from this spec, and they stay absent.

The committed IR records both as `read_only: true` while still listing them in the serializer's
**write path**. That combination is the trap: DRF accepts a payload carrying one and silently
drops it, so the write returns success and changes nothing, and the engine finds the same
difference on the next reconcile, and the one after that. A silent no-op is a loop, not an error.

The authoritative relationship runs the other way. `CircuitTermination.circuit` plus `term_side`
is what carries `unique(circuit, term_side)` (`docs/netbox-schema.md` ->
`circuits.CircuitTermination`), and NetBox maintains the two pointers on the circuit from that
side. Two writers for one relationship is how you get flapping, so the operator writes the
termination side only — once there is a termination kind to write it with.

The guard is doubled deliberately:

- **Nothing in this spec can express either column.** No request body this kind produces can
  contain them, which a controller test asserts against every recorded request rather than
  against the final stored object — a version that sent them would look identical in the
  stored object, because DRF drops the key.
- **Both are in the Descriptor's read-only list**, alongside `_abs_distance`. A future field map
  that ever reaches for one fails `Validate` at boot with `ErrFieldReadOnly`, rather than
  PATCHing forever in production.

Noted honestly: the ticket asks for the two pointers to be surfaced in `status`, and this build
does not do that. `NetBoxObjectStatus` is shared across every kind, and the termination kind is
not shipped — so there is nothing yet to report, and nothing yet to report it about. It comes
with `NetBoxCircuitTermination`.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`circuits.Circuit` is a `PrimaryModel`, which mixes in both `TagsMixin` and `CustomFieldsMixin`,
so it carries the **whole provenance stamp** when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md). `ContactsMixin` and `ImageAttachmentsMixin` contribute
a `GenericRelation` each and nothing to stamp.

`status.naturalKey` records the filters the object was located by — here always `provider_id`
and `cid` — which is what makes "why was this circuit adopted" answerable without re-deriving
the candidate list.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the circuit exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `providerRef` and `typeRef` resolve, and every optional reference is unset or resolves | any does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved` is a real condition here, unlike on the reference-free organisational kinds: all
four of `providerRef`, `typeRef`, `providerAccountRef` and `tenantRef` can report `RefNotFound`.

The one worth separating out is `providerRef`. While it is unresolved there is **no applicable
candidate at all**, so the object writes nothing rather than creating a circuit against whichever
provider NetBox defaulted to. The other three block the write for ordinary reasons — a `REQ`
column, or a column the spec claims — and none of them changes what the lookup would ask.

Reason glossary and retry intervals: [errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### No containment parent, in either direction

Every foreign key on `circuits.Circuit` is `on_delete=PROTECT` — `provider`, `provider_account`,
`type` and `tenant`. None qualifies as a containment parent under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4, and
`registry.ErrContainmentNotCascade` refuses a descriptor that declares one.

The reason is not fussiness. An owner reference on `providerRef` would promise a cluster-side
cascade NetBox refuses to perform: `kubectl delete netboxprovider` would garbage-collect the
circuit CRs, their finalizers would issue `DELETE`s NetBox rejects with `PROTECT`, and the
circuits would be gone from Kubernetes and still in NetBox. So deleting a provider, circuit type
or tenant that still has circuits is **refused by NetBox**, and surfaces on *that* object as
`Deleting=False, Reason=Protected` naming the blocker. Delete the circuits first.

The one `CASCADE` in this app runs the other way. `CircuitTermination.circuit` is
`on_delete=CASCADE`, so when that Kind ships it takes **this** one as its containment parent --
which is a fact about the termination's descriptor and not about this one.

### A hand-made circuit is adopted, not duplicated

`(provider, cid)` is a real `UniqueConstraint`, so a circuit somebody entered in the UI is found
by the lookup and taken over: `status.adopted=true`, `status.id` set to the pre-existing row, and
one circuit in NetBox rather than two. Creating a second would be refused by the index anyway, so
adoption is the only outcome that works — which matters here, because circuits are exactly the
objects a long-running NetBox already holds.

With `onConflict: Fail` — the default — a matched object is reported as
`Ready=False, Reason=Conflict` instead, and nothing is written until an operator chooses.

### Editing `cid` or `providerRef` changes identity

Both are natural-key terms, so editing either does not rename the NetBox circuit. It changes what
the CR is looking for, and the next reconcile creates a second circuit under the new pair,
leaving the first behind — still holding its terminations.

`typeRef`, `providerAccountRef`, `tenantRef`, `status`, the two dates, `commitRate`, `distance`,
`distanceUnit`, `description` and `comments` are all safe to edit.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A circuit is the record of a commercial
arrangement that a manifest recreates verbatim; deleting one frees no address, no number and no
range, which is what `Retain` was reserved for. See [deletion](../concepts/deletion.md).

A circuit with terminations attached deletes cleanly from NetBox's side --
`CircuitTermination.circuit` is `CASCADE` — which is another way of saying the terminations are
part of the circuit rather than independent of it.

### The read-only columns are never sent and never diffed

The descriptor's read-only list is `created`, `last_updated`, `url`, `display`, `termination_a`,
`termination_z` and `_abs_distance`. The two termination pointers are the interesting entries,
for the reason [above](#terminationa-and-terminationz-are-not-fields).

`_abs_distance` is `DistanceMixin`'s normalised metres, `decimal(13,4)`, derived from `distance`
and `distance_unit` on every save, and the IR records it as absent from the write path entirely.
It will not match the spec and is not supposed to.

### What is not here yet

This ships in the catalogue slice of the `circuits` app (#58), alongside
[`NetBoxProvider`](netboxprovider.md), [`NetBoxProviderAccount`](netboxprovideraccount.md),
[`NetBoxProviderNetwork`](netboxprovidernetwork.md) and
[`NetBoxCircuitType`](netboxcircuittype.md). Deferred to a later slice:

- **`NetBoxCircuitTermination`.** The A and Z ends, and the Kind that carries
  `unique(circuit, term_side)`. It brings a `GenericForeignKey` termination target and a
  containment parent pointing back here, so it is a slice of its own.
- **No inline `terminations:` list.** The ticket asks for one, and it cannot be written yet: an
  inline child set materialises CRs of a *child Kind*, and `NetBoxCircuitTermination` has no
  Descriptor in this build ([inline children](../concepts/inline-children.md)). Deferred with the
  termination kind itself, not instead of it.
- **`NetBoxCircuitGroup` and `NetBoxCircuitGroupAssignment`.** The assignment is a
  `GenericForeignKey` membership object keyed `(member_type, member_id, group)`, so it needs the
  [generic-ref](../concepts/generic-refs.md) machinery and the group kind together.
- **The three virtual-circuit kinds.** A separate model family in the same app, with their own
  constraints and their own termination model.

Absent from this spec deliberately:

- **`owner`** is `ForeignKey -> users.Owner`, and the whole `users` app is an excluded endpoint
  (`hack/coverage-exclusions.yaml`), so there is no Kind to point at. A field the CRD accepted
  and the payload dropped would report success while writing nothing.
- **`contacts` and `images`** are `GenericRelation`s from `ContactsMixin` and
  `ImageAttachmentsMixin` — the reverse of somebody else's foreign key, not columns. A circuit is
  a legal target of the union on [`NetBoxContactAssignment`](netboxcontactassignment.md), and the
  assignment is written from that object's own side.
- **`group_assignments`** is also a `GenericRelation`, and in particular it is
  `circuits.CircuitGroupAssignment`'s side of the relationship, which this milestone defers.
- **`termination_a` and `termination_z`**, read-only, for the reason above.

`tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbcircuit
NAME            CID         PROVIDER   STATUS    ID    READY   AGE
ntt-100000123   100000123   ntt        active    210   True    6m
lumen-4417      4417        lumen      planned   211   True    6m
ams-ix-77       77          ams-ix     active           False   45s
```

| Column | JSONPath |
|---|---|
| `CID` | `.spec.cid` |
| `PROVIDER` | `.spec.providerRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`CID` and `PROVIDER` are the natural key, in order, and both read the *spec* — so they show the
intent even while `providerRef` is unresolved and `ID` is still empty, which is the state of the
third row above.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `cid`, `providerRef` or `typeRef` | — | All three are required by the schema, because NetBox's columns are `REQ`. | Supply them. |
| `Ready=False`, `Reason=WaitingForRef` naming `providerRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | The provider does not exist yet. With it unresolved there is **no applicable candidate**, so the operator deliberately writes nothing rather than filing the circuit against a defaulted provider. | Create the [`NetBoxProvider`](netboxprovider.md), or fix the name. |
| `RefsResolved=False`, `Reason=RefDenied` | `RefsResolved` | A reference names another namespace with no grant there. | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. |
| `Ready=False`, `Reason=Invalid`, message names `..._unique_provideraccount_cid` | `Ready` | NetBox refused the create: another circuit already uses this `cid` under this provider account — and therefore under a **different provider**. The operator will not adopt it, by design. | Check which provider really sells this circuit. Correct `providerRef` or `cid`, or clear `providerAccountRef` if it is the wrong account. See [Why only one natural-key candidate](#why-only-one-natural-key-candidate). |
| `Ready=False`, `Reason=Conflict` | `Ready` | A hand-made circuit matched `(provider, cid)` and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Set `onConflict: Adopt` to take it over, or change `cid`. |
| `Ready=False`, `Reason=Conflict` naming a namespace | `Ready` | Another namespace already owns this circuit; a namespace boundary does not partition NetBox's uniqueness. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner. |
| A second circuit appeared after an edit | — | `spec.cid` or `spec.providerRef` was changed, and both are key terms. | See [Editing `cid` or `providerRef` changes identity](#editing-cid-or-providerref-changes-identity). |
| Deleting the provider, type or tenant is refused | on *that* object: `Deleting=False`, `Reason=Protected` | Every foreign key on this kind is `PROTECT`, which is also why this Kind takes no owner reference. | Delete the circuits first, or set `deletionPolicy: Retain` on them. |
| `kubectl apply` rejected, `distance` pattern | — | More than two decimal places, more than six integer digits, a sign, or a unit inside the string. A YAML bare `12.5` is a number, not a string. | `decimal(8,2)`, unsigned, quoted. The unit goes in `distanceUnit`. |
| `distanceUnit` cleared itself | none | NetBox nulls the unit on save whenever the distance is null. | Expected. Set `distance` too. |
| `kubectl apply` rejected on `status` | — | The value is not in the enum. A NetBox that extended `CircuitStatusChoices` through `FIELD_CHOICES` needs this CRD's enum widened. | Use a listed value. |
| `installDate` or `terminationDate` rejected | — | Not `YYYY-MM-DD`, or unquoted. | Quote it and use the ISO form. `""` is legal and clears the column. |
| A PATCH on every resync | `Synced=False` | Not expected for `distance` — it is compared numerically. | Read the `Synced` message for the field that differs, and `status.lastAppliedHash`. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the circuit re-enqueues off its event. |

## Related

- [`NetBoxProvider`](netboxprovider.md) — who sells the circuit, and the leading half of its identity
- [`NetBoxCircuitType`](netboxcircuittype.md) — what `typeRef` points at
- [`NetBoxProviderAccount`](netboxprovideraccount.md) — the account whose constraint is deliberately not a candidate
- [`NetBoxProviderNetwork`](netboxprovidernetwork.md) — the fourth catalogue kind in this slice
- [`NetBoxRackType`](netboxracktype.md) — the two-candidate fallback this kind declines, and why its candidates share a leading term
- [`NetBoxWirelessLink`](netboxwirelesslink.md) — the other `DistanceMixin` kind, sharing this one's `distanceUnit` enum
- [`NetBoxAggregate`](netboxaggregate.md) — the other nullable date cleared with `null`
- [`NetBoxRack`](netboxrack.md) — for contrast: an identity no constraint backs, and what that costs
- [`NetBoxCable`](netboxcable.md) — what a circuit termination will eventually be cabled to
- [`NetBoxTenant`](netboxtenant.md) — what `tenantRef` points at, and why a namespace is not a tenant
- [Lookups](../concepts/lookups.md) — candidate order, applicability and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete
- [Drift detection](../concepts/drift.md) — the numeric compare `distance` goes through
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [ADR-0003](../decisions/0003-ownership-and-references.md) — why a `PROTECT` foreign key gets no owner reference
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
