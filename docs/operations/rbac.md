# RBAC and the operator's blast radius

What the operator can read, why credential Secrets need a label, and how to narrow the
grant further. Tracked as [#100](https://github.com/ricardomolendijk/netbox-operator/issues/100)
(NBO-072).

## What the operator holds

The generated `ClusterRole` (`config/rbac/role.yaml`) is the whole of it:

| API group | Resource | Verbs | Why |
|---|---|---|---|
| `netbox.kubeforge.org` | `netboxendpoints` | `get`, `list`, `watch` | Its own kind. |
| `netbox.kubeforge.org` | `netboxendpoints/status` | `get`, `update`, `patch` | Conditions. |
| `""` | `events` | `create`, `patch` | Events on the objects it reconciles. |
| `""` | `secrets` | `get`, `list`, `watch` | The API token, and optionally a CA bundle, per endpoint. |

The last row is the blast radius. It is cluster-wide, so a compromised manager can read
every Secret in the cluster: every service-account token, TLS key and application
credential. The operator *needs* one key out of one Secret per `NetBoxEndpoint`, and the
gap between those two statements is what this page is about.

All three Secret verbs are load-bearing and none can be dropped today:

- `watch` is what makes a rotated token take effect without restarting the manager.
- `list` is not optional even with a scoped cache: a label-selected informer issues a
  `LIST` (with `labelSelector` on the query) before it `WATCH`es, so removing `list`
  breaks the informer at startup.
- `get` is what an uncached read uses; the reconciler's own reads are served from the
  informer.

Kubernetes RBAC cannot filter by label — a rule names resources and, at most, individual
object names — so labelling credential Secrets narrows *what the operator caches and can
read*, but not *what its ServiceAccount is permitted to read*. Only namespaced `Role`s fix
the permission itself; see [Option B](#option-b-namespace-enumerated-roles) below.

## The label is required

Every Secret a `NetBoxEndpoint` references — `spec.tokenSecretRef` and
`spec.tlsConfig.caBundleSecretRef` — must carry:

```yaml
metadata:
  labels:
    netbox.kubeforge.org/endpoint-credential: "true"
```

The manager configures `cache.Options.ByObject` with that label selector, so its Secret
informer only ever holds labelled Secrets. Without it the informer caches **every** Secret
in the cluster and the manager's memory scales with the cluster's Secret count rather than
with the number of endpoints — on a busy cluster, comfortably the operator's largest single
allocation.

The key is prefixed with the API group because credential Secrets are commonly shared with
other consumers, and an unprefixed `endpoint-credential` would collide with whatever else
labels them.

Labelling an existing Secret:

```sh
kubectl label secret netbox-token -n default netbox.kubeforge.org/endpoint-credential=true
```

## The failure mode when you forget

An unlabelled Secret is not in the informer's store, so a read through it comes back
`NotFound` — indistinguishable, at the API level, from a Secret that does not exist. The
endpoint therefore goes `Ready=False` with reason `SecretMissing` and a message that names
the label:

```
reading token secret default/netbox-token: secrets "netbox-token" not found; the secret
may exist but be invisible to the operator, which reads only Secrets labelled
netbox.kubeforge.org/endpoint-credential=true (see docs/operations/rbac.md)
```

```sh
kubectl get netboxendpoint homelab -o jsonpath='{.status.conditions[?(@.type=="Ready")]}'
```

If the Secret is there and the name matches, the label is missing. The condition is
deliberately ambiguous about which of the two causes applies, because the operator cannot
tell without an uncached read of a Secret it is trying not to read.

## Proving the negative case

Two independent checks:

1. `TestUnlabelledSecretIsInvisibleAndNamesTheLabel` in
   `internal/controller/netboxendpoint_controller_test.go` creates an unlabelled Secret
   with a direct API client, shows it exists, and asserts that a read through the
   manager's client returns `NotFound` while a labelled Secret in the same namespace is
   readable. Removing the cache scoping makes that test fail.
2. Against a live cluster, what the *ServiceAccount* may do — which is the separate,
   larger question the cache cannot answer:

   ```sh
   kubectl auth can-i get secrets --all-namespaces \
     --as=system:serviceaccount:netbox-operator-system:netbox-operator-controller-manager
   ```

   Today that answers `yes`. Under option B it answers `no`, and `yes` only for the
   namespaces named in the chart's values.

## Option B: namespace-enumerated Roles

What NBO-004 originally asked for and what a security reviewer will expect: a `Role` plus
`RoleBinding` per namespace that holds endpoint credentials, replacing the cluster-wide
`secrets` rule entirely. That is deliberately **not** implemented here. It buys the half
this change does not:

| | Label-selected cache (shipped) | Namespace-enumerated Roles |
|---|---|---|
| Manager memory | Scales with labelled Secrets | Unchanged by itself |
| ServiceAccount privilege | Every Secret in the cluster | Only the enumerated namespaces |
| Failure when misconfigured | `SecretMissing` naming the label | `Forbidden`, mapped back to a values file |
| Adding an endpoint in a new namespace | Label the Secret | Chart change and redeploy |

The blocker is that the namespace list is deploy-time configuration, which belongs with
the Helm chart (NBO-061) rather than with a hand-maintained `config/rbac` overlay. The two
compose: the scoped cache keeps working unchanged once the grant is narrowed, and the
combination is the intended end state before 1.0.

In the meantime, an operator who wants the privilege narrowed now can delete the `secrets`
rule from the `ClusterRole` after deploying and add their own `Role`/`RoleBinding` pair per
credential namespace, with verbs `get`, `list`, `watch`. Nothing in the operator assumes the
grant is cluster-wide.
