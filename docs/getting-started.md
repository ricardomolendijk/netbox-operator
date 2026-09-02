# Getting started

From an empty cluster to a site visible in NetBox. Six steps, about ten minutes, and no
prior knowledge of the operator's design.

**What you need**

- A Kubernetes cluster you can `kubectl apply` to (kind, k3s, EKS — anything at 1.27 or
  later) and `helm`.
- A NetBox at 4.2 or later, reachable **from inside the cluster**, and an API token for it.
- Nothing else. The commands below install from the published `v0.0.9` release. If you would
  rather install from a checkout of this repository, [installing](install.md#from-a-checkout)
  has that path and it is the same shape.

Everything below uses one namespace, `netbox-demo`, for your objects, and installs the
operator into `netbox-operator-system`.

## 1. Install the CRDs

**The CRDs are not part of the Helm release.** You apply them yourself, before the chart, on
install and on every upgrade:

```sh
kubectl apply --server-side --force-conflicts \
  -f https://github.com/ricardomolendijk/netbox-operator/releases/download/v0.0.9/netbox-operator-crds-0.0.9.yaml
```

From a checkout that is `make install-crds`, which runs the same `kubectl apply` against
`config/crd/bases/`. Either way it installs 64 CRDs. Check:

```sh
kubectl get crd | grep netbox.kubeforge.org | wc -l     # 64
```

<details>
<summary><strong>Why is this a separate step?</strong></summary>

Helm 3 stores the entire chart — `crds/` included — in the release `Secret`, and the API
server rejects any Secret over 1 MiB. 2.7 MB of CRDs did not compress under that, so they
left the chart rather than being squeezed under a ceiling the catalogue would breach again.
The full argument, including why nothing ever deletes them, is in
[installing](install.md#crds-and-why-they-are-not-in-the-chart).

</details>

## 2. Create the namespace your objects will live in

Do this **before** installing the chart. The chart renders a `Role` and `RoleBinding` into
every namespace you list in `credentialNamespaces`, and Helm will not create a `Role` in a
namespace that does not exist yet.

```sh
kubectl create namespace netbox-demo
```

## 3. Install the chart

```sh
helm install netbox-operator oci://ghcr.io/ricardomolendijk/charts/netbox-operator \
  --version 0.0.9 \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={netbox-demo}
```

Pin `--version`, and pin it to the same version as the CRD bundle above: the two are one
artefact split in two, and the registry still holds a `0.0.6` chart from before the split
that does not install at all.

`credentialNamespaces` is the one value nearly every install has to set. It does two things
from one list: it grants the operator read access to Secrets in those namespaces, and it
tells the manager which namespaces to build Secret informers from. They have to agree, which
is why one value produces both.

Watch the manager come up:

```sh
kubectl rollout status deploy/netbox-operator -n netbox-operator-system
```

<details>
<summary><strong>Do I need cert-manager?</strong></summary>

No. With cert-manager present the chart renders a validating webhook, which moves three
denials and three warnings from reconcile time to apply time. Without it, the chart renders
no webhook and starts the manager with `--enable-webhooks=false`. Nothing is unenforced
either way — every rule the webhook would have applied has a backstop at reconcile time, and
a blocked object performs zero NetBox writes.
[admission-webhooks.md](operations/admission-webhooks.md#what-breaks-when-it-is-off) has the
table.

</details>

## 4. Give the operator a token

The credential is an ordinary `Secret`, in the same namespace as the endpoint that uses it,
with **one required label**:

```sh
kubectl create secret generic netbox-token \
  -n netbox-demo --from-literal=token=<YOUR-NETBOX-API-TOKEN>

kubectl label secret netbox-token -n netbox-demo \
  netbox.kubeforge.org/endpoint-credential=true
```

The label is not optional. The operator reads Secrets through a label-scoped informer, so an
unlabelled Secret is genuinely invisible to it even though `kubectl get secret` shows it —
and the endpoint will report `SecretMissing`. This is the single most common first-run
problem.

## 5. Point at your NetBox

A `NetBoxEndpoint` is the connection: a URL, a reference to that Secret, and the result of
probing them.

```sh
kubectl apply -f - <<'EOF'
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxEndpoint
metadata:
  name: demo
  namespace: netbox-demo
spec:
  url: https://netbox.example.com
  tokenSecretRef:
    name: netbox-token
EOF
```

Then wait for it:

```sh
kubectl get netboxendpoint -n netbox-demo -w
```

```
NAME   URL                           MODE    DRIFT     VERSION   READY   AGE
demo   https://netbox.example.com    Apply   Correct   4.6.8     True    6s
```

`VERSION` filled in means the probe reached NetBox. `READY=True` means the token was accepted
and the version is supported. If it is not `True`, jump to
[when it says `Ready=False`](#when-it-says-readyfalse).

## 6. Create your first object

[`examples/site.yaml`](examples/site.yaml) is a complete, runnable manifest: it carries its
own namespace, Secret and endpoint, so you can also start from it directly. Here we reuse the
endpoint from step 5 and apply just the site:

```sh
kubectl apply -f - <<'EOF'
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSite
metadata:
  name: home
  namespace: netbox-demo
spec:
  endpointRef: demo
  name: Home
  slug: home
  status: active
  description: Home lab
EOF
```

```sh
kubectl get netboxsite -n netbox-demo
```

```
NAME   SLUG   STATUS   ID   READY   AGE
home   home   active   12   True    3s
```

`ID` is the NetBox id the operator created. Open
`https://netbox.example.com/dcim/sites/home/` and it is there.

**Now try changing it in NetBox.** Edit the description in the NetBox UI, and within one
`resyncPeriod` (10 minutes by default) the operator puts it back — that is the difference
between this and a one-shot import. Fields your spec never mentions are left exactly as
NetBox has them, so the operator can share an object with a human; see
[field ownership](concepts/field-ownership.md).

## When it says `Ready=False`

`Ready=False` is how this operator reports **everything**. It never crashes and never fails
silently, so the reason string on the condition is your diagnosis:

```sh
kubectl describe netboxsite home -n netbox-demo
kubectl get netboxsite home -n netbox-demo \
  -o jsonpath='{range .status.conditions[*]}{.type}={.status}/{.reason}: {.message}{"\n"}{end}'
```

The five you are most likely to see on a first run:

| `Reason` | What happened | Fix |
|---|---|---|
| `SecretMissing` | The Secret is absent, **unlabelled**, or its namespace is not in `credentialNamespaces` | Step 4's label, or add the namespace and `helm upgrade` |
| `WaitingForRef` | Something this object references has not resolved yet. Look at the `RefsResolved` condition for which | Usually nothing — it is the normal state on a first apply, and clears itself |
| `WaitingForEndpoint` | The `NetBoxEndpoint` is not `Ready` | Fix the endpoint first; everything else is downstream of it |
| `RefDenied` | A reference crossed a namespace and no `NetBoxRefGrant` allows it | Create the grant, in the **target's** namespace |
| `Conflict` | An object already exists in NetBox that matches, and this CR did not ask to take it over | Set `spec.onConflict: Adopt`, or point the CR elsewhere |

Two things that are **not** failures, and surprise people: `DryRunPending` and
`ReportPending` are `Ready=False` by configuration, on an endpoint running in `mode: DryRun`
or `driftMode: Report`. Nothing is wrong; nothing was sent.

[Troubleshooting](troubleshooting.md) has every reason the operator emits, keyed on the
symptom, plus the logs and metrics to reach for.

## Where to go next

- **[Adopt an existing NetBox](operations/exporting.md).** `nbctl export` reads a live NetBox
  and writes CR manifests for you to review and commit. It writes files and nothing else.
- **[Start in `Report` mode](operations/gitops.md#driftmode-report-for-the-first-week).** If
  you are pointing this at a NetBox that matters, run it read-only for a week first and read
  what it *would* have changed.
- **[More examples](examples/README.md)** — contacts, racks, cables, custom fields, the IPAM
  remainder.
- **[The reference pages](README.md#reference)** — one per kind, with every field, its
  default, and what happens when you get it wrong.
- **[Installing](install.md)** — every chart value, upgrading, uninstalling, and what an
  uninstall does *not* do to your NetBox.
- **[Concepts](README.md#concepts)** — references, drift, deletion, claims, ownership. Read
  these when something surprises you, not before.
