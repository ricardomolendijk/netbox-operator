# Workflows

Every job here is a thin wrapper over a `make` target, so a CI failure is reproducible
locally with one command. If CI can do something the `Makefile` cannot, that is a
`Makefile` gap — fix it there rather than in YAML.

Actions are pinned to a commit SHA rather than a tag, because a tag can be moved.

| Workflow | Gates |
|---|---|
| `ci.yaml` | `make build vet lint test verify`, `kustomize build`, `ruff check hack/` |
