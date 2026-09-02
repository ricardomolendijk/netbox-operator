# Coexisting with Flux and Argo CD

This operator reconciles **outward**, from Kubernetes to NetBox. Flux and Argo CD reconcile
**inward**, from Git to Kubernetes. The CRs sit in the middle, and if both sides wrote a
CR's `spec` they would fight forever — each one correcting drift the other just introduced,
at whatever the shorter of the two resync intervals is.

They do not, because of one invariant:

> **The operator never writes to a `spec`. Every operator write goes to `status`, plus one
> merge patch scoped to `metadata.finalizers`.**

Git is authoritative and NetBox is a projection of it. A change made in the NetBox UI is not
a competing opinion about desired state — it is drift, and it gets corrected back to what
the source says. There is no mode in which a UI edit wins, and no path by which one is
promoted into desired state. See
[ADR-0005](../decisions/0005-gitops-coexistence.md) for the reasoning.

## Which interactions are actually a problem

Most are not. Worth being precise, because the list is shorter than people expect:

| Interaction | Problem? |
|---|---|
| Human edits NetBox; the operator corrects it back to match the CR | **No.** That is drift correction, and it is the entire point |
| Git changes; Flux/Argo updates the CR; the operator pushes it to NetBox | **No.** That is the intended flow |
| The operator writes a CR's `status` | **No.** Flux ignores `status`; Argo CD ignores it for CRs by default |
| The operator writes a CR's `spec` | **Yes — this is the fight.** It does not happen; see below |
| The operator creates a CR that is not in Git | **Sometimes.** Argo CD flags it `OutOfSync` as extraneous. Nothing does this yet — inline children are NBO-032, which carries the annotations that quiet it |
| Git deletes a CR; Flux/Argo prunes it; the finalizer deletes the NetBox object | **No** — and it is the desired behaviour ([deletion](../concepts/deletion.md)) |

## How the invariant is enforced

Three layers, because "we intend not to" is not a property anybody can rely on eighteen
months from now:

1. **Structure.** The engine holds a `StatusWriter` and a `FinalizerWriter` and no client at
   all ([`internal/reconciler/generic.go`](../../internal/reconciler/generic.go)). There is
   nothing it could call to write a spec. The two writers are deliberately different types:
   status and `metadata.finalizers` are different subresources written by different calls,
   and keeping them apart is what makes the invariant checkable rather than aspirational.
2. **A runtime guard.** Every object controller writes through `specGuard`
   ([`internal/controller/specguard.go`](../../internal/controller/specguard.go)), which
   refuses an `Update` on any registered kind, and refuses a patch whose body reaches
   outside `metadata`. It returns `ErrSpecWriteForbidden`. Belt and braces against a future
   contributor, not a substitute for review.
3. **Tests.** A registry-wide unit test reconciles an unchanged object of *every* registered
   kind and asserts zero non-status writes; an `envtest` asserts `metadata.generation` does
   not move across a reconcile that writes status. A generation bump is the signature of a
   spec write, because the API server increments it for a change outside `metadata` and
   `status` and for nothing else — which is also how Argo CD notices.

The consequence to accept is that **an allocated address lives in `status`, not in Git.**
Allocations stay stable across a cluster rebuild by deriving a deterministic identity rather
than by writing back to Git (ADR-0005 §3, NBO-036).

## Argo CD

An `Application` needs one thing: stop diffing what the operator owns. Argo CD ignores
`status` on CRs by default, but say it explicitly rather than relying on that default
holding across Argo versions.

```yaml
apiVersion: argoproj.io/v1alpha1
kind: Application
metadata:
  name: netbox-inventory
  namespace: argocd
spec:
  project: default
  source:
    repoURL: https://github.com/example/infra.git
    path: netbox
    targetRevision: main
  destination:
    server: https://kubernetes.default.svc
    namespace: homelab
  syncPolicy:
    automated:
      prune: true          # a CR removed from Git is pruned, and its finalizer deletes
                           # the NetBox object -- the intended behaviour
      selfHeal: true       # safe: the operator never writes a spec, so there is nothing
                           # for selfHeal to fight
  ignoreDifferences:
    # status is the operator's output. Diffing it makes every reconcile an OutOfSync.
    - group: netbox.kubeforge.org
      kind: "*"
      jsonPointers:
        - /status
    # The finalizer is added by the operator before its first NetBox write, and it is not
    # in the manifest. Without this, every object is permanently OutOfSync on metadata.
    - group: netbox.kubeforge.org
      kind: "*"
      jsonPointers:
        - /metadata/finalizers
```

`prune: true` and `selfHeal: true` are both safe, and that is the point of the invariant:
`selfHeal` reverts a live spec that has diverged from Git, and the operator never diverges
one.

**Verifying it.** With the operator actively reconciling, an Application containing a
`NetBoxSite` stays `Synced`. Watch for longer than one `resyncPeriod` — a flap, if there
were one, would appear on a resync rather than on the initial sync:

```
$ kubectl -n argocd get application netbox-inventory
NAME               SYNC STATUS   HEALTH STATUS
netbox-inventory   Synced        Healthy

$ kubectl -n homelab get netboxsite dc1 -o jsonpath='{.metadata.generation}'
1
```

The generation stays at `1` through any number of reconciles. It moves only when Git does.
That is what `TestStatusOnlyReconcileNeverBumpsGeneration` asserts against a real API
server, so the manual check is a confirmation rather than the only evidence.

### Objects the operator generates, which are not in Git

`ignoreDifferences` covers the CRs Git *does* contain. The other case is a CR Git does not
contain at all: a child materialised from a parent's inline list, or the resource a claim
allocated. Argo treats a live resource with no counterpart in the manifest set as
**extraneous** and reports `OutOfSync` for it — permanently, since nothing will ever add it to
Git.

The operator handles this itself. Every object it generates carries Argo's own mechanism for
exactly this case:

```yaml
metadata:
  labels:
    app.kubernetes.io/managed-by: netbox-operator
    netbox.kubeforge.org/owner-uid: 8f1c…
  annotations:
    argocd.argoproj.io/compare-options: IgnoreExtraneous
    netbox.kubeforge.org/generated-by: netboxvirtualmachine/homelab/dns
    netbox.kubeforge.org/owned-by-path: spec.interfaces[eth0]
```

Nothing to configure for the default case; the annotation is on unless you switch it off. What
each marker is for, and why there are two of them, is in
[inline children](../concepts/inline-children.md#what-a-materialised-child-carries).

**Do not add `prune: false` or an exclusion for these kinds to work around an `OutOfSync`.**
If a generated child is showing as extraneous, the annotation is missing — check whether
`gitops.argocd.enabled` was turned off in the chart values.

## Flux

Flux prunes by inventory and ignores `status`, so a `Kustomization` needs nothing special:

```yaml
apiVersion: kustomize.toolkit.fluxcd.io/v1
kind: Kustomization
metadata:
  name: netbox-inventory
  namespace: flux-system
spec:
  interval: 10m
  path: ./netbox
  prune: true
  sourceRef:
    kind: GitRepository
    name: infra
  # No patches and no ignore rules: Flux diffs against its own inventory of applied
  # fields, and the operator writes none of them.
  healthChecks:
    - apiVersion: netbox.kubeforge.org/v1alpha1
      kind: NetBoxSite
      name: dc1
      namespace: homelab
```

`healthChecks` works because the operator sets `Ready` with `observedGeneration` on every
exit — `kubectl wait --for=condition=Ready` and Flux's health assessment read the same
condition.

One Flux-specific note: `spec.force` is for immutable fields and there are none here, so
leave it off. A forced apply is a delete-and-recreate of the CR, which takes the finalizer
path and deletes the NetBox object on the way through.

### Objects the operator generates

Flux prunes by its own inventory: a resource it did not apply is not in the `Kustomization`'s
inventory, so it is neither pruned nor diffed, and a materialised child needs nothing. That is
why the Flux annotation ships **disabled** and the Argo one does not.

`kustomize.toolkit.fluxcd.io/reconcile: disabled` exists for the narrower case of wanting the
child excluded from `flux diff` output too. Enable it with `gitops.flux.enabled: true`.

## Rebuilding a cluster from Git

The case people are right to ask about. Delete the cluster, apply the same manifests to a
fresh one, and nothing is duplicated in NetBox:

1. **Nothing has a `status.id`.** Every CR is fresh, so the engine cannot locate its object
   by id.
2. **So it looks up by natural key.** `slug` for both kinds that exist today — the same
   lookup it would use to adopt a hand-made object ([lookups](../concepts/lookups.md)).
3. **A match is adoption, and adoption is opt-in.** With the default `onConflict: Fail`,
   every object reports `Conflict` and writes nothing: the safe outcome, and not the one you
   want here.
4. **So a rebuild wants `onConflict: Adopt`** on the objects the operator owns. Each CR
   re-adopts the object it created before, records the same `status.id`, and reconciles it
   towards the spec that is still in Git. NetBox ends up exactly where it was.

`onConflict: Adopt` is the right value *in Git* for objects this operator owns, and `Fail`
is the right default *in the CRD* — finding somebody else's object is not permission to take
it over, and the difference between the two is one line in a manifest you control.

### Claims, which have no `onConflict` to set

The exception is an allocated address, which does not appear in Git at all. A
[`NetBoxIPAddressClaim`](../reference/netboxipaddressclaim.md) has no natural key to adopt by
and therefore no `onConflict` field: it re-adopts by **allocation identity** instead, and that
identity is derived from `(netbox url, namespace, kind, name)` — every one of which the same
manifest still has on a fresh cluster (ADR-0005 §3).

So a rebuild goes:

1. **The claim comes back with an empty `status`.** No `address`, so the reconciler's
   never-allocate-again guard does not fire, and the claim is about to allocate.
2. **It searches NetBox for its own identity first**, unconditionally:
   `GET /api/ipam/ip-addresses/?cf_k8s_allocation_identity=<identity>`.
3. **The object from before is still there**, because a torn-down cluster runs no finalizers —
   nothing ran the claim's deletion pass, so nothing freed the address. Note that this is the
   *rebuild* case specifically. A claim deleted with `kubectl delete` on a live cluster
   **does** free its address by default, and re-applying it then gets the same address only if
   nothing has taken it since
   ([#225](https://github.com/ricardomolendijk/netbox-operator/issues/225)); set
   `deletionPolicy: Retain` on a claim whose address must survive a deliberate delete too.
4. **So it is reclaimed rather than reallocated**: `status.address` comes back to the same
   value, `Allocated=True, Reason=ReclaimedByIdentity`, an `AllocationReclaimed` Event, and
   **zero** POSTs to `available-ips/`.

Nothing has to be set in Git for that to work, which is the point — there is no claim
equivalent of `onConflict: Adopt` to remember.

Two things will make a rebuild hand out a *different* address, and both are visible:

- **The claim was renamed.** The name is in the identity, so the renamed claim searches for
  something nothing carries. Copy the old claim's `status.allocationIdentity` into
  `spec.allocationIdentity` before renaming — and note that the old address stays allocated in
  NetBox until somebody deletes it, reported by the `AddressRetained` Event the deletion
  emitted.
- **The `NetBoxEndpoint`'s URL changed.** The URL is in the identity too, because the same
  claim pointed at a second NetBox is a second allocation. It is normalised, so a trailing
  slash or a redundant `/api` is not a change; a different host is.

`kubectl get nbipc -A` after a rebuild is the check: every `ADDRESS` should be the value it
had, and `kubectl describe` should say `ReclaimedByIdentity` rather than `AddressAllocated`.
The full table of what reclaim does and does not recover is in
[claims](../concepts/claims.md#what-reclaim-can-and-cannot-recover).

## Restoring NetBox from backup

The mirror image of the section above: this time **NetBox** is what was lost. It is the one
scenario the deterministic allocation identity cannot solve by itself, because the identity
reclaims an address by finding the object that already holds it — and a restore from empty
has no object to find.

There is no Git copy to fall back on, by design: the operator does not write allocated
values back to Git and will not
([ADR-0005 §4](../decisions/0005-gitops-coexistence.md#4-no-git-write-back-in-core)). **The
answer is to restore NetBox from its own backup.** Under
[ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git)
NetBox is the durable store for an allocated address, so it needs a backup for the same
reason any database holding production state does. A NetBox restored from nothing has also
lost every device, interface, cable and VLAN that this operator did not create; the addresses
are the smallest part of that.

### NetBox was lost, the cluster was not

Most of this recovers itself, and the addresses come back unchanged:

1. **Restore the NetBox database from backup** and bring it back up on the same URL. Nothing
   in Kubernetes needs to change. The `NetBoxEndpoint` re-probes on `spec.resyncPeriod` and
   returns to `Ready=True` once the token and version checks pass.
2. **Let the endpoint bootstrap re-run.** If the restore predates the provenance schema, the
   operator recreates its own tag and custom-field definitions, including the allocation
   identity field
   ([provenance → bootstrap](provenance.md#bootstrap-the-operator-creating-its-own-schema)).
3. **Objects the backup contains need no action.** A restore keeps the database's own ids, so
   each CR's `status.id` still resolves and the next reconcile is an ordinary drift check
   against the spec that is still in Git.
4. **Objects created after the snapshot are re-created.** `status.id` 404s, the engine clears
   it, the natural-key lookup finds nothing, and the object is created again from the CR.
   For an address allocated by a claim this lands **at the same address**, because by then
   the address is literal in the child `NetBoxIPAddress`'s spec — the claim itself never
   re-allocates
   ([ADR-0004](../decisions/0004-claims-first-allocation.md#statusaddress-is-immutable-the-operator-never-re-allocates)).
   That is the right outcome only while the backup is *newer* than the allocation. A snapshot
   taken before it can hand the same address to two claims: see
   [what survives what](#what-survives-what).
5. **Check what did not come back.** Every CR reaching `Ready=True` means the operator has
   rebuilt everything it manages. Anything else in NetBox — hand-made objects, another
   tool's, another cluster's — is outside this operator's reach and outside its ability to
   tell you it is missing.

### Both were lost

If the cluster is being rebuilt from Git *and* NetBox from backup, the order matters:

1. **Restore NetBox first, then apply the manifests.** A claim reclaims its address by
   searching NetBox for its own allocation identity. Bring the cluster up against an empty
   or not-yet-restored NetBox and it will find nothing, allocate a fresh address, and pin
   that in `status` — and the old value is then gone for good, because nothing else was
   holding it.
2. **Set `onConflict: Adopt`** on the objects this operator owns, as in
   [rebuilding a cluster from Git](#rebuilding-a-cluster-from-git). Every CR re-adopts the
   restored object instead of reporting `Conflict`.
3. **Read `status.address` on each claim afterwards.** Claims whose object is in the backup
   reclaim exactly what they had; claims allocated after the snapshot get a new address, and
   `status.address` is where you see which. Compare against whatever independent record you
   have — DNS, the NetBox changelog from before the loss, a device's static configuration.

### What survives what

| What was lost | Does an allocated address survive? | Because |
|---|---|---|
| NetBox, restored from a backup **taken after** the allocation; cluster intact | **Yes** | The child `NetBoxIPAddress` holds the address literally in its spec, and the claim never re-allocates |
| NetBox, restored from a snapshot **predating** the allocation; cluster intact | **Yes — and a second claim may be handed it too** | The restored database has no record of the address, so NetBox offers it as free to the next claim that asks, and the claim already holding it never re-reads its pin to notice ([#167](https://github.com/ricardomolendijk/netbox-operator/issues/167)) |
| Cluster, rebuilt from Git; NetBox intact | **Yes** | The deterministic allocation identity finds the existing object and adopts it (ADR-0005 §3) |
| Both; NetBox restored first | **Yes, for everything the backup contains** | Reclaim by identity finds the restored object |
| Both; NetBox empty or restored afterwards | **No** | Nothing holds the value any more, so every claim allocates afresh |

**The second row is the one to plan against, because nothing in the system reports it.**
`status.address` is a pin, not a lease: a claim that has allocated short-circuits before it
reaches the endpoint at all — the steady state of a settled claim is a reconcile that talks to
nobody, which is what makes "reconcile fifty times, POST once" structural
([ADR-0004](../decisions/0004-claims-first-allocation.md#statusaddress-is-immutable-the-operator-never-re-allocates)).
So a claim that allocated its address after the snapshot goes on reporting `Allocated=True`
for it, while the restored NetBox — which has never heard of that address — offers it to the
next claim that asks. Two claims, one address, both `Ready=True`, and neither of them did
anything wrong. Each of the three mechanisms this page recommends misses it for its own
reason:

- **Reclaim by identity** ([ADR-0005 §3](../decisions/0005-gitops-coexistence.md#3-allocations-survive-a-cluster-rebuild-without-writing-to-git))
  works by finding the object that already holds the address. The restore erased the row it
  would have found.
- **`onConflict: Adopt`**, which this page recommends keeping in Git for the objects this
  operator owns, will remove the one place a `Conflict` would have surfaced — once claims
  materialise their child `NetBoxIPAddress` at all. They still do not: a claim records what it
  allocated in `status.address` and writes no address CR, which is verified in
  [claims](../concepts/claims.md). (Inline children *are* built, for the parent kinds that
  declare them — this gap is the claim kinds specifically.) When that child does land, re-creating it under `Adopt` would take over
  whatever sits at the address by then, which may be the second claim's object — so this row
  gets worse, not better, unless the guard lands first.
- **Reading `status.address` afterwards**, [step 3 of a both-lost restore](#both-were-lost),
  reads the pin — and the pin is the thing that is wrong.

The check that does find it compares the pin against NetBox rather than trusting it: for each
claim, `GET /api/ipam/ip-addresses/?address=<status.address>` and compare the object's
allocation-identity custom field with the claim's `status.allocationIdentity`. The field is
`cf_k8s_allocation_identity` unless the endpoint renamed it — `spec.managedBy.allocationIdentityField`
is configurable, and reading the default on an endpoint that set something else returns
nothing and reads as "no hazard here". No object where a settled claim says there is one, or
an object carrying somebody else's identity, is this hazard.

The remedy is a human's, because both claims are entitled to the address and only you know
which NIC, DNS record or firewall rule is already using it.

!!! danger "Do not simply delete the losing claim"

    A claim never re-allocates under its own name, so the instinct is to delete the CR and
    re-create it under a different name — which does derive a new
    [allocation identity](#claims-which-have-no-onconflict-to-set) and allocate afresh. **In
    this scenario that can delete the address you decided to keep.** Deleting a claim frees
    its address by `DELETE`ing the NetBox object at the id in `status.netboxID`
    (`internal/reconciler/claim.go:1286`), unconditionally — nothing re-checks that the object
    still carries this claim's allocation identity. After a restore, that id came from the
    pre-restore database and the restored one has reissued it, quite possibly to the surviving
    claim's address.

    Free the losing claim by a route that cannot delete anything, in order of preference:

    1. Set `spec.deletionPolicy: Retain` on it **before** deleting the CR
       (`internal/reconciler/claim.go:1229`), which leaves the NetBox object alone.
    2. Or delete the NetBox object by hand first, then delete the CR, so the id is stale in
       the harmless direction.
    3. Break-glass only: the `netbox.kubeforge.org/skip-finalizer: "true"` annotation
       (`internal/reconciler/claim.go:1218`) drops the finalizer without touching NetBox.

The rule the whole row reduces to: **do not restore a snapshot older than your live
allocations without reconciling them against `status.address` first.**

Whether the engine should catch this itself — a settled claim re-reading its address on the
quiet path and reporting `Conflict` when another allocation identity holds it — is tracked on
[#167](https://github.com/ricardomolendijk/netbox-operator/issues/167). Until that lands, the
check above is the only thing standing in front of it.

The bottom row is the only *unrecoverable* loss, and it is a NetBox backup problem rather than
a Git one — the second row is recoverable, but only by the human check above, which is why it
is the one to plan against. If a specific address must survive even that, then it is not really a claim: put it in
Git as a `NetBoxIPAddress` with an explicit `spec.address`, which is the kind that exists for
exactly that requirement. A claim means "I don't want to know", and its address lives
wherever NetBox lives.

What there is not, in any of this, is an operator-side path that writes a recovered value
back into Git — see
[there is no mode where NetBox wins](#there-is-no-mode-where-netbox-wins). If you want
NetBox's post-restore contents as manifests, that is
[`nbctl export`](exporting.md), which writes files for a human to review and commit.

## Drift modes

`NetBoxEndpoint.spec.driftMode` decides what happens when NetBox stops matching a CR.

| Mode | Detects drift | Corrects it | Periodic resync | For |
|---|---|---|---|---|
| `Correct` | yes | yes | yes | The default, and the intended steady state |
| `Report` | yes | **no** | yes | The first week of an adoption, and running alongside a team that still edits NetBox by hand |
| `Off` | only on a CR change | yes | **no** | A very large NetBox where the resync cost is real |

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxEndpoint
metadata:
  name: homelab
  namespace: homelab
spec:
  url: https://netbox.home.arpa
  tokenSecretRef:
    name: netbox-token
  driftMode: Report          # Correct | Report | Off
  resyncPeriod: 10m          # ignored under Off
```

### `Report` is genuinely read-only

`Report` sends **nothing** — no `POST`, no `PATCH`, no `DELETE`, the finalizer's delete
included. It is not "write and log". That is enforced structurally: a `Report` endpoint
hands the engine a client that suppresses every mutation, so the mode does not depend on
every write path remembering to check a flag. A half-mutating dry run is worse than none,
because it teaches people to distrust the mode.

What you get instead:

```
$ kubectl -n homelab describe netboxtag watched
Status:
  Conditions:
    Type:     DriftDetected
    Status:   True
    Reason:   DriftDetected
    Message:  color: ff0000 -> 2196f3
    Type:     Synced
    Status:   False
    Reason:   DriftReported
    Type:     Ready
    Status:   False
    Reason:   ReportPending
Events:
  Type    Reason         Message
  Normal  DriftDetected  report only: would have written extras/tags (color: ff0000 -> 2196f3)
```

`Ready=False` is deliberate: NetBox does not match the spec, and saying otherwise would make
`kubectl wait` lie about a write that never happened. So do not gate a Flux `healthCheck` on
`Ready` while an endpoint is in `Report`.

On a dashboard the mode shows up as a gap between two counters:

```promql
sum(rate(netbox_operator_drift_detected_total[5m]))   # moving
sum(rate(netbox_operator_drift_corrected_total[5m]))  # flat at zero
```

They are separate metrics rather than one with a `corrected` label precisely so that
"reporting as configured" and "failing to write" are not the same shape
([observability](observability.md)). `netbox_operator_reconcile_total{result="reported"}` is
the per-reconcile view, and it is distinct from `result="dryrun"` because the two are set in
different fields and fixed in different ways.

Treat time spent in `Report` as migration time, not as a supported operating mode. Nothing
converges while it is on.

### `Off` disables the resync, not the operator

`Off` suppresses the periodic re-check. A CR change is a watch event rather than a requeue,
so an edit in Git still reconciles immediately — and when it does, it corrects the whole
object, the human's NetBox edit included. That surprise is the price of the mode.

Two things `Off` deliberately does **not** switch off:

- **The retry that unblocks a stuck object.** An object waiting for its endpoint, or refused
  a NetBox object it conflicts with, has not settled. It keeps retrying on the intervals in
  [errors and retries](../concepts/errors-and-retries.md), because nothing else will ever
  get it unstuck.
- **The `NetBoxEndpoint`'s own re-probe.** That checks the token, the version and
  reachability, none of which is drift. It continues on `spec.resyncPeriod`.

### There is no mode where NetBox wins

Promoting a NetBox-side edit back into a CR's `spec` is the obvious next feature request,
and it is deliberately absent. It would make the operator a second writer to desired state,
which is precisely what this page exists to prevent. If you want NetBox's current contents
as manifests, that is [`nbctl export`](exporting.md): it writes files for a human to review
and commit, rather than a controller writing specs. It skips the objects the operator
already manages, because those already have manifests in Git, and it never writes to Git
itself -- you read the diff and you commit it.

## Chart values

The Helm chart is where all of this is configured at install time
([installing](../install.md)). Two of the values look like operator settings and are not, so
the distinction is worth stating before the list.

```yaml
gitops:
  argocd:
    enabled: true            # argocd.argoproj.io/compare-options: IgnoreExtraneous
  flux:
    enabled: false           # kustomize.toolkit.fluxcd.io/reconcile: disabled
  extraAnnotations: {}

drift:
  mode: Correct              # Correct | Report | Off
  resyncPeriod: 10m          # ignored under Off

allocation:
  identity:
    mode: Derived            # Derived | Explicit
    customField: k8s_allocation_identity
```

### `drift.*` is per endpoint, and the chart only renders one

`driftMode` and `resyncPeriod` are fields of a `NetBoxEndpoint`, not settings of the manager.
That is deliberate: two NetBoxes in one cluster can be at different stages of an adoption,
one in `Correct` and one still in `Report`, and a single manager-wide switch could not
express that.

So `drift.mode` sets the field on the **optional** endpoint the chart renders with
`endpoint.enabled=true` — the one-command demo install. An endpoint you keep in Git, which
is what this page otherwise assumes, takes its value from its own manifest and the chart
never sees it. `allocation.identity.customField` works the same way: it lands on that
endpoint's `spec.managedBy.allocationIdentityField`.

If you are managing endpoints in Git and wondering which value to set, the answer is
neither — set `driftMode` in the manifest.

`allocation.identity.mode` is the odd one out. `Explicit` means each claim carries its own
`spec.allocationIdentity`, which is a choice about how you write manifests rather than
something the operator can be configured to require. The value documents the convention and
renders nothing.

### `gitops.*` is manager-wide, and the chart values do not reach it yet

The annotation set is a property of the operator rather than of one NetBox, so it is one
value for the install, and the chart renders it as `NETBOX_GENERATED_ANNOTATIONS` on the
Deployment.

**The manager does not read that variable.** Materialisation itself works — an inline
`interfaces` entry becomes a real child CR, and that child does get the annotations — but the
set it gets is the hardcoded default in `gitOpsDefaults()`
(`internal/controller/objectcontroller.go`), which is §5's documented default and nothing
else: **Argo CD on, Flux off, no extras**. Setting `gitops.flux.enabled=true` or
`gitops.extraAnnotations` today changes the Deployment's environment and changes no
annotation on any object. Said plainly, because a value that looks like it works and does not
is worse than one that is missing.

Until the wiring lands, a Flux install that needs
`kustomize.toolkit.fluxcd.io/reconcile: disabled` on generated children has to add it another
way — a Kustomize patch on the child kinds, or Flux-side exclusion.

`gitops.flux.enabled` is off by default and it is the one to turn on once it is wired, if you
run Flux:
`kustomize.toolkit.fluxcd.io/reconcile: disabled` on a CR the operator generated is what
stops Flux from pruning an object that was never in its inventory, in exactly the way
`IgnoreExtraneous` stops Argo CD reporting one as extraneous. Turning on the annotations for
a tool you do not run is noise, which is why neither is unconditional.

### `drift.mode: Report` for the first week

The documented adoption path, and the reason `Report` exists at all:

```sh
make install-crds        # the CRDs are not in the chart -- docs/install.md

helm install netbox-operator ./charts/netbox-operator \
  --namespace netbox-operator-system --create-namespace \
  --set credentialNamespaces={homelab} \
  --set endpoint.enabled=true --set endpoint.namespace=homelab \
  --set endpoint.url=https://netbox.home.arpa \
  --set drift.mode=Report
```

Then read what it would have changed — `kubectl describe`, the `DriftDetected` conditions,
and the gap between `netbox_operator_drift_detected_total` and
`netbox_operator_drift_corrected_total` — and when the reported drift is drift you agree
with, `helm upgrade --set drift.mode=Correct`.

Nothing converges while `Report` is on, and no object reaches `Ready=True`. `NOTES.txt`
says so after every install that sets it, because a mode that quietly does nothing is the
one people forget they left on.

### CRDs and Argo CD

The chart does not contain the CRDs, and `--include-crds` therefore does nothing here. That
is not a GitOps concession: Helm 3 stores the whole chart in the release `Secret` and 2.7 MB
of CRDs put it over the API server's 1 MiB cap, which failed every install of any kind
([installing](../install.md#crds-and-why-they-are-not-in-the-chart)).

For GitOps the shape is arguably better than it was. The CRDs are a plain multi-document
YAML — `make crd-bundle`, or `netbox-operator-crds-<version>.yaml` off the release — so the
tool that owns the release can own them too, as a source of its own that syncs first:

```yaml
  # the CRDs, first
  metadata:
    annotations:
      argocd.argoproj.io/sync-wave: "-1"
```

Two things an Argo CD `Application` needs either way:

```yaml
  syncPolicy:
    syncOptions:
      - ServerSideApply=true
```

`ServerSideApply=true` for **ownership**, not for size. Argo CD's client-side apply takes
sole ownership of every field it sends, so a CRD also touched by `make upgrade-crds` or by a
Helm-managed install becomes a fight between two managers; server-side apply makes that a
recorded co-ownership instead. It also drops the `last-applied-configuration` annotation,
which for these CRDs is worth about 55% of the stored object — but that is a saving rather
than a requirement. Earlier wording here said the annotation exceeded what the API server
accepts: it does not. The largest is 98,381 bytes against a 262,144 byte cap, measured. And
the ordering
the [installing](../install.md#crds-and-why-they-are-not-in-the-chart) page states — CRDs
before the manager, because a manager reconciling a field the old CRD prunes fails in a way
that looks like an operator bug.

A renderer that templates the chart away from the cluster cannot see the CRDs in discovery
and will trip the chart's precondition. Pass `--api-versions netbox.kubeforge.org/v1alpha1`,
or set `crds.check=false` in the `Application`'s Helm values:

```sh
helm template netbox-operator ./charts/netbox-operator \
  --namespace netbox-operator-system \
  --api-versions netbox.kubeforge.org/v1alpha1 \
  -f values.yaml >netbox-operator.yaml
```

## NetBox permissions

The recommended posture, and what to do on day one. It turns "do not edit NetBox by hand"
from a convention into a constraint, and makes the drift machinery a safety net rather than
a daily occurrence.

| Account | Permissions |
|---|---|
| The operator's API token | Write on every object type it manages; read elsewhere |
| Human users | **Read-only** |
| Anything else writing to NetBox | Nothing, or a documented exception (NBO-047) |

Two ways to get this wrong, both worth recognising:

- **The operator's own token is read-only.** Every mutation returns `403`. The client
  classifies that as an `AuthError`, which fails the `NetBoxEndpoint` with
  `Ready=False, Reason=AuthError` rather than scattering identical failures across every CR
  in the cluster. Look at the endpoint, not at the objects.
- **The token has write on some object types and not others.** Partial permissions produce
  per-kind `403`s that look like bugs: one kind reports `APIError` while its neighbours are
  `Ready`. Until the endpoint's permission probe lands (NBO-004's design note), the symptom
  is the diagnosis — compare the failing kinds against the token's object-type permissions
  in NetBox.

Read-only human accounts do not stop drift happening; a NetBox admin, a plugin or a
migration script can still change things. They stop it being routine.

## Troubleshooting

| Symptom | What you would see | Cause | Fix |
|---|---|---|---|
| Argo CD reports `OutOfSync` on every reconcile | the diff is under `/status` | `status` is being diffed | Add the `ignoreDifferences` block above |
| Argo CD reports `OutOfSync` on `metadata` | the diff is `finalizers` | the operator's finalizer is not in the manifest | Ignore `/metadata/finalizers`, as above |
| An object is `OutOfSync` and Argo keeps reverting it | `metadata.generation` climbing with no Git change | something *is* writing the spec — not this operator; look for a mutating webhook or a second controller | `kubectl get <kind> -o yaml` and read `managedFields` to find the writer |
| Drift reported and never corrected | `Ready=False, Reason=ReportPending`, `Synced=False, Reason=DriftReported` | `driftMode: Report` | Set `driftMode: Correct` |
| Drift reported and never corrected | `Ready=False, Reason=DryRunPending`, `Synced=False, Reason=DriftDetectedDryRun` | `mode: DryRun` — a different field | Set `mode: Apply` |
| A NetBox UI edit is never noticed | `Ready=True`, `DriftDetected=False`, and NetBox still holds the edit | `driftMode: Off` — nothing re-checks on a timer | Set `driftMode: Correct`, or touch the CR |
| Every object reports `Conflict` after a cluster rebuild | `Ready=False, Reason=Conflict`, the message names the NetBox id | `onConflict: Fail`, which is the safe default | Set `onConflict: Adopt` on the objects the operator owns, then re-apply |
| `ErrSpecWriteForbidden` in the manager log | `the operator may not write anything but the status of a CR it did not create` | code in the operator tried to write a spec. An operator bug, not a configuration problem | Open an issue and quote the line; it names the kind and the object |

## Related

- [ADR-0005 — Coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md) —
  why Git is authoritative, and why there is no write-back.
- [Drift detection](../concepts/drift.md) — what counts as drift, and the comparison rules
  that stop a correction loop from PATCHing forever.
- [Deletion](../concepts/deletion.md) — what a pruned CR does to its NetBox object, and how
  `deletionPolicy: Retain` opts out.
- [`NetBoxEndpoint`](../reference/netboxendpoint.md) — `spec.driftMode`, `spec.mode` and
  `spec.resyncPeriod` field by field.
- [Observability](observability.md) — the drift metrics, and what to alert on.
