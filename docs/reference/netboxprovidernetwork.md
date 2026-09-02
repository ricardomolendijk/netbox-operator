# `NetBoxProviderNetwork`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxProviderNetwork` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbprovidernet` |
| Status subresource | yes |

A `NetBoxProviderNetwork` is one `circuits.ProviderNetwork` in NetBox: the provider's own
network on the far side of the demarcation point — the thing a circuit terminates *into* when
the far end is not a site you own. NetBox models it as an object so that a `CircuitTermination`
can point at it through its generic foreign key, and so that several circuits can be recorded
as landing on the same provider cloud.

**In this build nothing points at it yet.** `circuits.CircuitTermination` has no Kind, so the
honest answer to "why would I create one of these today?" is that it is the target the
termination kind will need ([What is not here yet](#what-is-not-here-yet)). This ships in the
catalogue slice of the `circuits` app (#58); circuit terminations, circuit groups and the
virtual-circuit kinds are deferred, no dates.

It has the simplest identity in the provider family — one unconditional constraint, one
candidate, no pin — and it is worth reading next to
[`NetBoxProviderAccount`](netboxprovideraccount.md), whose constraint carries *the same
generated name* and is unusable:
[The same constraint name, two different answers](#the-same-constraint-name-two-different-answers).
It is also the one kind in the family with **no `ContactsMixin`** — its bases are
`PrimaryModel` and nothing else (`docs/netbox-schema.md` → `circuits.ProviderNetwork`) — so
there is not even a `contacts` `GenericRelation` to explain away.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxProviderNetwork
metadata:
  name: ntt-backbone
  namespace: default
spec:
  endpointRef: homelab
  providerRef:
    name: ntt
  name: Backbone
```

Two spec fields beyond the envelope, both required. `providerRef` is a value and not a pointer,
so a manifest without one is rejected by the API server rather than discovered at reconcile.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxProviderNetwork
metadata:
  name: ntt-backbone
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly -- Fail is the default
  deletionPolicy: Delete      # Delete | Retain -- Delete is this kind's default

  # The leading half of (provider, name), the one constraint on this model.
  providerRef:
    name: ntt

  name: Backbone              # unique per provider, not globally
  serviceId: NTT-BB-0001      # the provider's own service identifier; not part of the identity
  description: The provider cloud a circuit terminates into
  comments: |
    Peering is via the two Amsterdam PoPs.
```

A runnable copy is
[`../../config/samples/netbox_v1alpha1_netboxprovidernetwork.yaml`](../../config/samples/netbox_v1alpha1_netboxprovidernetwork.yaml).

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the
full treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `providerRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxProvider` | **yes** | — | `provider ForeignKey REQ -> circuits.Provider on_delete=PROTECT` |
| `name` | `string`, 1–100 | **yes** | — | `name CharField REQ len=100` |
| `serviceId` | `string`, ≤100 | no | — | `service_id CharField len=100` |
| `description` | `string`, ≤200 | no | — | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | `comments (PrimaryModel) TextField` |

### `spec.providerRef`

Required. The provider whose network this is, pointing at a
[`NetBoxProvider`](netboxprovider.md). NetBox's column is `REQ`, so there is no such thing as a
provider network without one.

It is the leading half of the natural key, so until it resolves the object reports
`RefsResolved=False` naming this field and makes **no NetBox write at all**.

A reference into another namespace needs a [`NetBoxRefGrant`](netboxrefgrant.md) in the
namespace being referenced; without it the object sits at `RefsResolved=False,
Reason=RefDenied` ([references](../concepts/references.md)). The column is
`on_delete=PROTECT`, so this is **not** a containment parent
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

### `spec.name`

Required, 1–100 characters. The network's name.

**Unique per provider rather than globally.** The `UNIQUE` is on the pair `(provider, name)`
and there is no column-level unique here, so two providers may both have a network called
`Backbone`. Do not treat this name as an identifier you can hand around on its own.

### `spec.serviceId`

Optional, up to 100 characters. The provider's own identifier for the service carried on this
network.

**Not part of the identity.** `service_id` carries no `UNIQUE` of any kind, so it is
deliberately neither a natural-key term nor a tiebreak between two matches: a filter on it can
match any number of rows, and a lookup that fell back to it would be a guess. Its wire spelling
is `service_id`, not `serviceId` — see
[The field map earns a table](#the-field-map-earns-a-table).

Omit the key to leave NetBox's own value alone; set it to the empty string to clear it. Absent,
empty and set are three states and the operator tells them apart from `metadata.managedFields`
-- see [field ownership](../concepts/field-ownership.md).

### `spec.description`

Optional free text, up to 200 characters, inherited from `PrimaryModel`. Clearable on the same
three-state terms as `serviceId`.

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`
(`docs/netbox-schema.md` → `circuits.ProviderNetwork`, `comments (PrimaryModel) TextField`), so
there is no length marker to derive. Clearable on the same terms.

## Natural keys

One candidate, and no conditional variant, from `circuits.ProviderNetwork.meta.constraints`:

```
UniqueConstraint(fields=('provider', 'name'), name='%(app_label)s_%(class)s_unique_provider_name')
```

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(provider_id, name)` | `?provider_id=<id>&name=<name>` | `providerRef` **resolves** |

Both filters are registered: `provider_id` is declared on `ProviderNetworkFilterSet` and `name`
is in its `meta_fields` (NetBox 4.6.8, `circuits/filtersets.py:133`). Checking is not ceremony:
django-filter **drops a parameter it does not recognise** and answers with the unfiltered set,
so an unregistered filter name would not fail the lookup — it would turn it into "adopt the
first provider network in NetBox"
([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206)).

The constraint has no `condition=` clause, so **there is no null pin here** and nothing to
express as a second candidate. `provider` is `REQ`; a provider-less provider network does not
exist to be looked up. The committed IR records the constraint with `unusable: null`
(`hack/testdata/ir-4.6.8.json.gz` → `circuits.ProviderNetwork`).

`providerRef` is therefore **not deferrable**: the only candidate matches on it, so it applies
only once the reference resolves, and until then the object writes nothing at all rather than
being created without its required column. `service_id` is neither a second candidate nor a
tiebreak, for the reason [`spec.serviceId`](#specserviceid) gives.

## The same constraint name, two different answers

`circuits.ProviderNetwork` and `circuits.ProviderAccount` both carry a constraint whose
generated name is literally the same string, and only one of them is usable:

    circuits.ProviderNetwork
      UniqueConstraint(fields=('provider', 'name'), name='..._unique_provider_name')

    circuits.ProviderAccount
      UniqueConstraint(fields=('provider', 'account'), name='..._unique_provider_account')
      UniqueConstraint(fields=('provider', 'name'), name='..._unique_provider_name',
                       condition=~Q(name=''))

The whole of the difference is the `condition=` clause on the second one:

| | `circuits.ProviderNetwork` | `circuits.ProviderAccount` |
|---|---|---|
| Constraint name | `%(app_label)s_%(class)s_unique_provider_name` | `%(app_label)s_%(class)s_unique_provider_name` |
| Condition | none | applies only to rows whose `name` is not the empty string |
| Reproducible as a filter pair | yes, exactly as written | no |
| IR verdict | `unusable: null` | `unusable: "constraint condition is more than a null pin: ['name']"` |
| Used as a candidate | yes, and it is the only one | no — that kind keys on `(provider, account)` |

A **null pin** the operator can reproduce: `?provider_id=null` is a filter NetBox understands,
so a constraint conditional on a column being NULL becomes a candidate with that parameter
pinned. The condition on `ProviderAccount` is not that. It says the constraint applies only to
rows whose `name` is non-empty, and there is no NetBox filter for "and this column is not the
empty string". A candidate that dropped the condition would match the *unconstrained* set --
which on a kind that adopts what it finds is
[#206](https://github.com/ricardomolendijk/netbox-operator/issues/206) again.

This kind has no such clause, so its constraint reproduces exactly. The lesson generalises: a
constraint's *name* says nothing about whether it is usable, so read the condition. See
[`NetBoxProviderAccount`](netboxprovideraccount.md) for the other half of the argument.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`circuits.ProviderNetwork` is a `PrimaryModel`, so it carries both `tags` and `custom_fields`
and is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby)
is set. See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the provider network exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `providerRef` resolves | it does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Retry intervals are the endpoint's, not this kind's — see
[errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### `name` is unique per provider, not globally

Worth repeating, because it is the thing people get wrong. Two CRs naming the same `name` under
*different* providers are two different objects that will never collide; two naming it under
the *same* provider are one object, and the second reports `Ready=False, Reason=Conflict`
([ADR-0002](../decisions/0002-crd-scoping.md)).

### The field map earns a table

One spec field is spelled differently from its NetBox column, which is exactly the mapping that
has to be declared rather than inferred:

| Spec field | NetBox field |
|---|---|
| `serviceId` | `service_id` |
| `providerRef` | `provider` |
| `name`, `description`, `comments` | unchanged |

NetBox **ignores a field name it does not know** rather than rejecting it, so a payload
carrying `serviceId` would write nothing and report success — a silent no-op, then drift the
next reconcile finds and tries to correct forever. `created`, `last_updated`, `url` and
`display` are declared read-only for the same reason from the other direction: NetBox drops
them on write, and a dropped write comes back as a difference every reconcile.

### No containment parent, and nothing cascades

`provider` is `on_delete=PROTECT`, so deleting a provider while a provider network points at it
is **refused** by NetBox rather than cascading. There is no server-side deletion for an owner
reference to mirror, so this kind declares no containment parent
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete`
([#176](https://github.com/ricardomolendijk/netbox-operator/issues/176) option B). A provider
network is configuration a manifest recreates verbatim; nothing about deleting one frees a
resource somebody else can take, which is what `Retain` was reserved for
([deletion](../concepts/deletion.md)).

### A hand-made provider network is adopted, not duplicated

The pair is unique in the database, so a lookup that matches an existing row takes it over
(`status.adopted=true`) rather than creating a second one, which a create would be refused for
anyway. That is what makes a fresh operator safe to point at a long-running NetBox.

### Editing `name` or `providerRef` changes identity

Both are natural-key terms. Editing either does not rename the NetBox object — it changes what
the CR is looking for, and the next reconcile creates a second provider network, leaving the
first behind. `serviceId`, `description` and `comments` are safe.

### What is not here yet

Nothing in this build points at a `NetBoxProviderNetwork`. `circuits.CircuitTermination` has no
Kind and no Descriptor, so there is no way to record a circuit landing on this network, and no
[`NetBoxCable`](netboxcable.md) can reach one either — a cable gets to a circuit through its
termination. Creating a provider network today is preparation: it is the object the termination
kind will resolve against once it exists (#58). A [`NetBoxCircuit`](netboxcircuit.md) can be
created and will name the same provider, but it cannot yet be joined to this network.

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at: a field the CRD accepted and
the payload dropped would report success while writing nothing.

## Printer columns

```
$ kubectl get nbprovidernet
NAME            PROVIDER   ID   READY   AGE
ntt-backbone    ntt        14   True    5m
ntt-metro-e     ntt        15   True    5m
colt-backbone   colt       16   True    2m
```

| Column | JSONPath |
|---|---|
| `PROVIDER` | `.spec.providerRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`NAME` is the CR's own name rather than `spec.name`, and the first and third rows are why the
distinction matters: two provider networks called `Backbone`, told apart only by their
provider. `spec.name` is deliberately not a printer column — it would render as a second
`NAME` header beside the object's own.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `RefsResolved=False`, `Reason=RefNotFound` | `RefsResolved` | No [`NetBoxProvider`](netboxprovider.md) by that name in the named namespace. | Create it, or fix the name. The network re-enqueues off the provider's event. |
| `RefsResolved=False`, `Reason=RefDenied` | `RefsResolved` | A cross-namespace `providerRef` with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant there. |
| `Ready=False`, `Reason=WaitingForRef`, and nothing in NetBox | `Ready` | `providerRef` has not resolved, so the only candidate does not apply and the object writes nothing at all. | Resolve the reference; see [`spec.providerRef`](#specproviderref). |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another CR already owns this `(provider, name)` pair, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Rename, or set `onConflict: Adopt` in the namespace that should own it. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the network re-enqueues off its event. |
| A second provider network appeared after an edit | — | `spec.name` or `spec.providerRef` was changed. | See [Editing `name` or `providerRef` changes identity](#editing-name-or-providerref-changes-identity). |
| Nothing can be made to terminate on the network | — | Expected in this build. | [What is not here yet](#what-is-not-here-yet). |

## Related

- [`NetBoxProvider`](netboxprovider.md) — the root of the `circuits` app, and this kind's required reference
- [`NetBoxProviderAccount`](netboxprovideraccount.md) — the same constraint name, and the answer that goes the other way
- [`NetBoxCircuit`](netboxcircuit.md) — the kind that will terminate into this one once terminations exist
- [`NetBoxCable`](netboxcable.md) — the other kind waiting on a circuit-termination Kind
- [Lookups](../concepts/lookups.md) — candidates, conditions, ambiguity and `Conflict`
- [References](../concepts/references.md) — `ObjectRef`, grants, and what an unresolved reference blocks
- [Generic references](../concepts/generic-refs.md) — the mechanism a `CircuitTermination` would use to point here
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete, and which kinds default to `Retain`
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
