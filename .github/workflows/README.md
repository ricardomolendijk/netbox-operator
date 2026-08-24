# Workflows

Every job here is a thin wrapper over a `make` target, so a CI failure is reproducible
locally with one command. If CI can do something the `Makefile` cannot, that is a
`Makefile` gap — fix it there rather than in YAML.

Actions are pinned to a commit SHA rather than a tag, because a tag can be moved.

| Workflow | Gates |
|---|---|
| `ci.yaml` | `make build vet lint test verify`, `kustomize build`, `ruff check hack/`, `make test-schema` |
| `docs.yaml` | `make docs-check`, `make docs-build`; deploys to GitHub Pages on push to `main` only |

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
