# Contributing

## Process

Work is tracked as `NBO-nnn` issues. **Ticket IDs are stable and never renumbered** —
they are referenced from commit messages, branch names and code comments.

| | Convention |
|---|---|
| Branch | `nbo-<nnn>-<slug>` — e.g. `nbo-012-resolver` |
| Commit subject | `NBO-nnn: <imperative summary>` |
| Pull request | **One feature, one issue.** Body ends with `Closes #N`. |
| Milestone | Every issue carries one (`M1`…`M10`) |

One PR per issue, scoped to that issue. No omnibus branches and no drive-by
refactors inside a feature PR — if you find something adjacent that needs fixing,
open an issue. Rebase rather than merge; keep history linear and readable.

**Gate tickets** (`kind/gate`) close a milestone. A gate may not be closed while any
other ticket in its milestone is open. That is the only hard process rule.

Releases are cut by the maintainer. Do not tag or publish.

## Definition of done

1. Every acceptance criterion on the issue is checked off.
2. Unit tests for logic; `envtest` for anything touching a controller.
3. `make generate manifests fmt vet lint test` is clean.
4. Any new NetBox field or endpoint assumption cites [`docs/netbox-schema.md`](docs/netbox-schema.md).
5. **Docs updated in the same PR.** A feature PR that does not touch `docs/` or
   `README.md` is incomplete — every kind gets a reference page, every concept gets
   a concept page.

## Engineering standards

Enforced by `.golangci.yml` in CI where a linter can check it, and by review where
one cannot.

### Extensibility is the primary architectural constraint

Adding a NetBox kind must mean **adding files, never editing shared logic**. A new
kind is exactly three additions and zero modifications:

```
api/v1alpha1/<app>_<kind>.go                     spec struct + kubebuilder markers
internal/registry/<app>_<kind>.go                a Descriptor, registered in init()
internal/controller/<app>_<kind>_controller.go   SetupWithManager + engine delegation
```

If a new kind requires a change to `internal/reconciler`, that is a design bug in the
engine: the missing behaviour belongs in the `Descriptor` as data, not in the engine
as a branch. **There is no `switch` on kind anywhere in the reconcile path** — that
switch is the specific smell this rule exists to prevent.

### Optional spec fields have three states, not two

An optional field can be **absent** (leave NetBox's value alone), **empty** (clear
NetBox's value) or **set**. The engine tells absent from empty by reading
`metadata.managedFields`, not by looking at the Go value, so writing a kind needs
nothing but two conventions:

- **Keep `omitempty`.** Taking it off makes a typed Go client marshal every unset
  string as `""` and claim it, so adopting a pre-existing NetBox object would wipe
  every value the user had not restated. That is the inverse of the bug, and worse.
- **Document the empty state on any field that has one.** One sentence in the field's
  doc comment, because that comment is what `kubectl explain` prints:

  ```go
  // Description is free text shown next to the tag.
  //
  // Omit it to leave NetBox's own value alone; set it to `""` to clear the value in
  // NetBox. The two are different intents and the operator can tell them apart
  // (docs/concepts/field-ownership.md).
  ```

  Leave the sentence off a field that has no third state: one that is required, one
  with a `+kubebuilder:default` so it is never absent, or one whose validation
  rejects the empty value. `TestClearableFieldsDocumentBothStatesInTheSchema` checks
  both directions against the generated CRDs — a field that documents an empty state
  its own schema forbids fails, and so does an object kind that documents none at all.

**"Spec omission means don't manage" is about your manifest, not about Go.** A field
you never wrote is unmanaged; a field you wrote as empty is managed and cleared. See
[`docs/concepts/field-ownership.md`](docs/concepts/field-ownership.md).

### Core logic lives in one place

- `internal/reconciler` — the only place a create/adopt/update/delete decision is made.
- `internal/resolver` — the only place a ref becomes an ID.
- `internal/netbox` — the only place an HTTP request is built.
- `internal/registry` — the only place per-kind facts live.

Controllers are wiring. A controller containing business logic has taken work that
belongs to the engine.

### Interfaces at the seams, structs for data

- Every collaborator crossing a package boundary is an interface, so it is fakeable
  without a live NetBox: `NetBoxClient`, `RefResolver`, `Differ`, `EndpointProvider`,
  `ChildMaterialiser`.
- Interfaces are defined by the **consumer**, kept to 1–3 methods, and never
  speculative — no interface without at least two implementations, one of which may
  be a test fake.
- Data is plain structs. No `map[string]any` in exported signatures outside
  `internal/netbox`, where it is the deliberate representation of an untyped body.
- Accept interfaces, return structs.

### Control flow

- **Guard clauses, always.** Validate and return early; the happy path is the
  least-indented code in the function.
- **Maximum nesting depth 3.**
- No `else` after a `return` / `continue` / `break`.
- A reconcile step is a named method, not a paragraph inside `Reconcile`.
- Errors are wrapped with context (`fmt.Errorf("resolving %s: %w", field, err)`) and
  classified by type, never by string matching.

### Logging

Structured, always — no `fmt.Printf`, no unstructured `log`.

- `logr` via controller-runtime, with a stable key set on every line: `kind`,
  `namespace`, `name`, `netboxID`, `endpoint`, `action`.
- `error` = needs a human. `info` = state changed. `debug` = everything else. A
  reconcile that changes nothing logs at `debug`, or the log is unreadable at scale.
- Take the logger from the context; never construct one ad hoc.
- Secrets never appear at any level. Redaction is a tested function, not a habit.

### Comments

Comments explain **why**, and only when the why is not derivable from the code. No
comment that restates the code, no decorative banners. Doc comments on exported
identifiers are required and are not the target of this rule.

Do write a comment when the reason is a NetBox quirk, and cite it:

```go
// Prefix is scoped via CachedScopeMixin since NetBox 4.2; `site` is a read-only
// cached column and writing it silently no-ops. See docs/netbox-schema.md.
```

### No TODOs without implementation

`TODO`, `FIXME` and `XXX` fail the build (`godox`). Unfinished work is an issue,
referenced from the code only when genuinely necessary
(`// Deferred fields are applied by the engine; see NBO-015.`). No stub returning
`nil` to be filled in later, no dead or commented-out code.

### Review checklist

Applied to every PR touching `internal/`:

1. Did this change require editing shared logic in order to add a kind? If yes, the
   engine is wrong.
2. Is every new interface consumer-defined and under four methods?
3. Does every new comment explain a *why* that is not obvious from the code?
4. Is the happy path the least-indented code in each new function?

## Local development

```sh
make help                      # list every target
make build                     # generate, manifests, fmt, vet, then build bin/manager
make lint test                 # golangci-lint, then unit tests + envtest
make verify                    # fail if generated output is not committed
make test-e2e                  # kind + a real NetBox (harness lands with NBO-017)
make run                       # run against the current kubeconfig
```

Tools install themselves into `./bin` at pinned versions on first use — there is nothing
to install by hand, and your global toolchain cannot change generated output.
