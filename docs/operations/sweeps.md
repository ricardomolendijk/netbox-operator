# Sweeps: finding what this cluster left behind in NetBox

A `NetBoxSweep` answers one question: **what is sitting in NetBox with this cluster's
provenance stamp on it and no CR left to reconcile it?**

It answers by listing, and it does nothing else. **A sweep never deletes a NetBox object, and
it has no mode, flag or annotation that makes it delete one.** That is not a default — the
controller holds a client narrowed to `List`, so there is no `Delete` to call by accident.

```console
$ kubectl get netboxsweeps -n homelab
NAME      ENDPOINT   ORPHANS   SUSPECTED   UNATTRIBUTED   READY   REASON     LAST RUN
nightly   homelab    2         0           1              True    Complete   4h
```

## Contents

- [Why reporting and not reclaiming](#why-reporting-and-not-reclaiming)
- [What makes an object visible to a sweep](#what-makes-an-object-visible-to-a-sweep)
- [The four verdicts](#the-four-verdicts)
- [The grace period](#the-grace-period)
- [When a sweep refuses to run](#when-a-sweep-refuses-to-run)
- [Cost: what one run actually asks NetBox for](#cost-what-one-run-actually-asks-netbox-for)
- [Reading a report](#reading-a-report)
- [What to do about a finding](#what-to-do-about-a-finding)
- [Metrics and alerting](#metrics-and-alerting)
- [Worked example](#worked-example)
- [Troubleshooting](#troubleshooting)

## Why reporting and not reclaiming

The two situations that leave objects behind are both real:

- A CR was removed from Git while the operator or the cluster was down, so no finalizer ever
  ran and nothing deleted the NetBox object.
- `deletionPolicy: Retain` — the finalizer came off deliberately and NetBox was left alone,
  which is the point of the policy. IPAM kinds default to it.

In both cases the object still carries this cluster's tag and stamp, and nothing in the
operator will ever look at it again.

The asymmetry that decides the design:

| | A leaked object | An object freed by mistake |
|---|---|---|
| Visible? | Yes — it is a row in NetBox, and this report names it | No — it is gone |
| Costs? | A row, and a prefix or address somebody may re-allocate later | A prefix or address that may **already have been handed to somebody else** |
| Reversible? | Yes, at any time, by hand or by `nbctl adopt` | No |

A tool that gets the second column wrong once earns a reputation it cannot lose. So the sweep
reports, and a human decides. This is the same answer
[`docs/operations/provenance.md`](provenance.md) gives to "is stamping mandatory": what a
sweep needs is for the stamp to be **sufficient** evidence of ownership, never for its absence
to be evidence of anything.

> **If you want deletion**, the shape it would take is on record: `action: Delete` gated on a
> confirmation annotation that must equal `metadata.name`, a deletion budget that aborts
> before the first delete rather than partway, and a per-object re-verification immediately
> before each `DELETE`. It is deliberately not implemented here — see
> [`specs/NBO-046-sweep.md`](../../specs/NBO-046-sweep.md) and the discussion on #182.

## What makes an object visible to a sweep

Three filters, ANDed. All three are required, and two of them are applied by NetBox rather
than by the operator, so an object outside them is never even fetched.

### 1. The `managedBy` tag — server-side

`?tag=<spec.managedBy.tag>`, default `k8s-managed`. The engine stamps it on every create and
adopt.

**A NetBox object without this tag is structurally out of a sweep's reach.** A hand-made
object, a row created by another tool, a leftover from a NetBox restore: none of them can
appear in any report, whatever else is true about them.

### 2. The cluster stamp — server-side, and the one that matters most

`?cf_<spec.managedBy.clusterField>=<spec.managedBy.clusterID>`, default
`?cf_k8s_cluster=<your clusterID>`.

**Two clusters may share one NetBox, and they are never coordinated**
([ADR: provenance](provenance.md), NBO-047). Each stamps its own `clusterID` and neither knows
the other exists. Without this filter, cluster A would list cluster B's perfectly healthy
objects, find no CR for any of them — because the CRs are in cluster B — and report B's entire
estate as orphaned. That is the single worst failure mode available to this feature, and it is
closed by an **exact-match filter applied by NetBox**: another cluster's object never reaches
the operator's memory, let alone its report.

It follows that `spec.managedBy.clusterID` must be **different on every cluster** sharing a
NetBox. Two clusters that were given the same identifier will each report the other's objects
as orphans, and no filter can save them — the stamp is the only thing that tells them apart.

### 3. The kind list — `spec.kinds`, required, no wildcard

Scanning a kind is an explicit act. There is no wildcard on purpose: one would silently start
listing every one of the ~120 kinds in the catalogue the moment a new one shipped, which is a
load change on somebody else's NetBox that nobody asked for.

A kind in the list that this build does not carry, or whose NetBox model cannot hold a stamp,
**refuses the whole run** rather than being skipped. See
[When a sweep refuses to run](#when-a-sweep-refuses-to-run).

### The namespace is not a filter, it is the attribution

There is no server-side namespace filter, because the stamp has no namespace field of its own.
The namespace comes out of `k8s_owner`, which the engine writes as
`<lowercased kind>/<namespace>/<name>`. A sweep in `team-a` attributes only the objects whose
owner stamp names `team-a`; everything else that came back is counted in
`status.summary.foreign` and never listed.

That is deliberate rather than a shortcut: an object stamped for a namespace this sweep cannot
see is that namespace's business, and **a sweep must not act on the disappearance of a
namespace it has no reach into** (ADR-0002).

## The four verdicts

Every object that comes back gets exactly one, and the **order the checks run in is the safety
property**:

1. **Claimed** — its NetBox id is some live CR's `status.id`, **or** its `k8s_uid` stamp is
   some live CR's `metadata.uid`.

   Both, because either alone has a hole: a CR whose status was lost (restored from a backup,
   or wiped by hand) still protects its object through the uid, and a kind whose NetBox model
   carries no `custom_fields` — `extras.Tag` is one — has no uid stamp at all and can only be
   matched by id. A terminating CR counts as a claim: its finalizer has not come off, so the
   operator is still going to deal with the object itself.

   **A claim is checked before anything else, and a claimed object is never a finding —
   whatever its own conditions say.** A CR sitting on `WaitingForRef` is not an orphan.

2. **Foreign** — the owner stamp names another namespace, or another Kind. Counted in
   `summary.foreign`, never listed.

3. **Unattributed** — the owner stamp is missing, or is not exactly three non-empty segments.

   This object cannot be attributed to anybody. It may be another namespace's object written
   by an older operator version, a genuine orphan from before the stamp existed, or a leftover
   from `netbox-populator`. The sweep cannot tell, so it reports the count and **never claims
   it is an orphan**. A half-read owner stamp is how a sweep would attribute somebody else's
   object to itself, so anything malformed lands here rather than being parsed optimistically.

4. **Orphaned** / **Suspected** — stamped for this namespace, and no live CR claims it. Which
   of the two depends on the grace period below.

The case worth staring at: a CR deleted and re-applied from the same manifest has **the same
`k8s_owner` and a new `k8s_uid`**. The old NetBox object is genuinely orphaned, and only the
uid says so — which is why a sweep matches on uid and not on the owner string.

## The grace period

`spec.gracePeriod`, default `1h`.

"The CR is gone" and "the CR is between a delete and a re-apply" look identical from NetBox.
So does "the operator was down while Git changed". They are told apart only by waiting.

A finding is reported as `Suspected` until it has been continuously unclaimed for
`gracePeriod`, and `Orphaned` after. `status.findings[].firstSeen` is the clock, it lives in
`status`, and it is carried forward run to run — so:

- a controller restart does not reset it;
- if `status` is lost the clock restarts, which fails **towards** `Suspected` and never
  towards `Orphaned`;
- a finding the previous run's cap dropped restarts its clock too, for the same reason.

The clock is the operator's own. NetBox's `last_updated` is never used for eligibility, so
clock skew between the two cannot promote a suspicion to an accusation.

`gracePeriod: 0s` reports everything as `Orphaned` on first sight. Reasonable for a one-off
audit; a poor default for anything that runs unattended.

`Unattributed` ignores the grace period entirely. It is not an accusation — it is "I cannot
tell" — and waiting would not change it.

## When a sweep refuses to run

A refused run writes a `Ready=False` condition with a reason, and **leaves
`status.findings`, `status.summary` and `status.lastRunTime` exactly as the last completed
run left them.**

That asymmetry is the whole safety design of the status shape:

> An empty `status.findings` must only ever mean **"the last complete scan found nothing"**,
> never **"the last scan could not see anything"**.

Read `Ready` and `lastRunTime` together to tell which report you are looking at.

| `Reason` | What happened | What to do |
|---|---|---|
| `EndpointNotReady` | The `NetBoxEndpoint` has no usable client, or has gone. | Fix the endpoint; the sweep retries every 5 minutes. |
| `EndpointDryRun` | The endpoint hands out a client that cannot write — `mode: DryRun`, **or** `driftMode: Report`. | See below. This is the most dangerous interaction in the feature. |
| `DriftOff` | The endpoint's `driftMode: Off`. | The operator is not tracking NetBox state, so the absence of a claim proves nothing. Set `driftMode` to `Correct` or `Report`… and `Report` then refuses for the reason above. A sweep needs a live, writing endpoint. |
| `ProvenanceDisabled` | `spec.managedBy` writes no stamp, or the cluster / uid / owner custom field does not exist in NetBox. | Configure `spec.managedBy.clusterID` and let the endpoint's bootstrap create the definitions. Nothing distinguishes this cluster's objects from anybody else's until it has. |
| `UnknownKind` | A `spec.kinds` entry has no descriptor in this build. | Remove it, or upgrade the operator. |
| `KindNotStampable` | A `spec.kinds` entry maps to a NetBox model with no `custom_fields` column — `NetBoxTag` is the case. | Remove it. Such an object can never carry the cluster stamp, so it can never be attributed; `provenance.md` already records that an unstampable object is never reclaimed by a sweep. |
| `Truncated` | A list paginated past the client's page cap. | See below. |
| `Timeout` | The run exceeded `spec.timeout`. | Raise the timeout, or split the sweep into several with fewer kinds each. A timeout is a failed run, never a partial one. |
| `APIError` | NetBox was unreachable, rate limiting, or failing. | Fix NetBox. |

Each refusal is a distinct reason because "the sweep did not run" has as many different fixes
as it has causes, and one `Refused` would send all of them to the same page.

### Why a non-writing endpoint refuses

`mode: DryRun` and `driftMode: Report` both hand the operator a client that cannot POST. A
suppressed create never returns an id, so **no CR on such an endpoint ever gets a
`status.id`** — and every object of every kind would be classified unclaimed. An entire
namespace reported as orphaned, from one field.

It is an explicit guard clause and not an emergent property, and it is read off the client
rather than off the spec, so a mode that suppresses writes cannot be missed by a second copy
of the rule.

### Truncation is a refusal, not a partial report

`internal/netbox`'s `List` returns a `*TruncatedError` when it reaches the page cap rather
than returning what it collected — a caller cannot tell a partial answer from a complete one.

For a sweep that distinction is the difference between a report and a false accusation: a
partial list makes live objects look absent, so an object whose page never arrived would be
reported as an orphan while its CR is sitting right there. So a truncated list refuses **the
whole run**, with `Reason=Truncated`, and the previous findings stand.

One failing kind refuses the whole run too, for the same reason: a report covering the kinds
that happened to answer is indistinguishable from a complete one, and the missing kind is
silently exonerated.

If you hit `Truncated`, you have more than `maxPages × pageSize` stamped objects of one kind —
250 000 with the defaults. Raise `NetBoxEndpoint.spec` page size, or narrow the sweep.

## Cost: what one run actually asks NetBox for

**One run over N kinds issues N list calls, and each follows `⌈objects / pageSize⌉` pages.**

```
requests = Σ over kinds of  ⌈ stamped objects of that kind in this cluster / pageSize ⌉
```

With the default `pageSize: 250`, a sweep over 3 kinds with fewer than 250 stamped objects
each is **3 HTTP requests**, once per `spec.interval`. 10 000 prefixes and 2 000 VLANs over
those same 3 kinds is `40 + 8 + 1 = 49`. `status.summary.scanned` and the `lists` key on the
`sweep complete` log line let you compute it after the fact.

Zero of those requests are writes. Zero Kubernetes API calls: the CRs come from the informer
caches their own controllers already run.

Two notes on the arithmetic:

- **`brief=true` is deliberately not used.** It would cut the response to id, display and url
  — but the stamp lives in `custom_fields`, which the brief serializer omits, so a brief list
  cannot tell a claimed object from an orphan. The full serialization is the price of being
  able to answer the question at all.
- **`spec.interval` defaults to `24h`, not to the endpoint's resync period.** A sweep on a
  resync cadence is a standing load on somebody's NetBox in exchange for a report nobody reads
  that often. An orphan is not urgent — it is a leak, and leaks are counted daily. Set it
  lower for a migration week; set `suspend: true` to stop it during a maintenance window
  without losing the report.

A sweep watches nothing but itself. It is not woken by CR events on the kinds it scans, which
is what keeps `interval` an actual bound.

## Reading a report

```yaml
status:
  lastRunTime: "2026-08-24T03:00:04Z"
  nextRunTime: "2026-08-25T03:00:04Z"
  lastRunDuration: 412ms
  summary:
    scanned: 214        # stamped objects examined, all kinds
    claimed: 211        # matched to a live CR: the healthy ones
    orphans: 2          # unclaimed for longer than gracePeriod
    suspected: 0        # unclaimed, still inside gracePeriod
    unattributed: 1     # stamped, no usable owner stamp
    foreign: 0          # this cluster, another namespace. Counted, never listed
  findings:
    - kind: NetBoxPrefix
      netboxID: 4471
      display: 10.20.30.0/24
      url: https://netbox.example.com/api/ipam/prefixes/4471/
      owner: netboxprefix/homelab/legacy-dmz
      uid: 8f0c1e5e-3a4b-4d1f-9c2a-71f0d3b8e5a1
      firstSeen: "2026-08-19T03:00:02Z"
      reason: Orphaned
  findingsTruncated: false
  conditions:
    - type: Ready
      status: "True"
      reason: Complete
      message: >-
        scanned 214 stamped object(s) over 2 kind(s) in 2 list call(s): 211 claimed,
        2 orphaned, 0 suspected, 1 unattributed, 0 in other namespaces; nothing was deleted
```

**`status.findings` is the durable record, and that is the point of the whole kind.** Events
age out — the default retention is an hour — and a metric has no detail. A finding written
into `status` is still there tomorrow, greppable across a namespace with
`kubectl get netboxsweeps -A -o yaml`, and it carries the three things needed to act:
`owner` names the manifest to go and look at, `url` opens the object in NetBox, and
`firstSeen` says how long it has been like that.

`findings` is capped at `spec.maxFindings` (default 100) with `findingsTruncated: true` beyond
it. The cap is not a nicety: an etcd object has a size limit, and a status carrying fifty
thousand entries does not get rejected on its own — it takes the CR with it.
`status.summary` always carries true counts, whatever the cap did, and the full list always
reaches the `debug` log (`"sweep finding"`).

The cap drops the least actionable: findings are ordered `Orphaned`, then `Suspected`, then
`Unattributed`, and within each by kind and NetBox id. So a truncated report still shows you
every orphan it can fit.

## What to do about a finding

| Finding | Likely cause | Remedy |
|---|---|---|
| `Orphaned`, and you recognise the `owner` | The CR was removed from Git while the operator was down. | Re-apply the manifest and let the engine adopt the object (`onConflict: Adopt`), or delete it in NetBox by hand. |
| `Orphaned`, and `deletionPolicy: Retain` was set | Working as intended. The object was left behind on purpose. | Nothing, or delete it by hand when it is genuinely finished with. This is the most likely false positive in any report. |
| `Orphaned` after a cluster rebuild | Every CR has a new `metadata.uid`, so every stamp names a CR that no longer exists. | Re-apply the manifests: the engine adopts by natural key and re-stamps with the new uid, and the finding clears on the next run. **Expect a full report the first time a sweep runs after a rebuild.** |
| `Unattributed` | Written before `k8s_owner` existed, or by `netbox-populator`. | `nbctl adopt` (NBO-039) once it lands; until then, stamp it by hand or accept it. |
| Everything at once | Almost always a bug in the *operator* or a misconfiguration, not a real mass orphan. | Check `Ready`'s reason and `summary.claimed`. `claimed: 0` with a large `orphans` means claims are not being seen at all — start with the endpoint's mode. |

Adopting is not this kind's job: `nbctl adopt` is, and a sweep's report is its input.

## Metrics and alerting

### `netbox_operator_sweep_findings`

| | |
|---|---|
| Type | Gauge |
| Labels | `kind`, `reason` (`Orphaned`, `Suspected`, `Unattributed`) |
| Cardinality | ~120 kinds × 3 reasons = **360** worst case |

A gauge and not a counter: the question is how many are outstanding *now*, and the same orphan
is found again by every run. Every scanned kind is set on every completed run, **zeros
included**, so an orphan that somebody adopts or deletes by hand shows up as the series
returning to zero and an alert on it clears.

A **refused** run does not touch it. Zeroing on refusal would report "no orphans" for the one
state in which the sweep could not see anything.

```promql
# Something has been left behind. Not urgent, and not nothing.
sum by (kind) (netbox_operator_sweep_findings{reason="Orphaned"}) > 0
```

> **Limitation, stated plainly.** The gauge is not labelled by the sweep's namespace or name,
> because those are user input and this package does not put user input in a label
> ([observability](observability.md), *Cardinality*). The consequence is that **two sweeps
> covering the same kind in two namespaces write the same series and the last run wins.**
> `status.findings` is the authoritative record; one sweep per kind per cluster is the
> configuration that makes this metric mean what it says.

### `netbox_operator_sweep_runs_total`

| | |
|---|---|
| Type | Counter |
| Labels | `result` — `Complete`, or a refusal reason |
| Cardinality | **10** series |

The freshness half. A findings gauge sitting at zero is either a clean cluster or a sweep that
has been refused since the last time it could see anything, and from metrics alone the only
way to tell is that `Complete` has stopped increasing.

```promql
# The sweep has not completed a run in two days. The report you are looking at is stale.
increase(netbox_operator_sweep_runs_total{result="Complete"}[2d]) == 0

# Refused, repeatedly. The reason label says which runbook row above applies.
sum by (result) (rate(netbox_operator_sweep_runs_total{result!="Complete"}[1h])) > 0
```

### Events

| Reason | Type | When |
|---|---|---|
| `OrphansFound` | Normal | A completed run confirmed at least one orphan. Normal, not Warning: an orphan is a fact about NetBox, not a malfunction of the operator. |
| `SweepRefused` | Warning | A run did not happen, and the findings in `status` are now older than they look. Emitted on the transition only, so a NetBox that is down does not fill the namespace. |

Events are the notification; `status` is the record. Never read a missing Event as a missing
orphan — the recorder aggregates, and Events age out.

## Worked example

A namespace migrating off `netbox-populator`, where nobody is sure what is left over.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxEndpoint
metadata:
  name: homelab
  namespace: homelab
spec:
  url: https://netbox.example.com
  tokenSecretRef: {name: netbox-token}
  mode: Apply                 # a sweep refuses a DryRun endpoint
  driftMode: Correct          # and refuses driftMode: Off, and Report
  managedBy:
    clusterID: prod-eu        # unique per cluster. This is the scope of every sweep
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSweep
metadata:
  name: nightly
  namespace: homelab
spec:
  endpointRef: homelab
  kinds: [NetBoxPrefix, NetBoxVLAN, NetBoxVRF]
  interval: 24h
  gracePeriod: 1h
```

1. **Wait for the endpoint.** Nothing can be attributed until `ProvenanceReady` is `True` —
   the `k8s_cluster`, `k8s_uid` and `k8s_owner` definitions have to exist in NetBox first.

   ```console
   $ kubectl get netboxendpoint homelab -n homelab \
       -o jsonpath='{.status.managedBy}{"\n"}'
   {"clusterID":"prod-eu","customFields":["k8s_allocation_identity","k8s_cluster","k8s_owner","k8s_uid"],"tag":"k8s-managed","tagID":31}
   ```

2. **Let every object get stamped.** Objects the operator created before `spec.managedBy` was
   set are stamped on their next reconcile — within one `resyncPeriod`. A sweep run before
   that reports fewer `claimed` and more `unattributed` than it will an hour later, so give it
   a resync period before believing the first report.

3. **Read the first report.** Expect `unattributed` to be non-zero on a migration: those are
   the populator's objects, which carry the tag and nothing else.

   ```console
   $ kubectl get netboxsweep nightly -n homelab -o jsonpath='{.status.summary}{"\n"}'
   {"scanned":214,"claimed":186,"orphans":3,"suspected":0,"unattributed":25,"foreign":0}
   ```

4. **Work the `Orphaned` list.** Three prefixes with owner stamps naming CRs that are not in
   Git any more. For each: re-apply the manifest if it should exist, or delete it in NetBox if
   it should not.

5. **Work the `Unattributed` list** with `nbctl adopt`, or by stamping them, or by deciding
   they are fine as they are. Nothing about them is urgent and nothing about them is a
   failure — they are objects the operator has no evidence about.

6. **Leave the sweep running.** `orphans` should sit at zero on a healthy namespace, and an
   alert on it going non-zero is the earliest signal that a delete did not complete.

## Troubleshooting

**`orphans` is enormous and `claimed` is zero.**
The claims are not being seen at all. In order of likelihood: the endpoint is `mode: DryRun`
or `driftMode: Report`, so no CR ever got a `status.id` — but then the sweep would have
refused, so check `Ready` first; or the CRs are in a different namespace from the sweep, which
makes them `foreign` rather than `claimed`; or `k8s_uid` is not being stamped, which
`status.managedBy.customFields` on the endpoint will tell you.

**Everything is `unattributed`.**
`k8s_owner` is not in the endpoint's `status.managedBy.customFields`, so nothing is being
stamped with an owner. Either `spec.managedBy.ownerField` was set to `""`, or the bootstrap
could not create the definition (`ProvenanceReady` will say which).

**A finding I fixed is still there.**
Findings are from the last **completed** run, and the default interval is a day. Check
`status.lastRunTime`. To force a run, edit anything on the sweep — a `spec` change triggers a
reconcile; an annotation change works too.

**`Ready=True` but `findings` is empty and I know there are orphans.**
Check `summary.foreign`. An object stamped for a different namespace is not this sweep's to
report, however sure you are that it is abandoned — create a sweep in *that* namespace. Then
check `summary.scanned`: zero means the tag or cluster filter matched nothing, so the objects
are either untagged (structurally invisible) or stamped for another `clusterID`.

**The sweep reports an object I deleted a CR for on purpose.**
`deletionPolicy: Retain` did exactly what it says. The report is correct — the object *is*
unclaimed — and this is the most likely false positive in any report. It is also why a sweep
does not delete: guessing which retained object was retained on purpose is not a guess a tool
gets to make.

## Related

- [Provenance](provenance.md) — the stamp everything here reads, why stamping is not
  mandatory, and why two clusters sharing one NetBox are never serialised.
- [Deletion](../concepts/deletion.md) — `deletionPolicy: Delete` and `Retain`, and why a
  finalizer that cannot finish keeps the CR alive rather than leaking the object.
- [Observability](observability.md) — every other metric, and the log key set.
- [`NetBoxSweep` reference](../reference/netboxsweep.md) — every field, every condition.
- [ADR-0002: CRD scoping](../decisions/0002-crd-scoping.md) — why a sweep is namespaced, and
  why that is a safety property here rather than a consequence.
