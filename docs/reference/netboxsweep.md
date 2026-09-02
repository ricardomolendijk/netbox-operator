# `NetBoxSweep`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxSweep` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbsweep` |
| Status subresource | **yes** |

A `NetBoxSweep` reports NetBox objects that carry this cluster's provenance stamp and have no
CR left to reconcile them. It describes **no** NetBox object of its own.

Two things to get straight before anything else.

**It never deletes.** There is no `action` field, no annotation and no flag that makes it
delete a NetBox object. The controller holds a client narrowed to `List`, so there is no
`Delete` to call by accident. A leaked object is visible in NetBox and reclaimable at any
time; one freed by mistake may already have been handed to somebody else, and no undo exists
for that. The reasoning, and the shape a deletion mode would have to take, are in
[`docs/operations/sweeps.md`](../operations/sweeps.md).

**Its scope is the endpoint's cluster stamp, not its own spec.** Two clusters may share one
NetBox and are never coordinated, so a sweep only ever considers objects stamped with **its
own** `spec.managedBy.clusterID` — a server-side exact-match filter, applied by NetBox. A
sweep with no stamp to filter on refuses to run rather than scanning cluster-wide.

## Minimal example

`spec.kinds` is the only field with no default, besides `endpointRef`. Everything else is a
knob.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSweep
metadata:
  name: nightly
  namespace: homelab
spec:
  endpointRef: homelab
  kinds: [NetBoxPrefix, NetBoxVLAN]
```

It needs a `NetBoxEndpoint` that is `Ready`, in `mode: Apply`, with `driftMode` not `Off`, and
with `spec.managedBy.clusterID` set:

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxEndpoint
metadata:
  name: homelab
  namespace: homelab
spec:
  url: https://netbox.example.com
  tokenSecretRef: {name: netbox-token}
  managedBy:
    clusterID: prod-eu
```

## Full example

Every field, with the defaults written out and commented as such.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSweep
metadata:
  name: nightly
  namespace: homelab
spec:
  endpointRef: homelab
  kinds:                       # required, MinItems=1, no wildcard
    - NetBoxPrefix
    - NetBoxVLAN
    - NetBoxVRF
  interval: 24h                # default
  gracePeriod: 1h              # default; 0s reports on first sight
  maxFindings: 100             # default; 1..1000
  timeout: 10m                 # default
  suspend: false               # default
```

## `spec`

### `spec.endpointRef`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Default | none |
| Validation | `MinLength=1` |

Names the `NetBoxEndpoint` to scan through, in this sweep's own namespace. There is no
cluster-wide default endpoint.

Both halves of the sweep's scope come from it: `spec.managedBy.tag` and
`spec.managedBy.clusterID`. It is also what makes a namespaced sweep a real boundary — the
endpoint is namespaced too, so a sweep in `team-a` cannot reach a NetBox that `team-a` cannot
already write to.

**If it is wrong.** A name that does not resolve, or an endpoint that is not `Ready`, is
`Ready=False, Reason=EndpointNotReady` with the namespace and name in the message. Retried
every 5 minutes. Nothing in `status.findings` is touched.

### `spec.kinds`

| | |
|---|---|
| Type | `[]string`, `listType=atomic` |
| Required | **yes** |
| Default | none |
| Validation | `MinItems=1`, `MaxItems=128`, each `MaxLength=63` and `Pattern=^NetBox[A-Za-z0-9]+$` |

The Kubernetes Kinds to scan. **There is no wildcard, and that is deliberate**: a wildcard
would silently start listing every one of the ~120 kinds in the catalogue the moment a new one
shipped, which is a load change on somebody else's NetBox that nobody asked for. Scanning a
kind is an explicit act.

Resolution is a table lookup keyed on the Kind name against the descriptor registry, so a kind
added to the catalogue becomes sweepable with no change to this controller.

**If it is wrong.** The pattern is enforced by admission, so `netboxprefix` or `Prefix` is
rejected at `kubectl apply`. Everything else surfaces at reconcile, and both refuse the whole
run rather than skipping the one kind — a report covering the kinds that happened to answer is
indistinguishable from a complete one:

- a Kind this build does not carry → `Ready=False, Reason=UnknownKind`;
- a Kind whose NetBox model has no `custom_fields` column → `Ready=False,
  Reason=KindNotStampable`. `NetBoxTag` is the case: `extras.Tag` has nowhere to put a stamp,
  so its objects can never be attributed to this cluster
  ([provenance](../operations/provenance.md), *Is stamping mandatory?*).

### `spec.interval`

| | |
|---|---|
| Type | `string` (Go duration) |
| Required | no |
| Default | `24h` (from `+kubebuilder:default="24h"`) |
| Validation | none beyond the duration format |

How often to run. A day rather than the endpoint's resync period: one run lists every stamped
object of every listed kind, so a sweep on a resync cadence is a standing load in exchange for
a report nobody reads that often. An orphan is a leak, and leaks are counted daily.

Requeues are jittered, so several sweeps applied together do not all wake at the same instant.

**If it is wrong.** A duration that does not parse is rejected by admission. Zero — reachable
only on an object written before the field existed — falls back to `24h` in the controller,
rather than becoming a hot loop.

### `spec.gracePeriod`

| | |
|---|---|
| Type | `string` (Go duration) |
| Required | no |
| Default | `1h` (from `+kubebuilder:default="1h"`) |
| Validation | none beyond the duration format |

How long an object must have been continuously unclaimed before it is reported as `Orphaned`
rather than `Suspected`.

"The CR is gone" and "the CR is between a delete and a re-apply" look identical from NetBox,
and so does "the operator was down while Git changed". They are told apart only by waiting.
Measured against `status.findings[].firstSeen`, which is written by the operator and carried
forward run to run — so it uses one clock, and NetBox's `last_updated` is never consulted.

`0s` reports every finding as `Orphaned` on first sight. Fine for a one-off audit, poor for
anything unattended.

`Unattributed` findings ignore it entirely: that verdict is "I cannot tell", not an accusation,
and waiting would not change it.

**If it is wrong.** Nothing fails. Too short produces reports that flap during a re-apply; too
long delays confirmation. Neither can promote a `Suspected` finding by accident, because the
clock only ever moves forward from `firstSeen`.

### `spec.maxFindings`

| | |
|---|---|
| Type | `integer` (int32) |
| Required | no |
| Default | `100` (from `+kubebuilder:default=100`) |
| Validation | `Minimum=1`, `Maximum=1000` |

Caps `status.findings`.

Not a nicety: an etcd object has a size limit, and a status carrying fifty thousand entries
does not get rejected on its own — it takes the CR with it. Beyond the cap
`status.findingsTruncated` is `true`, `status.summary` still carries true counts, and the whole
list reaches the `debug` log.

The cap drops the least actionable, because findings are sorted `Orphaned`, then `Suspected`,
then `Unattributed`, and within each by kind and NetBox id. A truncated report therefore still
shows every orphan it can fit. Exactly reaching the cap is not exceeding it.

**If it is wrong.** Out of range is rejected by admission. A cap smaller than the real finding
count is not an error — that is what `findingsTruncated` is for.

### `spec.timeout`

| | |
|---|---|
| Type | `string` (Go duration) |
| Required | no |
| Default | `10m` (from `+kubebuilder:default="10m"`) |
| Validation | none beyond the duration format |

Bounds one whole run, every kind together.

**If it is wrong.** Exceeding it is `Ready=False, Reason=Timeout` with zero findings written —
a failed run, never a partial one. Raise it, or split the sweep into several with fewer kinds
each.

### `spec.suspend`

| | |
|---|---|
| Type | `boolean` |
| Required | no |
| Default | `false` (from `+kubebuilder:default=false`) |
| Validation | none |

Stops scheduling without deleting the object or its findings. The lever to pull when a sweep
is generating load at the wrong moment.

While suspended: `Suspended=True, Reason=Suspended`, `status.nextRunTime` is cleared,
`status.findings` and `status.summary` are preserved, and **`Ready` is left exactly as the
last real run set it** — a suspended sweep has not failed, and overwriting its `Ready` would
lose the reason the last run settled on.

There is no requeue while suspended. Setting it back to `false` is a watch event on the
object, so the next run starts immediately.

## `status`

| Field | Type | Populated by | When |
|---|---|---|---|
| `lastRunTime` | `Time` | A **completed** run | Never moved by a refused run, so the gap between it and now is how stale the findings are |
| `nextRunTime` | `Time` | Every pass | `now + interval` after a completed run, `now + 5m` after a refusal, cleared while suspended |
| `lastRunDuration` | `string` | A completed run | Rounded to the millisecond |
| `observedGeneration` | `int64` | Every pass | Always set, because `kubectl wait` lies without it |
| `summary` | `SweepSummary` | A completed run | True counts, whatever `maxFindings` did to the list |
| `findings` | `[]SweepFinding` | A completed run | Sorted and capped |
| `findingsTruncated` | `bool` | A completed run | `true` when there were more findings than the cap |
| `conditions` | `[]Condition` | Every pass | |

**A refused run leaves `summary`, `findings`, `findingsTruncated` and `lastRunTime` exactly as
the last completed run left them.** That asymmetry is the whole design of this status: an
empty `findings` list must only ever mean "the last complete scan found nothing", never "the
last scan could not see anything". Read `Ready` and `lastRunTime` together.

A pass that observes exactly what is already stored writes nothing at all — every watcher
wakes on a `resourceVersion` bump, so a status that says nothing new is an Argo CD refresh and
an audit entry per sweep per run.

### `status.summary`

| Field | Meaning |
|---|---|
| `scanned` | Stamped NetBox objects examined, across every listed kind |
| `claimed` | Matched to a live CR in this namespace, by `status.id` or by `k8s_uid`. The healthy ones |
| `orphans` | Unclaimed for longer than `gracePeriod` |
| `suspected` | Unclaimed, still inside `gracePeriod` |
| `unattributed` | Stamped, with no usable owner stamp |
| `foreign` | This cluster, another namespace or another Kind. Counted and never listed, so `scanned` adds up and nobody reads the gap as a lost object |

`scanned = claimed + orphans + suspected + unattributed + foreign`, always.

### `status.findings[]`

| Field | Meaning |
|---|---|
| `kind` | The Kind whose descriptor found it, which is also the kind of CR that would have to exist for it not to be a finding |
| `netboxID` | NetBox primary key. With `kind`, the pair that identifies a finding across runs |
| `display` | NetBox's own `display` string — the only field a human can search the UI on |
| `url` | Absolute NetBox API URL, copied verbatim from the list response |
| `owner` | The `k8s_owner` stamp: `<kind>/<namespace>/<name>` of the CR that last wrote it. Empty on an `Unattributed` finding, which is what makes it unattributable |
| `uid` | The `k8s_uid` stamp. What separates "the CR was deleted" from "the CR was deleted and re-applied": the second has a new UID, so the old object is orphaned even though a CR of that name exists |
| `firstSeen` | When this sweep first found it unclaimed. The grace-period clock |
| `reason` | `Orphaned`, `Suspected` or `Unattributed` |

`firstSeen` is carried forward from the previous run's status, keyed on `(kind, netboxID)`. A
controller restart does not reset it. If `status` is lost — or the previous run's cap dropped
this finding — the clock restarts, which fails towards `Suspected` and never towards
`Orphaned`.

## Conditions

| Type | `True` when | `False` when | Reasons |
|---|---|---|---|
| `Ready` | The last run scanned every listed kind and the findings are the whole answer | The run was refused; findings are the **previous** run's | `Complete`, `EndpointNotReady`, `EndpointDryRun`, `DriftOff`, `ProvenanceDisabled`, `UnknownKind`, `KindNotStampable`, `Truncated`, `Timeout`, `APIError` |
| `Suspended` | `spec.suspend` is `true` | It is not | `Suspended`, `Scheduled` |

### Reason glossary

| Reason | Meaning |
|---|---|
| `Complete` | Every listed kind was scanned in full. The message carries the counts. |
| `EndpointNotReady` | The `NetBoxEndpoint` has no usable client, or could not be read. |
| `EndpointDryRun` | The endpoint hands out a client that cannot write — `mode: DryRun`, **or** `driftMode: Report`. A suppressed create never returns an id, so no CR has a `status.id` and every object would look unclaimed: an entire namespace reported as orphaned from one field. Read off the client rather than the spec, so a mode that suppresses writes cannot be missed by a second copy of the rule. |
| `DriftOff` | `driftMode: Off`. The operator is not tracking NetBox state, so the absence of a claim proves nothing. |
| `ProvenanceDisabled` | `spec.managedBy` writes no stamp, or the cluster / uid / owner custom field does not provably exist in NetBox. All three are required: the cluster field is the scope, the uid field is the claim check that survives a lost status, and the owner field is the only thing that says which namespace an object belongs to. |
| `UnknownKind` | A `spec.kinds` entry has no descriptor in this build. |
| `KindNotStampable` | A `spec.kinds` entry maps to a NetBox model with no `custom_fields` column. |
| `Truncated` | A list paginated past the client's page cap. A partial list makes live objects look absent, which is exactly the input that turns a report into a false accusation, so the whole run is refused. |
| `Timeout` | The run exceeded `spec.timeout`. |
| `APIError` | NetBox was unreachable, rate limiting or failing. |
| `Suspended` | On `Suspended`: `spec.suspend` is `true`. |
| `Scheduled` | On `Suspended=False`: running normally. |

Every refusal has its own reason because "the sweep did not run" has as many different fixes
as it has causes, and a single `Refused` would send all of them to the same runbook page.

### Retry intervals

| State | Next run |
|---|---|
| `Ready=True` | `spec.interval`, jittered |
| `Ready=False`, any reason | 5 minutes, jittered |
| `Suspended=True` | never; a spec edit is a watch event |

The refusal retry is fixed and short rather than `spec.interval`, because every refusal is a
state somebody else is fixing — an endpoint coming `Ready`, a `driftMode` being changed back,
a NetBox coming back up — and waiting a day to notice would make the sweep useless for the
whole day after a five-minute outage.

## Kind-specific behaviour

**One list call per kind, and none of them a write.** The scan is
`GET /api/<endpoint>/?tag=<tag>&cf_<clusterField>=<clusterID>`, paginated. So one run over N
kinds is `Σ ⌈objects / pageSize⌉` requests, which is N with the default page size of 250 and
fewer than 250 stamped objects per kind. Zero writes, and zero Kubernetes API calls — the CRs
come from the informer caches their own controllers already run.

**`brief=true` is deliberately not used**, even though it would cut the response to id,
display and url. The stamp lives in `custom_fields`, which NetBox's brief serializer omits, so
a brief list cannot tell a claimed object from an orphan.

**Claims are checked before attribution, and by two keys.** `status.id` catches the ordinary
case; `k8s_uid` catches a CR whose status was lost. Checking claims *first* is what makes a
claimed object with no stamp at all read as claimed rather than as unattributed. A terminating
CR counts as a claim: its finalizer has not come off, so the operator is still going to deal
with the object.

**A sweep watches nothing but itself.** No watch on the endpoint and no watch on the swept
kinds: waking a daily list of every stamped object on every CR write would turn `interval`
into a suggestion.

**The namespace comes out of `k8s_owner`, not out of a filter.** An object stamped for another
namespace is counted in `summary.foreign` and never listed — that namespace's own sweep is
where it belongs, and a sweep must not act on the disappearance of a namespace it cannot see.

## Printer columns

```console
$ kubectl get netboxsweeps -n homelab
NAME      ENDPOINT   ORPHANS   SUSPECTED   UNATTRIBUTED   READY   REASON     LAST RUN
nightly   homelab    2         0           1              True    Complete   4h
audit     homelab    0         0           0              False   Truncated  6d
```

| Column | JSONPath |
|---|---|
| `Endpoint` | `.spec.endpointRef` |
| `Orphans` | `.status.summary.orphans` |
| `Suspected` | `.status.summary.suspected` |
| `Unattributed` | `.status.summary.unattributed` |
| `Ready` | `.status.conditions[?(@.type=="Ready")].status` |
| `Reason` | `.status.conditions[?(@.type=="Ready")].reason` |
| `Last Run` | `.status.lastRunTime` |

`Last Run` next to `Ready` is not cosmetic: `Ready=False` with an old `Last Run` is the state
where the numbers to its left are real but stale, and reading them as current is the mistake
this table is laid out to prevent.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Nothing happens at all | no conditions | The CRD is installed and the controller is not, or the object is in a namespace the manager does not watch | Check the manager log for `netboxsweep` |
| `Ready=False` immediately | `EndpointNotReady` | Endpoint missing, not `Ready`, or in another namespace | `kubectl get netboxendpoint -n <ns>` |
| `Ready=False`, endpoint looks fine | `EndpointDryRun` | `mode: DryRun` **or** `driftMode: Report` | Set `mode: Apply` and `driftMode: Correct`. A sweep needs an endpoint whose CRs actually get ids |
| `Ready=False` | `ProvenanceDisabled` | No `spec.managedBy.clusterID`, or the bootstrap has not created the definitions | Check `status.managedBy.customFields` on the endpoint for `k8s_cluster`, `k8s_uid` and `k8s_owner` |
| `Ready=False` | `KindNotStampable` | `NetBoxTag` (or another model with no `custom_fields`) is in `spec.kinds` | Remove it |
| `Ready=False` | `Truncated` | More than `maxPages × pageSize` stamped objects of one kind | Raise the endpoint's page size, or split the sweep |
| `orphans` huge, `claimed: 0` | `Complete` | Claims are not being seen — almost always the endpoint's mode, or the CRs are in another namespace | See [sweeps.md](../operations/sweeps.md#troubleshooting) |
| Everything `unattributed` | `Complete` | `k8s_owner` is not being stamped | `spec.managedBy.ownerField` was emptied, or its definition was never created |
| A fixed finding is still listed | `Complete` | The report is from the last completed run, and the default interval is a day | Check `status.lastRunTime`; edit the object to force a run |
| An orphan I expect is missing | `Complete` | It is untagged (structurally invisible), stamped for another `clusterID`, or stamped for another namespace (`summary.foreign`) | Check `summary.scanned` and `summary.foreign` |
| An object I retained on purpose is reported | `Complete` | Working as intended: `deletionPolicy: Retain` leaves it unclaimed | Nothing. This is the most likely false positive in any report, and the reason a sweep does not delete |

## Related

- [Sweeps](../operations/sweeps.md) — the runbook: why reporting and not reclaiming, the cost
  arithmetic, the alerting, and a worked migration example.
- [Provenance](../operations/provenance.md) — the stamp every verdict here reads.
- [Deletion](../concepts/deletion.md) — `deletionPolicy: Retain`, which is what puts most of
  the objects in most reports there on purpose.
- [ADR-0002: CRD scoping](../decisions/0002-crd-scoping.md) — why this kind is namespaced, and
  why that is a safety property here rather than a consequence.
- [ADR-0005: GitOps coexistence](../decisions/0005-gitops-coexistence.md) — why the operator
  never writes a spec, which is also why a sweep cannot fix what it finds.
