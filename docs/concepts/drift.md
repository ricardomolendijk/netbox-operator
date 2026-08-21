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

Rules 1–4 are carried over from `netbox-populator`, which got them right. 5–8 are new.

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

## Purity

`Drift` takes plain data and returns plain data. It never touches a client. That is
asserted at compile time, because it is what makes the function exhaustively unit-testable
and lets `nbctl plan` reuse it unchanged.

The per-Kind comparison rules arrive as a `FieldRules` value rather than a lookup into the
registry, for the same reason.
