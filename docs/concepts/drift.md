# Drift detection

`Drift(live, desired, rules)` returns the fields that genuinely differ — exactly the
payload to `PATCH`, and empty when there is nothing to send.

```go
drift := netbox.Drift(liveObject, desiredPayload, rules)
if len(drift) == 0 {
    return nil // nothing to do; log at debug and requeue
}
```

## Why this is the most delicate code in the operator

A wrong comparison **does not fail loudly**. It produces a difference that the write
never satisfies, so the next reconcile finds the same difference and writes again. In a
one-shot CLI that is one wasted call. In a controller it is a hot loop against the NetBox
API for as long as the object exists.

Every rule below therefore has a regression test that takes a payload, pairs it with the
response NetBox actually returns for that payload, and asserts the second reconcile finds
nothing to do.

## What NetBox returns is not what you write

| # | Field shape | On read | On write | Compared by |
|---|---|---|---|---|
| 1 | Foreign key | `{"id":3,"name":"acme","slug":"acme"}` | `3` | `.id` |
| 2 | Choice | `{"value":"active","label":"Active"}` | `"active"` | `.value` |
| 3 | Many-to-many | `[{"id":2,…},{"id":1,…}]` | `[1,2]` | id set, order-independent |
| 4 | Decimal | `"1.00"` | `1` | numerically |
| 5 | Object-type list | `["dcim.device"]` | same | string set, order-independent |
| 6 | Custom fields | *every* defined field | the managed subset | desired keys only |
| 7 | Generic FK | `scope_type` + `scope_id` | same | **as a pair** |
| 8 | `ArrayField` | `[80,443]` | same | order-**sensitive** |
| 9 | To-many generic FK | `[{"object_type":…,"object_id":41,"object":{…}}]` | the pair only | pair set, order-independent |

Rules 1–4 are carried over from `netbox-populator`, which got them right. 5–9 are new.

### 6 — custom fields, and why the subset matters

NetBox returns every custom field defined for the object type, including ones this
operator has never heard of. Diffing the whole map would make the operator try to null
out another team's custom fields on every reconcile. Only keys present in the desired map
are compared, and since NetBox merges a partial `custom_fields` PATCH, sending the
managed subset is both correct and sufficient.

### 7 — generic FKs are one reference in two columns

`scope_type` + `scope_id` (on `Prefix`, `Cluster`, `WirelessLAN`, `VLANGroup`) and
`assigned_object_type` + `assigned_object_id` (on `IPAddress`) are single polymorphic
references. They are diffed together, and **when either changes, both are sent**: NetBox
validates the pair, and an id sent without its type is either rejected or interpreted
against the old type.

This is also where the populator has a live bug worth knowing about. Since NetBox 4.2
these models use `CachedScopeMixin`, and `site` is a read-only cached column (`_site`).
Writing `site` silently no-ops, and keying drift on it is correct only by luck for
site-scoped objects and wrong for anything scoped to a region, site group or location.
Drift keys on `(scope_type, scope_id)` and never on the cached column.

### 9 — a to-many generic FK is a set of pairs

`a_terminations` and `b_terminations` on `dcim.Cable` are the case, and the only one so
far ([`NetBoxCable`](../reference/netboxcable.md)). One field carries a **list** of
`{object_type, object_id}` objects rather than two columns carrying one pair, so rule 7
does not apply and neither does rule 3 — the elements are not ids.

Two things make the read differ from the write, and both would be permanent drift:

- **The order is NetBox's, not yours.** The elements are `dcim.CableTermination` rows,
  returned in `('cable', 'cable_end', 'connector', 'pk')` order
  ([`docs/netbox-schema.md`](../netbox-schema.md) → `dcim.CableTermination`,
  `meta.ordering`) rather than in the order they were POSTed.
- **The read carries a third key.** `GenericObjectSerializer` adds a read-only `object`
  expansion of the target (`netbox/netbox/api/serializers/generic.py:15`) that the write
  never sends.

So the comparison reads **only the two written keys**, sorts, and deduplicates — the same
argument rule 3 makes for an M2M, one level of nesting further in. Getting it wrong costs
more here than anywhere else on this page: `dcim.Cable` is the one
`UpdateStrategy: Recreate` kind and both these fields are in its `RecreateOn`, so a diff
that never settles is not a `PATCH` loop but **a cable deleted and re-created on every
resync** — which churns the changelog and rebuilds every `CablePath` through it each time.

### 8 — arrays are ordered, sets are not

`vid_ranges` and `ports` are Postgres `ArrayField`s where the order is data, so a
reordering is a real change. M2M fields are sets, where it is not. Using one rule for
both gives you either a hot loop or a missed change, depending on which way you guess.

## Fields you never set are never touched

Only keys present in `desired` are considered. A field the spec does not set is left
exactly as NetBox has it, which is what lets the operator share an object with a human or
another tool. There is no reconciliation of absence.

## `Hash` and server canonicalisation

NetBox rewrites some values as it stores them: `mac_address` is upper-cased, a prefix is
masked down to its network address. So the request and the response legitimately differ,
and no list of "canonicalisations NetBox performs" can be known to be complete.

`Hash(payload)` gives a stable digest of the *normalised* payload, so the engine can
record what it last applied and short-circuit when nothing has changed — rather than
teaching `Drift` every rewrite NetBox might do. Numeric strings and numbers hash
identically, matching how they compare.

## What happens to the drift once it is found

`Drift` says what differs. What the operator *does* about it is
`NetBoxEndpoint.spec.driftMode`, and it is a property of the endpoint rather than of this
function — which is why `Drift` stays a pure comparison and `nbctl plan` can reuse it
unchanged.

| Mode | Detects | Corrects | Periodic re-check |
|---|---|---|---|
| `Correct` | yes | yes | yes |
| `Report` | yes | no | yes |
| `Off` | only on a CR change | yes | no |

`Correct` is the default and the intended steady state: Git is authoritative, so a
NetBox-side edit is simply wrong. A UI edit is never promoted back into a CR's `spec` — there
is no mode for that, and [ADR-0005](../decisions/0005-gitops-coexistence.md) is why.

Whatever the mode, the two counters are separate: `drift_detected_total` moves for every
field that differs, `drift_corrected_total` only for the fields NetBox accepted. The gap
between them is the whole signal in `Report` and on a `DryRun` endpoint, where drift is found
and deliberately left alone. One counter with a `corrected` label would make "reporting as
configured" and "failing to write" the same shape on a dashboard.

See [coexisting with Flux and Argo CD](../operations/gitops.md) for the operational side of
each mode.

## Purity

`Drift` takes plain data and returns plain data. It never touches a client. That is
asserted at compile time, because it is what makes the function exhaustively unit-testable
and lets `nbctl plan` reuse it unchanged.

The per-Kind comparison rules arrive as a `FieldRules` value rather than a lookup into the
registry, for the same reason.
