# The convergence graph

One object per file, and the **file name order is the dependency order**. NBO-017 applies
this set in that order, in exactly its reverse, and in seeded random permutations, and
asserts that all three reach a byte-identical NetBox state.

That is why each file holds exactly one object: the suite applies them one at a time with
jitter between applies, so the manager observes genuinely interleaved creations. A single
`kubectl apply -f graph/` hands the API server the whole set at once and the intermediate
states this gate exists to test may never occur.

What the graph is shaped to exercise, beyond depth:

| | Where |
|---|---|
| A self-referential `name` ref | `03` → `02` (`NetBoxRegion.parentRef`) |
| A **required** ref, no natural key without it | `05` → `04` (`NetBoxLocation.siteRef`) |
| A **to-many** ref | `08` → `06` (`NetBoxContact.groups`) |
| The **scope union** (`scope_type`/`scope_id`) | `11`, `14` (`NetBoxVLANGroup.scope`, `NetBoxPrefix.scope`) |
| A **generic FK** pair | `16` (`NetBoxContactAssignment.objectRef`) |
| **Cross-namespace** refs, three of them | `11`, `12`, `14` reaching into `netbox-catalog` |
| The grant those crossings need | `00` |

The deepest chain is five levels: `region/emea → region/nl → vlangroup → vlan → prefix`.

`NetBoxEndpoint`s and their token Secrets are **not** here. They are created by the harness,
because their content is the address of a NetBox that only exists while the suite runs.

No endpoint sets `spec.managedBy`, so nothing is stamped. The test NetBox contains only what
the operator put there, so "the managed objects" and "everything in this NetBox" are the same
set — and leaving the stamp off keeps the canonical dump free of a tag and four custom fields
that say nothing about ordering.
