# References

How one object points at another, and what the API server rejects before the operator
ever sees it.

> **Status.** The `ObjectRef` type, its validation and the typed aliases are built
> (NBO-011), and so is resolution: all four modes, the typed errors and the condition
> vocabulary below (NBO-012). Cross-namespace `name` references are **default deny** and
> need a [`NetBoxRefGrant`](../reference/netboxrefgrant.md) (NBO-014). **Watches** are
> built too (NBO-013): a `name` reference that is waiting is woken by an event on the
> object it is waiting for — or on the grant that authorises it — rather than by the
> resync. See [Ordering and convergence](#ordering-and-convergence). What is not built
> yet: **deferred application**
> ([#27](https://github.com/ricardomolendijk/netbox-operator/issues/27)).
>
> **Grants** ([#26](https://github.com/ricardomolendijk/netbox-operator/issues/26)) and
> **cycle detection over the whole graph** (NBO-016) are both built — see
> [Crossing a namespace](#crossing-a-namespace) and [Cycles](#cycles). What is still missing on the cycle side is the
> engine's half: a `RefCycle` Event, and a `refs_unresolved` metric to alert on.
>
> **To-many references** (NBO-088) and **resolving on an id rather than on the target's
> readiness** (NBO-089) are both built — see
> [To-one and to-many](#to-one-and-to-many) and
> [A reference needs an id](#a-reference-needs-an-id-not-a-ready-target).

A **polymorphic** reference — one NetBox column pair that may point at any of several models
— is a union of the references described here, and has a page of its own:
[Generic references](generic-refs.md).

## What a reference is

A reference names one NetBox object. It is not a NetBox id — except in the one escape
hatch below — because an id is not knowable from a manifest, and a manifest that has to
know one cannot be applied to a fresh NetBox.

The **field name pins the target Kind**. `parentRef` on a `NetBoxRegion` can only be a
`NetBoxRegion`; there is no `kind` discriminator on the wire. A discriminator would make
every ref field accept every Kind and push the check to runtime, so the target is carried
by the field's Go type instead — see [the typed aliases](#the-typed-aliases).

## The four modes

Exactly one of these must be set. The API server enforces that, so a malformed reference
is rejected by `kubectl apply` rather than becoming a condition nobody reads.

| Mode | Shape | Use it when |
|---|---|---|
| `name` | `{name: eu-west}`, optionally `{namespace: catalogue}` | The target is a CR. **Prefer this.** |
| `slug` | `{slug: eu-west}` | The object exists in NetBox and the operator does not manage it. |
| `lookup` | `{lookup: {vid: "20", site: "home"}}` | No slug identifies it — a VLAN's identity is a pair. |
| `id` | `{id: 12}` | Escape hatch. The only place a raw NetBox primary key is accepted. |

`name` is the mode to reach for first, and not only by convention: it is the only one that
expresses a dependency the operator can *wait on*. A `slug` or a `lookup` either resolves
now or does not, whereas a named CR that does not exist yet is a state the operator can
sit in until it does — which is what lets a dependency graph be applied in any order and
still converge.

### `namespace`, and why it is on the common path

Every Kind is namespaced in `v1alpha1`
([ADR-0002](../decisions/0002-crd-scoping.md)), so a team namespace referring to a shared
catalogue namespace is the ordinary shape rather than an exotic one. `namespace` defaults
to the referring object's own, and may only be set together with `name` — a slug or a
lookup is resolved against NetBox, where Kubernetes namespaces do not exist.

Crossing a namespace requires a [`NetBoxRefGrant`](../reference/netboxrefgrant.md) in the
**target** namespace. There is no grant to write for the common case of staying put: a
reference that resolves to the referrer's own namespace is never authorised against
anything. See [crossing a namespace](#crossing-a-namespace) below.

## How a reference is resolved

`internal/resolver` is the only place a reference becomes an id. It never writes — to
NetBox or to Kubernetes — so a reference that cannot be resolved cannot have changed
anything on its way to failing.

| Mode | What the operator does | Costs |
|---|---|---|
| `name` | Reads the target CR in `namespace` (default: the referrer's) and takes its `.status.id`. | One cache read. No NetBox call at all. Crossing a namespace adds a grant list, and a `Namespace` read only if some grant selects by label. |
| `slug` | `GET /api/<target endpoint>/?slug=<slug>`. | One NetBox read. |
| `lookup` | `GET /api/<target endpoint>/?<filter>`, keys in sorted order so the request is stable. | One NetBox read. |
| `id` | `GET /api/<target endpoint>/<id>/`, to verify the object exists. | One NetBox read. |

The target's endpoint and its `app_label.model` object type come from the **target Kind's
own `Descriptor`**, never from the field name and never from pluralising a Kind. That is
also why a reference to a Kind the operator does not know is a distinct error rather than
a not-found: there is no endpoint to ask.

`name` is the only mode that resolves without talking to NetBox, and the only one that
resolves *into* a dependency the operator can wait on. The other three either answer now
or do not.

### What a resolved reference is used for

Two things, and both matter:

- It is written to the NetBox column the `Descriptor` names — `regionRef` → `region`.
- It becomes the value the **natural key** filters on. `dcim.Region` is unique on
  `(parent, name)` and filters on `parent_id`, so the id `parentRef` resolved to is what
  decides whether the engine creates a region or adopts an existing one
  ([lookups](lookups.md)). A resolver that only fed the payload would create a duplicate
  region on every fresh NetBox.

### Reading a reference does not verify which NetBox it came from

A NetBox id is only meaningful within one NetBox, and `NetBoxEndpoint` is namespaced. A
cross-namespace `name` reference therefore takes an id from a CR whose endpoint *may* be a
different NetBox, and the operator does not currently compare the two. Until it does
(`ErrRefEndpointMismatch`, which arrives with the endpoint work), point references at
namespaces that use the same NetBox.

## To-one and to-many

A reference field holds either **one** object or a **list** of them, and which it is comes
from its `Field.Class` in the kind's [`Descriptor`](descriptor.md#field-classes) — `RefOne`
or `RefMany`. Cardinality is a per-kind fact, not something guessed from the value: a
`RefOne` field holding a JSON array, or a `RefMany` field holding a single object, means the
descriptor and the CRD disagree, and it is refused rather than coerced.

```yaml
spec:
  tenantRef:                 # RefOne  -> tenant: 4
    name: acme
  importTargets:             # RefMany -> import_targets: [5, 7]
    - name: rt-65000-5
    - name: rt-65000-7
```

Each element resolves on its own, in any of the four modes, and each is authorised on its own
if it crosses a namespace.

### A list resolves whole, or not at all

If three of five elements resolve, **nothing is written for that field**. `RefsResolved` goes
to `False` and names each element that did not resolve.

This is not conservatism. NetBox's many-to-many write is a **full replacement**, so sending
the three that resolved deletes the two that did not — and reports a successful write while
doing it. Writing three of five tags is not a smaller version of the right answer; it is a
different, wrong answer that looks like success.

Each failed element is reported as its own blocker, with its own reason and its own retry
interval, because a missing CR and a missing NetBox slug are woken up by different things. The
condition then carries the first blocker's reason, a message naming every one of them, and the
soonest of their intervals — exactly as it does for several unresolved fields.

```
{"type":"RefsResolved","status":"False","reason":"RefNotReady",
 "message":"importTargets -> netboxroutetarget/team-a/rt-65000-5: not ready (the target has no status.id yet); importTargets -> netboxroutetarget/team-a/rt-65000-9: not found (no such object in the cluster)"}
```

### An empty list is a value

`importTargets: []` is a statement — this VRF has no route targets — and it is written as an
empty list. Omitting the field entirely is a different statement: spec omission means "do not
manage", so the column is left as NetBox has it. The two are deliberately distinct, the same
way an absent reference and a present-but-empty one are.

### Order is not data

The ids are written **sorted and deduplicated**. NetBox does not preserve many-to-many order
and [drift detection](drift.md) compares the field as a set, so the order the spec listed the
elements in carries no information. Sending them in spec order would advertise an ordering
nothing downstream honours, and make two specs that mean the same thing produce two different
create bodies.

Two references to the same object are one member of a set, which is what NetBox stores either
way.
## Ordering and convergence

**Apply order does not matter.** A manifest applied backwards converges as fast as one
applied in dependency order, and neither waits for a resync. That is the whole point of
`name` mode, and it is not a property of the resolver on its own: something has to wake an
object up when the thing it was waiting for arrives.

That something is a watch, and it runs in three steps.

1. **An index, on write.** Every object of a kind that declares a reference is indexed by
   the objects it points at, as `<kindlower>/<namespace>/<name>` — a `NetBoxSite` with
   `regionRef: {name: emea, namespace: netbox-catalog}` is indexed under
   `netboxregion/netbox-catalog/emea`. The index is recomputed by the API server's cache on
   every write, so a reference edited to point somewhere else stops matching the old target
   immediately. There are no stale edges to clean up.
2. **A watch, on the target's Kind.** Each kind watches every Kind it references. The
   watches are registered once in the shared controller shell from the kind's
   [`Descriptor`](descriptor.md), so a new kind inherits them without a line of code.
3. **A re-enqueue.** When an event on a target is admitted, the operator queries the index
   for that target and enqueues every referrer it finds — **across every namespace**, since
   a team namespace pointing at a shared catalogue is the ordinary shape. Whether such a
   reference is *allowed* to resolve is still decided at resolve time by the grant, which is
   why enqueuing it costs nothing to be generous about.

A five-level chain applied in reverse therefore converges in five reconciles rather than
five resync periods — in the test suite, a child region reaches `Ready` about a tenth of a
second after its parent appears, against a `resyncPeriod` of an hour.

### Which events count

Not all of them, and this is the part that has to be right. Every object in the cluster
writes its status as it reconciles, so a watch that woke referrers on *any* update would
enqueue one reconcile per reference per object per resync — at ~120 kinds, a storm the
operator inflicts on itself.

An event on a target is admitted when, and only when:

| Event | Admitted | Why |
|---|---|---|
| Create | always | The target may already carry `status.id` — a manager restart replays every object as a Create. |
| `status.id` appears, or changes | yes | This is the transition a waiting referrer exists for. A change means the object was recreated and the referrer is holding a dead id. |
| The `Ready` condition's status changes | yes | Including its first appearance. |
| `metadata.deletionTimestamp` is set | yes | A terminating target stops resolving, and its finalizer may hold it for a long time. |
| Delete | always | The referrer has to report `RefNotFound` rather than stand on a stale id. |
| `status.lastSyncTime`, `status.lastAppliedHash`, `observedGeneration`, `naturalKey`, `deferredPending` | **no** | None of them changes what a reference resolves to. |
| Any other condition — `Synced`, `DriftDetected`, `RefsResolved` | **no** | Same. |
| A spec edit on the *target* | **no** | A referrer resolves off the target's id, not off its description. |
| Generic | no | It carries no before-and-after to compare. |

Both halves of the pair are watched — the id *and* `Ready` — and
[#142](https://github.com/ricardomolendijk/netbox-operator/issues/142) is why both are still
needed now that it has landed. A reference requires an **id**, so `status.id` appearing is the
transition most referrers are waiting for. But a `Ready` change can move a target between
[refusing and reporting](#a-reference-needs-an-id-not-a-ready-target) — a `Conflict` cleared,
or a spec NetBox stops rejecting — and that changes what the same id resolves to.

### A grant is an event too

A `NetBoxRefGrant` written into a namespace re-enqueues the objects whose references reach
into that namespace, so the remedy in a `RefDenied` message takes effect when you apply it.
Referrers are found by a second index, over the namespaces an object's references cross
into; a reference that stays in its own namespace is not in it, because a namespace does not
grant itself access to itself.

### What is not woken

**Only `name` references have a reverse edge at all.** A `slug`, a `lookup` or an `id`
terminates in NetBox, where there is no Kubernetes object an event could arrive for — so
none of them is indexed and none of them is watched. A NetBox-side object appearing,
changing or going away is noticed on the retry interval in the table above, and nothing
else. That is the standing cost of not using a CR reference.

**A target that exists and never becomes usable produces no event**, so a referrer waiting
on one sits at `RefNotReady` — or at `RefTargetFailed` — indefinitely. That is intended: the
fix is on the target, and a poll would hide a stuck graph rather than reveal it.
`RefsResolved` on the referrer names the target, and the target's own `Ready` condition says
what is wrong with it. Note that "not usable" is narrower than "not `Ready`": a target that is
merely unfinished resolves and is reported, so it is not in this category at all.

### Seeing it

`netbox_operator_ref_enqueue_total{targetKind,referrerKind}` counts the referrers woken by
each kind of target. A dependency graph that is converging shows traffic here; one that is
converging only on the resync does not, which is the difference between a working watch and
a watch that is quietly matching nothing.

The index itself is internal to the manager, and `kubectl --field-selector` cannot query it:
the API server only exposes the selectable fields a CRD declares. To find an object's
referrers by hand, read the `RefsResolved` message on the referrer — it names the target of
every reference that did not resolve — or search the ref field across the cluster:

```console
$ kubectl get netboxregion -A \
    -o jsonpath='{range .items[?(@.spec.parentRef.name=="emea")]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'
```

[Stuck references](../operations/stuck-references.md) is the operational version of this:
which condition to read, what the metrics mean, and the two caveats on that `jsonpath`.

## Crossing a namespace

A `name` reference that resolves into another namespace is **denied unless that namespace
grants it**, with a [`NetBoxRefGrant`](../reference/netboxrefgrant.md) living **in the
namespace being referenced**. The full field reference is on that page; what matters here is
where the check sits and what it does not cover.

The grant lives in the target namespace because that is the only direction in which it is a
capability rather than a claim. A grant in the *referring* namespace would be an object
anybody could write about somebody else's namespace, which authorises nothing. Read every
grant as "*this* namespace is readable by …".

This is everyday machinery rather than a security nicety. Every kind is namespaced, so
`deviceTypeRef`, `manufacturerRef`, `tags` and every other catalogue reference from a team
namespace into a shared `netbox-catalog` namespace crosses one
([ADR-0002](../decisions/0002-crd-scoping.md), cost 2) — which is why the wildcard form
exists and is the one to reach for:

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRefGrant
metadata:
  name: catalogue-readable-by-all
  namespace: netbox-catalog     # the namespace being referenced
spec:
  from:
    - namespaces: All           # All | Selector
```

That is one object per catalogue namespace, and it does not need editing when a team is
onboarded or when this operator learns a new kind. Narrower audiences use
`namespaces: Selector` with a label selector over the **referring `Namespace`'s** labels;
narrower targets use `to: [{kinds: […], names: […]}]`.

### Three things about the check

**Same-namespace references are free.** A reference that does not leave its own namespace
returns before anything is read — no grant list, no `Namespace` read. It keys on the
namespace the reference *resolves to*, so writing `namespace:` explicitly with your own
namespace in it is still free.

**The grant is checked before the target is read.** A denied reference makes zero reads in
the target namespace, so "denied" and "not found" cannot be told apart. Otherwise the
condition message would be an existence oracle for a namespace the referrer has no access
to. The [cycle walk](#a-cycle-through-a-namespace-you-may-not-reference) uses the same
check, for the same reason and at the same point.

**`NetBoxEndpoint` is never covered by an omitted `kinds` list.** A catalogue reference hands
over an id; an `endpointRef` hands over use of another namespace's token Secret, which is a
capability rather than a lookup. Lending one has to be spelled out —
`to: [{kinds: [NetBoxEndpoint], names: [shared]}]` — so that the wide, ergonomic grant every
catalogue namespace wants cannot quietly lend credentials as well. See
[why `NetBoxEndpoint` is the exception](../reference/netboxrefgrant.md#why-netboxendpoint-is-the-exception).

### A grant is not NetBox authorisation

**A grant protects the Kubernetes reference graph, and nothing in NetBox.** Only `name` mode
is gated, because it is the only mode with a Kubernetes namespace on the far side. A `slug`,
`lookup` or `id` reaches NetBox directly, using the *referring* namespace's own endpoint and
token — there is no namespace to authorise against and no grant anywhere that can gate it.

So a namespace denied `{name: emea, namespace: netbox-catalog}` can write `{slug: emea}` and
get the same id, if its own token may read `dcim.region`. That is the correct boundary rather
than a hole: the grant says who may depend on your *CRs*; NetBox's own object permissions are
the only thing that says who may read *NetBox*. If an object must not be readable from a
namespace, that is a permission on that namespace's token.

Revoking a grant likewise does not clear what was already written: the referrer stops
resolving the reference and reports it, and the live NetBox value is left alone, exactly as
for any reference that stops resolving.

## A namespace does not imply a tenant. An endpoint may supply a default.

**Decided** on
[#173](https://github.com/ricardomolendijk/netbox-operator/issues/173), and not built yet.

`tenant` is a foreign key on almost every IPAM model (`ipam.Prefix`, `ipam.IPAddress`,
`ipam.VLAN`, `ipam.VRF`, `ipam.IPRange`, `ipam.ASN` — `docs/netbox-schema.md`), and a
multi-tenant cluster usually already spells the tenant out in its namespace names
(`team-blue`, `customer-acme`). So the question is whether the namespace *is* the tenant.

It is not. `tenantRef` stays an ordinary optional reference, and the namespace name is never
read as a tenant slug — a cluster whose namespaces are not named after tenants must not become
one the operator is hostile to. What is added instead is an **opt-in default on the endpoint**:

```yaml
kind: NetBoxEndpoint
spec:
  defaultTenantRef: {name: acme}   # optional; applied to objects that omit tenantRef
```

- It applies **only when the object omits `tenantRef`** ([field ownership](field-ownership.md)
  is what tells an omitted field from a deliberately empty one). An object that sets
  `tenantRef` overrides the default; an object that sets it *empty* has no tenant.
- Nothing is implicit unless a cluster admin asked for it, per endpoint — which is the
  Kubernetes-normal shape for a default, rather than a convention nobody opted into.
- **`status` records the tenant that was applied.** That is what keeps "the spec says what
  happens" true when the spec is silent: the applied value is visible on the object rather than
  inferable only from the endpoint.
- **A default that does not resolve blocks visibly.** If `defaultTenantRef` names a tenant that
  is missing, not ready or not granted, the object waits with `RefsResolved=False` exactly as an
  explicit ref would — same reasons, same intervals, below. A default that silently did nothing
  would be worse than one that blocks, because the object would go `Ready` filed under no
  tenant at all.

The cost, accepted: an object's effective tenant is no longer readable from its own manifest
alone. That is the price of not making every object in every namespace restate the same
`tenantRef`, and `status` is what pays it back.

## What happens when it does not resolve

Every failure is one of eight causes. Each maps to exactly one `RefsResolved` reason and one
retry interval; `Ready` reports `WaitingForRef` for all of them, because that is the
question a `kubectl wait` is asking.

| Cause | `RefsResolved` reason | Retried | Why that interval |
|---|---|---|---|
| Nothing to point at | `RefNotFound` | On the missing CR's creation; **1 min** for a missing NetBox object | A CR's creation is an event the operator receives. NetBox announces nothing, so a timer is the only thing that will notice. |
| Target exists, has no id yet | `RefNotReady` | On the target's own event | The target's own reconcile is what changes this, and that reconcile's result is an event. |
| Target has an id for an object it no longer describes | `RefTargetFailed` | On the target's own event | Somebody has to fix the **target**, and the fix arrives as an event on it. See [readiness](#a-reference-needs-an-id-not-a-ready-target). |
| Several NetBox objects match | `RefAmbiguous` | **10 min** | Only a human can say which one was meant. |
| Cross-namespace, no grant | `RefDenied` | On a grant event in the target namespace | Writing the grant is the fix, and writing it is what retries the reference. |
| The references depend on each other | `RefCycle` | Only on a spec change | No order of reconciles resolves it. See [Cycles](#cycles). |
| The graph is too deep, or too wide, to walk | `RefDepthExceeded` | Only on a spec change | A 33-hop chain is a mistake, and the walk will not guess past its cap. |
| Target Kind has no descriptor, or its CRD is not installed | `RefKindUnavailable` | **10 min** | The manifest is correct; the fix is an operator upgrade. |
| A [polymorphic reference](generic-refs.md) names a target its column will not take | `RefTypeNotAllowed` | Only on a spec change | No object appearing anywhere makes an illegal target legal. |

Two more reasons appear on `RefsResolved` and are not resolution failures at all:
`AllResolved`, and `NotImplemented` for a reference this build cannot dispatch on. As of
NBO-019 that set is **empty**, and the reason survives as the guard rather than as a state: a
declared reference that comes back neither resolved nor blocked is left out of the payload and
reported, which keeps the object off `Ready` rather than writing a value the operator guessed
at.

Neither of the two references that used to be in it is any more. A **to-many** reference —
`tags`, `ipam.VRF.import_targets`, `dcim.Site.asns`, `dcim.Interface.wireless_lans` — resolves
element by element (see [to-one and to-many](#to-one-and-to-many)), and a
[generic foreign key](generic-refs.md) resolves through its union's dispatch table.

When several references are unresolved, the condition carries the **first** blocker's
reason — a reason is a single value tooling keys on — and a message naming every one of
them. The retry is the **soonest** of their intervals, so a reference that could resolve in
a minute is not held behind one that needs a human.

`RefNotReady` is a state, not a failure. Nothing about it is logged as an error, no Event
is emitted, and no backoff is applied: a graph applied in any order converges only if "the
target does not exist yet" is normal. It means one thing and one thing only — the target has
**no `status.id`** — and the message quotes the target's own `Ready` reason where there is one
to quote, so the reader is sent to the object that is actually holding things up:

```
regionRef -> netboxregion/catalogue/emea: not ready
  (the target has no status.id yet)
```

`RefTargetFailed` is the neighbouring case, and a different promise: the target *has* an id,
and its own `Ready` reason says that id is for an object it no longer describes. Nothing
clears it on a timer — somebody has to edit the target — so it gets none.

```
regionRef -> netboxregion/catalogue/emea: target failed
  (target Ready=False, Reason=Invalid: "slug must be unique")
```

## A reference needs an id, not a `Ready` target

**Decided** in [NBO-089](https://github.com/ricardomolendijk/netbox-operator/issues/142). The
discriminator is the target's `Ready` **reason**, not its `Ready` **status**.

### Why not readiness

`Ready=False` is not one state. `driftMode: Report`
([ADR-0005](../decisions/0005-gitops-coexistence.md)) makes it the **steady** state of every
object at an endpoint, by design: drift is detected, reported, and never corrected, so the
object never matches the spec and never reports that it does. `mode: DryRun` has the identical
shape.

Requiring readiness therefore meant that every object at a `Report` endpoint blocked every
object referring to it, indefinitely — and `Report` is the mode meant to be left on for a week
while an existing NetBox is adopted, which is exactly when a catalogue namespace holds the
objects everything else points at. "Correct, and unusable for the case it exists for" was the
honest description.

`status.id` is only written once the object provably exists in NetBox. That is the whole claim
a referrer needs in order to write its own payload: not *this object matches its spec*, but
*this NetBox object exists and here is its id*.

### What still refuses

An id can be **stale** rather than merely uncorrected, and three target reasons are where that
happens — the object the CR manages is not the object the CR now describes:

| Target `Ready` reason | Referrer | Why |
|---|---|---|
| `Conflict` | **blocked** (`RefTargetFailed`) | NetBox holds an object matching the target's natural key that it may not claim, so any id it still carries came from a key it no longer has. |
| `AdoptOnly` | **blocked** (`RefTargetFailed`) | `onConflict: AdoptOnly` and nothing matched — the same shape. |
| `Invalid` | **blocked** (`RefTargetFailed`) | NetBox rejected the payload, so the object's fields are not what the target says, and referring to it propagates that. |
| `ReportPending`, `DryRunPending` | resolves, reported | The id is right and the write was deliberately suppressed. |
| `WaitingForRef`, `DeferredFieldPending` | resolves, reported | The id is right and a column is missing. Blocking here would cascade one unresolved leaf all the way up a chain. |
| `APIError`, `WaitingForEndpoint`, `WaitingForKey` | resolves, reported | NetBox being unreachable says nothing about which object the id names. |
| no `Ready` condition at all | resolves | Mid-write, or written by a build that reported none. The id is proven either way. |

The **default is to proceed**, and the direction is deliberate. A block-list that misses a
genuinely broken state lets one stale id through, reported on the referrer's own condition. An
allow-list that misses a benign one reintroduces the cluster-wide stall this rule exists to
end, and stays invisible until somebody runs `Report` in anger.

A target with **no id** is `RefNotReady` regardless of its reason — including a `Conflict`,
which is its ordinary shape: the engine refused to claim anything, so there is nothing to
refuse a referrer over. That is the case that genuinely has to wait, and it is what
`ErrRefNotReady` now means and only means.

### Proceeding is not silence

A reference that resolved against a target that is not `Ready` says so on the referrer's own
`RefsResolved` — the condition somebody debugging is already reading. `RefsResolved` stays
`True` and the referrer reaches `Ready`, because its own work is done:

```
{"type":"RefsResolved","status":"True","reason":"AllResolved",
 "message":"resolved parentRef; parentRef -> catalogue/emea: resolved, target not ready
            (target Ready=False, Reason=ReportPending: \"the endpoint's driftMode is Report, so nothing was sent\")"}
```

Without the note, a `Ready=True` object would be pointing at something unfinished with nothing
anywhere saying so.

### An unresolved reference is created without, and reported

The object is **still created**, with the unresolvable reference left out of the payload,
and it reports `Ready=False, Reason=WaitingForRef`
([#132](https://github.com/ricardomolendijk/netbox-operator/issues/132)). The alternative —
writing nothing at all — turns an optional field into a required one and stops a
half-applied graph from making any progress.

What makes that safe rather than silent is the `Ready` half. Without it, a reference that is
not part of the kind's identity would be dropped while the object reported `Ready=True`:
`kubectl apply` succeeds, `kubectl wait --for=condition=Ready` passes, and NetBox never
receives the value. A unit test asserts exactly that combination cannot occur.

Where the reference **is** part of the identity — `parentRef` on a `NetBoxRegion` — nothing
is written at all, one step later and for a better reason: no natural-key candidate is
applicable, so the engine cannot tell whether the object exists and reports
`Ready=False, Reason=WaitingForKey` while `RefsResolved` names the reference behind it.
Creating there would duplicate the region, and falling through to the top-level candidate
would adopt an unrelated one ([lookups](lookups.md)).

A reference that resolved yesterday and does not today does **not** clear the NetBox field.
The object stops writing that column, reports `RefsResolved=False`, and leaves the live value
alone. Unresolved is not the same as cleared: clearing a field is something you ask for by
writing an empty value, and it is tracked separately from the value itself
([field ownership](field-ownership.md)).

An **absent** reference and a **present but empty** one are two different states, not one.
An absent field is not resolved, not blocked and not reported — nobody asked for it. A
present `{}` is refused (`RefsResolved=False, Reason=Invalid`), because a reference that
names no object names nothing, and the API server rejects it before the operator sees it
anyway. Neither one clears a live NetBox value: clearing a foreign key will be an explicit
instruction, on the back of the field ownership server-side apply makes readable
([#121](https://github.com/ricardomolendijk/netbox-operator/issues/121)).

### Why is my object waiting?

```console
$ kubectl get netboxsite ams -o jsonpath='{.status.conditions[?(@.type=="RefsResolved")]}'
{"type":"RefsResolved","status":"False","reason":"RefNotReady",
 "message":"regionRef -> netboxregion/team-a/emea: not ready (the target has no status.id yet)"}
```

Read it right to left: the reason names the class of problem, the message names the field
you wrote and the object it pointed at. From there:

- `RefNotReady` → the target has no `status.id` yet. Look at the target's own conditions; if
  the message quotes one, the target is the object to look at, not this one.
- `RefTargetFailed` → the target has an id for an object it no longer describes. The message
  quotes the target's own reason. Fix the **target**; nothing here clears on a timer. Note what
  this is *not*: a target that is merely unfinished — one at a `Report` endpoint, or waiting on
  its own reference — resolves and is [reported rather than
  blocked](#a-reference-needs-an-id-not-a-ready-target).
- `RefNotFound` on a `name` → the CR does not exist. Check the namespace: it defaults to the
  referrer's, not to the one the catalogue lives in.
- `RefNotFound` on a `slug`, `lookup` or `id` → NetBox does not hold it. Nothing will
  announce it appearing, so this retries on a timer.
- `RefDenied` → the message names the `NetBoxRefGrant` to create and the namespace to create
  it in. If you already have one, check it is in the namespace being *referenced* and not the
  one referring.
- `RefAmbiguous` → the message names every matching id and what NetBox calls each one
  (`id 12 (EMEA), id 19 (Emea)`). That display is usually the whole answer — `EMEA` versus
  `Emea` is the cause. Replace the `slug` with a `lookup` that adds whatever distinguishes
  them, or with the `id`.
- `RefKindUnavailable` → the operator has no descriptor for that Kind, or the CRD is not
  installed. Your manifest is fine.
- `RefCycle` → the message names the ring. Edit any one object in it; see
  [Cycles](#cycles).
- `RefDepthExceeded` → the chain of blocking references from this object is longer than 32,
  or the graph around it is wider than 256 objects. Flatten it.

## Cycles

`a.parentRef -> b` and `b.parentRef -> a`. Neither can resolve, because each is waiting for
the other to become `Ready` first, and no order of reconciles gets there. Two ordinary
manifests reach it: `dcim.Region`, `dcim.SiteGroup`, `dcim.Location` and
`tenancy.TenantGroup` are all self-referential trees in NetBox (`docs/netbox-schema.md`,
`ForeignKey -> self`), so a `parentRef` pointing the wrong way is a typo away.

Without detection the symptom is two objects sitting on `RefNotReady` forever, each
truthfully reporting that it is waiting for the other, and nothing anywhere saying they are
waiting for *each other*.

### What is detected

Before it resolves anything, and before it makes any NetBox request at all, the operator
walks the reference graph away from the object it is reconciling. If the walk comes back to
that object, the ring is reported:

```console
$ kubectl get netboxregion a -o jsonpath='{.status.conditions[?(@.type=="RefsResolved")]}'
{"type":"RefsResolved","status":"False","reason":"RefCycle",
 "message":"parentRef -> netboxregion/team-a/b: reference cycle
            (netboxregion/team-a/a -> netboxregion/team-a/b -> netboxregion/team-a/a)"}
```

Three properties of that report are the feature, rather than details of it:

- **The message names the ring, in order.** "A cycle was detected" would leave the reader to
  find it by hand, which is all of the work.
- **It starts and ends at the object you are looking at.** The path is rendered from that
  object's own perspective, so you never have to work out whether your object is even in it.
- **Every member reports it.** A ring of three reports on all three, each naming the same
  ring from its own starting point. Reporting it on whichever object reconciled first would
  leave everybody else on a plain wait, and a reader looking at one of those would conclude
  their object was fine and somebody else's was broken.

Nothing coordinates that last one. Each participant walks the graph itself, and every member
of a ring is on it, so each one finds it independently — from the cache, without a NetBox
call, and without writing anything anywhere.

A one-hop cycle — `parentRef` naming the object it is written on — reads
`netboxregion/team-a/a -> itself`. It is the mistake people actually make, and nothing in the
CRD schema can prevent it: `dcim.Region.parent` is a foreign key to its own table.

A cycle is never retried on a timer. Only an edit to one of the participants can break it,
and an edit arrives as a watch event; coming back every minute would re-derive the same
verdict for as long as the manifest stands.

### Why only `name` references can cycle

A `slug`, a `lookup` or an `id` resolves against NetBox, where there is no CR to be waiting
for: it either answers now or does not. Only a `name` names a Kubernetes object whose own
reconcile has to happen first, so only a `name` can be one edge of a deadlock. That bounds
the whole problem to the CR graph — the walk stops dead at the first NetBox-side reference,
and at any reference to a Kind whose CRD is not installed.

An object that is simply **missing** stops the walk too. If `a -> b -> c` and `c` does not
exist, that is `RefNotFound` on `b`, not a cycle on `a`: nothing is waiting on `c`, because
`c` is not there to wait for. Reporting a cycle would send you hunting for one that is not in
your manifests.

### Which references are followed

Not all of them, and this is the part that has to be right. NetBox's own foreign-key graph
contains cycles that are *supposed* to be there — `Device -> primary_ip4 -> IPAddress ->
assigned_object -> Interface -> device -> Device` — and the two-pass PATCH
([#27](https://github.com/ricardomolendijk/netbox-operator/issues/27)) exists precisely to
resolve them. A detector that reported those would make the deferred design unusable.

The rule is one sentence: **follow a reference if and only if the referring object cannot be
created until it resolves.**

| Reference | Followed? | Why |
|---|---|---|
| An ordinary one — `regionRef`, `siteRef`, `tenantRef` | yes | Nothing is written until it resolves, and a target that is not `Ready` never resolves. |
| A `DeferAlways` field — `primary_ip4`, `nat_inside` | **no** | It cannot be resolvable at create time by construction; the second PATCH breaks the ring by design. |
| A `DeferIfUnresolved` field no natural key matches on — `lag` | **no** | The object is created without it and the field is PATCHed in afterwards. Nothing waits. |
| A `DeferIfUnresolved` field a natural key **does** match on — `parent` | **yes** | With it unresolved no candidate is applicable, so the engine refuses to create the object at all ([lookups](lookups.md)). It genuinely blocks. |
| `slug`, `lookup`, `id` | no | Resolves in NetBox; no CR is waiting. |
| A Kind with no descriptor, or no CRD installed | no | Outside the CR graph, and it costs no read. |

### A cycle through a namespace you may not reference

The walk consults the grant check **before it follows an edge**, exactly as resolution does
before it reads a target. An edge into a namespace that grants the object being reconciled
nothing is a **terminus**: not followed, and not named anywhere in what that object reports.

Otherwise the ring itself is the oracle. A namespace with no grant into `netbox-catalog` could
write a manifest closing a ring through `netboxregion/netbox-catalog/x` and read back out of
its own condition message that `x` exists and points where it points — the very thing checking
the grant before the read exists to prevent, reached by another path. The leak is narrow
(existence and reference structure, never field values; it takes a deliberately-constructed
ring; and the reference is denied regardless, so only the *message* was over-informative) and
closing it is cheaper than arguing about it.

What you see instead is the missing grant, which is the part you can act on:

```console
$ kubectl get netboxregion b -o jsonpath='{.status.conditions[?(@.type=="RefsResolved")]}'
{"type":"RefsResolved","status":"False","reason":"RefDenied",
 "message":"parentRef -> netboxregion/netbox-catalog/x: denied (namespace \"team-a\" is not
            permitted to reference namespace \"netbox-catalog\": create a NetBoxRefGrant …)"}
```

The cost, stated plainly: **a cycle that exists only through an unauthorised edge is not
reported as a cycle.** Nothing proceeds silently — the reference across that edge is denied, so
the object is blocked either way — but the reason is `RefDenied` rather than `RefCycle`, and no
message anywhere names the ring. Write the grant and the next reconcile reports the ring in
full.

Two consequences worth knowing:

- A cycle wholly inside one namespace is unaffected, and costs nothing: a same-namespace edge
  is authorised without reading a grant, so the walk on the overwhelmingly common shape reads
  no `NetBoxRefGrant` at all.
- Edges are authorised from the perspective of **the object being reconciled**, because that
  object's condition is what carries the path. A path it prints can only name objects it could
  have referenced itself.

### The depth limit

The walk follows at most **32** references away from one object. A chain exactly that long is
walked to its end and converges; the 33rd reference is not followed, and the object reports
`RefsResolved=False, Reason=RefDepthExceeded` instead:

```
parentRef -> netboxregion/team-a/r1: reference chain too deep
  (the chain of blocking references from netboxregion/team-a/r0 is more than 32 deep
   (netboxregion/team-a/r0 -> ... -> netboxregion/team-a/r33))
```

32 is an order of magnitude above anything the schema produces — the nested trees are the
only structures that nest at all, and a hierarchy 32 levels deep is a mistake rather than a
model — while the alternative to a cap is an unbounded walk over a graph a manifest author
controls.

It is deliberately **not** reported as a cycle. Telling the author of a 40-deep region tree
that they have a cycle sends them hunting for something that does not exist, and the fix here
is a different one: flatten the hierarchy.

The same reason covers the other cap. Depth bounds the length of a path and says nothing
about the breadth of a graph, so the walk also stops after reading **256** objects and
reports what it did rather than claiming there is no cycle:

```
the blocking references reachable from netboxregion/team-a/a span more than 256 objects,
so the search for a cycle was stopped at netboxregion/team-a/n257
```

### What it costs

One cache read per object in the blocking-reference closure, per reconcile, and no NetBox
request at any point — so the check cannot be rate-limited and cannot fail because NetBox is
down. The reads are capped at 256, and a resolution pass reads each object once whether the
walk wanted it or the resolution did, so detection does not double the reads a reconcile was
already making. In the shape a manifest usually has, the closure is the object's parent chain:
one or two reads.

The walk reads the informer cache, so it can miss a cycle that was created a moment ago. The
next reconcile of any participant catches it, and nothing is remembered between reconciles: a
stale verdict of "no cycle" would be far worse than recomputing a walk that is bounded by
construction.

### Fixing one

Edit any single object in the ring — point its reference somewhere else, or remove it. That
is one `kubectl apply`, the watch delivers it, and every participant clears on its next
reconcile. There is nothing to restart and no requeue to wait out.

## What the API server rejects

Five CEL rules live on the `ObjectRef` type itself, so a new ref field cannot forget them.

| Rejected | Why |
|---|---|
| Two modes at once, or none | A reference that names two objects names neither. |
| `{name: ""}` | Emptiness is checked, not merely presence — an empty string is not a name. |
| `namespace` without `name` | Meaningless for the NetBox-side modes. |
| `{id: 0}` | NetBox primary keys start at 1, so zero is never an object. `ID` is a pointer precisely so `0` is distinguishable from unset. |
| A `lookup` key that is not a lowercase NetBox filter name | Catches `Site` for `site` before it becomes a silent no-match. |
| `lookup` containing `limit`, `offset`, `format`, `brief`, `ordering` or `q` | Pagination and formatting are not identity. |
| A `lookup` value over 200 characters | Bounds the query the operator will build. |

`q` is singled out deliberately. It is NetBox's fuzzy search, and behind a reference that
must identify exactly one object a fuzzy filter would make ambiguity the normal case
rather than an error — the failure
[errors and retries](errors-and-retries.md#why-ambiguity-is-an-error) exists to prevent.

## The typed aliases

One defined Go type per referencable Kind. The alias is where the target is written down,
and the resolver reads it from the *type* rather than from a switch on the field name.

| Alias | Kind | NetBox model | Endpoint |
|---|---|---|---|
| `TagRef` | `NetBoxTag` | `extras.Tag` | `extras/tags` |
| `RegionRef` | `NetBoxRegion` | `dcim.Region` | `dcim/regions` |
| `SiteRef` | `NetBoxSite` | `dcim.Site` | `dcim/sites` |
| `SiteGroupRef` | `NetBoxSiteGroup` | `dcim.SiteGroup` | `dcim/site-groups` |
| `LocationRef` | `NetBoxLocation` | `dcim.Location` | `dcim/locations` |
| `TenantRef` | `NetBoxTenant` | `tenancy.Tenant` | `tenancy/tenants` |

Model and endpoint spellings are from `docs/netbox-schema.md` and its endpoint map. Only
`NetBoxTag`, `NetBoxSite` and `NetBoxRegion` exist as Kinds so far; the other three aliases
are declared ahead of their Kinds because a reference is declarable before its target is
implemented, and the remaining ~40 arrive with the generator
([NBO-042 (#66)](https://github.com/ricardomolendijk/netbox-operator/issues/66)).

Each alias implements `RefTarget`:

```go
type RefTarget interface {
    TargetGVK() schema.GroupVersionKind
    AsObjectRef() ObjectRef
}
```

A compile-time assertion covers every alias, so one that forgets its methods fails the
build rather than the first reconcile that needs it. The table above is pinned by a unit
test — if the two disagree, that test fails.

## A reference whose target is a union

`scope` on a scoped kind is one spec field that writes two NetBox columns and may point at
any of four Kinds. It is still a reference — same resolver, same modes, same grant check,
same watches — but it is declared on the `Descriptor`'s `GenericFKs` rather than in `Fields`,
because a `Field` maps one spec name to one API name. See [generic-refs.md](generic-refs.md).

## How the descriptor sees a reference

Per-kind facts are data, so a reference is a `Field` in the kind's
[`Descriptor`](descriptor.md) with a reference `Class` and a `Target`:

```go
{Spec: "parentRef",     API: "parent",         Class: registry.ClassRefOne,  Target: v1alpha1.RegionRef{}.TargetGVK()},
{Spec: "importTargets", API: "import_targets", Class: registry.ClassRefMany, Target: v1alpha1.RouteTargetRef{}.TargetGVK()},
```

The class carries the cardinality, and it carries it *once*: the same declaration is what
[drift detection](drift.md) reads to compare the field as an order-independent id set. Before
[NBO-088](https://github.com/ricardomolendijk/netbox-operator/issues/141) those were two
declarations — a `Ref: true` bool and an entry in a separate `M2M` list — which could disagree
with each other, and did not add up to a cardinality the resolver could dispatch on.

`Target` is written as the alias's own answer rather than as a fresh GVK literal, so the
alias stays the single source of truth for what it points at.

There is no closure here. Nothing in a `Descriptor` is a func — a closure cannot be
emitted by the generator, printed in a diff or linted — and none is needed, because the
engine already reads a spec through its JSON representation rather than through per-kind
accessors. The target Kind is the only per-kind fact a resolver needs.

A `Target` on a field whose class is not a reference is rejected at manager start
(`ErrTargetNotRef`): it is almost always a class left at `Value`, and left alone it produces a
field the resolver ignores and the engine writes to NetBox verbatim.

A to-many reference in a natural key (`ErrToManyNaturalKey`) or as the `ContainmentRef`
(`ErrContainmentToMany`) is rejected too. Both are places where exactly one object is
required, and both fail *silently* if allowed through — a candidate reading a list is never
applicable, so the object waits forever for an identity it cannot build.

The converse — a reference with no `Target` — is still not rejected at start, because a typed
alias exists for nine Kinds and a descriptor may legitimately declare a reference to a Kind
that has none yet. The resolver reports such a field as `RefKindUnavailable`, with a message
that says the descriptor names no target rather than blaming the manifest, and the object
does not reach `Ready`. Turning the check into a boot failure waits for the aliases the
generator emits ([NBO-042 (#66)](https://github.com/ricardomolendijk/netbox-operator/issues/66)).
