# Workflows

Every job here is a thin wrapper over a `make` target, so a CI failure is reproducible
locally with one command. If CI can do something the `Makefile` cannot, that is a
`Makefile` gap — fix it there rather than in YAML.

Actions are pinned to a commit SHA rather than a tag, because a tag can be moved.

| Workflow | Gates |
|---|---|
| `ci.yaml` | `make build vet lint test verify`, `kustomize build`, `ruff check hack/`, `make test-schema` |
| `docs.yaml` | `make docs-check`, `make docs-build`; deploys to GitHub Pages on push to `main` only |
| `e2e.yaml` | `make test-e2e`; nightly, on a pull request labelled `area/refs`, and on demand — **not** on every PR |

## Why `e2e.yaml` is separate

`make test-e2e` brings up a kind cluster and a real NetBox 4.6.8 with its Postgres and Redis,
then applies the ordering gate's graph once per permutation. That is tens of minutes of real
API round trips, and a gate that slow in the default pipeline is the first thing somebody
switches off — so it is not in `ci.yaml`.

The label filter is a **job-level `if`**, not an event filter: GitHub cannot filter a
`pull_request` event by label, and a skipped job is green, so a pull request without
`area/refs` is not blocked by a check that never ran.

Nothing rots between runs. `ci.yaml`'s `make vet` compiles `test/e2e`, and its `make test`
runs `test/e2e/harness`'s own unit tests — the canonical NetBox dump and the fixture ordering
are pure functions and the load-bearing half of that gate. See
[`docs/operations/e2e.md`](../../docs/operations/e2e.md).

## The docs site

`docs.yaml` publishes `docs/` and nothing else. `mkdocs.yml`'s `docs_dir` is the whole
allowlist -- a new top-level directory is invisible to the build by default -- and
`make docs-build` asserts that against the output, failing on `plan.md`, `roadmap.md` or
`specs/` by name and on any other published path with no source under `docs/`.

Only the `deploy` job carries `pages: write` and `id-token: write`, and it is gated on
`github.event_name == 'push' && github.ref == 'refs/heads/main'`. A `pull_request` run
builds, checks and packages the artifact, then stops. The repository's default workflow
token is read-only; the job-level `permissions:` block is what grants the two Pages scopes,
so no repository setting needs changing. Pages itself has never been enabled on this
repository, so `actions/configure-pages` runs with `enablement: true` and turns it on with
`build_type: workflow` the first time the workflow runs on `main`.

**Follow-up, deliberately not done here:** `site_url` in `mkdocs.yml` is already the final
`https://netbox.kubeforge.org/`, so canonical links and `sitemap.xml` will not need
rewriting. Until the domain is registered and a `CNAME` is configured, the site serves from
the `github.io` URL and those canonical links point at a domain that does not resolve yet.
Adding the custom domain is a repository setting plus a `docs/CNAME` file; it does not
touch this workflow.
