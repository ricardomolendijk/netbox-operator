# The reconcile engine

`internal/reconciler.Engine` is the only place a create, adopt or update decision is made.
One engine drives every kind: everything that differs between kinds arrives as data on a
[`registry.Descriptor`](../../internal/registry/registry.go), so there is no branch on Kind
anywhere in the package and adding a kind is three new files and no edit here.

```go
result, err := engine.Reconcile(ctx, obj)   // obj is any kind embedding the shared envelope
```

## The decision

```
1. Descriptor for the object's kind        no descriptor -> returned error (a wiring bug)
2. Client for spec.endpointRef             not Ready     -> WaitingForEndpoint, requeue 30s
3. Payload from the spec                   unmapped field -> Invalid, zero writes
4. Locate the live object
     a. status.id set -> GET by id         404 -> clear status.id, fall through to (b)
     b. natural-key candidates, in order   0 matches -> create
                                           1 match   -> adopt or update
                                           >1        -> Conflict, zero writes
                                           no usable candidate -> WaitingForKey, zero writes
5. Create   POST, record status.id, status.url        Event Created
   Adopt    only when asked for, else Conflict        Event Adopted
   Update   Drift() -> PATCH exactly the diff         Event Updated
   No drift log at debug and write nothing
6. Conditions, status, requeue after the endpoint's resyncPeriod ± 10%
```

Every exit goes through one status helper, which always sets `observedGeneration` --
forgetting it makes `kubectl wait` lie, which is the kind of bug nobody notices until an
automation hangs -- and which **writes nothing when nothing changed**. That second half is
what makes a no-drift resync free: otherwise every object in the cluster would take a
status write every resync period, for no new information.

`status` is the only thing the engine writes, ever. See
[ADR-0005](../decisions/0005-gitops-coexistence.md).

## Ordered natural-key candidates

`Descriptor.Candidates(state)` returns the candidates that are usable for this object's
current spec, in the descriptor's priority order. The engine tries them in turn and stops at
the first that matches something.

Two things about the order are load-bearing:

- **A candidate that does not apply is skipped, not relaxed.** `ipam.VRF` keys on `rd` when
  set and on `name` otherwise; `dcim.Device` on `(name, site, tenant)` or on the
  tenant-is-null variant. Both are priority lists, not fallbacks.
- **No applicable candidate at all means wait, and write nothing.** A `dcim.Region` whose
  `parentRef` is declared but has not resolved must *not* fall through to
  `name WHERE parent IS NULL`: that candidate finds an unrelated top-level Region, and the
  follow-up PATCH reparents somebody else's data. `Ready=False, Reason=WaitingForKey`, zero
  requests, and the next resync tries again.

The lookup that actually ran is recorded in `status.naturalKey`, including when it matched
nothing -- the first question about an object that was not adopted is what the engine looked
for.

**More than one match is a `Conflict` that names every matching NetBox id, and zero
writes.** Not first-match adoption: `ipam.Prefix` and `ipam.IPAddress` have no
`meta.constraints` at all, so several matches is a routine state rather than a corrupt
database, and picking one silently reparents whatever else keys on it. This is why the
engine's `Reader` lists rather than calling `GetOne`: `netbox.AmbiguousError` carries a
count, and a count is not something an operator can act on.

## Adoption is opt-in

Finding an existing NetBox object that matches the natural key is **not** permission to take
it over. The engine's very next step reconciles that object towards this CR's spec, and
there is no undo for that: opting in is one field, recovering from a wrong adoption is a
restore.

So `spec.onConflict` gates it, and its default is the refusal:

| `spec.onConflict` | Nothing matches | One object matches |
|---|---|---|
| `Fail` (default) | create | `Ready=False, Reason=Conflict` naming the id, zero writes |
| `Adopt` | create | adopt, then reconcile drift |
| `AdoptOnly` | `Ready=False, Reason=AdoptOnly`, zero writes | adopt, then reconcile drift |

`AdoptOnly` is for objects a human owns, where the operator should correct drift but never
bring one into existence.

A typed spec field rather than an annotation, for three reasons: the API server can validate
an enum and cannot validate an annotation; `kubectl explain` shows it; and it is in Git,
where [ADR-0005](../decisions/0005-gitops-coexistence.md) says desired state belongs.
`status.adopted` records the outcome.

An object located by an id the CR already recorded in `status.id` raises no adoption
question -- it is already ours.

> This deviates from `plan.md` §6.1, which defaulted `onConflict` to `Adopt`. Defaulting to
> adoption makes the destructive outcome the one you get by not reading the docs.

## Spec field names and NetBox field names

`KeyField.Spec` and `ContainmentRef` are **CR spec** names (`vrfRef`, `primaryIP4Ref`).
`Deferred`, `ReadOnly`, `M2M`, `ObjectTypeLists` and `RecreateOn` are **NetBox API** names
(`vrf`, `primary_ip4`). Something has to bridge them, and that something is
`Descriptor.Fields`: an explicit `{Spec, API, Ref}` table per kind.

```go
Fields: []registry.Field{
    {Spec: "name", API: "name"},
    {Spec: "objectTypes", API: "object_types"},
    {Spec: "siteRef", API: "site", Ref: true},
    {Spec: "primaryIP4Ref", API: "primary_ip4", Ref: true},
},
```

The alternative was a convention owned by the payload builder: strip `Ref`, then camelCase
to snake_case. It is smaller, and it is wrong exactly where being wrong is expensive.
`primaryIP4Ref` becomes `primary_i_p4`, `oobIPRef` becomes `oob_i_p`, `wirelessLANs` becomes
`wireless_l_a_ns`. Each needs an acronym list, and an acronym list is a per-kind fact wearing
a convention's clothes.

What settles it is the failure mode. **NetBox ignores a field it does not recognise rather
than rejecting it**, so a wrong name is not a 400 — it is a write that reports success and
changes nothing, forever. A convention fails silently; an explicit pair cannot. And since
the M7 generator (NBO-041/042) derives the Descriptor from NetBox's own OpenAPI schema, it
has both names in hand already: emitting the pair is less work than emitting an acronym
table.

Being explicit also buys boot-time validation, which a convention cannot have.
`Descriptor.Validate` rejects a duplicate spec or API name, a spec field mapped onto a
read-only column, a natural key or containment ref naming a spec field the map does not
declare, and a deferred field no reference writes. All at manager start, rather than one
reconcile at a time.

Two things sit outside the table by design:

- **The engine's own spec fields** (`endpointRef`, `onConflict`) are excluded from every
  payload. The exclusion list is read off the `NetBoxObjectSpec` struct with reflection, so
  a field added to the envelope cannot leak into NetBox as an unknown column.
- **A polymorphic reference** writes two columns from one spec field, which a `{Spec, API}`
  pair cannot express, so it names its spec field on the `GenericFKSpec` instead:
  `{TypeField: "scope_type", IDField: "scope_id", Spec: "scopeRef"}`.

**A spec field with no mapping at all is refused**, not dropped: `Ready=False,
Reason=Invalid` naming the field, and zero writes. That is the one case where a missing
entry could otherwise be silent.

### How the engine reads a spec it knows nothing about

Through its JSON form. `json.Marshal(obj)` gives the spec keyed by exactly the names
`Field.Spec` and `KeyField.Spec` use — the names a user writes in YAML — so a generated kind
needs no accessor, no reflection over Go types, and no code the generator has to emit. The
only per-kind code the engine needs is two one-line methods returning the embedded envelope
and status.

A value's shape then decides what can be done with it: a scalar is written and can carry a
filter, a list is written and cannot, and a `Ref` field is neither until
`internal/resolver` lands (NBO-012). Until then a declared reference is left out of the
payload and reported as `RefsResolved=False, Reason=NotImplemented`, which is the M1 contract
NBO-009 states. Which also means: a natural key that filters on a reference has no usable
candidate yet, so such an object waits rather than creating a duplicate. That is the correct
outcome, and it is the same code path that will handle a genuinely unresolved parent later.

## Drift decides the PATCH

`netbox.Drift` and `netbox.Changes` do the comparing — see [drift](drift.md) for the eight
shapes NetBox returns that naive comparison gets wrong. The engine's part is:

- The `FieldRules` handed to `Drift` are built from the Descriptor's `M2M`,
  `ObjectTypeLists`, `Arrays` and `GenericFKs`. A field class the Descriptor cannot express
  is a comparison that never converges, which is a PATCH loop rather than an error.
- The PATCH body and the `Updated` Event are built from the same change set, so they cannot
  disagree about which fields were sent. The Event renders `field: old → new`; a foreign key
  is shown as the id it points at and a choice field as its value, because an Event message
  is truncated and the interesting part must not be buried in a JSON dump.
- Empty drift logs at debug and writes nothing — not to NetBox, and not to the cluster.
- `status.lastAppliedHash` records a digest of the last payload NetBox accepted. It is
  deliberately **not** used to skip a PATCH: skipping on an unchanged desired payload would
  suppress exactly the NetBox-side drift correction that
  [ADR-0005](../decisions/0005-gitops-coexistence.md) exists to guarantee. A payload that
  NetBox canonicalises into a permanent diff is a bug in `Drift`, and it belongs where it is
  testable.

A kind whose identity lives somewhere a PATCH cannot reach declares
`UpdateStrategy: Recreate` with the identity-bearing fields in `RecreateOn`. A change to one
of those is a delete followed by a create; anything else on the same kind is still an
ordinary PATCH.

## DryRun invents nothing

A DryRun client returns the payload it would have sent, marked suppressed, and no id. The
engine recognises that and leaves `status.id` alone: `Ready=False, Reason=DryRunPending` and
`Synced=False, Reason=DriftDetectedDryRun`, with an Event describing the write that did not
happen. There are no synthetic negative ids — an id that does not exist server-side would be
written to `status.id` and then treated as real forever.

The recreate path is safe for the same reason: `status.id` is only ever taken from a create
response, so a suppressed delete followed by a suppressed create leaves the id of the object
that is still there.

## Failures are conditions, not errors

`Reconcile` returns an error for exactly two things: a kind with no registered descriptor,
and a failed status write. Everything else — every NetBox failure, every wait — is a
condition plus a chosen requeue. A returned error means controller-runtime backoff, and
backoff on a normal waiting state is minutes of latency for nothing.

The mapping from failure to condition and requeue lives in one function and classifies by
error **type**, never by message; see [errors and retries](errors-and-retries.md) for the
table itself and why NetBox's wording is not something to match on.

| Condition | True when | Reasons |
|---|---|---|
| `Ready` | the object exists in NetBox and matches the spec | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending` |
| `Synced` | the last write succeeded and nothing has drifted since | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun` |
| `RefsResolved` | every reference resolved to an id | `AllResolved`, `NotImplemented` |

Requeues carry ±10% jitter, so a manifest applied all at once does not resync in lockstep
for the rest of its life and turn one NetBox into the bottleneck.

## Wiring a kind

The engine's collaborators are consumer-defined interfaces, so it is testable with no NetBox
and no cluster: `Reader` and `Writer` (a NetBox client), `Endpoints` (a `spec.endpointRef`
to a client), `Descriptors` (per-kind facts), `StatusWriter` and `Recorder`. A kind's
controller supplies them and does nothing else — a controller containing business logic has
taken work that belongs to the engine.

Its CR embeds `NetBoxObjectSpec` and `NetBoxObjectStatus` and exposes them:

```go
func (t *NetBoxTag) NetBoxSpec() *NetBoxObjectSpec     { return &t.Spec.NetBoxObjectSpec }
func (t *NetBoxTag) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }
```

## Not here yet

Finalizers and deletion (NBO-007), reference resolution (NBO-012), deferred two-pass
patching (NBO-015) and inline children (NBO-032) are each their own ticket. None of them is
stubbed: an empty hook that returns "nothing to do" reads as implemented and is not.
