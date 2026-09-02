# Workflows

Every job in `ci.yaml`, `docs.yaml` and `e2e.yaml` is a thin wrapper over a `make` target, so
a CI failure is reproducible locally with one command. If CI can do something the `Makefile`
cannot, that is a `Makefile` gap — fix it there rather than in YAML. `release.yaml` is the one
exception and says why [below](#why-releaseyaml-is-not-a-thin-wrapper).

Actions are pinned to a commit SHA rather than a tag, because a tag can be moved.

| Workflow | Jobs | Gates |
|---|---|---|
| `ci.yaml` | `build`, `manifests`, `chart`, `python` | `make build vet lint test verify`; `kustomize build config/default` and `config/crd`; `make helm-verify helm-package crd-bundle` against the pinned Helm; `ruff check hack/` and `make test-schema` |
| `docs.yaml` | `build`, `deploy` | `make docs-tools docs-check docs-build`, then the Pages artifact; deploys to GitHub Pages on push to `main` only |
| `e2e.yaml` | `convergence` | `make test-e2e`; nightly, on a pull request labelled `area/refs`, and on demand — **not** on every PR |
| `release.yaml` | `release` | The tag must equal `Chart.yaml`'s `version` *and* `appVersion`; `make helm-lint helm-template helm-package crd-bundle`, then the multi-arch image, the SBOM, the cosign signature, the OCI chart push and the GitHub release |

The `chart` job is the one most easily missed, and it is the one that gates what a user
installs: `make helm-verify` (lint, four renders, and the golden RBAC diff), `make helm-package`
(which fails if the packaged chart crosses 256 KB) and `make crd-bundle` (the
`netbox-operator-crds-<version>.yaml` that replaces the `crds/` directory the chart no longer
has — see [`docs/install.md`](../../docs/install.md)). It pins Helm itself rather than taking
the runner's, for the same reason every other tool here is pinned.

## Why `release.yaml` is not a thin wrapper

Four of its steps are `make` calls; the rest is version resolution, the fork/publish gate,
buildx, SBOM generation, cosign, the OCI chart push and `gh release`. None of those is
reproducible locally on purpose — a `make` target that could push to `ghcr.io` and cut a
GitHub release is a target somebody runs by accident. The gate is explicit: it publishes only
when the repository is `ricardomolendijk/netbox-operator` **and** the ref is a `v*` tag, so a
`workflow_dispatch` run, or a tag on a fork, builds the same artefacts into the run and
publishes nothing.

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
rewriting. Until the domain is registered and configured, the site serves from the
`github.io` URL and those canonical links point at a domain that does not resolve yet.

Adding the custom domain is a repository setting and **nothing else** -- there is no
`docs/CNAME` file and there should not be one. GitHub's documentation on managing a custom
domain is explicit that "if you are publishing from a custom GitHub Actions workflow, no
`CNAME` file is created, and any existing `CNAME` file is ignored and is not required".
A `CNAME` under `docs/` would build, publish (`docs_dir` is the allowlist, so it would pass
`hack/check-docs-links.py --site`) and do nothing, while reading like the thing keeping the
domain resolving. The setting is the only thing keeping it resolving.
