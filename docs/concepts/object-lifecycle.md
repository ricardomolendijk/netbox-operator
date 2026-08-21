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
| **The deferred-field second pass** | **Designed, not implemented** | below |
| **Reference resolution** | **Designed, not implemented** | NBO-012 (#24) |
| **Inline child materialisation** | **Designed, not implemented** | NBO-032 (#45) |

Nothing pending is stubbed. There is no hook that returns "nothing to do", because an empty
hook reads as implemented and is not.

## The deferred-field second pass

> **Status: designed, not implemented.** [NBO-015
> (#27)](https://github.com/ricardomolendijk/netbox-operator/issues/27). `payload.go`
> explicitly does not filter deferred fields yet, and there is no `status.deferredPending`
> on the envelope. The *declaration* side is built — see
> [the Descriptor](descriptor.md#deferral-and-the-identity-guard) for `Deferred`, the two
> modes, and the `ErrDeferredNaturalKey` guard that already runs at manager start. What
> follows is the engine behaviour that consumes it.

Some NetBox references cannot exist at create time by construction rather than by ordering.
A `dcim.Device`'s `primary_ip4` is a `OneToOneField` to an `ipam.IPAddress`
(`docs/netbox-schema.md` → `dcim.Device`); that address needs a `dcim.Interface` to be
assigned to; that interface needs the Device. No apply order resolves it, so the design
creates the object first and PATCHes the field afterwards.

### The two passes

| Pass | Payload |
|---|---|
| 1 | The create, with deferred fields stripped. `status.id` is recorded as normal — the object provably exists. |
| 2 | A PATCH containing **only** the deferred keys whose references now resolve. Nothing else, so a deferred PATCH can never mask or re-send an ordinary field. |

Between them the object reports `Ready=False, Reason=DeferredFieldPending`, with a new
`status.deferredPending` listing the spec fields still waiting. That field is worth adding
rather than leaving the answer in a condition message: the intermediate state is legitimate
and potentially long-lived, and "what is this waiting for" should be answerable from
`kubectl get -o yaml`.

### Three commitments that keep it honest

**A pending field is excluded from the diff, not compared against absent.** Otherwise every
reconcile of a pending object finds drift and PATCHes nothing — the hot-loop shape that
[drift detection](drift.md) exists to prevent. The strip step returns the excluded key list
precisely so the differ can be told.

**Exactly two writes per converged object, ever.** One POST and one PATCH. A third write
means either the differ is comparing a pending field or the second pass is re-sending an
already-applied one. Both are regressions with a test each.

**An unresolvable deferred field leaves the object `Ready=False` forever, on purpose.**
`kubectl wait --for=condition=Ready` failing is the correct outcome. The operator must not
claim success for an object missing a field the user asked for.

Once applied, a deferred field is an ordinary managed field: it enters the desired payload
and drift correction fixes a UI change to it like any other. Under `mode: DryRun` neither
pass runs, the object reports `DryRunPending`, and `status.deferredPending` is still
populated, since it is computed from references rather than from writes.

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
   every `CounterCacheField`; `M2M` and `GenericFKs` from the field kinds. All of it is
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
