# `NetBoxProviderAccount`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxProviderAccount` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbprovideracct` |
| Status subresource | yes |

A `NetBoxProviderAccount` is one `circuits.ProviderAccount` in NetBox: the billing account a
circuit is bought under. One provider may sell you several, which is why
[`NetBoxCircuit`](netboxcircuit.md) carries both a provider and a provider account, and why both
appear in that kind's uniqueness constraints.

Endpoint `circuits/provider-accounts`, object type `circuits.provideraccount`. Its bases are
`ContactsMixin, PrimaryModel` (`docs/netbox-schema.md` -> `circuits.ProviderAccount`), so it is
both taggable and custom-fieldable and carries the provenance stamp in full.

Its own columns are few — `provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT`,
`account CharField REQ len=100` and `name CharField len=100`, plus `PrimaryModel`'s
`description CharField len=200` and `comments TextField`. What makes the kind worth reading is
not the column list but the constraint list: it declares two `UniqueConstraint`s and only one of
them can be an identity. See
[The condition that cannot be a filter](#the-condition-that-cannot-be-a-filter).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxProviderAccount
metadata:
  name: ntt-eu-billing
  namespace: default
spec:
  endpointRef: homelab
  providerRef:
    name: ntt
  account: "EU-4417"
```

Two required fields beyond the envelope, and both are halves of the same natural key. Until
`providerRef` resolves the object writes nothing at all — see
[`spec.providerRef`](#specproviderref).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxProviderAccount
metadata:
  name: ntt-eu-billing
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly -- Fail is the default
  deletionPolicy: Delete      # Delete | Retain -- Delete is this kind's default

  providerRef:
    name: ntt
  account: "EU-4417"

  name: EU billing account    # written to NetBox, and NOT part of the identity
  description: Invoiced monthly
  comments: |
    Renegotiated in Q3; the new rate card is on the contract page.
```

A runnable copy is
[`../../config/samples/netbox_v1alpha1_netboxprovideraccount.yaml`](../../config/samples/netbox_v1alpha1_netboxprovideraccount.yaml).

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope and
behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `providerRef` | [`ObjectRef`](../concepts/references.md) | **yes** | — | `provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT` |
| `account` | `string`, 1–100 | **yes** | — | `account CharField REQ len=100` |
| `name` | `string`, ≤100 | no | — | `name CharField len=100` |
| `description` | `string`, ≤200 | no | — | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | `comments (PrimaryModel) TextField` |

Every column is from `docs/netbox-schema.md` -> `circuits.ProviderAccount`.

### `spec.providerRef`

**Required.** An [`ObjectRef`](../concepts/references.md) pointing at a
[`NetBoxProvider`](netboxprovider.md). It is a value rather than a pointer in the Go type,
because there is no state in which it may be absent.

Required because NetBox's column is `REQ`. It is also the leading half of the only natural key,
so until it resolves the object reports `RefsResolved=False` naming this field and makes **no
NetBox write at all**. There is nothing to create: a provider-less account is not a state NetBox
has.

Written to NetBox as `provider`, filtered as `provider_id`.

`PROTECT`, so NetBox refuses to delete a provider while any account points at it. That surfaces
on the *provider* as `Deleting=False, Reason=Protected` — and it is also why this reference is
not a containment parent, see
[No containment parent, and PROTECT is the reason](#no-containment-parent-and-protect-is-the-reason).

Cross-namespace use needs a [`NetBoxRefGrant`](netboxrefgrant.md) in the namespace holding the
provider.

### `spec.account`

**Required**, 1 to 100 characters. The account number or identifier the provider bills you under.

The trailing half of the natural key. `(provider, account)` is the one unconditional
`UniqueConstraint` on the model, so this value has to be distinct among the accounts held with
one provider — and may repeat freely across providers.

### `spec.name`

Optional, up to 100 characters. A human label for the account, for people who do not recognise
the account number on sight.

**Written to NetBox, and not part of the identity.** Those are two separate statements and the
second does not weaken the first: `name` is in the descriptor's field map and reaches the
payload, it is diffed, and drift on it is corrected like any other column. What it is not is a
lookup key — see [The condition that cannot be a filter](#the-condition-that-cannot-be-a-filter)
for why the constraint that names it cannot be turned into a query.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and set
are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

### `spec.description`, `spec.comments`

Optional free text; `description` is capped at 200 characters and `comments` is a `TextField`, so
there is no length to derive and none is imposed.

Clearable on the same three-state terms as `name`.

## Natural keys

One candidate, and one only:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(provider, account)` | `?provider_id=<id>&account=<account>` | `providerRef` **resolves** |

Backed by the first of the model's two constraints:

```
UniqueConstraint(fields=('provider', 'account'), name='%(app_label)s_%(class)s_unique_provider_account')
```

Both filters are registered, and checking that is not a formality: django-filter drops a query
parameter it does not recognise and answers with the **unfiltered** set, so an unregistered
filter does not fail loudly — it returns whatever the table held and the engine adopts the first
of it (#206). `provider_id` is declared on `ProviderAccountFilterSet` and `account` is in its
`meta_fields` (NetBox 4.6.8, `circuits/filtersets.py:103`).

`providerRef` is **not deferred and cannot be**: the candidate matches on it, so stripping it
from a create would mean the lookup asked a different question from the create it decided on
(`registry.ErrDeferredNaturalKey`). With `providerRef` declared and unresolved, no candidate
applies, and the object waits rather than being created without its required column.

The second constraint is deliberately not a second candidate.

## The condition that cannot be a filter

`circuits.ProviderAccount` declares two constraints (`docs/netbox-schema.md` ->
`circuits.ProviderAccount`, `meta.constraints`):

```
UniqueConstraint(fields=('provider', 'account'), name='%(app_label)s_%(class)s_unique_provider_account')
UniqueConstraint(fields=('provider', 'name'),    name='%(app_label)s_%(class)s_unique_provider_name',
                 condition=~Q(name=''))
```

The second looks like a perfectly good `(provider, name)` key and is not one, because of the
`condition=`. Read it: the constraint applies only to rows whose `name` is a non-empty string.

**A condition is not automatically unusable.** A *null pin* the operator can reproduce.
`?location_id=null` is a filter NetBox understands — django-filter's own `null_value` rather
than an `__isnull` suffix `BaseFilterSet` would drop — and it is exactly how
[`NetBoxRack`](netboxrack.md#natural-keys) expresses `location IS NULL` in a candidate. #216 is
what made that expressible, and after it a candidate whose pins are all foreign keys is a
candidate the operator can ask for.

This condition is not that. There is no NetBox filter for "and this column is not the empty
string". Nor can a null pin approximate it: the column is `NOT NULL` with `blank=True`, so an
unset `name` is stored as the empty string and never as SQL `NULL`. Whether `name` is empty and
whether `name` is null are different questions, and only the second has a filter behind it.

So a candidate built from that constraint would have to drop the condition, and a candidate that
drops a condition queries the **unconstrained** set: every account under the provider, including
the ones carrying no label at all. On a kind that adopts what it finds, that is the class of
defect behind #206 and #216 — a lookup that answers a wider question than the one that was
asked, and takes over the first row that comes back.

The extractor reaches the same conclusion independently, which is what makes this evidence rather
than an opinion. The committed IR records the verdict against the constraint itself:

```json
"unusable": "constraint condition is more than a null pin: ['name']"
```

(`hack/testdata/ir-4.6.8.json.gz` -> `circuits.ProviderAccount`), and
[coverage](../coverage.md#natural-key-candidates-the-ir-calls-unusable) carries it as a row in
the unusable-constraint table:

| model | shipped | constraint | verdict | detail |
|---|---|---|---|---|
| `circuits.ProviderAccount` | yes | `%(app_label)s_%(class)s_unique_provider_name` | unusable | constraint condition is more than a null pin: ['name'] |

One consequence has to be stated on its own, because it is the misreading this page exists to
prevent: **`name` is still written**. Unusable as an identity is not the same as unmanaged. The
constraint is real and NetBox enforces it; the operator simply cannot *search* by it, so it looks
the object up by `(provider, account)` and then writes `name` like any other column.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`circuits.ProviderAccount` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and
is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

`status.naturalKey` shows `provider_id` and `account` and never `name`, which is the quickest way
to confirm on a live object which constraint the engine is using.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the account exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `providerRef` resolves | it does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved` does real work here, unlike on the reference-free organisational kinds where it is
`True` by construction: `providerRef` can report `RefNotFound`, and while it is unresolved the
object writes nothing. Retry intervals are the endpoint's, not this kind's.

## Kind-specific behaviour

### An unresolved provider writes nothing

The only candidate starts at `provider_id`, so with `providerRef` unresolved there is no identity
to look up and nothing to create. The object reports `RefsResolved=False, Reason=RefNotFound`
naming `providerRef` and performs **zero NetBox writes** — not a create with the field left out,
which is what a deferred reference would do on a kind whose key did not include it.

### No containment parent, and `PROTECT` is the reason

`provider` is this kind's only foreign key and it is `on_delete=PROTECT`, so deleting a provider
with accounts under it is **refused** by NetBox rather than cascading. An owner reference from the
provider CR to this one would therefore promise a cascade NetBox will not perform: Kubernetes
would garbage-collect the child CR on the parent's deletion while NetBox still held the row
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4). The descriptor declares no
containment reference and no cascade, and states that by omission — the flag is read off the
Django field's own `on_delete` rather than asserted by hand.

Deleting a provider that still has accounts surfaces on the *provider* as
`Deleting=False, Reason=Protected`. Delete the accounts first.

### Renaming `account` changes identity, renaming `name` does not

`account` is half the natural key, so editing it does not rename the NetBox account — it changes
what the CR is looking for, and the next reconcile creates a second account under the same
provider, leaving the first behind. Editing `name`, `description` or `comments` is a PATCH on the
object the CR already owns. This asymmetry is the practical face of the whole
[condition](#the-condition-that-cannot-be-a-filter) argument.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A provider account is configuration a manifest
recreates verbatim; nothing about deleting one frees a resource somebody else can take, which is
what `Retain` was reserved for. See [deletion](../concepts/deletion.md).

In practice a delete is often refused anyway: the circuit and virtual-circuit columns pointing at
a provider account are `PROTECT` too.

### Read-only fields

`created`, `last_updated`, `url` and `display` are in the descriptor's read-only list. They are
returned by NetBox and never sent; declaring them is what stops a value NetBox computes being
diffed against a spec that cannot hold it and PATCHed forever.

### What is not here yet

This kind ships in the catalogue slice of the `circuits` app (#58), alongside
[`NetBoxProvider`](netboxprovider.md), [`NetBoxProviderNetwork`](netboxprovidernetwork.md) and
[`NetBoxCircuit`](netboxcircuit.md); circuit terminations, circuit groups and the virtual-circuit
kinds are deferred to later work.

Absent deliberately from this Kind:

- **`owner`.** `ForeignKey -> users.Owner`, and the whole `users` app is an excluded endpoint
  (`hack/coverage-exclusions.yaml`), so there is no Kind to point at and nothing will ever write
  it.
- **`contacts`.** A `ContactsMixin` `GenericRelation` — the reverse of
  [`NetBoxContactAssignment`](netboxcontactassignment.md)'s foreign key rather than a column
  here. A provider account is already a legal target of that union; the assignment is written
  from that object.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

## Printer columns

```
$ kubectl get nbprovideracct
NAME             PROVIDER   ACCOUNT   ID   READY   AGE
ntt-eu-billing   ntt        EU-4417   12   True    4m
ntt-apac         ntt        AP-9920   13   True    4m
colt-main        colt       884120         False   30s
```

| Column | JSONPath |
|---|---|
| `PROVIDER` | `.spec.providerRef.name` |
| `ACCOUNT` | `.spec.account` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`PROVIDER` reads the *spec*, so it shows the intent even while the reference is unresolved and
`ID` is empty — the third row above. The two leading columns are exactly the two halves of the
natural key, which is what makes a duplicated identity visible in the listing.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `providerRef` or `account` | — | Both are required by the schema, because both NetBox columns are `REQ`. | Supply them. |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `Reason=RefNotFound` | `providerRef` names a CR that does not exist. No write was attempted. | Create the [`NetBoxProvider`](netboxprovider.md), or fix the name. |
| `RefsResolved=False`, `Reason=RefNotReady` | | The provider CR exists but has no `status.id` yet. | Wait; check the provider's own conditions. |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace `providerRef` with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant. |
| `Ready=False`, `Reason=Invalid`, NetBox `409` naming provider and account | `Ready` | Two accounts under one provider were given the same `account` value; NetBox's own unique index refused the second. | Give them distinct account numbers. The value only has to be unique per provider, not globally. |
| `Ready=False`, `Reason=Invalid`, NetBox `400` or `409` naming `name` | `Ready` | A non-empty `name` collided with another account under the same provider, through the *conditional* constraint. NetBox enforces it even though the operator cannot search by it. | Change the label, or clear it with `""`. |
| Two CRs sharing a `name` with different `account`s both reconciled, where one object was expected | — | `name` does not identify anything here. The identity is `(provider, account)`. | See [The condition that cannot be a filter](#the-condition-that-cannot-be-a-filter). To point two manifests at one account, give them the same `account`. |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this `(provider, account)` and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Set `onConflict: Adopt` in the namespace that should own it ([ADR-0002](../decisions/0002-crd-scoping.md)). |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the account re-enqueues off its event. |
| A second account appeared after an edit | — | `spec.account` was changed; it is half the identity. | See [Renaming `account` changes identity](#renaming-account-changes-identity-renaming-name-does-not). |
| Deleting the provider is refused | on the *provider*: `Deleting=False`, `Reason=Protected` | `provider` is `PROTECT`, and this Kind takes no owner reference for exactly that reason. | Delete the accounts first. |

## Related

- [`NetBoxProvider`](netboxprovider.md) — the required reference this kind's identity starts at
- [`NetBoxProviderNetwork`](netboxprovidernetwork.md) — the sibling under the same provider, whose `(provider, name)` constraint carries no condition and *is* a natural key
- [`NetBoxCircuit`](netboxcircuit.md) — what a provider account is bought for
- [`NetBoxRack`](netboxrack.md) — the contrast: a conditional candidate whose condition *is* a null pin, and can therefore be asked for
- [Lookups](../concepts/lookups.md) — candidates, null pins, ambiguity and `Conflict`
- [References](../concepts/references.md) — how `providerRef` resolves, and what happens while it does not
- [Coverage](../coverage.md#natural-key-candidates-the-ir-calls-unusable) — the full table of constraints the IR calls unusable
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
