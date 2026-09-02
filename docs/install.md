# Installing

Three ways in, and one thing to know about all of them: **the CRDs are not part of the Helm
release.** They are applied first, by their own command, on install and on every upgrade.
[Why](#crds-and-why-they-are-not-in-the-chart) is a hard limit in Kubernetes rather than a
preference, and the chart refuses to install if you skip the step.

| Path | For |
|---|---|
| [Helm](#helm) | Most installs |
| [`make deploy`](#make-deploy) | Working on the operator, or a cluster where kustomize is the deployment tool |
| [OLM](#olm) | Not shipped yet — see below |

## Helm

Two commands, in this order. The first is the CRDs; the second is everything else.

`--create-namespace` only creates the *release* namespace (`netbox-operator-system` below) —
it says nothing about the namespaces listed in `credentialNamespaces`. The chart renders a
`Role`/`RoleBinding` into each of those, and Helm refuses to create either in a namespace that
does not exist yet, so create it first:

```sh
kubectl apply --server-side --force-conflicts \
  -f https://github.com/ricardomolendijk/netbox-operator/releases/download/v0.0.6/netbox-operator-crds-0.0.6.yaml

kubectl create namespace homelab

helm install netbox-operator oci://ghcr.io/ricardomolendijk/charts/netbox-operator \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={homelab}
```

`netbox-operator-crds-<version>.yaml` is attached to every GitHub release, and it is what
replaces the `crds/` directory an OCI chart no longer has. Use the file from the release
whose version you are installing — the chart and its CRDs are one artefact split in two, not
two independently versioned things.

**Neither half is published yet, so the two commands above do not work today.** No GitHub
release has ever carried an asset — every release run so far failed before uploading them —
so the CRD bundle URL 404s. The only chart in the registry is `0.0.6`, pushed before the CRDs
came out of it, which is the 348 KB package whose release `Secret` the API server rejects.
Until a release publishes both halves together, install from a checkout, where the CRD step
is a make target:

```sh
make install-crds        # kubectl apply --server-side -f config/crd/bases/

kubectl create namespace homelab

helm install netbox-operator ./charts/netbox-operator \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={homelab}
```

Skipping the first command is an install-time error naming the command you missed, not a
manager that CrashLoops twenty seconds later — see
[the precondition](#the-precondition-and-when-to-turn-it-off).

`credentialNamespaces` is the one value most installs have to set, and it is worth
understanding before you do — see [the Secret blast radius](#the-secret-blast-radius).

### Values

Every value is in [`charts/netbox-operator/values.yaml`](../charts/netbox-operator/values.yaml)
with a comment saying what it does. The ones that change behaviour rather than placement:

| Value | Default | What it reaches |
|---|---|---|
| `crds.check` | `true` | Nothing rendered. It fails the install when the operator's CRDs are not on the cluster — [below](#the-precondition-and-when-to-turn-it-off) |
| `credentialNamespaces` | `[default]` | A `Role`/`RoleBinding` per namespace **and** `NETBOX_CREDENTIAL_NAMESPACES` on the manager |
| `drift.mode` | `Correct` | `NetBoxEndpoint.spec.driftMode` on the endpoint the chart renders |
| `drift.resyncPeriod` | `10m` | `NetBoxEndpoint.spec.resyncPeriod`, ditto |
| `gitops.argocd.enabled` | `true` | `argocd.argoproj.io/compare-options: IgnoreExtraneous` in the generated-object annotation set |
| `gitops.flux.enabled` | `false` | `kustomize.toolkit.fluxcd.io/reconcile: disabled`, ditto |
| `gitops.extraAnnotations` | `{}` | merged into the same set |
| `allocation.identity.customField` | `k8s_allocation_identity` | `spec.managedBy.allocationIdentityField` on the rendered endpoint |
| `metrics.enabled` | `true` | `--metrics-bind-address` and a `Service` |
| `metrics.serviceMonitor.enabled` | `false` | A `ServiceMonitor`, if the Prometheus Operator CRD exists |
| `webhook.enabled` | `true` | A `ValidatingWebhookConfiguration`, a `Service`, a cert-manager `Certificate` and `--enable-webhooks` — **if the cert-manager CRDs exist**, see below |
| `webhook.certManager.required` | `false` | Nothing; it makes cert-manager's absence an install-time error instead of a degraded install |
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
| `crds.install` | The chart has no CRDs to install — they are [a separate artefact](#crds-and-why-they-are-not-in-the-chart) and a value that pretended Helm managed them would lie. `crds.check` exists and only decides whether a render *asserts* they are there |
| A cluster-wide `deletionPolicy` default | It is `Delete` for every Kind and not configurable ([deletion](concepts/deletion.md#the-two-policies)). Per-Kind defaults existed until [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) and were the wrong place to answer the question; a chart value would be a third. Write `deletionPolicy: Retain` on the objects that should outlive their CR |
| A second webhook certificate mechanism | cert-manager is the only certificate path, deliberately — [admission-webhooks.md](operations/admission-webhooks.md#certificates) has the reasoning. `webhook.certManager.*` configures that one path and there is no `webhook.tls.existingSecret` beside it |
| `webhook.failurePolicy`, `timeoutSeconds`, the rules | Decisions with a written argument and a test holding them, not knobs. `Ignore` is only defensible because the rules never reach outside `netbox.kubeforge.org`, and a value that let one install widen them would break that argument silently ([admission-webhooks.md](operations/admission-webhooks.md#failurepolicy-ignore-and-why)) |
| `onConflict` | Per object, in Git. `Adopt` on a rebuild is a property of the manifests, not of the install ([gitops.md](operations/gitops.md#rebuilding-a-cluster-from-git)) |

### cert-manager, and what a default install does without it

**cert-manager is optional, and the install tells you which of the two you got.** The
[validating webhook](operations/admission-webhooks.md) needs a serving certificate, and
cert-manager is the only path to one here — a decision with a written argument, not a gap.
So the chart gates every webhook object on the `cert-manager.io/v1` CRDs actually existing:

| cert-manager | What the chart renders | The manager |
|---|---|---|
| installed | `Certificate`, a self-signed `Issuer`, the webhook `Service`, the `ValidatingWebhookConfiguration` with `cert-manager.io/inject-ca-from` | serves admission; the `Pod` waits in `ContainerCreating` until cert-manager has written the `Secret`, which is seconds |
| absent | none of it | started with `--enable-webhooks=false` |

The second row is the fix for
[#249](https://github.com/ricardomolendijk/netbox-operator/issues/249): the manager serves
the webhook by default and **exits** when the certificate is not on disk, so a chart that
rendered nothing and left that default alone CrashLooped a vanilla `helm install`. It now
degrades instead, and `NOTES.txt` says so in as many words.

A degraded install is a real loss, and a bounded one: three denials and three warnings move
from apply time to reconcile time, where every one of them has a backstop and a blocked
object performs **zero** NetBox writes. The table is
[what breaks when it is off](operations/admission-webhooks.md#what-breaks-when-it-is-off).
Everything the CRD schema and CEL enforce — `endpointRef` immutability, every one-of, the
CIDR host-bits check — is enforced by the API server either way.

To install cert-manager first and get admission from the start:

```sh
helm install cert-manager oci://quay.io/jetstack/charts/cert-manager \
  --namespace cert-manager --create-namespace --set crds.enabled=true
```

Two things worth knowing about the gate:

- **Installing cert-manager afterwards does not turn admission on by itself.** Helm reads
  the cluster's API versions at render time, so run `helm upgrade` once cert-manager is
  there. Nothing else changes.
- **`webhook.certManager.required=true` refuses to install without it.** Set it where
  admission is a control you rely on rather than a nicety — under `failurePolicy: Ignore`
  a webhook that is quietly not there admits everything and logs nothing to say so, and a
  loud install-time failure is the only way to not learn that later.

### The Secret blast radius

The chart grants Secret access **one namespace at a time**, and the `ClusterRole` it renders
carries no `secrets` rule at all. `credentialNamespaces` produces both halves of the grant
from one list:

- a `Role` and `RoleBinding` for `get`, `list`, `watch` on Secrets in each namespace, and
- `NETBOX_CREDENTIAL_NAMESPACES` on the manager, which is the list it builds informers from.

They have to agree or the operator is broken in one of two ways, which is why one value
produces both. `*` is rejected by the schema *and* by the template, because the schema is
skipped by an older Helm and this is not a check to lose.

**`[default]` is a placeholder, not a recommendation.** The `Role` grants `get`, `list` and
`watch` on *every* Secret in each listed namespace — `resourceNames` cannot narrow a `list`
or a `watch`, which have no resource name in them — so listing a namespace that also holds
other applications' credentials hands those to the operator's ServiceAccount as well. List
a namespace that holds endpoint credentials and little else, and on most clusters `default`
is not it:
[the grant is every Secret in the namespace](operations/rbac.md#the-grant-is-every-secret-in-the-namespace-labelled-or-not).

Every Secret an endpoint references must also carry
`netbox.kubeforge.org/endpoint-credential: "true"`, or it is invisible to the operator even
when the namespace is granted. That label is a cache filter and not a second permission
boundary: it decides what the operator reads, not what it is allowed to read. Both failure
modes, and their exact condition messages, are in [rbac.md](operations/rbac.md).

Note that `helm upgrade` with a **shorter** list does not delete the `Role`s it no longer
renders — Helm removes resources it previously owned, so it does in fact clean up on upgrade,
but a `Role` applied by an earlier `kubectl apply` or by `make deploy` is not the chart's to
remove. `kubectl delete role,rolebinding -l app.kubernetes.io/name=netbox-operator -n <ns>`
is the check.

### CRDs, and why they are not in the chart

The chart used to ship all 64 CRDs in Helm's `crds/` directory, and that made **every**
`helm install` of it fail:

```
Error: INSTALLATION FAILED: create: failed to create: Secret
"sh.helm.release.v1.netbox-operator.v1" is invalid: data: Too long:
may not be more than 1048576 bytes
```

Helm 3 stores the *whole chart* in the release `Secret` — `crds/` included, because `crds/`
is chart content even though it is never templated — alongside the rendered manifest and the
values, gzipped and base64-encoded. The API server rejects any `Secret` whose data exceeds
1 MiB. 2.7 MB of CRDs packaged to a 424168-byte `.tgz`, and that did not come back under the
limit. Without them the same chart packages to under 20 KB.

That is a hard ceiling and the catalogue only grows, so the CRDs left the release rather than
being squeezed under it ([#265](https://github.com/ricardomolendijk/netbox-operator/issues/265),
[#268](https://github.com/ricardomolendijk/netbox-operator/issues/268)). `make helm-package`
now fails if the packaged chart crosses 256 KB, so this cannot come back quietly.

What that changes, in three lines:

- **You install the CRDs.** `make install-crds` from a checkout, or the
  `netbox-operator-crds-<version>.yaml` attached to the release. Both are one `kubectl apply
  --server-side`. Server-side because it avoids the `last-applied-configuration` annotation
  a client-side apply stores inside every object, which for these CRDs is 55% of the stored
  size again — `netboxcables` is 191,094 bytes applied server-side and 295,197 bytes applied
  client-side. Note this is etcd bloat and *not* a limit: measured against a real 1.34 API
  server, the largest annotation is 98,381 bytes against a 262,144 byte cap, so all 64 CRDs
  fit client-side with 2.66x headroom. Earlier wording here claimed they exceeded it; they
  do not. The 1 MiB limit this project actually hit was the Helm release Secret, an
  aggregate over the whole chart, which is a different limit on a different object.
- **You upgrade them too, before the chart.** Helm never touched them and still does not,
  so this is the same trap it always was, now with the step in front of you rather than
  hidden behind an install that quietly did it once.
- **Nothing ever deletes them.** `helm uninstall` could not remove them before and cannot
  now. Deliberate, and the more important half: deleting a CRD deletes every CR of that
  kind, and their finalizers would then delete the NetBox objects those CRs describe. An
  uninstall that emptied somebody's NetBox would be a catastrophe with a plausible-looking
  command in front of it.

So upgrading is two commands, in this order:

```sh
make upgrade-crds        # kubectl apply --server-side -f config/crd/bases/
helm upgrade netbox-operator ./charts/netbox-operator
```

or, against a release:

```sh
kubectl apply --server-side --force-conflicts \
  -f https://github.com/ricardomolendijk/netbox-operator/releases/download/v0.0.6/netbox-operator-crds-0.0.6.yaml
helm upgrade netbox-operator oci://ghcr.io/ricardomolendijk/charts/netbox-operator --version 0.0.6
```

CRDs first, because a new chart whose manager reconciles a field the old CRD prunes is the
failure that looks like a bug in the operator.

For anyone who would rather have one tool own everything, the CRD bundle is an ordinary
multi-document YAML: commit it, or point Argo CD or Flux at its URL, and the ordering above
becomes a sync wave. See [gitops.md](operations/gitops.md#crds-and-argo-cd).

### The precondition, and when to turn it off

Taking the CRDs out of the release buys a working install and costs one ordering constraint,
and an ordering constraint nothing enforces is a footgun. So the chart checks: if
`netbox.kubeforge.org/v1alpha1` is not among the cluster's API versions, `helm install` fails
immediately with the command you missed in the message.

Without it the install would succeed, the `Deployment` would roll out, and the manager would
exit while building its caches — `no matches for kind "NetBoxEndpoint"`, in a container log,
several minutes and one `kubectl logs` away from the command that caused it.

`crds.check=false` turns it off, and there is exactly one reason to: a render that happens
away from the cluster cannot see discovery and will fail even when the CRDs are installed.
That is `helm template` without `--api-versions netbox.kubeforge.org/v1alpha1`, `helm lint`
(which has no such flag), and GitOps renderers that do the same. It is a render-time
assertion and nothing else — switching it off changes nothing about what gets installed.

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
