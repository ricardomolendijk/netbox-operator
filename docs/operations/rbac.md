# RBAC and the operator's blast radius

What the operator can read, why credential Secrets need both a label and a listed
namespace, and what happens when one of them is missing.

## What the operator holds

Two objects, and between them the whole of it. The cluster-wide `ClusterRole`
(`config/rbac/role.yaml`, generated from the `+kubebuilder:rbac` markers):

| API group | Resource | Verbs | Why |
|---|---|---|---|
| `netbox.kubeforge.org` | `netboxendpoints`, `netboxregions`, `netboxsites`, `netboxtags` | `get`, `list`, `watch` (+ `patch`, `update` on the object kinds) | Its own kinds. |
| `netbox.kubeforge.org` | `*/status`, `*/finalizers` | `get`, `update`, `patch` | Conditions and finalizers. |
| `""` | `events` | `create`, `patch` | Events on the objects it reconciles. |
| `""` | `configmaps`, `coordination.k8s.io/leases` | `get`, `list`, `watch`, `create`, `update`, `patch`, `delete` — **in the operator's own namespace only** | Leader election. |

**There is no `secrets` rule in the `ClusterRole`.** Secret access is granted one namespace
at a time, by a `Role` and `RoleBinding` per namespace listed in
`config/rbac/credential-namespaces/namespaces.txt`:

| API group | Resource | Verbs | Scope |
|---|---|---|---|
| `""` | `secrets` | `get`, `list`, `watch` | Each listed namespace, and nothing else |

So the blast radius is now the listed namespaces. A compromised manager can read every
Secret in those, and no Secret anywhere else — where before NBO-072 it could read every
Secret in the cluster: every service-account token, TLS key and application credential.

All three verbs are load-bearing:

- `watch` is what makes a rotated token take effect without restarting the manager. It is
  not implied by `get` and `list`; a `Role` carrying only those two makes the API server
  refuse the watch (asserted in `TestNamespacedRoleCarriesTheInformersWatch`).
- `list` is what the informer issues before it watches. A label selector becomes a query
  parameter on that request, not a different verb, so it cannot be dropped.
- `get` is not issued in normal operation — every read is served from the informer — and is
  kept because it is strictly weaker than `list`, and because it is the verb
  `kubectl auth can-i get secrets` asks about.

Kubernetes RBAC cannot filter by label: a rule names resources and, at most, individual
object names. So the credential label narrows *what the operator caches and can see*, and
the namespaced `Role`s narrow *what its ServiceAccount is permitted to read*. Both are
needed, and they fail differently — see below.

### The grant is every Secret in the namespace, labelled or not

Said plainly, because the label and the `Role` are easy to read as one mechanism: the label
is a cache filter, not a permission boundary. What the `Role` above says is `get`, `list`
and `watch` on `secrets` in that namespace, and that means all of them. Anything holding
the operator's ServiceAccount token can read **every** Secret in **every** listed
namespace — unlabelled ones, and ones belonging to other applications, included.

`resourceNames` looks like the way to narrow that to the Secrets an endpoint actually
names, and it cannot be used here. RBAC matches `resourceNames` against the name in the
request, and a `LIST` or a `WATCH` over a collection carries no name: a rule with
`resourceNames` authorises neither. The informer LISTs and then WATCHes, so the rule has to
cover the collection, so it covers every Secret in the namespace. That is Kubernetes RBAC,
not a shortcut taken here — and it is why the namespace, not the Secret, is the unit this
whole page is about.

Two things follow, and both are worth acting on:

- **List namespaces that hold credentials, not namespaces that hold workloads.** A
  namespace dedicated to endpoint credentials keeps the grant to Secrets you meant to
  share. Listing a busy application namespace hands the operator its TLS keys and its
  database passwords too, and no label on them changes that.
- **The chart's default is `credentialNamespaces: [default]`**, which on most clusters is
  exactly such a busy namespace. It is a default to replace, not one to accept — see
  [installing](../install.md#the-secret-blast-radius).

`kubectl auth can-i --list -n <namespace> --as=$SA` prints what the grant really is. Note
that it says nothing about labels, because the grant does not.

## Adding a namespace

**A Helm install adds it to the value**, whole list at a time, because Helm replaces a list
rather than appending to it — and the namespace has to exist first, since a `Role` cannot
be created in one that does not:

```sh
kubectl create namespace team-a
helm upgrade netbox-operator ./charts/netbox-operator \
  --namespace netbox-operator-system --reuse-values \
  --set credentialNamespaces={default,team-a}
```

**A kustomize install** takes the same list from a file. One line, then regenerate:

```sh
echo team-a >> config/rbac/credential-namespaces/namespaces.txt
make manifests
```

That rewrites two generated files from the list, and they must agree or the operator is
broken in one of two ways — so one input produces both:

| Generated | What it is | If it were missing |
|---|---|---|
| `credential-namespaces/rbac.yaml` | A `Role` and `RoleBinding` per namespace | The manager's informer for that namespace is refused and it never syncs |
| `credential-namespaces/manager_env_patch.yaml` | `NETBOX_CREDENTIAL_NAMESPACES` on the Deployment | A grant nobody uses, and endpoints there still fail |

`make verify` fails if you edit the list and forget to regenerate, and
`TestGeneratedGrantMatchesTheNamespaceList` fails if the two ever disagree.

Then redeploy: `make deploy`, or `kustomize build config/default | kubectl apply -f -`. The
namespace has to exist first — a `Role` cannot be created in a namespace that does not, and
`kubectl apply` says so plainly.

Removing a namespace is the same edit in reverse, and note that `kubectl apply` will not
delete the `Role` it no longer emits — `kubectl delete -n <namespace> role,rolebinding
-l app.kubernetes.io/name=netbox-operator` does.

### Why the namespace list is deploy-time

The operator cannot widen its own RBAC: a `Role` the deployer did not create is a `Role`
that does not exist, so no amount of watching `NetBoxEndpoint`s would let it read a Secret
in a namespace nobody granted. The list is therefore configuration, and it is a plain text
file rather than a chart value because the manifests here are what
`kustomize build config/default` renders.

The Helm chart takes the same list as `credentialNamespaces` and renders the same two halves
from it — a `Role`/`RoleBinding` per namespace and `NETBOX_CREDENTIAL_NAMESPACES` — and
refuses `*` in both its `values.schema.json` and its templates, for the reason
`hack/credential-rbac.sh` refuses it here. See
[installing](../install.md#the-secret-blast-radius).

One structural consequence worth knowing before you move things around: the transformers
that namespace and prefix the operator's own resources live in `config/base`, not in
`config/default`. Kustomize applies a kustomization's transformers to everything it
accumulates, components included — so with `namespace:` in `config/default`, the
per-namespace credential `Role`s would all be rewritten into the operator's namespace,
which is exactly the grant they exist to avoid.

## Reading Secrets cluster-wide anyway

Nothing in the operator assumes its grant is narrow, and nothing assumes it is wide either.
To go back to a cluster-wide read — a cluster with too many credential namespaces to
enumerate, or one where they are created dynamically — you need both halves, deliberately:

```yaml
# An overlay of your own. Neither half ships here.
apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: netbox-operator-manager-role
rules:
- apiGroups: [""]
  resources: ["secrets"]
  verbs: ["get", "list", "watch"]
```

and `NETBOX_CREDENTIAL_NAMESPACES=*` on the manager, which switches the informer from one
per namespace to a single cluster-scoped one. The label selector still applies, so the
memory win of NBO-072's first half survives; the privilege win does not.

There is no way to get this by accident. An unset or empty `NETBOX_CREDENTIAL_NAMESPACES`
makes the manager exit at startup naming the file to edit, rather than quietly falling back
to a cluster-scoped informer that would then fail its first `LIST` with a bare `Forbidden`.

## The label is required, in every listed namespace

Every Secret a `NetBoxEndpoint` references — `spec.tokenSecretRef` and
`spec.tlsConfig.caBundleSecretRef` — must carry:

```yaml
metadata:
  labels:
    netbox.kubeforge.org/endpoint-credential: "true"
```

The manager's Secret informers select on that label, so they only ever hold labelled
Secrets in listed namespaces. Without the selector the informers would hold **every**
Secret in those namespaces and the manager's memory would scale with their Secret count
rather than with the number of endpoints.

The key is prefixed with the API group because credential Secrets are commonly shared with
other consumers, and an unprefixed `endpoint-credential` would collide with whatever else
labels them.

Labelling an existing Secret:

```sh
kubectl label secret netbox-token -n default netbox.kubeforge.org/endpoint-credential=true
```

## The failure modes when you forget

Both are `Ready=False` with reason `SecretMissing`, because to the reader they are one
problem — the operator cannot read that Secret — and the message says which:

**A namespace that is not in the list.** Caught before the read is attempted, so the
message can name the namespace, what is granted instead, and the fix in both install
paths' terms — the operator cannot tell which one deployed it, and the artefact to change
is different in each:

```
credential namespace not granted: the operator has no Role for Secrets in namespace
"team-a" and is granted default; grant it and redeploy -- Helm: `--set
credentialNamespaces={default,team-a}`; kustomize: add "team-a" to
config/rbac/credential-namespaces/namespaces.txt and run `make manifests`
(see docs/operations/rbac.md)
```

The `--set` carries the whole list rather than the missing entry because Helm replaces a
list value: `{team-a}` alone would revoke every namespace already granted.

**A Secret without the label.** The Secret is not in the informer's store, so the read
comes back `NotFound` — indistinguishable, at the API level, from a Secret that does not
exist:

```
reading token secret default/netbox-token: secrets "netbox-token" not found; the secret
may exist but be invisible to the operator, which reads only Secrets labelled
netbox.kubeforge.org/endpoint-credential=true (see docs/operations/rbac.md)
```

If the Secret is there and the name matches, the label is missing. The condition is
deliberately ambiguous about which of the two applies, because the operator cannot tell
without an uncached read of a Secret it is trying not to read.

A third, rarer message covers the namespace being listed while the cluster disagrees — the
`Role` was never applied, or was deleted:

```
reading token secret team-a/netbox-token: secrets "netbox-token" is forbidden: ...; the
operator's namespace list includes team-a but the cluster grants it nothing there, so the
Role the list promised was never applied or has been deleted: `helm upgrade` re-renders
it, or apply the Role and RoleBinding from config/rbac/credential-namespaces (see ...)
```

Reading any of them:

```sh
kubectl get netboxendpoint homelab -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
```

Note that a namespace in `NETBOX_CREDENTIAL_NAMESPACES` whose `Role` is missing entirely
stops the *manager*, not just the endpoint: the informer for that namespace cannot sync, so
`kubectl logs` shows `failed waiting for *v1.Secret Informer to sync`. That is the case the
two generated files exist to prevent.

## Proving it

Three checks, in decreasing order of how much you have to trust them.

1. **`TestSecretInAnUnlistedNamespaceIsForbidden`** (`internal/controller/secretcache_test.go`)
   creates a Secret in an unlisted namespace, grants a `Role` in a listed one, and asserts
   that the operator's identity is refused `get`, `list` and `watch` on the first. It runs
   against envtest's real API server with RBAC enabled, impersonating the ServiceAccount,
   so it is the same authorization decision a cluster makes — and it runs in CI on every
   commit rather than being a paragraph somebody has to believe. It fails if the
   cluster-wide rule comes back.

2. **`TestNamespacedRoleCarriesTheInformersWatch`**, in the same file, is the one that
   decides whether this design works at all. Under nothing but a namespaced `Role` it shows
   that a namespaced `WATCH` is authorised, that a cluster-scoped `LIST` or `WATCH` is
   `Forbidden`, that a scoped informer built from `SecretScope.CacheOptions()` syncs and
   serves reads, and that a rotated token arrives over that watch. The middle assertion is
   the reason `cache.Options.ByObject[...].Namespaces` is mandatory here rather than an
   optimisation: a cluster-scoped informer asks
   `GET /api/v1/secrets?watch=true`, which RBAC evaluates at the cluster scope, where no
   `Role` reaches.

3. **Against a live cluster**, what the ServiceAccount may do:

   ```sh
   SA=system:serviceaccount:netbox-operator-system:netbox-operator-controller-manager

   # An unrelated namespace: no. Before NBO-072 this answered yes.
   kubectl auth can-i get secrets -n kube-system --as=$SA
   kubectl auth can-i list secrets --all-namespaces --as=$SA

   # A listed namespace: yes, all three verbs.
   kubectl auth can-i get secrets -n default --as=$SA
   kubectl auth can-i watch secrets -n default --as=$SA
   ```

   `kubectl auth can-i --list -n kube-system --as=$SA` is the fuller answer: `secrets`
   should not appear in it.
