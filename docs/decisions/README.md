# Decision records

Short, dated records of decisions that are expensive to reverse, and the reasoning
behind them. The point is that nobody has to re-litigate them from scratch — and
that if they *are* revisited, it is with the original trade-off in view.

| # | Decision | Status |
|---|---|---|
| [0001](0001-api-group-and-kind-naming.md) | API group `netbox.populator.io`; kinds prefixed `NetBox` | Accepted |
| [0002](0002-crd-scoping.md) | Split the catalogue by scope using a transitive-closure rule | Accepted |
| [0003](0003-ownership-and-references.md) | `parentRef` carries the FK; owner references are added only where legal | Accepted |
| [0004](0004-claims-first-allocation.md) | Allocation is a separate `*Claim` kind, not a mode of the resource | Accepted |
