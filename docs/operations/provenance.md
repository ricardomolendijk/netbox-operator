# Provenance: what the operator writes into NetBox, and how to stop it

| | |
|---|---|
| Configured by | [`NetBoxEndpoint.spec.managedBy`](../reference/netboxendpoint.md#specmanagedby) |
| Default | **off** — an unset `spec.managedBy` stamps nothing |
| Lands with | NBO-075 (M1) |

Every NetBox object the operator manages can be attributed to a cluster, a namespace and a
CR. That attribution is a **stamp**: one NetBox tag, and a small set of custom fields. This
page is what the stamp contains, why each part exists, what it costs, and what stops working
if you switch it off.

It is off until you configure it, because the one thing the stamp cannot do without is a
cluster identifier, and that is not something an operator may invent — see
[Why the cluster identifier is not derived](#why-the-cluster-identifier-is-not-derived).

## Turning it on

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
  managedBy:
    clusterID: prod-eu                              # required; no default, deliberately
    tag: k8s-managed                                # default
    uidField: k8s_uid                               # default
    clusterField: k8s_cluster                       # default
    ownerField: k8s_owner                           # default
    allocationIdentityField: k8s_allocation_identity  # default
    bootstrap: true                                 # default
```

```console
$ kubectl get nbep homelab -o jsonpath='{.status.managedBy}' | jq
{
  "clusterID": "prod-eu",
  "tag": "k8s-managed",
  "tagID": 41,
  "customFields": ["k8s_allocation_identity", "k8s_cluster", "k8s_owner", "k8s_uid"]
}
```

`status.managedBy.tagID` is the part you cannot work out from the spec: a NetBox object is
tagged **by id**, not by name.

## What gets written

Onto every managed object, on create and on adopt, and re-applied on any later update:

| What | NetBox column | Value | Consumer |
|---|---|---|---|
| The tag | `tags` | `spec.managedBy.tag`, by id | `NetBoxSweep` (NBO-046) decides what it may delete by this tag alone |
| Owning CR's UID | `custom_fields.k8s_uid` | `metadata.uid` of the CR | NBO-006: answers "is this the same object", which a name cannot |
| Cluster | `custom_fields.k8s_cluster` | `spec.managedBy.clusterID`, verbatim | NBO-047: two clusters managing one object differ here and nowhere else |
| Owning CR | `custom_fields.k8s_owner` | `<lowercased kind>/<namespace>/<name>` | The human-readable half: which manifest to go and edit |
| Allocation identity | `custom_fields.k8s_allocation_identity` | `sha256(url \n namespace \n kind \n name)[:16]`, written by a **claim** and by nothing else | [ADR-0005 §3](../decisions/0005-gitops-coexistence.md): it is how a claim finds its own previous allocation, so the same manifest reclaims the same address on a rebuilt cluster ([claims](../concepts/claims.md)) |

`k8s_owner` is spelled the same way as the `netbox.kubeforge.org/generated-by` annotation in
[ADR-0005 §2](../decisions/0005-gitops-coexistence.md), so one string identifies a CR on both
sides of the fence.

`k8s_allocation_identity` is the one field the ordinary stamp never writes: the value belongs to
the allocation engine, and an object that is not the result of a claim carries the definition
and no value. NetBox answers a `custom_fields` key that has no `extras.CustomField` behind it
with a **400**, so the definition has to exist before the first claim can write one, which is
why it is created for every stamping endpoint whether or not a claim ever uses it.

Set `allocationIdentityField: ""` on an endpoint that will never serve a claim and it is not
created at all — but note the consequence: a claim on that endpoint **refuses to allocate**,
`Ready=False, Reason=IdempotencyKeyUnavailable`, zero POSTs. For a claim the identity store is
mandatory rather than decorative, because without it a lost HTTP response is unrecoverable and
every retry burns another address. The same is true of leaving `spec.managedBy` off entirely.

The definition is created with `filter_logic: exact` rather than NetBox's own default of
`loose`. Every one of these fields is an identity, and `?cf_k8s_allocation_identity=<value>`
under loose filtering is a *substring* match — the wrong answer to "which object **is** this
one". An existing definition is never narrowed to match, for the same reason the object-type
list is only ever widened: it may be shared with your own use of it.

### Tags are merged, never replaced

`tags` is a full-replacement list in the NetBox API: a `PATCH` carrying `tags: [41]` removes
every other tag on the object. So the operator **unions** its tag into whatever is already
there. A tag you applied by hand in the NetBox UI survives every reconcile.

The one exception is a kind whose own spec declares `tags` — none does in `v1alpha1`. There
the spec owns the list and only the operator's tag is added to it, because otherwise a tag
removed from a manifest could never be removed from NetBox.

### Custom fields are merged too

NetBox merges a partial `custom_fields` `PATCH`, and the operator sends only the keys it
owns. Custom fields somebody else defined on the same object types are read, ignored and
left alone.

### Which kinds carry a stamp

Only the ones whose NetBox model has the columns. Two mixins decide it, and each kind
declares what it has as data on its `Descriptor`
([`internal/registry`](../concepts/descriptor.md)):

| Kind | NetBox model | `tags` | `custom_fields` | Stamped |
|---|---|---|---|---|
| `NetBoxSite` | `dcim.Site` (`PrimaryModel`) | yes | yes | fully |
| `NetBoxRegion` | `dcim.Region` (`NestedGroupModel`) | yes | yes | fully |
| `NetBoxTag` | `extras.Tag` (`TagBase`) | no | no | **not at all** |

`extras.Tag` inherits django-taggit's `TagBase` and neither `TagsMixin` nor
`CustomFieldsMixin` ([`docs/netbox-schema.md` → `extras.Tag`](../netbox-schema.md)) — a tag
cannot be tagged. So a `NetBoxTag` is a fully managed object that carries no stamp, by
construction and permanently. Its `status.provenance` is unset, and that is the honest value.

Writing `tags` to such a kind would not fail loudly: NetBox ignores a column it does not
know, the next read would find the value absent, and the operator would send it again on
every resync forever.

## Is stamping mandatory?

**No — and it must not become mandatory. `NetBoxSweep` only ever considers stamped objects,
and reports unstamped ones without touching them.**

The tempting alternative is to make the stamp a precondition for managing an object at all,
so that "managed" and "stamped" become the same set and a sweep can treat *anything without
the tag* as an orphan. That is rejected, for three reasons:

1. **It cannot be true.** `extras.Tag` has nowhere to put a stamp, and it is not the last
   such model. A rule that a managed object is always stamped would make `NetBoxTag`
   unmanageable.
2. **It makes an optional feature load-bearing.** `spec.managedBy` is unset by default and
   `bootstrap` can be switched off; both are deliberate, and both would become "the operator
   refuses to work" if a stamp were required.
3. **The sweep's safety does not need it.** What a sweep needs is for the tag to be
   *sufficient* evidence of ownership, not *universal*. "Delete only what carries my tag and
   my cluster id" is safe whether or not other managed objects exist; "delete anything
   without my tag" is not safe under any circumstances, because it is also every object a
   human ever made.

The practical consequence, and the trade this buys: **an unstamped managed object is never
reclaimed by a sweep.** If you switch provenance on after the operator has already created
objects, they are stamped on their next reconcile — within one `resyncPeriod` — and become
sweepable then. Objects on a kind that cannot be stamped never do, and a sweep is expected
to report them so the gap is visible rather than silent.

This is a recommendation rather than a settled law; `NetBoxSweep` (NBO-046) is where it gets
tested against a real cluster.

## Bootstrap: the operator creating its own schema

`extras.CustomField` has a **required** `object_types` list, so the operator has to know
which NetBox models it stamps. That list is **derived from the descriptor registry** — every
registered kind that declares `custom_fields` contributes its `app_label.model`. It is not
hand-maintained, because a hand-maintained list is correct exactly until the next kind lands
and the failure surfaces as a 400 on that kind and nowhere else.

On every endpoint reconcile, before any object of any kind is handed a client, the operator:

1. Looks the tag up by slug. Creates it if absent, with **no** `object_types` — restricting
   the tag to the kinds registered today would make the first object of a kind added
   tomorrow unstampable against a NetBox nobody thought to widen.
2. Looks each custom field up by name. Creates it if absent, as `type: text`, in the
   `Kubernetes` group.
3. **Widens** an existing definition whose `object_types` does not cover every model the
   operator now stamps. Types are only ever added, never removed: the definition may be
   shared with your own use of it, and narrowing somebody else's schema on a resync is not
   something an operator should do.

Every step looks before it writes, so re-running changes nothing — a second pass issues the
same reads and no writes at all.

### When a new kind is registered after bootstrap ran

Step 3 is the answer. A new operator version knows about a NetBox model that the
`CustomField` in your NetBox does not, and the first write to that model would be a 400. The
bootstrap runs at process start and on every endpoint resync, so the definition is widened
at the next reconcile — immediately on upgrade, and within one `resyncPeriod` at worst. If
the token cannot widen it, the **endpoint** fails with `BootstrapFailed` rather than the new
kind failing one object at a time.

### NetBox permissions

The token needs, in addition to whatever the objects themselves require:

| Permission | For |
|---|---|
| `extras.view_tag`, `extras.view_customfield` | looking the definitions up — needed even with `bootstrap: false` |
| `extras.add_tag`, `extras.add_customfield` | creating a missing definition |
| `extras.change_customfield` | widening `object_types` when a kind is added |

## Turning it off

There are three sizes of "off", and they are not the same thing.

### Stamp nothing: leave `spec.managedBy` unset

The default. No tag, no custom fields, no requests to `extras/tags` or
`extras/custom-fields`, and no `ProvenanceReady` condition at all — nothing was asked for, so
there is nothing to report.

**What breaks.** Everything downstream of the stamp:

| Feature | Without a stamp |
|---|---|
| `NetBoxSweep` (NBO-046) | Nothing to sweep. Every object looks like somebody else's, which is safe and useless |
| Multi-writer conflicts (NBO-047) | Invisible. Two clusters reconciling one object just fight, and neither says so ([multi-writer](multi-writer.md)) |
| Allocation reclaim (ADR-0005 §3) | A claim **refuses to allocate at all**: `Reason=IdempotencyKeyUnavailable`, zero POSTs. Not a degraded mode — see above |
| Adoption audit (NBO-006) | An adopted object records nothing about who took it over except in the CR's own `status` |

Drift detection, adoption, deletion and everything else in the engine are unaffected.

### Stamp, but do not touch the schema: `bootstrap: false`

The operator looks the definitions up and never creates one. Use it when a NetBox admin owns
the custom-field schema, or when the operator's token is deliberately read-only on `extras`.

If everything it needs already exists, this is indistinguishable from `bootstrap: true` — the
stamp resolves and the endpoint is `Ready`.

If anything is missing, the **endpoint** goes `Ready=False` with
`Reason=BootstrapDisabled`, and the `ProvenanceReady` message names exactly what to create:

```console
$ kubectl describe nbep homelab
Status:
  Conditions:
    Type:     ProvenanceReady
    Status:   False
    Reason:   BootstrapDisabled
    Message:  bootstrapping netbox provenance: a provenance definition is missing and
              bootstrap is disabled: tag k8s-managed, custom field k8s_cluster
```

Failing the endpoint is deliberate, and it is the whole point of the gate. A stamp that
cannot be written is a `custom_fields` key with no definition behind it, which NetBox answers
with a 400 — on every object, of every kind, through this endpoint, forever. One unusable
endpoint carrying the reason is strictly easier to act on than a hundred identical 400s.

### Stamp less: switch one field off

Set any of `uidField`, `clusterField`, `ownerField` or `allocationIdentityField` to the empty
string and that definition is neither created nor written. The tag and the remaining fields
are unaffected.

```yaml
managedBy:
  clusterID: prod-eu
  allocationIdentityField: ""   # this endpoint will never serve an IP claim
```

Write it as an explicit `""` in YAML — an *absent* key gets the CRD default, which is the
name rather than nothing.

`tag` cannot be switched off. It is the field `NetBoxSweep` keys on, and a provenance
configuration with no tag would be a stamp nothing can find.

## Conditions

| Condition | Status | Reason | Meaning |
|---|---|---|---|
| `ProvenanceReady` | absent | — | `spec.managedBy` is unset |
| `ProvenanceReady` | `True` | `Provisioned` | Every definition exists. The message carries the tag id and the field list |
| `ProvenanceReady` | `False` | `BootstrapDisabled` | `bootstrap: false` and something is missing. `Ready=False` too |
| `ProvenanceReady` | `False` | `BootstrapFailed` | NetBox refused. Usually a token without `extras.add_customfield`. `Ready=False` too |
| `ProvenanceReady` | `False` | `BootstrapSuppressed` | Something is missing and this endpoint cannot write, because `mode: DryRun` or `driftMode: Report`. `Ready` is **unaffected** |

`BootstrapSuppressed` does not fail the endpoint, and that asymmetry is the point: an endpoint
that sends nothing cannot produce the 400 the gate exists to prevent. A rehearsal endpoint
reports what it would have created and carries on, and the payload it reports for each object
includes the stamp — a dry run that omitted it would be rehearsing a different write.

`Authenticated` and `VersionSupported` stay `True` through a bootstrap failure. The bootstrap
runs after both gates, so reaching it means both passed; setting them to `Unknown` would
retract two answers this reconcile did establish and send you to the token or the version,
neither of which is what is wrong.

### Retry intervals

| Reason | Requeue after | Why |
|---|---|---|
| `BootstrapDisabled` | 10m | A human has to create a definition. Nothing the operator does will produce one |
| `BootstrapFailed` | 2m | Usually a permission grant, and two minutes is soon enough to notice it landing |

Both carry ±10% jitter, like every other interval on the endpoint.

## What an object reports

```console
$ kubectl get nbsite home -o jsonpath='{.status.provenance}' | jq
{
  "clusterID": "prod-eu",
  "tag": "k8s-managed",
  "customFields": {
    "k8s_cluster": "prod-eu",
    "k8s_owner": "netboxsite/homelab/home",
    "k8s_uid": "6f1a63c8-2f8d-4a0e-9a1e-3f5c2b7d1e04"
  }
}
```

`status.provenance` is what was **written**, not what is configured. An object stamped before
you edited `spec.managedBy` reports the old stamp until its next reconcile, which is the
honest answer to "what is on the object in NetBox right now". It is unset when nothing was
stamped, including on a kind that cannot carry a stamp.

## Why the cluster identifier is not derived

`clusterID` is required and has no default. The operator could have derived one — the
`kube-system` namespace UID and the API server URL are both to hand, and both are stable
enough to be tempting. Neither is used, because:

- **Nobody can predict it.** The first thing anyone does with a provenance stamp is search
  NetBox for it. `?cf_k8s_cluster=prod-eu` is a query a human can type;
  `?cf_k8s_cluster=8f14e45f-ceea-467a-9d1e-c1d0b06e0e58` is one they have to go and look up
  first, in the cluster, which may be the cluster that just burned down.
- **It changes when you rebuild.** A `kube-system` UID is regenerated by a fresh cluster.
  [ADR-0005 §3](../decisions/0005-gitops-coexistence.md) exists so that a claim reclaims its
  address across exactly that event, and an identity keyed on a value the rebuild changes
  cannot do it.
- **An API server URL is not an identity.** It is a network path, it differs between an
  in-cluster and an out-of-cluster view of the same cluster, and it changes when the load
  balancer does.

Pick a short, stable, human-meaningful string and put it in Git. `prod-eu`, `lab`,
`dc1-mgmt`. It is validated against `^[a-zA-Z0-9._-]+$` so it stays usable in a NetBox
filter.

## Two clusters, one NetBox

Supported, and **never coordinated**. Two clusters with different `clusterID` values
managing the same NetBox object will both keep correcting it towards their own spec, and the
operator does not stop them. What the stamp buys is that the fight is **visible**: whichever
cluster wrote last owns `k8s_cluster`, so a value that keeps changing is the symptom, and
`k8s_owner` names the manifest on the other side. Writing over a foreign `k8s_cluster` also
raises a `Conflict` condition naming the other writer, records the claimant and how long it
has been there in `status.conflict`, emits an Event, counts it in
`netbox_operator_conflicts_total` — and then writes anyway. All four are a report, not a gate
(NBO-047). [Two writers, one NetBox object](multi-writer.md) is the operator's side of it in
full: the classification, what is deliberately *not* reported, and the runbook.

**The operator will not serialise writes between clusters.** Decided, not deferred
([#18](https://github.com/ricardomolendijk/netbox-operator/issues/18)): making a foreign
`k8s_cluster` refuse the write would put a read in front of every write on the hot path, and
then need a lease with a TTL for the cluster that dies mid-write, and then a documented
break-glass for when the lease is wrong. That is a lot of moving parts, each with its own
failure mode, for a problem nobody has reported. Visible-and-noisy is the whole of the
position, and the answer if it hurts is to stop two clusters managing one object — not to
turn a setting on.

So, in practice, if two clusters share a NetBox:

- **Give each one a distinct `clusterID`.** Without that the stamp cannot tell them apart
  and the symptom above is invisible.
- **Partition what they manage.** Two clusters can share a NetBox indefinitely with no
  interaction at all, as long as their manifests describe disjoint sets of NetBox objects.
  Overlap is the only thing that fights.
- **Watch for overlap rather than assuming it away.** `?cf_k8s_cluster=prod-eu` lists what a
  cluster claims; the `Conflict` condition and `status.conflict` are the per-object signal, and
  `increase(netbox_operator_conflicts_total[1h]) > 0` is the cluster-wide one. Alert on a
  window rather than an instant — a conflict during a migration is expected and transient
  ([multi-writer](multi-writer.md#metrics-and-alerting)).
- **Expect no winner.** Neither side backs off, and there is no field that makes one
  authoritative. The write rate is bounded by the shorter of the two resync periods, so the
  cost of leaving it running is API calls and a changelog full of flapping.

Give each cluster the same `tag` and a different `clusterID` if they share a NetBox and you
want one sweep vocabulary; give them different tags if you want their sweeps entirely
independent.

## Related

- [`NetBoxEndpoint` reference](../reference/netboxendpoint.md) — every field, condition and
  reason in full
- [ADR-0005 — Coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md) —
  §2 for how generated CRs are labelled, §3 for the allocation identity this stamp carries
- [Coexisting with Flux and Argo CD](gitops.md) — the NetBox permission model, and what
  `driftMode` does to a write
- [The Descriptor](../concepts/descriptor.md) — where `Taggable` and `CustomFieldable` live,
  and why they are data rather than a list in the engine
- [Two writers, one NetBox object](multi-writer.md) — the three supported multi-cluster
  shapes, what the operator reports when two writers overlap, and what to do about it
- [Drift detection](../concepts/drift.md) — why `tags` is compared as an unordered id set and
  `custom_fields` only on the keys the operator sets
