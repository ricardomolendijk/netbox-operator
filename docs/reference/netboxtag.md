# `NetBoxTag`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxTag` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbtag` |
| Status subresource | yes |
| Lands with | NBO-008 (M1) |

A `NetBoxTag` is one `extras.Tag` in NetBox: a label with a colour, a sort weight, and an
optional list of the models it may be applied to.

It is also the first kind driven end to end by the generic engine, which is why it is worth
knowing what it is *not*. `extras.Tag` has no foreign keys at all — no site, no tenant, no
parent — so a `NetBoxTag` reaches `Ready` without any reference resolution, and everything
on this page is the engine's ordinary behaviour rather than anything specific to tags.
Applying this tag *to* another object is a different thing entirely: that is a `tags` field
on the other kind, and it does not exist yet: NBO-073 put `tags` in
[the schema reference](../netbox-schema.md) — an M2M onto `extras.Tag`, written as a list of
ids — but naming a `NetBoxTag` from another CR needs the reference system (NBO-011).

## Minimal example

The fewest fields that work. Everything else is defaulted.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTag
metadata:
  name: managed
  namespace: homelab
spec:
  endpointRef: homelab
  name: Managed
  slug: managed
```

The `NetBoxEndpoint` named by `endpointRef` must exist in the same namespace and be
`Ready`; see [`NetBoxEndpoint`](netboxendpoint.md) for how to create one. Until it is, the
tag reports `Ready=False, Reason=WaitingForEndpoint` and writes nothing.

## Full example

Every field set explicitly, with the defaults written out.

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTag
metadata:
  name: managed
  namespace: homelab
spec:
  endpointRef: homelab
  onConflict: Fail                 # default
  deletionPolicy: Delete           # default

  name: Managed
  slug: managed
  color: "9e9e9e"                  # default, and NetBox's own default
  description: Managed by netbox-operator
  weight: 1000                     # default, and NetBox's own default
  objectTypes:                     # omit entirely to allow every taggable model
    - dcim.device
    - virtualization.virtualmachine
```

Quote `color` and `weight` carefully in YAML: `color: 2196f3` is a string, but
`color: 123456` is a *number* and admission rejects it, so always quote the colour.

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope every object
kind embeds. They configure the operator rather than describe a NetBox object, and the
engine excludes exactly those three from every payload it sends.

### `spec.endpointRef`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Default | none |
| Validation | `+kubebuilder:validation:MinLength=1` |

Name of the `NetBoxEndpoint` to write through, **in this object's own namespace**. There is
no cluster-wide default endpoint, so an omitted reference cannot be resolved into one.

**If it is wrong.** An empty string is rejected at admission. A name with no endpoint behind
it — or one whose endpoint is not `Ready` — gives `Ready=False, Reason=WaitingForEndpoint`,
message `the netbox endpoint has no ready client: netboxendpoint "homelab" in namespace
"homelab"`, retried every 30s, and **zero** NetBox requests. There is no watch from an
object to its endpoint yet, so the 30s requeue is what notices the endpoint coming up.

### `spec.onConflict`

| | |
|---|---|
| Type | `string` (`ConflictPolicy`) |
| Required | no |
| Default | `Fail` (`+kubebuilder:default=Fail`) |
| Validation | `+kubebuilder:validation:Enum=Fail;Adopt;AdoptOnly` |

What to do when NetBox already holds a tag with this `slug` that this CR did not create.

| Value | Nothing matches | One tag matches |
|---|---|---|
| `Fail` (default) | create it | `Ready=False, Reason=Conflict` naming the NetBox id, zero writes |
| `Adopt` | create it | adopt it, then correct drift |
| `AdoptOnly` | `Ready=False, Reason=AdoptOnly`, zero writes | adopt it, then correct drift |

`Fail` is the default because the step immediately after adoption reconciles that tag
towards this spec, and there is no undo. Opting in is one field; recovering from a wrong
adoption is a restore.

**If it is wrong.** Any other value is rejected at admission by the enum. `Fail` against an
existing tag gives `Ready=False, Reason=Conflict`, message `netbox object 41 already matches
this object's natural key and was not created by it; set spec.onConflict to Adopt or
AdoptOnly to take it over`, a `Warning`/`Conflict` Event, and a retry every
`spec.resyncPeriod`. `AdoptOnly` on a fresh NetBox waits forever by design — the tag will
never be created.

### `spec.deletionPolicy`

| | |
|---|---|
| Type | `string` (`DeletionPolicy`) |
| Required | no |
| Default | `Delete` (`+kubebuilder:default=Delete`) |
| Validation | `+kubebuilder:validation:Enum=Delete;Retain` |

What happens to the NetBox tag when this CR is deleted. `Delete` removes it; `Retain` drops
the finalizer and leaves NetBox alone. Read fresh on every pass, so switching to `Retain` is
a way out of a delete NetBox keeps refusing. See [deletion](../concepts/deletion.md).

**If it is wrong.** Any other value is rejected at admission. `Retain` fails nothing and is
silent apart from a `Retained` Event on deletion — a tag left behind in NetBox is exactly
what was asked for.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Default | none |
| Validation | `+kubebuilder:validation:MinLength=1`, `+kubebuilder:validation:MaxLength=100` |

The tag's label in the NetBox UI, unique across NetBox.

`name` is declared on django-taggit's `TagBase` rather than on `extras.Tag`, so
[`docs/netbox-schema.md`](../netbox-schema.md) does not list it under `extras.Tag` — see
that file's preamble on inherited columns, which are as required and as writable as
declared ones.

**If it is wrong.** Empty or over 100 characters is rejected at admission. A `name` that
duplicates a *different* tag's name is not caught by the operator: `slug` is the natural
key, so the engine creates a new tag and NetBox rejects it with a 400 on the unique
constraint. That surfaces as `Ready=False, Reason=Invalid` with NetBox's message, a
`Warning`/`Invalid` Event, and a retry every `spec.resyncPeriod` — retrying an unchanged
payload cannot succeed, so fix the spec.

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Default | none |
| Validation | ``+kubebuilder:validation:Pattern=`^[-a-zA-Z0-9_]+$` ``, `+kubebuilder:validation:MinLength=1`, `+kubebuilder:validation:MaxLength=100` |

The tag's URL-safe identifier, and **this kind's natural key**: it is the one filter the
engine looks the tag up by before deciding create-versus-adopt, and it is recorded in
`status.naturalKey`.

Also inherited from `TagBase`, and column-unique there — which is why one candidate is
enough for this kind, where most kinds need an ordered list of them.

**If it is wrong.** A slug with a space, a dot or a slash is rejected at admission by the
pattern, as is one over 100 characters. Editing `slug` on an existing `NetBoxTag` is not
rejected and is not a rename: the engine still finds the tag by `status.id`, sees `slug`
differ, and PATCHes it — so the NetBox tag *is* renamed, in place. Two `NetBoxTag`s sharing
a slug is covered under [kind-specific behaviour](#a-slug-is-global-and-this-crd-is-not).

### `spec.color`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Default | `9e9e9e` (`+kubebuilder:default="9e9e9e"`) |
| Validation | ``+kubebuilder:validation:Pattern=`^[0-9a-f]{6}$` `` |

Six hexadecimal digits, lowercase, **without** a leading `#`.

The default is NetBox's own (`docs/netbox-schema.md` → `extras.Tag`,
`color ColorField def='ColorChoices.COLOR_GREY'`, which is `9e9e9e`). It is defaulted here
rather than left unset deliberately: a field that never reaches a payload is a field the
operator can never correct, so an unmanaged `color` would let a UI edit stand forever.

**If it is wrong.** `#2196f3`, `2196F3` and `f00` are all rejected at admission by the
pattern — the leading hash and the uppercase are the two people try first. A valid-looking
colour NetBox rejects gives `Ready=False, Reason=Invalid`.

### `spec.description`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Default | none |
| Validation | `+kubebuilder:validation:MaxLength=200` |

Free text shown next to the tag.

**If it is wrong.** Over 200 characters is rejected at admission. Worth knowing: an
**empty** description is not managed rather than being pushed as empty, so setting
`description` and then removing it again leaves the old value in NetBox. See
[what "spec omission" actually means](#spec-omission-is-go-level-not-yaml-level).

### `spec.weight`

| | |
|---|---|
| Type | `integer` (`int32`) |
| Required | no |
| Default | `1000` (`+kubebuilder:default=1000`) |
| Validation | `+kubebuilder:validation:Minimum=0`, `+kubebuilder:validation:Maximum=32767` |

Sort order in the NetBox UI, lowest first (`docs/netbox-schema.md` → `extras.Tag`,
`meta.ordering: ('weight', 'name')`).

Defaulted to NetBox's own 1000 for the same reason as `color`. The Go field is a pointer,
so a deliberate `weight: 0` reaches NetBox instead of being dropped as a zero value.

**If it is wrong.** Negative, or above 32767, is rejected at admission — the ceiling is
Postgres's `smallint`, which is what `PositiveSmallIntegerField` compiles to.

### `spec.objectTypes`

| | |
|---|---|
| Type | `[]string` |
| Required | no |
| Default | none (absent — every taggable model is allowed) |
| Validation | per item: ``+kubebuilder:validation:items:Pattern=`^[a-z_]+\.[a-z0-9_]+$` ``, `+kubebuilder:validation:items:MaxLength=100` |

Restricts which NetBox models the tag may be applied to.

The values are Django `ContentType` strings, **not** references to other CRs:
`extras.Tag.object_types` is a `ManyToManyField` onto `contenttypes.ContentType`
(`docs/netbox-schema.md` → `extras.Tag`), so there is no NetBox object behind an entry and
nothing to resolve. The spelling is lowercased and unpunctuated — `dcim.device`,
`virtualization.vminterface` — which is what the pattern enforces.

**If it is wrong.** `dcim.Device` and `NetBoxDevice` are both rejected at admission by the
item pattern, which is the point of having one: NetBox would accept the field and then
reject or ignore the value. A well-formed `app_label.model` string for a model that does not
exist, or one that cannot carry tags, is *not* caught at admission — NetBox rejects it and
the tag reports `Ready=False, Reason=Invalid` with NetBox's message. The list is compared
order-independently, so reordering it is not drift and produces no PATCH.

## `status`

Every field is optional and written by the engine. `status` is a subresource, so
`kubectl apply` never touches it, and the engine writes nothing else on the object except
`metadata.finalizers`.

| Field | Type | Populated by | When |
|---|---|---|---|
| `id` | `int64` | the `id` of a create response, or of the tag that was adopted | once the tag provably exists server-side. **Cleared** when a read or write finds it gone (404), so the next pass re-creates or re-adopts |
| `url` | `string` | the `url` field of the last create or patch response | on every write that returned one. A response with no `url` leaves the previous value standing rather than blanking a URL that is still correct |
| `naturalKey` | `map[string]string` | the lookup that ran, filter by filter — `{"slug": "managed"}` | on every lookup, **including one that matched nothing**. The first question about a tag that was not adopted is what the engine looked for |
| `adopted` | `bool` | the engine | `true` only when the tag was found by its natural key rather than created. Cleared alongside `id` when the tag goes missing |
| `lastAppliedHash` | `string` | a digest of the last payload NetBox accepted | on every successful write. It is a record, deliberately **not** used to skip a PATCH — that would suppress exactly the UI-drift correction the operator exists for |
| `lastSyncTime` | `metav1.Time` | the engine | on every successful write. Untouched by a reconcile that found nothing to do, or every resync would bump every object's `resourceVersion` |
| `deletionAttempts` | `int32` | the engine | counts deletes NetBox refused with `PROTECT`. Non-zero only while the CR is terminating |
| `observedGeneration` | `int64` | `metadata.generation` | **every** status write, success or failure. `observedGeneration < metadata.generation` means the conditions describe an older spec |
| `conditions` | `[]metav1.Condition` | the engine | every reconcile that changed something. `+listType=map`, `+listMapKey=type` |

**Not cleared on failure**: `url`, `naturalKey`, `lastAppliedHash` and `lastSyncTime` all
describe the last time something worked, and stay put through any number of failures. Read
them next to `observedGeneration` and the `Ready` condition, never on their own.

`id` and `adopted` are the exception, and they are cleared *together*: a 404 on either the
read or the write means the tag went away behind the operator's back, and keeping a dead id
would retry it forever.

A reconcile that changes no status field **writes nothing at all** — not to NetBox and not
to the cluster. That is what makes a no-drift resync free.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the tag exists in NetBox and matches the spec | anything else at all | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or the last check found no drift | drift was found and not corrected — `mode: DryRun` or `driftMode: Report` | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `DriftDetected` | NetBox differs from the spec and nothing was sent to change it | nothing differed, or the difference was corrected | `NoDrift`, `DriftCorrected`, `DriftDetected` |
| `RefsResolved` | every reference in the spec resolved | never, on this kind | `AllResolved` |
| `Deleting` | never | while the CR is terminating and the NetBox side is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved` is always `True` with `AllResolved` on a `NetBoxTag`, because `extras.Tag`
has no foreign keys. On a kind that does have them, this condition reports
`NotImplemented` until NBO-012 lands — `NetBoxTag` never does, which is why it is the kind
the engine could be proved with first.

`Deleting` is only ever `False`. The finalizer comes off the moment the NetBox side settles,
so a `True` would have to sit on a CR that no longer exists to carry it; the `Reason` is
therefore always *what is holding the deletion up*.

`DriftDetected` answers a different question from `Synced`: `Synced` is about what the engine
did, `DriftDetected` is about what NetBox currently holds. It is the one to alert on while an
endpoint is in `driftMode: Report`, where `Ready=False` is expected and permanent. It is
`False` after a correction as well as after a pass that found nothing, so it is a stable
statement rather than a value that flaps on every write, and its message carries the field
list — "there is drift" is not something anyone can act on.

### Reasons

| Reason | On | Meaning |
|---|---|---|
| `Synced` | `Ready` | the tag exists and matches |
| `WaitingForEndpoint` | `Ready`, `Deleting` | `spec.endpointRef` has no `Ready` endpoint behind it. No request was made |
| `WaitingForKey` | `Ready` | no natural-key candidate was usable, so the engine could not tell whether the tag exists. Unreachable on this kind — `slug` is required, so the one candidate always applies |
| `Conflict` | `Ready` | NetBox holds a tag this CR may not claim: one matches `slug` and `onConflict` is `Fail`, several match, or a write came back 409 |
| `AdoptOnly` | `Ready` | `onConflict: AdoptOnly` and nothing matched |
| `Invalid` | `Ready` | NetBox rejected the payload, or the spec cannot be turned into one. Retrying it unchanged cannot succeed |
| `APIError` | `Ready`, `Deleting` | NetBox was unreachable, rate limiting, failing, or rejected the token |
| `DryRunPending` | `Ready` | the endpoint is `mode: DryRun`, so the write was reported and not sent |
| `ReportPending` | `Ready` | the endpoint is `driftMode: Report`, so the write was reported and not sent. Distinct from `DryRunPending` because it is a different field to change |
| `NoDrift` | `Synced`, `DriftDetected` | the live tag already matched; nothing was sent |
| `DriftCorrected` | `Synced`, `DriftDetected` | fields differed and were PATCHed. The message is the change set, `field: old → new` |
| `DriftDetectedDryRun` | `Synced` | fields differ and the endpoint is in DryRun, so they were reported rather than corrected |
| `DriftReported` | `Synced` | fields differ and the endpoint is `driftMode: Report`, so they were reported rather than corrected |
| `DriftDetected` | `DriftDetected` | NetBox differs from the spec and nothing was sent. The message is the change set, or a note that NetBox holds no such object at all |
| `AllResolved` | `RefsResolved` | every reference resolved. Always, on this kind |
| `Protected` | `Deleting` | NetBox refused the delete because something still references the tag. NetBox's own message is carried through verbatim |

### Retry intervals

Nothing here is returned as a controller error, so none of it produces controller-runtime
backoff. Each interval is how long before the whole decision is made again.

| Outcome | Requeue after |
|---|---|
| `Ready=True` | the endpoint's `spec.resyncPeriod` (default 10m), ± 10% jitter |
| `WaitingForEndpoint` | 30s |
| `Conflict`, `AdoptOnly`, `Invalid`, `WaitingForKey` | `spec.resyncPeriod` — none of them improves by asking sooner |
| `APIError`, 5xx or transport | 30s |
| `APIError`, 429 | the `Retry-After` header, or 5s |
| `APIError`, 401/403 | 2m. The fix is a token, and the endpoint is where that gets reported |
| `APIError`, the tag vanished mid-write | 1s — the next pass re-creates or re-adopts it |
| `Deleting`, `Protected` | 10s, doubling per refusal, capped at 5m |
| `Deleting`, `WaitingForEndpoint` | 30s |

The ± 10% jitter on the success path is what stops a whole manifest applied at once from
resyncing in lockstep for the rest of its life.

## Kind-specific behaviour

### A slug is global, and this CRD is not

NetBox enforces `slug` uniqueness across the whole instance. `NetBoxTag` is namespaced
([ADR-0002](../decisions/0002-crd-scoping.md)), and tags are catalogue-shaped, so two teams
declaring `managed` in their own namespaces is routine rather than exotic.

What happens is: **the first one to reconcile wins, and the second reports `Conflict`.**

```
$ kubectl get nbtag -A
NAMESPACE   NAME     SLUG     COLOR    ID   READY   AGE
team-a      shared   shared   9e9e9e   7    True    4m
team-b      shared   shared   9e9e9e        False   2m

$ kubectl describe nbtag shared -n team-b | tail -3
  Type    Status  Reason     Message
  Ready   False   Conflict   netbox object 7 already matches this object's natural key and
                             was not created by it; set spec.onConflict to Adopt or AdoptOnly
                             to take it over
```

Neither corrupts the other. The loser writes nothing at all, records no `status.id`, and
stays refused across every resync — it does not eventually take over.

The message names the **NetBox object**, not the winning CR, and that is a limitation rather
than a choice: the engine reconciles one object at a time and never sees the other
namespace. To find the winner, search for the id:

```sh
kubectl get nbtag -A -o jsonpath='{range .items[?(@.status.id==7)]}{.metadata.namespace}/{.metadata.name}{"\n"}{end}'
```

The intended resolution is for one namespace to own the tag and the others to reference it
across namespaces, which a [`NetBoxRefGrant`](netboxrefgrant.md) in the owning namespace now
permits. The alternatives are to move the duplicate out, or to set `onConflict: AdoptOnly` on
it — which makes both CRs manage the same NetBox tag, and makes the last writer win on every
field they disagree about.

### `objectTypes` are content types, not references

`object_types` is declared on the descriptor as an `ObjectTypeList` rather than an `M2M`
(`internal/registry/extras_tag.go`). Both are many-to-many and both compare
order-independently, so the difference is invisible in the diff and total in effect: an
`M2M` holds NetBox object ids resolved from sibling CRs, and a resolver told to resolve
`dcim.device` would go looking for a CR that cannot exist. See
[the descriptor](../concepts/descriptor.md#objecttypelists-versus-m2m).

NetBox returns the list as `app_label.model` strings on this endpoint and as nested
`{app_label, model}` objects on some others; both read shapes reduce to the same set, so
neither produces a permanent diff.

### `spec` omission is Go-level, not YAML-level

"Only fields present in the spec are sent" is implemented as "only non-zero Go fields are
sent": the engine reads a spec through `json.Marshal` of the stored object, and `omitempty`
collapses *absent* and *empty* into the same thing.

For a `NetBoxTag` that means:

- `description: ""` and no `description` at all are indistinguishable. Neither is sent, so
  a description that has been set once cannot be cleared through this CR — edit it in
  NetBox, or set it to a single space.
- `objectTypes: []` likewise cannot clear a restriction that is already in NetBox.
- `color` and `weight` are **not** affected, because both carry a `+kubebuilder:default`, so
  they are always present and always managed. That is the reason they have one.

This is a property of the shared engine rather than of tags, and it is the trade that lets
the operator co-exist with humans editing the same object
([ADR-0005](../decisions/0005-gitops-coexistence.md)).

### What the operator never writes

`created`, `last_updated`, `url` and `display` are read-only (`docs/netbox-schema.md`,
preamble) and are declared as such on the descriptor. Writing one would not fail — NetBox
silently no-ops — so it would produce a PATCH every resync, forever. There is no spec field
that maps onto any of them, and `Descriptor.Validate` rejects a descriptor that adds one.

### Renaming versus re-creating

`UpdateStrategy` is `Patch`, so every editable field — `slug` included — is changed in
place. A tag keeps its NetBox id for its whole life, and nothing about this kind ever takes
the delete-then-create path.

## Printer columns

```
$ kubectl get netboxtags -n homelab
NAME         SLUG        COLOR    ID   READY   AGE
managed      managed     2196f3   7    True    4d
deprecated   deprecated  9e9e9e   9    True    4d
guarded      guarded     ff9800        False   6m
```

| Column | Type | Source |
|---|---|---|
| `NAME` | string | `metadata.name` |
| `SLUG` | string | `.spec.slug` |
| `COLOR` | string | `.spec.color` |
| `ID` | integer | `.status.id` |
| `READY` | string | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | date | `.metadata.creationTimestamp` |

An empty `ID` with `READY=False` means the operator has no claim on any NetBox object:
either it has not created one yet, or it refused to. `kubectl get nbtag` works too.

## Troubleshooting

| Symptom | Condition you would see | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, "should match `^[0-9a-f]{6}$`" | none; admission refused the object | `color` has a leading `#`, uppercase digits, or three digits | Write `color: "2196f3"` |
| `kubectl apply` rejected, "cannot unmarshal number into string" | none; admission refused the object | `color: 123456` is a YAML number | Quote it |
| `kubectl apply` rejected, "should match `^[a-z_]+\.[a-z0-9_]+$`" | none; admission refused the object | an `objectTypes` entry is `dcim.Device` or a CRD kind name | Use the Django spelling, lowercased: `dcim.device` |
| `kubectl apply` rejected, "should match `^[-a-zA-Z0-9_]+$`" | none; admission refused the object | `slug` contains a space, dot or slash | Use a slug, not a name |
| `READY=False`, empty `ID`, immediately after apply | `Ready=False, Reason=WaitingForEndpoint` | no `Ready` `NetBoxEndpoint` called `spec.endpointRef` in this namespace | `kubectl get nbep -n <ns>`; fix the endpoint, and the tag converges on its own |
| `READY=False`, empty `ID`, message names an id | `Ready=False, Reason=Conflict` | a tag with this `slug` already exists in NetBox and `onConflict` is `Fail` | Adopt it deliberately with `onConflict: Adopt`, or change the `slug` |
| `READY=False`, message says "matches N netbox objects" | `Ready=False, Reason=Conflict` | more than one NetBox tag matched `slug`. Should be impossible — the column is unique — so suspect a proxy rewriting the query | Look at the ids in the message and remove the duplicate |
| `READY=False`, empty `ID`, "nothing matched" | `Ready=False, Reason=AdoptOnly` | `onConflict: AdoptOnly` and there is no tag to adopt | Create the tag in NetBox, or switch to `Adopt` |
| `READY=False`, message is a NetBox validation error | `Ready=False, Reason=Invalid` | NetBox rejected the payload: a duplicate `name`, or an `objectTypes` entry naming a model that does not exist or cannot be tagged | Fix the spec. Retrying the same payload cannot succeed |
| `READY=False`, message names a spec field | `Ready=False, Reason=Invalid`, `spec field is not in the descriptor's field map` | a descriptor bug, not a user error | File an issue quoting the field name |
| `READY=False`, message is a transport or 5xx error | `Ready=False, Reason=APIError` | NetBox is unreachable or failing. Not this object's fault, so nothing is failed permanently | Check the endpoint; retried every 30s |
| `READY=False`, message says 401 or 403 | `Ready=False, Reason=APIError` | the token is wrong or lacks write permission on `extras.tag` | Fix the Secret. The endpoint reports it too, and is the better place to look |
| Drift reported and never corrected | `Ready=False, Reason=DryRunPending`, `Synced=False, Reason=DriftDetectedDryRun` | the endpoint is `mode: DryRun` | Set `mode: Apply` on the `NetBoxEndpoint` |
| Drift reported and never corrected | `Ready=False, Reason=ReportPending`, `Synced=False, Reason=DriftReported`, `DriftDetected=True` | the endpoint is `driftMode: Report`, which sends nothing at all | Set `driftMode: Correct`. See [gitops](../operations/gitops.md) |
| A colour edited in the NetBox UI is never corrected | `Ready=True`, `DriftDetected=False`, and NetBox still holds the edit | the endpoint is `driftMode: Off`, so nothing re-checks on a timer | Set `driftMode: Correct`, or touch the CR |
| A colour edited in the NetBox UI comes back | `Ready=True, Synced=True, Reason=DriftCorrected` | working as designed: a UI edit is drift | Change `spec.color`, or set the endpoint to `DryRun` while you investigate |
| A description set in NetBox is not removed by clearing `spec.description` | `Ready=True, Synced=True, Reason=NoDrift` | an empty string is indistinguishable from an absent field | See [`spec` omission is Go-level](#spec-omission-is-go-level-not-yaml-level) |
| Two tags in NetBox where there should be one | `Ready=True` on both CRs | two `NetBoxTag`s with **different** slugs — `slug` is the natural key, `name` is not | Give them the same slug and delete one CR, or accept both |
| `kubectl delete` hangs | `Deleting=False, Reason=Protected` | NetBox refuses the delete while something is still tagged with it. The message names what | Untag those objects. The retry backs off from 10s to 5m and unblocks itself |
| `kubectl delete` hangs, endpoint down | `Deleting=False, Reason=WaitingForEndpoint` | the finalizer stays on rather than orphaning the tag | Bring the endpoint back; or accept the orphan with `netbox.kubeforge.org/skip-finalizer=true` |
| CR deleted, tag still in NetBox | none; the CR is gone | `deletionPolicy: Retain`, the skip-finalizer annotation, or a CR whose `status.id` was never recorded | Check the Events on the namespace: `Retained`, `FinalizerSkipped` or `NothingToDelete` says which |
| Conditions do not match the spec you just applied | `status.observedGeneration` < `metadata.generation` | the reconcile for the new spec has not finished | Wait one pass; if it persists, read the manager log |

## Related

- [The Descriptor](../concepts/descriptor.md) — the per-kind data behind this page, and why
  `object_types` is an `ObjectTypeList` and not an `M2M`.
- [The reconcile engine](../concepts/engine.md) — the create/adopt/update decision, in one
  page.
- [Drift detection](../concepts/drift.md) — why what NetBox returns is not what you wrote.
- [Deletion](../concepts/deletion.md) — the two policies, the finalizer ordering, and
  getting out of a `PROTECT`-blocked delete.
- [Lookups](../concepts/lookups.md) — how `slug` becomes `?slug=managed`.
- [`NetBoxEndpoint`](netboxendpoint.md) — the connection every tag writes through.
- [ADR-0002](../decisions/0002-crd-scoping.md) — why this kind is namespaced, which is what
  makes a cross-namespace slug collision possible at all.
- [ADR-0005](../decisions/0005-gitops-coexistence.md) — why a NetBox UI edit is drift rather
  than a competing opinion.
