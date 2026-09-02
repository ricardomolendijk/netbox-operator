# Troubleshooting

Symptom first, then the command that shows the cause.

Almost every problem this operator has shows up in one place: a `Ready` condition that is
`False`, with a `Reason` naming what went wrong. So the fastest route to an answer is to read
the reason and look it up.

```sh
kubectl get netboxendpoints,netboxsites,netboxprefixes -A     # or any kind you applied
kubectl describe <kind> <name> -n <namespace>                 # read the Conditions block
```

- **[Jump to the reason index](#the-reason-index)** if you already have a reason string.
- **[Start here](#start-here)** if you do not.

For *why* the operator reports rather than errors, and why the retry delays differ by reason,
see [reconciliation](concepts/reconciliation.md).

## Start here

### Is the connection up?

Nothing else can work until the `NetBoxEndpoint` every object references is `Ready`.

```sh
kubectl get netboxendpoints -A
```

The printer columns are `URL`, `MODE`, `DRIFT`, `VERSION`, `READY`, `AGE`. `READY` is the one
that matters; `VERSION` being populated tells you the probe reached NetBox at least once, and
`DRIFT` tells you whether this endpoint is allowed to correct drift at all.

```sh
kubectl describe netboxendpoint <name> -n <namespace>
```

An endpoint carries up to four conditions:

| Condition | Says |
|---|---|
| `Ready` | Always current, and the one to act on |
| `Authenticated` | Whether the token was accepted |
| `VersionSupported` | Whether NetBox's version is inside `[4.2.0, 5.0.0)` |
| `ProvenanceReady` | Whether the tag and custom fields `spec.managedBy` asks for exist. **Absent entirely when `spec.managedBy` is unset**, which is the default |

`Authenticated` and `VersionSupported` are set to `Unknown` whenever the reconcile failed
before it could establish them, with a reason saying so — `not probed: authentication failed
first`, for instance. So they never disagree with `Ready`: anything other than `False` on
either means the operator did not get far enough to have an opinion, never that a stale
answer is being reported as current.

Note the shape that catches people out: `BootstrapDisabled` and `BootstrapFailed` are
`Ready=False` reasons that leave `Authenticated=True` **and** `VersionSupported=True`. The
connection is fine; the provenance bootstrap is not.

### Is the object making progress?

```sh
kubectl get netboxendpoint <name> -n <namespace> -o jsonpath='{.status}' | jq
```

If `status.observedGeneration` is behind `metadata.generation`, the operator has not finished
a pass since your last edit. Compare them directly — this works on any kind:

```sh
kubectl get <kind> <name> -n <namespace> \
  -o custom-columns='GEN:.metadata.generation,OBSERVED:.status.observedGeneration,READY:.status.conditions[?(@.type=="Ready")].status,REASON:.status.conditions[?(@.type=="Ready")].reason'
```

### Nothing at all is happening

| Symptom | Command | What it means |
|---|---|---|
| `READY` is empty and `describe` shows no conditions | `kubectl logs -n <ns> deploy/<manager>` | No reconcile has run. The controller is not running, not watching this namespace, or the manager is crash-looping. Check the pod, then leader election if you run more than one replica |
| `no matches for kind "NetBoxSite"` on apply | `kubectl get crd \| grep netbox.kubeforge.org` | The CRDs are not installed. They are **not** part of the Helm release — see [installing](install.md) |
| The manager pod exits at startup with `no matches for kind` | as above | Same cause: the chart was installed with `crds.check=false` and no CRDs |

## The reason index

Every reason below is a string the code actually emits, and every one of them appears
verbatim in a condition's `reason` field. Anything not on this page is not something this
operator produces.

Read a reason together with its **condition type** — several strings are reused. `Conflict`
is a condition type *and* a `Ready` reason *and* a `ChildrenReady` reason; `Truncated` and
`APIError` mean different things on a `NetBoxSweep` than on an object.

### The five you are most likely to hit

| `Reason` | On | Means | Fix |
|---|---|---|---|
| [`SecretMissing`](#secretmissing) | `Ready=False` on a `NetBoxEndpoint` | The token or CA-bundle Secret is absent, unlabelled, or in a namespace nobody granted | Create it, label it, or add the namespace to `credentialNamespaces` |
| [`WaitingForRef`](#waitingforref) | `Ready=False` on an object CR | One of its references has not resolved. Look at `RefsResolved` for which and why | Usually nothing — it is the normal first-apply state |
| [`RefDenied`](#refdenied) | `RefsResolved=False` | A cross-namespace reference with no `NetBoxRefGrant` permitting it | Create the grant in the *target* namespace |
| [`RefKindUnavailable`](#refkindunavailable) | `RefsResolved=False` | The target Kind has no descriptor in this build, or its CRD is not installed | Re-run `make install-crds`, or upgrade the operator |
| [`Conflict`](#conflict) | `Ready=False` on an object CR | Several NetBox objects match the natural key, or one matches and you did not ask to adopt it | Decide: `onConflict: Adopt`, or make the key unambiguous |

### `SecretMissing`

`Ready=False`, `Authenticated=False`, `VersionSupported=Unknown`. Retries every 30s.

Three different situations produce this one reason, and the operator genuinely cannot tell
them apart — distinguishing "absent" from "unlabelled" would need an uncached read of the very
Secret the scoped cache exists to avoid. So the message covers all three and you check them in
order:

```sh
kubectl get secret <name> -n <ns> --show-labels
kubectl auth can-i get secrets -n <ns> \
  --as=system:serviceaccount:netbox-operator-system:netbox-operator
```

1. **The Secret does not exist**, or is in another namespace. `spec.tokenSecretRef` and
   `spec.tlsConfig.caBundleSecretRef` are same-namespace only, by design — the message says
   which of the two it was looking for.
2. **The Secret exists but is not labelled.** The operator reads Secrets through a
   label-scoped informer, so an unlabelled Secret is invisible to it even though `kubectl`
   shows it:
   ```sh
   kubectl label secret <name> -n <ns> netbox.kubeforge.org/endpoint-credential=true
   ```
3. **The namespace is not granted.** The operator's `ClusterRole` carries no `secrets` rule at
   all; access is a `Role` per namespace, from the chart's `credentialNamespaces`. An endpoint
   in an ungranted namespace reports `SecretMissing` naming the namespace. See
   [RBAC](operations/rbac.md).

### `WaitingForRef`

`Ready=False` on an object CR. This is the single `Ready`-level reason for **every** reference
failure, and it deliberately says nothing about which one — that is on `RefsResolved`:

```sh
kubectl get <kind> <name> -n <ns> \
  -o jsonpath='{range .status.conditions[?(@.type=="RefsResolved")]}{.reason}{": "}{.message}{"\n"}{end}'
```

| `RefsResolved` reason | Means | Fix | Retries? |
|---|---|---|---|
| `AllResolved` | Every declared reference resolved, or none is declared | — | — |
| `RefNotReady` | The target CR exists but has no NetBox id yet | Nothing. **This is the normal state on a first apply** and clears on its own | on a watch |
| `RefNotFound` | No CR of that name, no NetBox object for that slug or lookup, or a raw id NetBox does not hold | Apply the target, or fix the name | timer |
| `RefTargetFailed` | The target CR *has* an id but is itself stuck on `Conflict`, `AdoptOnly` or `Invalid` | Fix the target; this one clears itself | on a watch |
| `RefAmbiguous` | A slug or lookup matched several NetBox objects. The message names every id | Narrow the lookup, or point at a sibling CR by name | timer |
| [`RefDenied`](#refdenied) | Cross-namespace with no grant | Create a `NetBoxRefGrant` | **terminal** — clears on a grant watch |
| `RefCycle` | The references form a ring. Every member reports it, and the message names the ring in order | Break the cycle | **terminal** |
| `RefDepthExceeded` | The ref graph was too deep or too wide for the cycle check | Flatten the hierarchy | **terminal** |
| `RefTypeNotAllowed` | A polymorphic ref names a target the NetBox column will not take | Point it at a legal type — the message lists them | **terminal** |
| [`RefKindUnavailable`](#refkindunavailable) | The target Kind is not in this build, or its CRD is not installed | Install the CRDs, or upgrade | timer |
| `NotImplemented` | A declared ref the resolver neither resolved nor refused | A bug. Please file it | timer |
| `Invalid` | The failure was not a resolver error at all | A bug. Please file it | timer |

"Terminal" means there is no retry timer: the operator is not going to change its mind on its
own, and only an edit or the arrival of the missing object clears it. That is a feature — a
cycle retried every thirty seconds is a storm.

[references](concepts/references.md) has the full model; [stuck
references](operations/stuck-references.md) is the walkthrough for one that will not clear.

### `RefDenied`

`RefsResolved=False`. A reference crossed a namespace boundary and no
[`NetBoxRefGrant`](reference/netboxrefgrant.md) permits it. The grant lives in the
**target's** namespace — the namespace being referenced, not the one referencing.

```sh
kubectl get netboxrefgrants -A
```

The [admission webhook](operations/admission-webhooks.md) warns about this at apply time, if it is
enabled. Without cert-manager it is not, and the first you hear of it is this condition — which
is the designed backstop, not a failure: a denied reference performs **zero** NetBox writes.

### `RefKindUnavailable`

`RefsResolved=False`. The Kind on the other end of the reference has no Descriptor in this
build of the operator, or its CRD is not installed on the cluster.

```sh
kubectl get crd | grep netbox.kubeforge.org | wc -l    # expect 64
kubectl api-resources --api-group=netbox.kubeforge.org
```

If the count is short, the CRDs and the chart are out of step — the CRDs are applied by you,
and `helm upgrade` never touches them. Re-run `make upgrade-crds` (or apply the bundle) and
then upgrade the chart. See [installing](install.md#crds-and-why-they-are-not-in-the-chart).

### `Conflict`

`Conflict` is three different things, and which one you have depends on where it appears.

**As a `Ready=False` reason on an object CR.** The engine found something it will not write
over:

| Message says | Cause | Fix |
|---|---|---|
| several objects match | The natural key is ambiguous in NetBox | Delete or rename the duplicates, or give the CR a more specific key |
| one object matches and adoption was not requested | The object already exists in NetBox and this CR did not ask to take it over | Set `spec.onConflict: Adopt` if you want it, or point the CR elsewhere |
| a protected foreign key | NetBox refused the write because of `on_delete=PROTECT` | Deal with the referencing object first |
| two spec declarations for one column | Two fields of the same spec both want to write one NetBox column | The message names both. Remove one |

**As a condition type, `Conflict=True`.** This is the [multi-writer](operations/multi-writer.md) report,
and its reasons are `ForeignCluster` (the object's cluster stamp names a different cluster) or
`ForeignOwner` (our cluster, a different CR). The condition is *removed* rather than set to
`False` when the conflict clears, so its presence is the signal.

**As a `ChildrenReady=False` reason.** Two inline entries collide — a duplicate key, or two
claims on one column. See [inline children](concepts/inline-children.md).

### Every other `Ready=False` reason, by kind

#### On a `NetBoxEndpoint`

| `Reason` | Conditions it sets | Requeue | Means |
|---|---|---|---|
| `Ready` | `Ready=True`, `Authenticated=True`, `VersionSupported=True` | `spec.resyncPeriod`, default `10m` | Working |
| `SecretMissing` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown` | 30s | [above](#secretmissing) |
| `CABundleMissing` | `Ready=False`, `Authenticated=Unknown` | 30s | The CA bundle Secret is absent. Distinct from `SecretMissing` on purpose: the token read fine, so you are pointed at the right Secret |
| `TokenMissing` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown` | 30s | The Secret exists but has no usable value under the key. Default key is `token`; an empty value counts as missing |
| `AuthError` | `Ready=False`, `Authenticated=False`, `VersionSupported=Unknown` | 2m | 401 or 403. The token is wrong, revoked, expired or disabled. A read-only token is *not* this symptom — `/api/status/` needs only an authenticated read, so it probes `Ready=True` and fails later, on the first write |
| `ProbeFailed` | `Ready=False`, `Authenticated=Unknown`, `VersionSupported=Unknown` | 30s | NetBox is unreachable, or answered with something that is not NetBox: connection refused, no such host, timeout, TLS failure, a 404 on `/api/status/`, or an HTML login page. The message carries the first 200 characters of what came back |
| `VersionUnsupported` | `Ready=False`, `VersionSupported=False`, `Authenticated=True` | 10m | NetBox is outside `[4.2.0, 5.0.0)` |
| `VersionUnparseable` | `Ready=False`, `VersionSupported=False`, `Authenticated=True` | 10m | The `netbox-version` header did not parse as `major.minor[.patch]`. Usually a proxy answering instead of NetBox. The offending string is in `status.netboxVersion` |
| `InvalidConfig` | `Ready=False`, `Authenticated=Unknown`, `VersionSupported=Unknown` | 2m | `spec.url` is empty, unparseable, not `http`/`https`, names no host, or carries a query string, a fragment or userinfo — none of which belongs in a NetBox base URL, and a query string in particular would choose the request path ([#298](https://github.com/ricardomolendijk/netbox-operator/issues/298)). CEL on the CRD rejects the same four at apply time, so reaching this condition means the object predates the rule. Or the CA bundle Secret exists but is missing its key (default `ca.crt`) or holds no usable certificate |
| `BootstrapDisabled` | `Ready=False`, `ProvenanceReady=False`, `Authenticated=True`, `VersionSupported=True` | 10m | `spec.managedBy.bootstrap: false` and a definition the stamp needs does not exist. The message names each one |
| `BootstrapFailed` | `Ready=False`, `ProvenanceReady=False` | 2m | NetBox refused the bootstrap — usually a token without `extras.add_customfield` |
| `Provisioned` | `ProvenanceReady=True` | — | Every definition `spec.managedBy` asks for exists |
| `BootstrapSuppressed` | `ProvenanceReady=False` only — **does not fail `Ready`** | — | `mode: DryRun` or `driftMode: Report`, so nothing could be created |

Retry delays are per reason on purpose: a wrong token retried every 30 seconds is a
credential-stuffing pattern against your own NetBox. An edit reconciles immediately either
way, so you never actually wait out a 10-minute timer.

#### On an object CR

| `Reason` | Means | Retries? |
|---|---|---|
| `Synced` | `Ready=True`. The NetBox object exists and matches | resync |
| `WaitingForRef` | [above](#waitingforref) | see the ref reason |
| `WaitingForEndpoint` | The `NetBoxEndpoint` this object references is not `Ready`. Fix the endpoint | 30s |
| `WaitingForKey` | No natural-key candidate is usable, so the engine cannot tell whether the object exists. It writes **nothing** rather than risk a duplicate. See [lookups](concepts/lookups.md) | timer |
| `AdoptOnly` | `spec.onConflict: AdoptOnly` and nothing in NetBox matched. The operator will not create it | timer |
| `Conflict` | [above](#conflict) | timer |
| `Invalid` | NetBox returned a 400, or the spec asks for something the engine cannot express. The message says which | timer |
| `Truncated` | A lookup paginated past the client's page cap; nothing was written | timer |
| `APIError` | NetBox unreachable, 5xx, 429, or a 404 after the object had been located. Also a failed child write | backoff |
| `ReservedByOperator` | The CR names the `k8s-managed` tag or one of the provenance custom fields the bootstrap owns. Pick another name — see [provenance](operations/provenance.md) | timer |
| `DeferredFieldPending` | The object exists; a deferred field has not been PATCHed on yet. `status.deferredPending` names them. Normal, and transient | short |
| `DryRunPending` | The endpoint is `mode: DryRun`. **This is not a failure** — the write was reported, not sent | resync |
| `ReportPending` | The endpoint is `driftMode: Report`. **Also not a failure** | resync |

`DryRunPending` and `ReportPending` are `Ready=False` **by configuration**. If you run an
endpoint in `Report` mode, do not alert on `Ready=False` — alert on `DriftDetected=True`
instead.

The other conditions an object carries:

| Condition | Reasons | Says |
|---|---|---|
| `Synced` | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` | Whether the live object matches the spec, and whether anything was sent |
| `DriftDetected` | `DriftDetected` when `True`; `NoDrift` / `DriftCorrected` when `False` | NetBox differs from the spec and nothing was sent. The message is the change set, `field: old → new`. See [drift](concepts/drift.md) |
| `ParentOwned` | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` | Whether deleting the containment parent will garbage-collect this object. `CascadeUnavailable` means the parent is in another namespace, or was written as a `slug`/`lookup`/raw `id` rather than a sibling CR name |
| `ChildrenReady` | `AllReady`, `PendingChildren`, `Conflict`, `PruneBlocked`, `APIError` | For kinds with inline children. `PendingChildren` is normal on a first apply; `PruneBlocked` means the prune wanted to delete more children than the parent declares and so deleted **none** |
| `Deleting` | see below — **only ever `False`** | Why a delete is not finishing |

#### On a claim

`Allocated` and `Ready` carry the same reason on failure.

| `Reason` | Means |
|---|---|
| `AddressAllocated` | `Ready=True`, `Allocated=True`. Done |
| `ReclaimedByIdentity` | An object already carrying this claim's allocation identity was found and adopted, rather than a second allocation being made. This is what makes a cluster rebuild safe |
| `AllocationPending` | Nothing allocated yet and nothing is wrong: first pass, or waiting on a ref |
| `PoolExhausted` | No free object in the pool. Not terminal — a 10m timer *and* a watch on the pool |
| `PoolNotAllocatable` | The pool has `mark_utilized`, or a `status` the claim kind does not accept. No override |
| `ReclaimedOutsidePool` | An object with this claim's identity exists, but outside the pool the claim now names. Somebody moved the pool, or the claim |
| `AllocationConflict` | More than one NetBox object carries this claim's identity. The operator never allocates and never deletes here — it wants a human |
| `ForeignAllocation` | A given `spec.allocationIdentity` names an object stamped to another CR or cluster |
| `IdempotencyKeyUnavailable` | The endpoint has nowhere to store an allocation identity, so a POST could not be made exactly-once. **Zero POSTs**, no override. Set `spec.managedBy.allocationIdentityField` on the endpoint |
| `AllocationContended` | `NetBoxIPRangeClaim` only: every computed placement lost a race to another writer. Distinct from `PoolExhausted` |

[claims](concepts/claims.md) explains each of these and what to go and fix.

#### On a `NetBoxSweep`

| `Reason` | Means |
|---|---|
| `Complete` | `Ready=True`. Every listed kind was scanned in full |
| `Scheduled` / `Suspended` | On the `Suspended` condition. `spec.suspend: true` deliberately leaves `Ready` untouched |
| `EndpointNotReady` | The endpoint is missing or has no usable client |
| `EndpointDryRun` | The endpoint is `mode: DryRun`, so every object would look unclaimed. Refused rather than reported wrongly |
| `DriftOff` | The endpoint's `driftMode: Off` |
| `ProvenanceDisabled` | `spec.managedBy` writes no stamp, or the custom fields do not exist. Nothing to sweep on |
| `UnknownKind` | A kind in `spec.kinds` has no registered Descriptor. The whole run is refused |
| `KindNotStampable` | A kind's NetBox model has no `custom_fields` column — `extras.Tag` is one. Expected and permanent for those kinds |
| `Truncated` | A list paginated past the page cap |
| `Timeout` | The run exceeded `spec.timeout` |
| `APIError` | NetBox unreachable, rate limiting, or failing |

[sweeps](operations/sweeps.md) has the whole model, including why a sweep reports and never deletes.

## `kubectl delete` is hanging

The `Deleting` condition is only ever `False` — it exists to say why a delete has not
finished. `kubectl get <kind> <name> -o jsonpath='{.status.conditions}' | jq` shows it.

| `Reason` | Means | Get out of it by |
|---|---|---|
| `Protected` | NetBox refused the delete: another object references this one through a protected foreign key. The message carries NetBox's own body | Deleting the referencing object first. The retry backs off and completes on its own — this is **not** an error to retry faster. When the blockers are CRs in this cluster: `kubectl annotate <kind> <name> netbox.kubeforge.org/cascade-delete=true` and the operator deletes them for you ([deletion](concepts/deletion.md#cascading-a-refused-delete)) |
| `Cascading` | The delete was refused, `cascade-delete` is set, and the CRs referencing this one have been deleted and not yet finished going | Waiting. Their own finalizers remove their NetBox objects and this delete retries |
| `PendingDependents` | The child CRs this object materialised still exist | Waiting. They are being deleted |
| `WaitingForEndpoint` | The object is real and its id is known, so the finalizer holds rather than orphaning it in NetBox | Fixing the endpoint. To force it through and accept the orphan: `kubectl annotate <kind> <name> netbox.kubeforge.org/skip-finalizer=true` |
| `DataLossBlocked` | The delete would destroy data NetBox will not warn about — today that is `NetBoxCustomField`, whose deletion drops every value of it | `kubectl annotate <kind> <name> netbox.kubeforge.org/allow-data-loss=true`, deliberately, once you have read what goes |

If you want the NetBox objects to survive a `kubectl delete`, that is
`spec.deletionPolicy: Retain` on the CR *before* you delete it.
[deletion](concepts/deletion.md) has the full order of precedence.

## Events

Conditions say what the state is; Events say what happened. They are the fastest way to see a
sequence rather than a snapshot:

```sh
kubectl get events -n <ns> --field-selector involvedObject.name=<name> --sort-by=.lastTimestamp
```

Normal: `Created`, `Adopted`, `Updated`, `Recreated`, `Deleted`, `Retained`,
`NothingToDelete`, `DriftDetected`, `ChildMaterialised`, `ChildPruned`, `Allocated`,
`AllocationReclaimed`, `OrphansFound`.

Warning: `Conflict`, `ConflictSustained`, `Invalid`, `DeleteBlocked`, `FinalizerSkipped`,
`ChildFieldReverted`, `PoolExhausted`, `PoolNotAllocatable`, `ReclaimedOutsidePool`,
`AllocationConflict`, `ForeignAllocation`, `AllocationContended`, `PoolUnexpectedStatus`,
`SweepRefused`.

Three are worth knowing about specifically:

- **`ChildFieldReverted`** means somebody hand-edited a field on a materialised child that the
  parent owns, and the materialiser put it back. The Event names the fields. Edit the parent.
- **`AddressRetained`** is `Normal` for a `deletionPolicy: Retain`, and a **`Warning`** for a
  `Delete` the operator gave up on — that second one is a leaked address and wants a look.
- **`DeleteBlocked`** and **`ConflictSustained`** are each emitted exactly once, at a
  threshold, rather than on every retry. Their absence does not mean the condition cleared.

A `NetBoxEndpoint` reuses its condition reason as the Event reason, and emits only on
transition.

## Confirming which Secret is actually in use

The controller indexes endpoints by both referenced Secret names. To see the reference the
operator resolves, rather than the one you think you set:

```sh
kubectl get netboxendpoint <name> -n <namespace> \
  -o jsonpath='{.spec.tokenSecretRef.name}{"/"}{.spec.tokenSecretRef.key}{"\n"}'
```

An empty key means the default, `token`. Then check the value is non-empty:

```sh
kubectl get secret <secret> -n <namespace> -o jsonpath='{.data.token}' | base64 -d | wc -c
```

Anything other than a positive byte count produces `TokenMissing`.

Rotation is watched: editing either Secret takes effect on the next reconcile, with no
restart. If it did not, the Secret you edited is not the one referenced, or it is in another
namespace.

## Forcing a reconcile

There is no `kubectl reconcile`. Any change to the object enqueues it, so an annotation edit is
the standard way:

```sh
kubectl annotate <kind> <name> -n <namespace> \
  netbox.kubeforge.org/force-sync="$(date -u +%FT%TZ)" --overwrite
```

`force-sync` is **not** a key the operator recognises — any annotation would do. It works
because an annotation edit is a write, and a write enqueues. Note that it does **not** bump
`metadata.generation` — only spec changes do — so confirm the pass happened from the
condition's `lastTransitionTime` or the manager log, not from `observedGeneration`.

The annotations the operator *does* read are `netbox.kubeforge.org/skip-finalizer`,
`netbox.kubeforge.org/parent-ownership` and `netbox.kubeforge.org/allow-data-loss`.

## Reading the logs

Every log line carries a `controller` field naming the controller that emitted it — the
endpoint controller is `netboxendpoint`, and each object kind has its own.

```sh
kubectl logs -n <ns> deploy/<manager> | grep '"controller":"netboxendpoint"'
kubectl logs -n <ns> deploy/<manager> | grep 'endpoint ready'        # one line per successful probe
kubectl logs -n <ns> deploy/<manager> | grep 'endpoint not ready'    # failures, with the classified reason
kubectl logs -n <ns> deploy/<manager> -f | grep '"name":"<object-name>"'
```

Request bodies are logged at `-v=1` only, through a tested redaction pass rather than a
convention (`internal/netbox/do.go`): `auth_psk`, `psk`, `preshared_key`, `password`, `token`,
`secret`, `private_key` and `api_key` are masked, and `custom_fields` collapse to their key
names. The API token itself never appears at any level. Raise verbosity with
`--zap-log-level=1`.

## Metrics

The manager serves Prometheus metrics on `--metrics-bind-address`. The Helm chart sets it and
renders a `Service` by default (`metrics.enabled: true`); the kustomize path does not.

| Metric | Use |
|---|---|
| `controller_runtime_reconcile_total{result="requeue_after"}` | Normal operation. **Every failure path lands here, not in `error`** |
| `controller_runtime_reconcile_total{result="error"}` | Should be near zero. A rising value means the operator's own machinery is failing — status-update conflicts, informer-cache reads — not that NetBox is down |
| `controller_runtime_reconcile_errors_total` | Same signal. Safe to alert on precisely because NetBox's uptime does not feed it |
| `controller_runtime_reconcile_time_seconds` | A slow NetBox shows up here. A single reconcile can legitimately take minutes — see below |

[observability](operations/observability.md) has the operator's own metrics, which are the ones that say
something about NetBox rather than about controller-runtime.

## A slow NetBox delays other endpoints

The endpoint controller runs with controller-runtime's default `MaxConcurrentReconciles` of 1,
so endpoints are reconciled one at a time. A NetBox that black-holes packets rather than
refusing them occupies that single worker for up to `spec.timeout` — `30s` by default — and
every other endpoint waits.

The probe deliberately makes **one** attempt per reconcile: the client is built with no
retries, so the 30-second requeue *is* the retry. Before that, four client-side retries behind
a 30-second timeout could hold the worker for the better part of three minutes. It is bounded
at one timeout now, but the serialisation is still there.

Symptom: one unreachable endpoint, and every other endpoint's conditions updating a timeout
later than expected. Mitigation is to lower `spec.timeout` on the endpoint that is timing out.

## The Secret blast radius

Worth knowing before you install this in a shared cluster, and it is narrower than it used to
be.

**The `ClusterRole` carries no `secrets` rule at all.** Secret access is granted one namespace
at a time — a `Role` and `RoleBinding` per namespace named in the chart's
`credentialNamespaces`, or in `config/rbac/credential-namespaces/namespaces.txt` on the
kustomize path. The same list becomes `NETBOX_CREDENTIAL_NAMESPACES` on the manager, which is
what it builds its Secret informers from, so the grant and the cache cannot disagree. `*` is
rejected by the chart's schema *and* by its template.

**The informer cache is also scoped, by label.** Within a granted namespace the manager caches
only Secrets carrying `netbox.kubeforge.org/endpoint-credential=true`
(`internal/controller/secretcache.go`), so manager memory scales with the number of credential
Secrets rather than with the cluster's total. That is why an unlabelled Secret is invisible —
the most common consequence, and [the first symptom on this page](#secretmissing).

If the manager is being OOM-killed, check how many Secrets carry the credential label, and how
many namespaces are in `credentialNamespaces`.

[RBAC](operations/rbac.md) has the `kubectl auth can-i` checks and the overlay to add a cluster-wide read
back if you genuinely want one.

## Underlying NetBox error types

A reason is a translation of a typed client error, not an independent diagnosis. Full table and
retry policy in [errors and retries](concepts/errors-and-retries.md).

| HTTP from NetBox | Client type | Becomes |
|---|---|---|
| 401, 403 | `*netbox.AuthError` | `AuthError` on an endpoint; `APIError` on an object |
| 404 | `*netbox.NotFoundError` | `ProbeFailed` on an endpoint; `APIError` on an object that had already been located |
| 400, other 4xx, unparseable body | `*netbox.ValidationError` | `ProbeFailed` on an endpoint; `Invalid` on an object |
| 429 | `*netbox.RateLimitError` | `ProbeFailed` / `APIError` |
| 5xx, transport failure | `*netbox.TransientError` | `ProbeFailed` / `APIError` |
| 409 or a protected-FK body | `*netbox.ProtectedError` | `Deleting=False, Reason=Protected` on a delete; `Ready=False, Reason=Conflict` on a write |
| >1 match on a single-object lookup | `*netbox.AmbiguousError` | `Ready=False, Reason=Conflict` |

## Related

- [Stuck references](operations/stuck-references.md) — the walkthrough for a reference that will not clear
- [RBAC](operations/rbac.md) — what the operator can read, and the label every credential Secret needs
- [Reconciliation](concepts/reconciliation.md) — why a failure is a condition and not an error
- [Errors and retries](concepts/errors-and-retries.md) — the retry tiers, and why they differ
- [Observability](operations/observability.md) — the operator's own metrics and what to alert on
