# Decision records

Short, dated records of decisions that are expensive to reverse, and the reasoning
behind them. The point is that nobody has to re-litigate them from scratch — and
that if they *are* revisited, it is with the original trade-off in view.

| # | Decision | Status |
|---|---|---|
| [0001](0001-api-group-and-kind-naming.md) | API group `netbox.kubeforge.org`; kinds prefixed `NetBox` | Accepted |
| [0002](0002-crd-scoping.md) | Everything is namespaced in `v1alpha1` | Accepted |
| [0003](0003-ownership-and-references.md) | `parentRef` carries the FK; owner references are added only where legal; inline child sugar stays, with every inline field optional | Accepted |
| [0004](0004-claims-first-allocation.md) | Allocation is a separate `*Claim` kind, not a mode of the resource | Accepted |
| [0005](0005-gitops-coexistence.md) | Git is authoritative; the operator never writes `spec`; no Git write-back | Accepted |

## `v1beta1` promotion

**Approved for M9** · 2026-08-24
([#19](https://github.com/ricardomolendijk/netbox-operator/issues/19)). Recorded here rather
than as an ADR of its own: it schedules the decisions above rather than adding one.

M9 is after the physical-plant kinds have exercised the ref shapes and the generic-FK union
against the hardest models NetBox has (`dcim.Cable` in particular). Promoting earlier risks
discovering that the union is wrong at the one moment when fixing it costs a conversion.

Gated on five things being closed first, tracked as a checklist on NBO-062 rather than as
separate blockers:

1. **The ref shapes** — `ObjectRef`'s four modes, the scope union, the generic-FK union,
   `TagRef`. They are in every kind's spec, so they are the most expensive thing to change.
2. **`NetBoxRefGrant`'s shape**, and whether the wildcard/selector form
   [ADR-0002](0002-crd-scoping.md) needs is right.
3. **Whether the catalogue kinds become cluster-scoped** — the one item that needs a decision
   rather than a review, because [flipping a shipped CRD's scope deletes every object of that
   kind](0002-crd-scoping.md#revisiting).
4. **Whether inline child sugar stays.** Settled early: it stays, on terms that let `v1beta1`
   drop it ([ADR-0003 rule 5](0003-ownership-and-references.md)).
5. **The `status` shape and condition vocabulary**, since tooling starts depending on
   condition reasons.

One item was deliberately *not* left to the boundary: the API group rename was pulled forward
into `v1alpha1` instead of riding the promotion, because every object written under the old
group is one that would have to be migrated later
([ADR-0001](0001-api-group-and-kind-naming.md)).
