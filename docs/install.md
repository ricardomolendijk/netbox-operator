# Installing

Three ways in, and one thing to know about all of them: **a chart upgrade does not upgrade
the CRDs.** That is a Helm 3 property, not a bug here, and the section below says what to do
about it.

| Path | For |
|---|---|
| [Helm](#helm) | Most installs |
| [`make deploy`](#make-deploy) | Working on the operator, or a cluster where kustomize is the deployment tool |
| [OLM](#olm) | Not shipped yet — see below |

## Helm

```sh
helm install netbox-operator oci://ghcr.io/ricardomolendijk/netbox-operator/charts/netbox-operator \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={homelab}
```

Nothing is published yet — the chart lives in `charts/netbox-operator/` and installs from a
checkout in the meantime:

```sh
helm install netbox-operator ./charts/netbox-operator \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={homelab}
```

`credentialNamespaces` is the one value most installs have to set, and it is worth
understanding before you do — see [the Secret blast radius](#the-secret-blast-radius).

### Values

Every value is in [`charts/netbox-operator/values.yaml`](../charts/netbox-operator/values.yaml)
with a comment saying what it does. The ones that change behaviour rather than placement:

| Value | Default | What it reaches |
|---|---|---|
| `credentialNamespaces` | `[default]` | A `Role`/`RoleBinding` per namespace **and** `NETBOX_CREDENTIAL_NAMESPACES` on the manager |
| `drift.mode` | `Correct` | `NetBoxEndpoint.spec.driftMode` on the endpoint the chart renders |
| `drift.resyncPeriod` | `10m` | `NetBoxEndpoint.spec.resyncPeriod`, ditto |
| `gitops.argocd.enabled` | `true` | `argocd.argoproj.io/compare-options: IgnoreExtraneous` in the generated-object annotation set |
| `gitops.flux.enabled` | `false` | `kustomize.toolkit.fluxcd.io/reconcile: disabled`, ditto |
| `gitops.extraAnnotations` | `{}` | merged into the same set |
| `allocation.identity.customField` | `k8s_allocation_identity` | `spec.managedBy.allocationIdentityField` on the rendered endpoint |
| `metrics.enabled` | `true` | `--metrics-bind-address` and a `Service` |
| `metrics.serviceMonitor.enabled` | `false` | A `ServiceMonitor`, if the Prometheus Operator CRD exists |
| `endpoint.enabled` | `false` | A `NetBoxEndpoint` and its token `Secret`, for a one-command demo |

A typo fails at install time rather than at controller startup, because
`values.schema.json` covers the whole surface:

```
$ helm install netbox-operator ./charts/netbox-operator --set drift.mode=Corect
Error: values don't meet the specifications of the schema(s) in the following chart(s):
netbox-operator:
- drift.mode: drift.mode must be one of the following: "Correct", "Report", "Off"
```

`additionalProperties` is `false` throughout, for the same reason: `--set drift.mod=Report`
would otherwise be accepted and silently ignored.

### Which values are the chart's, and which belong in the CR

The distinction matters more than it looks, and it is the reason two of the values above
say "on the endpoint the chart renders" rather than "on the manager".

**`driftMode` and `resyncPeriod` are per-`NetBoxEndpoint`, on purpose.** Two NetBoxes in one
cluster can be at different stages of an adoption — one in `Correct`, one still in `Report` —
so there is no manager-wide setting for either, and a chart value cannot create one. What
`drift.mode` does is set the field on the *optional* endpoint the chart renders
(`endpoint.enabled=true`). An endpoint you keep in Git takes its own value from its own
manifest, and this chart never sees it. Same for `allocation.identity.customField`, which
lands on that endpoint's `spec.managedBy`.

**`gitops.*` is manager-wide**, because the annotation set is a property of the operator
rather than of one NetBox. It is rendered as `NETBOX_GENERATED_ANNOTATIONS` on the
Deployment. Nothing materialises a child CR yet — that is
[#45](https://github.com/ricardomolendijk/netbox-operator/issues/45) — so the value is
plumbed and inert until then, which is said here rather than left to be discovered.

### What this chart does not expose

Absent on purpose, so that "there is no value for it" is an answer rather than an omission:

| Not a value | Why |
|---|---|
| A cluster-wide Secret read | It would undo [#100](https://github.com/ricardomolendijk/netbox-operator/issues/100) with one `--set`. The overlay to add it yourself is in [rbac.md](operations/rbac.md#reading-secrets-cluster-wide-anyway) — deliberately two steps and outside the chart |
| `crds.install` | Helm's `crds/` directory is not conditional; a value that pretended otherwise would lie. GitOps users template with `--include-crds`, below |
| Per-Kind `deletionPolicy` defaults | They come off each Kind's Descriptor, not from configuration. The table is in [deletion](concepts/deletion.md#the-default-depends-on-the-kind) — and since [#186](https://github.com/ricardomolendijk/netbox-operator/issues/186) it is not in `kubectl explain` either, so the docs are where you read it |
| Webhook `Certificate` | There is no webhook server yet ([#68](https://github.com/ricardomolendijk/netbox-operator/issues/68)). A cert-manager `Certificate` for a server that does not listen is a value that only fails later |
| `onConflict` | Per object, in Git. `Adopt` on a rebuild is a property of the manifests, not of the install ([gitops.md](operations/gitops.md#rebuilding-a-cluster-from-git)) |

### The Secret blast radius

The chart grants Secret access **one namespace at a time**, and the `ClusterRole` it renders
carries no `secrets` rule at all. `credentialNamespaces` produces both halves of the grant
from one list:

- a `Role` and `RoleBinding` for `get`, `list`, `watch` on Secrets in each namespace, and
- `NETBOX_CREDENTIAL_NAMESPACES` on the manager, which is the list it builds informers from.

They have to agree or the operator is broken in one of two ways, which is why one value
produces both. `*` is rejected by the schema *and* by the template, because the schema is
skipped by an older Helm and this is not a check to lose.

Every Secret an endpoint references must also carry
`netbox.kubeforge.org/endpoint-credential: "true"`, or it is invisible to the operator even
when the namespace is granted. Both failure modes, and their exact condition messages, are
in [rbac.md](operations/rbac.md).

Note that `helm upgrade` with a **shorter** list does not delete the `Role`s it no longer
renders — Helm removes resources it previously owned, so it does in fact clean up on upgrade,
but a `Role` applied by an earlier `kubectl apply` or by `make deploy` is not the chart's to
remove. `kubectl delete role,rolebinding -l app.kubernetes.io/name=netbox-operator -n <ns>`
is the check.

### CRDs, and why an upgrade does not touch them

The chart ships all CRDs in `crds/`, which means:

- **`helm install` installs them.** No separate step, no `--skip-crds` dance.
- **`helm upgrade` does not update them.** Helm 3 installs `crds/` once and never looks at
  it again. This is the trap; it is stated rather than worked around.
- **`helm uninstall` leaves them.** Deliberate, and the more important half: deleting a CRD
  deletes every CR of that kind, and their finalizers would then delete the NetBox objects
  those CRs describe. An uninstall that emptied somebody's NetBox would be a catastrophe
  with a plausible-looking command in front of it.

So upgrading is two commands:

```sh
make upgrade-crds        # kubectl apply --server-side -f charts/netbox-operator/crds/
helm upgrade netbox-operator ./charts/netbox-operator
```

CRDs first, because a new chart whose manager reconciles a field the old CRD prunes is the
failure that looks like a bug in the operator. Server-side apply, because a CRD this size
exceeds the annotation client-side apply stores inside it.

The alternative, for anyone who would rather have one tool own everything, is to stop using
`crds/` and template them in — see
[gitops.md](operations/gitops.md#chart-values) for the `--include-crds` form.

### Uninstalling

```sh
helm uninstall netbox-operator -n netbox-operator-system
```

Namespaced resources go; the CRDs and the CRs stay, as above. To go all the way, delete the
CRs first and let the finalizers run, watch them drain, and only then delete the CRDs:

```sh
kubectl delete netboxsites,netboxtags,... --all -A     # finalizers delete the NetBox objects
kubectl delete crd -l app.kubernetes.io/name=netbox-operator
```

If you want the NetBox objects to survive, that is `deletionPolicy: Retain` on the CRs
*before* you delete them, or the `netbox.kubeforge.org/skip-finalizer=true` annotation.
[deletion.md](concepts/deletion.md) has the full order of precedence.

## `make deploy`

The kustomize path, and what CI builds:

```sh
make docker-build IMG=netbox-operator:dev
make deploy IMG=netbox-operator:dev
```

The credential namespaces are `config/rbac/credential-namespaces/namespaces.txt` here rather
than a value — one namespace per line, then `make manifests`. The chart reads the same
concept from `credentialNamespaces`; the file is what `kustomize build config/default`
renders from.

## OLM

Not shipped. An OLM bundle needs a `ClusterServiceVersion` whose owned-CRD list is generated
from the same registry the CRDs are — 22 entries hand-maintained would be wrong within one
release — plus a pinned `operator-sdk` and a bundle image. It is tracked as part of
[#62](https://github.com/ricardomolendijk/netbox-operator/issues/62) and is not blocking a
Helm install.

## Releasing

Not something this repository automates the *timing* of. A maintainer pushes a tag; the
[release workflow](../.github/workflows/release.yaml) does everything after it, and nothing
before. There is no release-on-merge, no automatic version bump and no scheduled job.

`charts/netbox-operator/Chart.yaml`'s `version` and `appVersion` are the single source of the
version, and the workflow refuses a tag that disagrees with them. A `workflow_dispatch` run,
or a tag on a fork, builds the same image, chart and SBOM into the run's artifacts and
publishes nothing.

## Related

- [RBAC and the operator's blast radius](operations/rbac.md)
- [Coexisting with Flux and Argo CD](operations/gitops.md) — including the chart values
- [Observability](operations/observability.md) — the metrics endpoint this chart exposes
- [Deletion](concepts/deletion.md) — what an uninstall does to NetBox, and how to stop it
