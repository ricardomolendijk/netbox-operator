# Object lifecycle: what is still designed

Most of the per-object loop is built and documented elsewhere. This page is the register of
what is **not** built yet, so that intent is written down without being mistaken for
behaviour.

Every section below carries its own status marker. A page-level banner was tried and
removed: it went stale the moment one of the tickets merged, and a stale "not implemented"
label on shipped code is worse than no label at all.

## Where the built behaviour is documented

| Topic | Status | Read |
|---|---|---|
| Create / adopt / update, natural-key location, drift, DryRun | Built (NBO-006) | [the reconcile engine](engine.md) |
| Per-kind facts, deferral declaration, the identity guard | Built (NBO-005) | [the Descriptor](descriptor.md) |
| Lookup modifiers, null pins | Built (NBO-005, NBO-069) | [lookups](lookups.md) |
| Finalizer, deletion policy, `PROTECT`-aware retry | Built (NBO-007) | [deletion](deletion.md) |
| Field comparison and normalisation | Built (NBO-003) | [drift detection](drift.md) |
| Typed errors and retry policy | Built (NBO-002) | [errors and retries](errors-and-retries.md) |
| Endpoint reconcile loop, conditions, requeue policy | Built (NBO-004) | [reconciliation](reconciliation.md) |
| The shared `status` envelope | Built (NBO-006) | `api/v1alpha1/netboxobject_types.go` |
| The deferred-field second pass | Built (NBO-015) | below |
| Reference resolution: four modes, typed errors | Built (NBO-012) | [references](references.md) |
| Inline child materialisation | Built (NBO-032, NBO-034, NBO-033) | [inline children](inline-children.md) |
| **`NetBoxDevice`'s nine other component lists** | **Designed, not implemented** | NBO-052, NBO-053 |

Nothing pending is stubbed. There is no hook that returns "nothing to do", because an empty
hook reads as implemented and is not.

**The inline row is precise about what shipped.** The materialiser is built, and two Kinds use
it: `NetBoxDevice`'s `interfaces` and their `addresses` (NBO-034), and
`NetBoxVirtualMachine`'s `interfaces`, their `addresses` and its `disks` (NBO-033). A VM's
inline address additionally reaches the VM's own `primary_ip4` through the deferred field
below, which is the one case where the sugar flows back up into its parent's payload.

What is not built is the rest of a device's components: `consolePorts`, `powerPorts`,
`frontPorts`, `rearPorts`, `deviceBays`, `moduleBays` and `inventoryItems` have no Kind to
materialise yet, so declaring the fields would accept input the operator cannot honour. Each
arrives as one more `InlineChildSet` from the same method. No other Kind carries an inline list.

## The deferred-field second pass

> **Status: built (NBO-015).** [#27](https://github.com/ricardomolendijk/netbox-operator/issues/27).
> The *declaration* side landed with the Descriptor — see
> [the Descriptor](descriptor.md#deferral-and-the-identity-guard) for `Deferred`, the two
> modes, and the `ErrDeferredNaturalKey` guard that runs at manager start. This is the engine
> behaviour that consumes it.

Some NetBox references cannot exist at create time by construction rather than by ordering.
A `dcim.Device`'s `primary_ip4` is a `OneToOneField` to an `ipam.IPAddress`
(`docs/netbox-schema.md` → `dcim.Device`); that address needs a `dcim.Interface` to be
assigned to; that interface needs the Device. The cycle is in NetBox's own model, so no apply
order resolves it: the engine creates the object without the field and PATCHes it afterwards.

### The two passes

| Pass | Payload |
|---|---|
| 1 | The create, with `DeferAlways` columns stripped. `status.id` is recorded as normal — the object provably exists. |
| 2 | An ordinary drift PATCH. NetBox lacks a column the spec declares, so the differ finds exactly that column and sends exactly that column. |

The second pass is the differ rather than a separate deferred writer, and that is the whole
of the design. The strip is applied to a **copy** of the payload, used only as the POST body;
the desired state that every later pass diffs the live object against keeps the field. So one
pass after the create, "NetBox does not hold a value the spec asks for" is a true statement,
and correcting it is what the engine already does for every other field.

The alternatives are both broken, and in opposite directions:

- **Strip the request and keep the field in the diff, without applying it.** The diff is then
  satisfied only by a PATCH the request never carries, so it re-fires on every pass: a write
  per resync for the lifetime of the object. That is the hot loop
  [drift detection](drift.md) opens by warning about.
- **Strip the desired state too.** The field is then never written and never compared — the
  silent omission [issue #132](https://github.com/ricardomolendijk/netbox-operator/issues/132)
  exists to make impossible.

Between the two passes the object reports `Ready=False, Reason=DeferredFieldPending`, and
`status.deferredPending` lists the **CR spec fields** still waiting. The requeue after the
create is five seconds rather than the endpoint's resync: nothing failed, the id is in hand,
and making `primary_ip4` land ten minutes after the machine it names would be a latency bug
dressed as a retry policy.

Five seconds is right exactly once, though, and every later pending pass falls back to the
resync. That fallback is a guard rather than a default: a PATCH NetBox accepts and silently
ignores — which is what it does with a read-only column a descriptor failed to declare
(`docs/netbox-schema.md`, preamble) — leaves the field pending forever, and a five-second
interval would turn that from a slow PATCH loop into a fast one.

`status.deferredPending` is a status field rather than only a condition message because the
state is legitimate and can be permanent, so "what is this object still waiting to write" has
to be answerable from `kubectl get -o yaml` and greppable across a namespace:

```console
$ kubectl get netboxvirtualmachine web-01 -o jsonpath='{.status.deferredPending}'
["primaryIP4Ref"]
```

### The two modes are not cosmetic

`DeferAlways` is stripped from the create whenever it resolves. `DeferIfUnresolved` never is:
an unresolved reference is already left out of the payload, so "defer only when it does not
resolve" is satisfied by doing nothing, and "include it when it does" by not stripping it.

That difference is why only `DeferIfUnresolved` may name a field a natural key matches on,
enforced at boot by `ErrDeferredNaturalKey`. Stripping a resolved `parent` from an MPTT
create would change the object's identity from `(parent, name)` to `(name)` — so the lookup
that decided to create would have asked a different question from the create it decided on,
and the follow-up PATCH would reparent whatever a name-only lookup adopted. The guard is
exercised against the registered descriptors, not against a fixture
(`internal/registry/deferred_test.go`).

### Three commitments that keep it honest

**Exactly two writes per converged object.** One POST and one PATCH, and then nothing however
many times the object is reconciled. A third write means either the differ is comparing a
field the create never sent or the second pass is re-sending a value NetBox already holds;
both are regressions, and `TestDeferAlwaysIsStrippedThenPatched` reconciles ten more times
and counts.

**A pending field is decided by the differ, not by a second opinion.** Whether a deferral has
landed is answered with `netbox.Drift` against the live object, the same comparison the PATCH
is built from. A reference reads back as a nested object and is written as an id, so an
independent equality check would either report an applied field as pending forever or a
pending one as done.

**An unresolvable deferred field leaves the object `Ready=False` forever, on purpose.**
`kubectl wait --for=condition=Ready` failing is the correct outcome; the operator must not
claim success for an object missing a field the user asked for. It waits at the resolver's own
interval rather than spinning, and `status.deferredPending` keeps naming the field.

### Which reason, and when

| State | `Ready` reason | `status.deferredPending` |
|---|---|---|
| Reference has not resolved | `WaitingForRef` | lists the field |
| Resolved, stripped from the create, PATCH still to come | `DeferredFieldPending` | lists the field |
| Applied | `Synced` | empty |
| Endpoint in `DryRun`, or `driftMode: Report` | `DryRunPending` / `ReportPending` | lists the field |

`WaitingForRef` wins over `DeferredFieldPending` because it is the more specific answer to
"why": the engine has nothing to write, rather than something it has not sent yet, and the two
are fixed in different places. Both populate `deferredPending`, so the field list is there
either way. Under `DryRun` neither pass runs and the list is still populated, since it is
computed from what the spec asks for and NetBox lacks rather than from what was written.

Once applied, a deferred field is an ordinary managed field: it is in the desired payload, and
drift correction fixes a UI change to it like any other.

### What is deliberately not promised

The second pass is the ordinary differ, so on an object that was created and then edited in
the same window the PATCH carries the deferred column *and* whatever else drifted. An earlier
draft of this page promised a deferred-only PATCH; that would mean two writes where one
suffices, and a second place where the engine decides what to send. One differ, one decision.

No shipped kind declares a deferral yet — `primary_ip4` arrives with
`virtualization.VirtualMachine` and `dcim.Device`. The engine's behaviour is proven against
the fake kind, which is where engine behaviour is always proven
(`internal/reconciler/deferred_test.go`); the identity guard is proven against the real
descriptors.


## Proposal: generate the descriptors, not the types

> **Status: proposal.** This argues for a narrower scope than [NBO-041
> (#65)](https://github.com/ricardomolendijk/netbox-operator/issues/65) and [NBO-042
> (#66)](https://github.com/ricardomolendijk/netbox-operator/issues/66) currently specify.
> Neither ticket is implemented, so nothing here contradicts shipped code.

Those tickets propose ingesting the OpenAPI schema and `models.json` into one IR, then
emitting types, registry entries and controllers from it. The scope is worth reconsidering,
because the usual justification for code generation does not apply here and a different one
does.

**The weak argument: keeping up with NetBox releases.** The operator is pinned to a single
extracted schema version, and the supported range is compiled in as `[4.2.0, 5.0.0)`
(`internal/netbox/version.go`). NetBox minor releases add columns; they rarely move the ones
the operator writes. Building an emitter toolchain to track that is a lot of machinery aimed
at a slow-moving target.

**The strong argument: the initial fan-out.** Roughly 120 kinds is the real cost, and it is
paid once, up front. Hand-transcribing 120 `Descriptor`s means hand-transcribing 120
endpoint paths, object-type strings, natural-key sets and read-only column lists — and every
one of those mistakes fails *silently*. A wrong endpoint path is a 404 on first use, which
is at least visible. A wrong natural key adopts an unrelated object. A missing `ReadOnly`
entry writes a cached column that NetBox ignores, which is a PATCH loop for the lifetime of
the object. Those failure modes scale with kind count.

**NetBox does change in the way that matters, though rarely.** The 4.2 replacement of `site`
with `(scope_type, scope_id)` plus a read-only `_site` cache (`docs/netbox-schema.md` →
`dcim.CachedScopeMixin`) happened *inside* the supported range, and it no-ops rather than
erroring. So "upstream barely changes" is true on average and dangerous in the tail.

### The narrower shape

1. **Generate only the `Descriptor` data.** Endpoint path and object type from the endpoint
   map; `NaturalKeys` from `meta.constraints`; `ReadOnly` from the `_`-prefixed columns and
   every `CounterCacheField`; each field's `Class` (which is what `M2MFields()`,
   `ObjectTypeListFields()` and `ArrayFields()` derive from) and `GenericFKs` from the field
   kinds. All of it is
   already in `docs/netbox-schema.md`, produced by the extractor that exists
   (`hack/extract-netbox-schema.py`). This is the mechanical, high-volume,
   silently-wrong part.

   `Descriptor` is already shaped for this. Nothing in it is a func, precisely so a template
   can emit it and a diff can show it — see [the Descriptor](descriptor.md#why-the-per-kind-facts-are-data).
   The type was designed for a generator; it does not need an IR to feed it.

2. **Keep CRD types and controllers hand-written.** That is where the judgement lives: which
   fields to expose, what to call them, which references are `ObjectRef`s, what the printer
   columns should be. Generating them buys little and costs an override mechanism for every
   deviation, which is where generator projects go to die.

3. **Make upstream drift a test, not a generator input.** Re-run the extractor in CI and
   diff against the committed `docs/netbox-schema.md`. A NetBox change that matters then
   arrives as a failing golden diff a human reads and acts on, rather than as a silent
   regeneration. This is close to what [NBO-043
   (#67)](https://github.com/ricardomolendijk/netbox-operator/issues/67) already reaches
   for, and it is the part that would have caught the 4.2 scope change.

### What this trades away

Generated CRD types would guarantee that a field present in NetBox is exposed in the CRD.
Under this proposal, adding a field to an existing kind stays a manual edit, and a field
nobody notices stays unexposed until someone asks for it. That is an acceptable trade: an
unexposed field is a feature request, whereas a wrong natural key is data corruption, and
only the second is what generation is being bought for.

It also means the coverage audit ([NBO-060
(#61)](https://github.com/ricardomolendijk/netbox-operator/issues/61)) carries more weight,
since it becomes the mechanism that finds unexposed fields rather than the generator.

## A naming note that outlived its ticket

`plan.md` §6.1 calls the field `spec.reclaimPolicy`. The built field is
`spec.deletionPolicy`, matching
[ADR-0003](../decisions/0003-ownership-and-references.md#deletion-policy), because
`reclaimPolicy` is PersistentVolume vocabulary where it means something materially
different. `deletionPolicy` is the name to expect; see [deletion](deletion.md).
