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
   Resolve every declared reference        any unresolved -> WaitingForRef, zero writes
                                           (except a deferred field, which proceeds)
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
- **No applicable candidate at all means wait, and write nothing.** A candidate that omits a
  declared-but-unresolved `parentRef` must *not* be fallen through to: `name WHERE parent IS
  NULL` finds an unrelated top-level Region, and the follow-up PATCH reparents somebody else's
  data. `Ready=False, Reason=WaitingForKey`, zero requests, and the next resync tries again.

  Since [#195](https://github.com/ricardomolendijk/netbox-operator/issues/195) the lookup is
  not reached at all in that particular case -- a declared reference that did not resolve
  withholds the write before the lookup, and reports `WaitingForRef`. This guard stands behind
  it, for a candidate made inapplicable by something that is not a reference.

The lookup that actually ran is recorded in `status.naturalKey`, including when it matched
nothing -- the first question about an object that was not adopted is what the engine looked
for.

**More than one match is a `Conflict` that names every matching NetBox id, and zero
writes.** Not first-match adoption: `ipam.Prefix` and `ipam.IPAddress` have no
`meta.constraints` at all, so several matches is a routine state rather than a corrupt
database, and picking one silently reparents whatever else keys on it.

The engine's `Reader` asks for one object rather than listing and counting: `GetOne` returns
a [`netbox.AmbiguousError`](errors-and-retries.md#why-ambiguity-is-an-error) carrying the id
and the `display` of every match, and that error *is* the `Conflict` message, verbatim.
Counting here as well would be a second decision about when a lookup is ambiguous, and the
only thing a second one can do is disagree with the first.

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
`Deferred`, `ReadOnly` and `RecreateOn` are **NetBox API** names (`vrf`, `primary_ip4`).
Something has to bridge them, and that something is `Descriptor.Fields`: an explicit
`{Spec, API, Class}` table per kind.

```go
Fields: []registry.Field{
    {Spec: "name", API: "name"},
    {Spec: "objectTypes", API: "object_types", Class: registry.ClassObjectTypeList},
    {Spec: "siteRef", API: "site", Class: registry.ClassRefOne},
    {Spec: "primaryIP4Ref", API: "primary_ip4", Class: registry.ClassRefOne},
    {Spec: "importTargets", API: "import_targets", Class: registry.ClassRefMany},
},
```

`Class` is what the field *is* — a value, one reference, a list of references, a list of
content types, or an ordered array — and it is the only declaration of both the field's
cardinality and how its value is compared. The comparison sets `Drift` needs are derived
from it; see [field classes](descriptor.md#field-classes).

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
filter, a list is written and cannot, and a `Ref` field is neither until `internal/resolver`
has turned it into an id (NBO-012). One resolution per pass, and the id it yields goes both
into the payload and into the spec the natural key filters on — so a key that matches on a
reference becomes usable exactly when the reference resolves.

**A reference the spec declares is a precondition for the write.** Declared and unresolved
means zero NetBox requests, on every kind and whether or not the reference is part of the
identity, with `RefsResolved=False` carrying which of the eight causes it was and
`Ready=False, Reason=WaitingForRef`. A reference the spec does *not* declare is not a
precondition, and the object is created immediately without it. The one exception is a
[deferred field](object-lifecycle.md), which exists precisely because its reference cannot
resolve until the object does. See
[the rule](reconciliation.md#a-declared-reference-is-a-precondition-for-the-write) and
[references](references.md).

## Drift decides the PATCH

`netbox.Drift` and `netbox.Changes` do the comparing — see [drift](drift.md) for the eight
shapes NetBox returns that naive comparison gets wrong. The engine's part is:

- The `FieldRules` handed to `Drift` are derived from the classes on `Descriptor.Fields`
  (`M2MFields()`, `ObjectTypeListFields()`, `ArrayFields()`) plus `GenericFKs`. A field class
  the Descriptor cannot express is a comparison that never converges, which is a PATCH loop
  rather than an error.
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

`Reconcile` returns an error for exactly three things: a kind with no registered descriptor,
a status write the API server refused for anything other than [losing a
race](errors-and-retries.md#a-cached-read-is-not-a-conflict), and a live status read it would
not answer. Everything else — every NetBox failure, every wait — is a condition plus a chosen
requeue. A returned error means controller-runtime backoff, and backoff on a normal waiting
state is minutes of latency for nothing.

The mapping from failure to condition and requeue lives in one function and classifies by
error **type**, never by message; see [errors and retries](errors-and-retries.md) for the
table itself and why NetBox's wording is not something to match on.

| Condition | True when | Reasons |
|---|---|---|
| `Ready` | the object exists in NetBox and matches the spec | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `Truncated`, `APIError`, `DryRunPending` |
| `Synced` | the last write succeeded and nothing has drifted since | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun` |
| `RefsResolved` | every reference resolved to an id | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded`, `RefKindUnavailable`, `NotImplemented` |

Requeues carry ±10% jitter, so a manifest applied all at once does not resync in lockstep
for the rest of its life and turn one NetBox into the bottleneck.

## Wiring a kind

The engine's collaborators are consumer-defined interfaces, so it is testable with no NetBox
and no cluster: `Reader` and `Writer` (a NetBox client), `Endpoints` (a `spec.endpointRef`
to a client), `Descriptors` (per-kind facts), `RefResolver` (a reference to an id),
`StatusWriter`, `StatusReader` (this object's own status, read past the cache — see
[a cached read is not a conflict](errors-and-retries.md#a-cached-read-is-not-a-conflict))
and `Recorder`. A kind's
controller supplies them and does nothing else — a controller containing business logic has
taken work that belongs to the engine.

**Every collaborator that can touch the outside world takes the reconcile's `ctx` first.**
`Endpoints.Endpoint` included, even though the adapter behind it answers from two in-memory
caches and cannot block today: "cannot block" is a property of one implementation, not of a
signature, and a signature that cannot be cancelled makes a blocking read unnoticeable. An
object controller runs a single worker by default, so one uninterruptible read stalls every
object of its kind, and manager shutdown, a leader-election handover and a per-reconcile
deadline all arrive as a cancelled context and as nothing else. It is also what carries the
request-scoped logger, without which an adapter cannot report anything it decides to
tolerate (`internal/controller/endpointprovider.go`).

`Endpoints.Endpoint` returns `(Endpoint, bool)` rather than `(Endpoint, error)` deliberately.
The engine has exactly two responses available — use the endpoint, or wait for it — so a
third return it could not act on differently would widen the seam for no behaviour. The
adapter does distinguish the two states it can be in: no cached client is a miss and becomes
`Ready=False/WaitingForEndpoint`, while a client it holds but a CR it could not read is *not*
a miss — the client is proof the endpoint reconciled, and only the resync period and drift
mode are missing, both of which have defaults matching the CRD's and neither of which can
authorise a write, since `DryRun` and `driftMode: Report` are enforced by the client's own
mode. That failure is reported in the log, at debug, rather than in the return type.

Its CR embeds `NetBoxObjectSpec` and `NetBoxObjectStatus` and exposes them:

```go
func (t *NetBoxTag) NetBoxSpec() *NetBoxObjectSpec     { return &t.Spec.NetBoxObjectSpec }
func (t *NetBoxTag) NetBoxStatus() *NetBoxObjectStatus { return &t.Status }
```

## Not here yet

Deferred two-pass patching (NBO-015) and inline children (NBO-032) are each their own
ticket. Neither is stubbed: an empty hook that returns "nothing to do" reads as implemented
and is not.
