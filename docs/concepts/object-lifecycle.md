# Object lifecycle

> **Status: designed, not implemented.**
>
> Nothing on this page exists in the codebase. There is no `internal/reconciler` package
> with an engine in it, and no object Kind. It is the design for
> [NBO-006 — generic reconcile engine](https://github.com/ricardomolendijk/netbox-operator/issues/6),
> with the deletion half from
> [NBO-007 — finalizer and deletion policy](https://github.com/ricardomolendijk/netbox-operator/issues/7)
> and the deferred-field half from
> [NBO-015 — deferred fields](https://github.com/ricardomolendijk/netbox-operator/issues/27).
>
> Read it as intent. The one loop that does exist today is the endpoint controller,
> documented in [reconciliation](reconciliation.md). Where a design decision here has an
> already-built dependency — the typed errors, `Drift`, `NaturalKey` — that dependency is
> named and is real; the loop that consumes it is not.

## What is built and what is not

| Piece | Status |
|---|---|
| Typed NetBox errors and the retry policy (`internal/netbox/errors.go`, `do.go`) | Built |
| `Drift` / `Changes` / `Hash` (`internal/netbox/drift.go`) | Built |
| `NaturalKey`, `KeyField`, `NullField`, `Applicable` (`internal/registry/naturalkey.go`) | Built |
| `Descriptor` and the Kind registry (`internal/registry/registry.go`) | Built |
| `NetBoxEndpoint` and its controller (`internal/controller/`) | Built |
| The per-object reconcile loop | **Not built** — NBO-006 (#6) |
| Finalizer, `spec.deletionPolicy`, `PROTECT`-aware deletion (`internal/reconciler/finalizer.go`) | Built — NBO-007 (#7); documented at [deletion](deletion.md) |
| Waiting for owned child CRs before deleting | **Not built** — NBO-032 |
| The deferred-field second pass | **Not built** — NBO-015 (#27) |
| Any object Kind at all (`NetBoxTag`, `NetBoxSite`, …) | **Not built** — NBO-008 (#8) onward |

Everything below is the design of the four rows marked *not built*.

## The intended shape

One engine drives every Kind, parameterised by a `Descriptor` fetched from
`internal/registry`. Per-Kind controllers are wiring only. That constraint is already
enforced on the data side: nothing in a `Descriptor` is a function, because a closure
cannot be emitted by a template, printed in a diff, serialised or linted, so a descriptor
carrying one would put per-kind logic back into shared code through the back door
(`internal/registry/registry.go:1`–`:10`).

The same level-triggered discipline as the endpoint loop applies, for the same reasons —
see [the level-triggered model](reconciliation.md#the-level-triggered-model). It is
sharper here, because this loop *writes* to NetBox, so a step that is not safe to repeat
does not merely waste a call, it creates a duplicate object.

## The create / adopt / update decision

The design turns on one question asked in a fixed order: **does the live object already
exist, and how do we know?**

### Step 1 — `status.id`, if set

`GET /api/<endpoint>/<id>/`.

A 404 here is **not an error**. It means the object was deleted server-side behind the
operator's back — someone in the NetBox UI, another tool, a database restore. The design
says: clear `status.id` and fall through to step 2. The client already supports this
precisely, by attaching the id to the error so the caller can tell an id-lookup 404 from
any other (`internal/netbox/client.go:211`–`:223`, `internal/netbox/errors.go:53`–`:65`).

Treating it as an error instead would leave the CR permanently pointing at a row that does
not exist, retrying a `GET` that cannot start succeeding. The recovery has to be a normal
path, not an exception, because it is a normal thing for a human to do.

### Step 2 — natural-key lookup

`GET /api/<endpoint>/?<filters>`, using the first applicable candidate from
`Descriptor.NaturalKeys`.

| Matches | Decision |
|---|---|
| 0 | **Create.** POST, then record `status.id`. |
| 1 | **Adopt or update.** Same object either way; whether it counts as an adoption depends on whether `status.id` was already set. |
| >1 | **Conflict.** Write nothing. |

### Step 3 — the write

Create issues a POST and records `status.id`, `status.url` and the natural key that was
actually used. Adoption honours `spec.onConflict`:

| `spec.onConflict` | Behaviour |
|---|---|
| `Adopt` (default) | Take the existing object over and reconcile it. |
| `AdoptOnly` | Take it over, but never create one. For objects a human owns, where the operator should correct drift but must not bring new ones into being. |
| `Fail` | Report a `Conflict` condition and stop. |

Update computes `Drift(live, desired, rules)` and PATCHes exactly the difference, or
writes nothing when the difference is empty. `Drift` is built and documented — see
[drift detection](drift.md); this page does not restate its comparison rules.

The zero-write case is the one to protect. A converged object must issue **no** API calls
on a resync. That is what makes a 10-minute resync across thousands of objects affordable,
and it is the property that a wrong normalisation in `Drift` destroys silently rather than
loudly.

## Natural-key lookup order, and what a multi-match means

`Descriptor.NaturalKeys` is a **priority list, and more than one entry is the normal case,
not a fallback**. `Descriptor.Candidates(state)` filters it to the applicable candidates
in the declared order (`internal/registry/registry.go:220`–`:230`).

`NaturalKey.Applicable` (`internal/registry/naturalkey.go:137`) has two halves and both
are load-bearing:

- A candidate that **matches** on a field requires that field to be **resolved** — to hold
  a value the client can filter on right now. An optional key the user left unset makes
  that candidate inapplicable, which is how `ipam.VRF` falls from `(rd)` to `(name)`.
- A candidate that **pins a field to null** requires that field to be **undeclared**. A
  `dcim.Region` whose `parent` is declared but not yet created must not fall through to
  `name WHERE parent IS NULL`.

That second rule is the subtle one, and the reason is identity corruption rather than a
missed lookup. `dcim.Region` is unique on `(parent, name)` *and* separately on `(name)`
with the condition `parent IS NULL` (`docs/netbox-schema.md` →
`dcim.Region.meta.constraints`). A child Region that fell through to the null-pinned
candidate would match an unrelated top-level Region of the same name, adopt it, and the
follow-up PATCH would reparent somebody else's data. When no candidate is applicable, the
engine waits. Waiting is correct; creating would mint a wrong identity.

Lookups may carry a modifier. `LookupIExact` emits `?name__ie=` because `dcim.Device` and
`virtualization.VirtualMachine` are unique on `Lower('name')`
(`docs/netbox-schema.md` → `dcim.Device.meta.constraints`,
`virtualization.VirtualMachine.meta.constraints`), so an exact lookup does not find `DNS`
for a CR named `dns` and the engine would go on to create a second object. Substring,
prefix and negation lookups are deliberately absent from `knownLookups`
(`internal/registry/naturalkey.go:49`): a natural key has to identify at most one object,
and those cannot.

### Why >1 match stops instead of picking

More than one match is `*netbox.AmbiguousError` and a `Conflict` condition, and the engine
writes nothing.

This is a real case rather than a defensive one. `ipam.Prefix` and `ipam.IPAddress` have
**no** `meta.constraints` at all, and `ipam.VRF.name` is not unique — only `rd` carries a
column-level unique index (`docs/netbox-schema.md` → `ipam.Prefix`, `ipam.IPAddress`,
`ipam.VRF`). So a lookup that is supposed to identify one object can legitimately return
several, and "take the first result" silently adopts an unrelated one. For a VRF that is
not cosmetic: every prefix and address keyed on that VRF gets reparented on the next
reconcile. The tool this operator replaces does exactly that. The reasoning is written
down at `internal/netbox/errors.go:105`–`:120` and in
[errors and retries](errors-and-retries.md#why-ambiguity-is-an-error).

## The deferred-field second pass

Some NetBox references **cannot exist at create time**, by construction rather than by
ordering. A `dcim.Device`'s `primary_ip4` is a `OneToOneField` to an `ipam.IPAddress`
(`docs/netbox-schema.md` → `dcim.Device`); that address needs a `dcim.Interface` to be
assigned to; that interface needs the Device. There is no apply order that resolves it,
so the design creates the object first and PATCHes the field afterwards.

The engine is meant to distinguish that from the case that only *looks* circular — a
reference whose target has not been reconciled yet, where waiting is correct and
patching later is wrong. Hence two modes, already declared as data in the registry
(`internal/registry/registry.go:109`–`:120`):

| Mode | When it applies | Example |
|---|---|---|
| `DeferAlways` | The reference cannot exist at create time at all. Always stripped from the POST, always applied by a second PATCH. | `primary_ip4`, `primary_ip6`, `oob_ip`, `virtual_chassis`, `nat_inside` |
| `DeferIfUnresolved` | Included in the POST when it resolves; deferred only when it does not. | `parent` on every nested-group Kind, `lag`, `qinq_svlan` |

Deferring a `parent` unconditionally would create the object as top-level, where it can
adopt an unrelated top-level object of the same name — the `dcim.Region` failure described
above. Two modes exist because one mode is right for `primary_ip4` and wrong for `parent`.

The registry already enforces the guard that makes this safe: a `DeferAlways` field that
appears by value in **any** natural-key candidate is rejected by
`Descriptor.Validate()`, which runs at manager start so a bad descriptor fails the boot
rather than one reconcile (`internal/registry/registry.go:300`–`:344`, sentinel
`ErrDeferredNaturalKey`). Null pins are exempt, because such a candidate asserts the field
is unset, which is exactly the state a create with the field deferred is in.

Three further design commitments, none implemented:

- **The intermediate state is honest.** Between the two passes the object reports
  `Ready=False, Reason=DeferredFieldPending`, with `status.deferredPending` listing the
  spec fields still waiting. It is a legitimate, potentially long-lived state, not a
  transient blip, so it gets a field rather than only a condition message.
- **A pending field is excluded from the diff, not compared against absent.** Otherwise
  every reconcile of a pending object shows drift and PATCHes nothing — the hot-loop shape
  that [drift detection](drift.md) exists to prevent.
- **Exactly two writes per converged object, ever.** One POST and one PATCH. A third
  write means either the differ is comparing a pending field or the second pass is
  re-sending an already-applied one.

A deferred field whose reference never resolves leaves the object `Ready=False` forever,
on purpose. `kubectl wait --for=condition=Ready` failing is the correct outcome; the
operator must not claim success for an object missing a field the user asked for.

## Finalizers and deletion

> **Built at NBO-007 (#7)**, except where noted. Full page:
> [deletion](deletion.md). This section is the summary and the one row still outstanding.

The claim being tested was that **`PROTECT` plus backoff is the topological sort** — that
the operator needs no hand-maintained deletion-ordering table, because NetBox declares its
foreign keys with `on_delete=PROTECT` almost everywhere (NetBox source:
`netbox/ipam/models/ip.py`, e.g. `Prefix.vrf`, `IPAddress.vrf`) and will refuse a delete
whose dependents are still present. It holds: there is no ordering table in the codebase.

The finalizer is `netbox.populator.io/finalizer`, added on first reconcile and **persisted
by its own API write before any create**. Order matters in both directions: a finalizer
added after the POST leaves a window in which the process can die between creating a NetBox
object and recording that it must be cleaned up, and a finalizer removed before the DELETE
succeeds leaves nothing to retry it. Both windows orphan the object.

The sequence on a CR with a deletion timestamp, as implemented in
`internal/reconciler/finalizer.go`:

| Step | Condition | Action | |
|---|---|---|---|
| 1 | `netbox.populator.io/skip-finalizer=true` | Drop the finalizer, `FinalizerSkipped` Event, leave NetBox alone. | Built |
| 2 | `spec.deletionPolicy: Retain` | Drop the finalizer, `Retained` Event, leave NetBox alone. | Built |
| 3 | `status.id == 0` | Nothing the operator can prove it owns. Drop the finalizer, `NothingToDelete` Event. | Built |
| 4 | The endpoint is not `Ready` | `Deleting=False, Reason=WaitingForEndpoint`. Keep the finalizer. | Built |
| 5 | Owned child CRs still present | `Deleting=False, Reason=PendingDependents`. Wait for Kubernetes GC, which deletes them first because they carry an owner reference. | **Not built** — NBO-032, which is what creates child CRs in the first place |
| 6 | Otherwise | `DELETE /api/<endpoint>/<id>/`. `204` or `404` → drop the finalizer. `409`, or a body naming a protected relation → `Deleting=False, Reason=Protected` with the server's message verbatim, requeue with capped exponential backoff. | Built |

Steps 1–3 are answered before the endpoint is resolved, deliberately: an escape hatch that
only works when NetBox is reachable is not an escape hatch. Step 4 is the judgement call —
blocking is reversible and an orphan is not, so the finalizer stays on and the condition
names the annotation that overrides it. Step 5 is the only row this ticket did not build,
and it is not a stub: there is nothing that materialises a child CR yet, so there is nothing
to wait for.

`spec.deletionPolicy` is `Delete | Retain`, matching
[ADR-0003](../decisions/0003-ownership-and-references.md#deletion-policy). `Retain` is for
migrating off the operator, or for objects shared with something else. It is read fresh on
every pass rather than latched when deletion starts, which makes switching to `Retain` the
gentle way out of a delete NetBox keeps refusing.

Four points worth stating explicitly, all of them now in the code:

**Never force.** No cascade parameter, no dependent-hunting, no ordering table. The
dependent's own deletion unblocks the parent, and the retry finds it unblocked. The
server's opinion about what still references what is more reliable than a list the
operator would have to keep in sync with 159 models.

**The `Protected` message names the blocker.** `*netbox.ProtectedError` preserves NetBox's
body (`internal/netbox/errors.go:70`–`:77`), and detecting a Django `ProtectedError` that
arrived with a non-409 status is confined to one function that matches on wording,
deliberately (`internal/netbox/errors.go:158`–`:163`). "Cannot delete" without a reason is
the worst possible operator experience.

**Backoff is capped at five minutes**, doubling from ten seconds, counted in
`status.deletionAttempts` because a controller has no memory between passes. After three
refusals it surfaces once as a `DeleteBlocked` Event, so a stuck delete is visible rather
than silent and rather than repeated.

**There is a break-glass.** A finalizer added but never removed makes a namespace
undeletable forever. The annotation `netbox.populator.io/skip-finalizer=true` drops the
finalizer without touching NetBox, and overrides every other step. It is documented as
break-glass because that is what it is: it guarantees an orphan in NetBox, and that is
sometimes the right trade.

## The status envelope

Designed, not implemented. The intent is that every object Kind shares one status shape
so that tooling and `kubectl` behave identically across ~120 kinds: `status.id`,
`status.url`, `status.naturalKey` (the key actually used, which is what makes an adoption
debuggable), `status.resolvedRefs`, `status.adopted`, `status.lastSyncTime`,
`status.lastAppliedHash`, `status.observedGeneration`, `status.conditions`.

Two rules carry over from the endpoint loop, and both are already settled:

- **All status writes go through one helper that always sets `observedGeneration`.**
  Forgetting it makes `kubectl wait` lie — see
  [condition conventions](reconciliation.md#condition-conventions).
- **`status.id` is set only once the object provably exists server-side.** In particular
  `mode: DryRun` invents nothing: `Client.Create` returns the payload marked suppressed
  rather than a synthetic id (`internal/netbox/client.go:225`–`:236`). A reference to a
  dry-run object reports unresolved, which is more honest than a fake id that a later
  reconcile would treat as real.

`status.lastAppliedHash` uses `netbox.Hash` (built, `internal/netbox/drift.go:261`) as a
cheap short-circuit: NetBox canonicalises some values on write, so comparing a hash of the
last *applied* payload avoids having to teach `Drift` every canonicalisation NetBox
performs — a list nobody can be sure is complete.

## Where the spec and the built code already disagree

`plan.md` §6.1 calls the field `spec.reclaimPolicy`. NBO-007 renames it to
`spec.deletionPolicy`, matching
[ADR-0003](../decisions/0003-ownership-and-references.md#deletion-policy), on the grounds
that `reclaimPolicy` is PersistentVolume vocabulary where it means something materially
different. `deletionPolicy` is the field that exists, built at NBO-007.

`plan.md` §6.2 sketches `Descriptor.NaturalKeys` as `[]func(o Object) map[string]string`
and `Deferred` as `[]string`. The built registry uses neither: natural keys are the data
type `NaturalKey` with `KeyField` / `NullField` / `Lookup`, and `Deferred` is
`[]DeferredField` with a mode. Both changes are deliberate and both are in the built code,
so the built code wins.

## Proposal: generate the descriptors, not the types

> **Status: proposal.** This argues for a narrower scope than
> [NBO-041](https://github.com/ricardomolendijk/netbox-operator/issues/65) and
> [NBO-042](https://github.com/ricardomolendijk/netbox-operator/issues/66) currently
> specify. Neither ticket is implemented, so nothing here contradicts shipped code.

Those tickets propose ingesting the OpenAPI schema and `models.json` into one IR, then
emitting types, registry entries and controllers from it. The scope is worth
reconsidering, because the usual justification for code generation does not apply here and
a different one does.

**The weak argument: keeping up with NetBox releases.** The operator is pinned to a single
extracted schema version, and the supported range is compiled in as `[4.2.0, 5.0.0)`
(`internal/netbox/version.go:18`–`:21`). NetBox minor releases add columns; they rarely
move the ones the operator writes. Building an emitter toolchain to track that is a lot of
machinery aimed at a slow-moving target.

**The strong argument: the initial fan-out.** Roughly 120 kinds is the real cost, and it
is paid once, up front. Hand-transcribing 120 `Descriptor`s means hand-transcribing 120
endpoint paths, object-type strings, natural-key sets and read-only column lists — and
every one of those mistakes fails *silently* rather than loudly. A wrong endpoint path is a
404 on first use, which is at least visible. A wrong natural key adopts an unrelated
object, and a missing `ReadOnly` entry writes a cached column that NetBox ignores, which is
a PATCH loop for the lifetime of the object. Those are the failure modes this codebase has
already been bitten by, and they scale with kind count.

**NetBox does change in the way that matters, though rarely.** The 4.2 replacement of
`site` with `(scope_type, scope_id)` plus a read-only `_site` cache
(`docs/netbox-schema.md` → `dcim.CachedScopeMixin`) happened *inside* the supported range,
and it no-ops rather than erroring. So "upstream barely changes" is true on average and
dangerous in the tail.

### The narrower shape

1. **Generate only the `Descriptor` data.** Endpoint path and object type from the
   endpoint map; `NaturalKeys` from `meta.constraints`; `ReadOnly` from the `_`-prefixed
   columns and every `CounterCacheField`; `M2M` and `GenericFKs` from the field kinds. All
   of it is already in `docs/netbox-schema.md`, produced by the extractor that exists
   (`hack/extract-netbox-schema.py`). This is the mechanical, high-volume, silently-wrong
   part.

   `Descriptor` is already shaped for this. Nothing in it is a func, precisely so that a
   template can emit it and a diff can show it (`internal/registry/registry.go:1`–`:10`).
   The type was designed for a generator; it does not need an IR to feed it.

2. **Keep CRD types and controllers hand-written.** That is where the judgement lives —
   which fields to expose, what to call them, which references are `ObjectRef`s, what the
   printer columns should be. Generating them buys little and costs an override mechanism
   for every deviation, which is where generator projects go to die.

3. **Make upstream drift a test, not a generator input.** Re-run the extractor in CI and
   diff against the committed `docs/netbox-schema.md`. A NetBox change that matters then
   arrives as a failing golden diff that a human reads and acts on, rather than as a
   silent regeneration. This is close to what
   [NBO-043](https://github.com/ricardomolendijk/netbox-operator/issues/67) already
   reaches for, and it is the part that would have caught the 4.2 scope change.

### What this trades away

Generated CRD types would guarantee that a field present in NetBox is exposed in the CRD.
Under this proposal, adding a field to an existing kind stays a manual edit, and a field
nobody notices stays unexposed until someone asks for it. That is an acceptable trade: an
unexposed field is a feature request, whereas a wrong natural key is data corruption, and
only the second one is what generation is being bought for.

It also means the coverage audit
([NBO-060](https://github.com/ricardomolendijk/netbox-operator/issues/61)) carries more
weight, since it becomes the mechanism that finds unexposed fields rather than the
generator.
