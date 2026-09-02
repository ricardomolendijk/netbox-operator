# Two writers, one NetBox object: what the operator reports, and what it will not do

| | |
|---|---|
| Requires | [`NetBoxEndpoint.spec.managedBy`](provenance.md) with a `clusterID` |
| Reports on | `status.conflict`, the `Conflict` condition, the `Conflict` Event, `netbox_operator_conflicts_total` |
| Prevents | **nothing** — see [Why nothing is serialised](#why-nothing-is-serialised) |

One NetBox can be reconciled from several clusters, and one cluster can hold two manifests
that describe the same NetBox object. Neither is refused, and neither is coordinated. What
this page is about is the third thing: **finding out**, by name, when it is happening to you.

The short version:

- Two writers on one object is a misconfiguration in every case anybody has named. The
  operator reports it loudly and keeps writing.
- The report names the other writer's cluster and the other writer's manifest, because a
  conflict you cannot attribute is a conflict you cannot resolve.
- The fix is always to stop one of the two claims. There is no setting that makes two writers
  correct.

## The three shapes

"Supported but not coordinated" is only safe if you know which shape you are in.

| Shape | Supported | What makes it work |
|---|---|---|
| **Disjoint** — several clusters, one NetBox, no object claimed twice | **Yes**, indefinitely | Distinct `clusterID` per cluster, and manifests that describe disjoint sets of NetBox objects. This is the lab/staging/prod pattern, and the stamp is what makes an accidental overlap visible rather than mysterious |
| **Active/passive** — one cluster writes, the standby does not | **Yes** | The passive cluster runs its endpoint with `driftMode: Report` or `mode: DryRun`, so it reads and reports and sends nothing. Failover is switching it to `Apply`. Note the asymmetry: a passive cluster still *reports* conflicts, which is exactly what you want the day before a failover |
| **Active/active over the same objects** | **No** | Nothing. Both clusters write, each one's next resync undoes the other's, and NetBox's changelog fills with the two of them taking turns. Detected and reported on both sides; not prevented, not arbitrated |

The write rate in the unsupported shape is bounded by the shorter of the two
`resyncPeriod`s, so the cost of leaving it running is API calls and an unreadable changelog
rather than an outage. It is still wrong.

## What counts as a conflict

Every write path reads the live object first — the engine has to, to know what to PATCH — so
the check costs no extra request. It compares the stamp on the live object
([provenance](provenance.md)) against this endpoint's own identity:

| Live object's stamp | Verdict | Reported as |
|---|---|---|
| `k8s_cluster` names a different cluster | Another cluster manages this object | `Conflict/ForeignCluster` |
| `k8s_cluster` is ours, `k8s_owner` names a different CR | Another namespace, or a second CR in this one, manages this object | `Conflict/ForeignOwner` |
| `k8s_cluster` and `k8s_owner` are ours | Ours | — |
| No stamp at all | Unmanaged | — (this is what `spec.onConflict` is for) |

`ForeignCluster` is decided first, because it is the fact nothing else can see: two clusters
cannot read each other's CRs, so an overlap between them is invisible except in the stamp.

### What is deliberately *not* a conflict

Over-reporting is the only way to break a feature whose entire output is a report. Three
things look like conflicts and are not:

- **An object with no stamp.** It is unmanaged, or managed by something that left no name.
  Taking it over is the adoption question, and `spec.onConflict` already answers it. Every
  object that predates the operator is in this set, so reporting them would mean reporting
  everything.
- **A human's edit in the NetBox UI, corrected on the next pass.** That is **drift**, not a
  competing claim. Git is authoritative
  ([ADR-0005](../decisions/0005-gitops-coexistence.md)) and the operator putting the value
  back is it working as designed — see [drift detection](../concepts/drift.md) and the
  `DriftDetected` condition. Calling that a conflict would not merely be noisy, it would be
  wrong.
- **A CR deleted and re-applied.** `kubectl delete && kubectl apply` gives the CR a new
  `metadata.uid` and changes nothing else, so its own object's `k8s_uid` briefly names a uid
  that no longer exists. The verdict is decided on `k8s_cluster` and `k8s_owner`, which both
  still match, so this is silent — otherwise every re-applied object in the cluster would
  carry a conflict, and a condition that fires on a routine operation is a condition nobody
  reads.

There is also one case that is honestly **not detectable**: an object stamped with a tag and
no `k8s_uid`, `k8s_cluster` or `k8s_owner` — an older operator, or `netbox-populator`. There
is no name in it to report, and "somebody, somewhere, wrote this" is not something anybody can
act on. Such an object is reported through the ordinary adoption path (`status.adopted` and
the `Adopted` Event) instead. If you are migrating off another writer, the signal to watch is
NetBox's own: `?cf_k8s_cluster=<your cluster>` returning every object you expect to own.

## What a conflict looks like

```console
$ kubectl describe nbprefix mgmt -n team-a
Status:
  Conflict:
    Cluster ID:      prod-us
    First Observed:  2026-08-24T09:14:02Z
    Observations:    7
    Owner:           netboxprefix/team-b/mgmt
    Reason:          ForeignCluster
  Conditions:
    Type:     Conflict
    Status:   True
    Reason:   ForeignCluster
    Message:  netbox ipam/prefixes/412 is also claimed by netboxprefix/team-b/mgmt in cluster
              prod-us; this cluster (prod-eu) has written to it anyway and the other writer
              will write it back, so the object flaps between the two specs. Writes are
              deliberately not serialised between writers, so nothing here resolves on its
              own: stop one of the two claims -- delete or suspend netboxprefix/team-b/mgmt,
              or narrow one of the two specs so they describe different netbox objects
Events:
  Warning  Conflict           58m  netbox-operator  netbox ipam/prefixes/412 is also claimed by …
  Warning  ConflictSustained  18m  netbox-operator  still claimed by netboxprefix/team-b/mgmt in cluster prod-us after 5 consecutive reconciles: …
```

Four things carry the report, because each survives something the others do not:

| | Survives | Carries |
|---|---|---|
| `status.conflict` | Every reconcile, restarts, leader elections | Who, since when, and how many passes. Greppable across a namespace, which a condition message is not |
| The `Conflict` condition | The same | The full sentence, including what to do about it |
| The `Conflict` Event | About an hour | The transition — that this *started* |
| `netbox_operator_conflicts_total` | Scrape retention | The cluster-wide rate. The only one of the four that is a number you can alert on |

`Ready` is deliberately untouched. The object does match its spec — for as long as it takes
the other writer to write again — and failing it here would turn every `kubectl wait` in a
deliberately overlapping setup into a timeout while changing nothing about the overlap.

### One conflict, or a fight

`status.conflict.observations` counts **consecutive reconciles that found the same claimant**.
It is the difference between the two things that look identical in a single pass:

- **1, and then gone.** A flap. A migration, a cluster mid-rebuild, somebody restamping an
  object by hand in the NetBox UI. Nothing to do.
- **A number that keeps climbing.** Two writers taking turns. Multiply by the endpoint's
  `resyncPeriod` for how long it has been going, or read `firstObserved`.

The `Conflict` Event fires once, when a claimant is first seen. `ConflictSustained` fires
once more at **five** consecutive observations — most of an hour at the default resync — and
then not again. The count keeps climbing in the status either way. A changed claimant resets
it to one, because somebody else taking the object over is a different fight.

## What to do about one

1. **Read the two names.** `status.conflict.owner` is the other manifest, in the spelling
   `<kind>/<namespace>/<name>`; `status.conflict.clusterID` is the cluster it lives in. If
   that is not this cluster, the CR is not in front of you and nothing here can look it up —
   go to that cluster.
2. **Decide which side should own the object.** There is no arbiter and there is not going to
   be one: the only honest arbiter is a human looking at two manifests.
3. **Stop the other claim.** One of:
   - Delete the losing CR. Set `spec.deletionPolicy: Retain` on it first if you want the
     NetBox object left in place — the default deletes it, which is not what you want when the
     other cluster is about to keep managing it.
   - Suspend the losing cluster's endpoint: `driftMode: Report` (reads and reports, writes
     nothing) or `mode: DryRun`. This is the active/passive shape, and it is the fastest thing
     to do at 3am.
   - Narrow one of the two specs so the two describe different NetBox objects. This is the
     real fix for the common case, which is two teams' manifests overlapping by accident.
4. **Verify.** The `Conflict` condition disappears and `status.conflict` is unset on the very
   next reconcile of the winning object — within one `resyncPeriod`, or immediately if you
   touch the CR. The counter stops moving. Both are checks you can automate; the absence of
   the condition is the normal state, so there is no "resolved" value to look for.

### Finding every conflict in the cluster

```console
$ kubectl get nbprefix,nbsite,nbvlan -A \
    -o jsonpath='{range .items[?(@.status.conflict)]}{.kind}{"/"}{.metadata.namespace}{"/"}{.metadata.name}{"\t"}{.status.conflict.reason}{"\t"}{.status.conflict.owner}{"\t"}{.status.conflict.observations}{"\n"}{end}'
NetBoxPrefix/team-a/mgmt	ForeignCluster	netboxprefix/team-b/mgmt	7
```

That is the whole reason the finding is a status field rather than only a condition message.

### Finding what another cluster claims

From the NetBox side, which is the only place both writers are visible at once:

```console
$ curl -sH "Authorization: Token $NETBOX_TOKEN" \
    "$NETBOX_URL/api/ipam/prefixes/?cf_k8s_cluster=prod-us" | jq '.count'
```

Swap the `clusterID` for your own to see what *this* cluster claims. `k8s_owner` on each
object names the manifest behind it. `NetBoxSweep` (NBO-046) scopes every query it makes the
same way, for the same reason: a cluster must not report — or reclaim — an object another
cluster's stamp is on.

## Metrics and alerting

`netbox_operator_conflicts_total{kind,reason}` counts reconciles that found another writer's
stamp and wrote anyway.

```promql
# Somebody must look: a conflict that is still being observed.
sum by (kind, reason) (increase(netbox_operator_conflicts_total[1h])) > 0
```

Alert on a **window**, not on an instant, and give it a `for:` of at least a resync period.
A conflict during a rolling migration or a cluster rebuild is expected and transient, and an
alert that fires on one of those is an alert that gets muted.

It is a counter rather than a gauge of "objects currently in conflict", and that is a
deliberate trade: keeping such a gauge accurate needs a series per object, which means a label
carrying a namespace and a name, which is how a metric takes a Prometheus down
(`internal/metrics`, `TestNoUnboundedLabels`). It is not labelled by the other writer's
cluster id either — that value is read out of a NetBox custom field, so its cardinality is
somebody else's to decide. Who is on the object; how many is in the metric; the two are read
together.

`netbox_operator_drift_detected_total{kind,field}` is the companion signal
([observability](observability.md)). Under ADR-0005 a spike in it is no longer a human editing
NetBox: it is either an undeclared writer or a missing normalisation in the operator's own
drift comparison, and both want somebody to look.

## Why nothing is serialised

The operator will not take a lock, a lease, or any other kind of mutual exclusion over a
NetBox object. That is **decided, not deferred**
([#18](https://github.com/ricardomolendijk/netbox-operator/issues/18)), and the reasoning is
in [provenance](provenance.md#two-clusters-one-netbox). In short:

1. **It cannot be built correctly on this API.** A lease would have to live in NetBox, the
   only store both clusters share, as a custom field that is read and then written.
   Check-then-write is not atomic over the REST API — no compare-and-swap, no `If-Match`
   precondition on an object `PATCH` — so the window between our read and our write is exactly
   the race the lease was supposed to remove. A lock with a race in it is worse than no lock,
   because it is trusted.
2. **The cost is permanent and on the hot path.** Every write becomes two round trips, plus a
   renewal loop, plus a reaper for leases held by a cluster that no longer exists, plus a
   break-glass for when the reaper is wrong. That is a subsystem, paid on every reconcile of
   every object forever, to protect against a configuration that is a mistake in every case we
   can name.
3. **Visibility is the right answer to the actual failure.** Active/active over shared objects
   is something you want to *see and fix*, not to have arbitrated behind your back. An
   arbitrated fight is still a fight; it is just quieter.

Where NetBox does offer atomicity the operator already uses it: the allocation endpoints run
under NetBox's own advisory lock, so handing out an address is race-free across clusters today
([claims](../concepts/claims.md), [ADR-0004](../decisions/0004-claims-first-allocation.md)).
It is only mutation of an object that already exists that is unserialised, and that is the
narrower problem.

If NetBox ever gains conditional requests — an `If-Match`/ETag precondition on writes — this
is worth revisiting. Until then the answer is a conflict and a name.

## What this does not do

Stated plainly, so nothing here is a surprise at 3am:

- **It does not stop the write.** By design, above. Both sides of an active/active overlap
  keep writing.
- **It does not stop a delete.** A CR whose NetBox object has been taken over by another
  cluster still deletes that object when the CR is deleted, because the operator deletes by the
  id it recorded when it created the object and never by a lookup. Set
  `spec.deletionPolicy: Retain` on a CR you are retiring in favour of another writer — that is
  the supported way to hand an object over, and it is one field.
- **It does not exist without provenance.** No `spec.managedBy.clusterID` means no stamp,
  which means no conflict is detectable at all: two clusters just fight, and neither says so.
  This is the single most important reason to turn stamping on, and the consequence table in
  [provenance](provenance.md#turning-it-off) says so too.
- **It does not report an unattributable writer**, for the reason above.
- **It does not arbitrate.** Neither side is marked the winner, ever.

## Related

- [Provenance](provenance.md) — what the stamp contains, how to turn it on, and the full
  reasoning behind never serialising writes
- [Coexisting with Flux and Argo CD](gitops.md) — `driftMode`, and why the operator never
  writes a `spec`
- [Observability](observability.md) — every metric, its labels and its cardinality
- [ADR-0005 — Coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md) — why
  a human's NetBox edit is drift and not a competing claim
- [Drift detection](../concepts/drift.md) — the other half of "somebody is fighting the
  operator"
