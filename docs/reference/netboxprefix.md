# `NetBoxPrefix`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxPrefix` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbprefix` |
| Status subresource | yes |
| Lands with | NBO-024 (M3) |

A `NetBoxPrefix` is one `ipam.Prefix` in NetBox: a network in CIDR notation, IPv4 or IPv6,
optionally in a VRF and optionally attached to a Region, SiteGroup, Site or Location.

> ### If you are coming from `netbox-populator`
>
> **Every prefix that tool has ever created on a NetBox 4.2-or-newer server is unscoped, and
> nothing anywhere reports it.** `netbox-populator` writes prefixes with `{"site": <id>}`, and
> since NetBox 4.2 `ipam.Prefix` has no `site` column: `POST` returns `201`, the prefix is
> created, and the `site` key is dropped. Nothing drifts afterwards either, because the
> spec's `site` is compared against a column that does not exist — so the object reports
> itself synced forever. See
> [the failure this prevents](../concepts/generic-refs.md#the-failure-this-prevents).
>
> Adopting those prefixes with this operator will **scope them on the first reconcile**, which
> shows up as a `DriftCorrected` event and a `PATCH` carrying `scope_type` and `scope_id` on
> every one of them. That burst is the fix landing, not a fault. After it, `kubectl get
> nbprefix` shows a `SCOPE` column that is no longer empty.
>
> The operator's API cannot express the old shape: there is no `siteRef` on this kind, and no
> sugar that expands into one.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata:
  name: home-lan
  namespace: default
spec:
  endpointRef: homelab
  deletionPolicy: Retain
  prefix: 10.0.20.0/24
```

A global, unscoped prefix. That is a legitimate and common shape rather than a half-filled
one — both scope columns are nullable.

`deletionPolicy: Retain` is in the *minimal* example on purpose; see
[`spec.deletionPolicy`](#specdeletionpolicy).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata:
  name: home-lan
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail                 # default
  deletionPolicy: Retain           # NOT the default -- see below

  prefix: 10.0.20.0/24
  status: active                   # default

  # NetBox's scope, not Kubernetes'. At most one member.
  scope:
    siteRef:
      name: home

  vrfRef:
    name: vrf-home
  vlanRef:
    lookup:
      vid: "20"
      site: home
  roleRef:
    slug: lan

  isPool: false
  markUtilized: false

  description: Home LAN
  comments: Managed by netbox-operator.
```

There is no `tenantRef` yet — `tenancy.Tenant` is
[NBO-021](https://github.com/ricardomolendijk/netbox-operator/issues/33). A field that is
accepted and writes nothing is worse than a field that is not there.

There is no `parentRef`, and there never will be. See
[a prefix has no parent](#a-prefix-has-no-parent).

There is no `fromPrefixRef` and no `prefixLength`: allocation is a separate kind
([ADR-0004](../decisions/0004-claims-first-allocation.md)), so `spec.prefix` is required.

## `spec`

`endpointRef` and `onConflict` come from the shared envelope and behave identically on every
kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full treatment of each.

### `spec.deletionPolicy`

| | |
|---|---|
| Type | `string` (`DeletionPolicy`) |
| Required | no |
| Default | `Delete` — **and it should be `Retain` on this kind** |
| Validation | `Enum=Delete;Retain` |

**Write `deletionPolicy: Retain` on every `NetBoxPrefix`.** Deleting a prefix destroys the
record of who a range of addresses belonged to, and that record is not recoverable by
re-creating the object: NetBox's change log, journal entries and contacts go with the row, and
a fresh prefix at the same CIDR is a different object with a different id. Deleting a site or
a tag is reversible in the way that matters; deleting an allocation is not.

The default is `Delete` because `deletionPolicy` is declared once on the shared envelope every
object kind embeds, and this API has no way to give one kind a different default: redeclaring
the field on `NetBoxPrefixSpec` makes `controller-gen` emit
`allOf: [{default: Retain}, {default: Delete}]`, which the API server rejects outright, and
the engine would still read the envelope's own copy. Changing that is a change to the
envelope rather than to this kind, and is tracked as follow-up work.

Until then this is a manifest convention, not an enforced one. `Retain` appears in both
examples above and in `config/samples/netbox_v1alpha1_netboxprefix.yaml` for that reason.

### `spec.prefix`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=43`, CEL `isCIDR(self)`, CEL `cidr(self) == cidr(self).masked()` |

The network in CIDR notation. `prefix IPNetworkField REQ` on `ipam.Prefix`
(`docs/netbox-schema.md` → `ipam.Prefix`).

One field for both address families, because `IPNetworkField` is one column: `fd00:10::/64`
and `::/0` are as ordinary here as `10.0.20.0/24`. The validation is a CEL CIDR check rather
than a regex for exactly that reason — a v4-shaped pattern is the mistake this rule exists
instead of.

`/32` and `/128` are legal and are ordinary prefixes. A `10.0.20.10/32` `NetBoxPrefix` is a
different NetBox object from a `10.0.20.10/32` `NetBoxIPAddress` (NBO-025), and NetBox models host
routes both ways without conflating them. The one you probably want is the address.

**If it is wrong.** Both rules are enforced at admission, so `kubectl apply` fails and no
object is stored:

```console
$ kubectl apply -f prefix.yaml
The NetBoxPrefix "home-lan" is invalid: spec.prefix: Invalid value: "string": prefix has
host bits set; write the network address, which is cidr(self).masked()
```

The host-bit rule is belt and braces rather than pedantry. NetBox canonicalises a prefix to
its network address on write, so `10.0.20.5/24` would be stored as `10.0.20.0/24` and every
later comparison would run against a value the manifest never contained. Rejecting it up front
turns a silent rewrite into an immediate message.

### `spec.scope`

| | |
|---|---|
| Type | [`ScopeRef`](genericref.md#scoperef) |
| Required | no |
| Default | none — omitted means a global prefix |
| Validation | CEL: at most one of `regionRef`, `siteGroupRef`, `siteRef`, `locationRef` |

Attaches the prefix to a Region, SiteGroup, Site or Location. Written as NetBox's
`(scope_type, scope_id)` pair, where the type half is an `app_label.model` string —
`dcim.region`, `dcim.sitegroup`, `dcim.site`, `dcim.location`.

The three states are all meaningful and all different:

| You write | The operator sends |
|---|---|
| nothing | neither column; whatever NetBox holds stays |
| `scope: {}` | both columns as `null`, clearing the scope |
| `scope: {siteRef: {name: home}}` | `scope_type: dcim.site`, `scope_id: <that site's id>` |

`<= 1` rather than `== 1`, because an unscoped prefix is legal: neither column carries a real
`REQ` ([the `REQ` trap](../concepts/generic-refs.md#the-req-trap-in-the-schema-digest)).

Two of the four members point at Kinds this build does not have —
`NetBoxSiteGroup` and `NetBoxLocation`. Using one reports `RefsResolved=False`,
`Reason=RefKindUnavailable` naming the field, and the pair is **left out of the payload**
rather than guessed at. That is the designed outcome: the member is declared before its Kind
exists precisely so it cannot be silently dropped.

**If it is wrong.** Two members is an admission rejection. An unresolvable member is
`RefsResolved=False` with `RefKindUnavailable`, `RefNotFound` or `RefNotReady`, and
`Ready=False, Reason=WaitingForRef`. A `scope_type` NetBox refuses for this model is a `400`,
surfaced as `Ready=False, Reason=Invalid` carrying NetBox's own field error verbatim, with a
long backoff — retrying an invalid payload before the spec changes is pointless. CEL makes
that last case unreachable through `ScopeRef`, which is why it is the one path documented and
not demonstrated.

### `spec.vrfRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxVRF` |
| Required | no |

Puts the prefix in a VRF. `vrf ForeignKey -> ipam.VRF on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.Prefix`).

**Part of this kind's identity**, which is the thing to know about it. Leaving it unset means
the global table, and that is a *different prefix* from the same CIDR inside a VRF rather than
the same prefix with a field missing. See [natural keys](#natural-keys).

Written as `vrf`, filtered as `vrf_id`. Both spellings appear below because for a foreign key
the write name and the filter name genuinely differ.

### `spec.vlanRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxVLAN` |
| Required | no |

The VLAN this prefix is carried on. `vlan ForeignKey -> ipam.VLAN on_delete=PROTECT`.

Not part of identity and not a containment parent: a prefix outliving its VLAN is a normal
state, so this reference contributes no owner reference
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

A VLAN has no slug, so `lookup` is usually the mode you want: `{vid: "20", site: "home"}`.

### `spec.roleRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxRole` |
| Required | no |

What the prefix is *for* — management, storage, guest. `role ForeignKey -> ipam.Role
on_delete=SET_NULL`.

An **object**, not a string. `ipam.Role` is a real NetBox model with its own slug and weight,
which is what distinguishes this field from `NetBoxIPAddress.role` — that one is a choice
column that happens to share the name.

`NetBoxRole` is [NBO-055](https://github.com/ricardomolendijk/netbox-operator/issues/56), so
a `name`-mode reference reports `RefKindUnavailable` today. `slug`- and `id`-mode references
resolve against NetBox directly and work now.

### `spec.status`

| | |
|---|---|
| Type | `string` (`PrefixStatus`) |
| Required | no |
| Default | `active` |
| Validation | `Enum=container;active;reserved;deprecated` |

`status CharField len=50 def=UNRESOLVED:PrefixStatusChoices.STATUS_ACTIVE
choices=PrefixStatusChoices` (`docs/netbox-schema.md` → `ipam.Prefix`). The four values are
read from `netbox/ipam/choices.py` in the 4.6.8 tree the digest was taken from, because the AST
walk records the choice *class* rather than its members.

**These are not a Site's four.** A prefix is `container`, `active`, `reserved` or
`deprecated`; there is no `planned`, `staging`, `decommissioning` or `retired`.

Defaulted to NetBox's own default so the operator manages the field from the first reconcile.
A defaulted field that never reaches a payload is a field the operator can never correct.

NetBox returns it as `{"value": "active", "label": "Active"}` and accepts the bare value. The
engine's comparison handles that with no per-kind field class
([drift](../concepts/drift.md)).

### `spec.isPool` / `spec.markUtilized`

| | |
|---|---|
| Type | `boolean` (pointer) |
| Required | no |
| Default | none — absent means "not managed" |

`is_pool BooleanField def=False` and `mark_utilized BooleanField def=False`
(`docs/netbox-schema.md` → `ipam.Prefix`).

`isPool` makes NetBox count the network and broadcast addresses as usable and treat the prefix
as a pool for utilisation. `markUtilized` forces 100% utilisation regardless of what is inside
it. Both change NetBox's **arithmetic**, not its data or any foreign key, so neither
participates in the natural key — and both are ordinary drift targets a human will flip in the
UI.

Both are pointers in Go, and the column's `def=False` is why. A plain `bool` cannot tell "not
managed" from "managed as `false`", so adopting a prefix somebody had marked as a pool would
silently clear it on the first reconcile. Omit the key and NetBox's value is left alone; write
`false` and `false` is sent.

### `spec.description` / `spec.comments`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `description`: `MaxLength=200`. `comments`: none — it is a `TextField` |

Both inherited from `PrimaryModel` rather than declared on `ipam.Prefix`
(`docs/netbox-schema.md` → `ipam.Prefix`); an inherited column is as writable as a declared
one.

Both are clearable: omit to leave NetBox's own value alone, set to `""` to clear it. The two
are different intents and the operator can tell them apart
([field ownership](../concepts/field-ownership.md)).

## Natural keys

Two candidates, tried in this order:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(prefix, vrf)` | `?prefix=<cidr>&vrf_id=<id>` | `vrfRef` **resolves** to an id |
| 2 | `prefix` where `vrf IS NULL` | `?prefix=<cidr>&vrf_id__isnull=true` | `vrfRef` was **never declared** |

**`ipam.Prefix` has no `meta.constraints` at all.** Its only table-level lines in
`docs/netbox-schema.md` are

```
meta.ordering: (F('vrf').asc(nulls_first=True), 'prefix', 'pk')
meta.indexes: (models.Index(fields=('scope_type', 'scope_id')), GistIndex(fields=['prefix'],
   name='ipam_prefix_gist_idx', opclasses=['inet_ops']))
```

— an ordering and two **non-unique** indexes. So `(vrf, prefix)` above is the ordering tuple
read as a *convention*, and it is not a database uniqueness guarantee. Duplicate prefixes are
legal in NetBox: when the enclosing VRF has `enforce_unique: false` (`docs/netbox-schema.md` →
`ipam.VRF`, `enforce_unique BooleanField def=True`), or when the deployment has global
uniqueness enforcement switched off.

That has a consequence worth stating plainly: **more than one match is a legitimate server
state, not necessarily a mistake.** The engine reports `Ready=False, Reason=Conflict` naming
every candidate id and writes nothing
([why ambiguity is an error](../concepts/errors-and-retries.md#why-ambiguity-is-an-error)).
For a duplicate-tolerant deployment the escape hatch is an `id`-mode reference or an adopted
object: once `status.id` is set, the object is reconciled by id and the natural key is not
consulted again, so the ambiguity only ever bites on first adoption.

The order is not a fallback chain. Candidate 2 is not "what to try if 1 fails" — it is the
identity of a *different* object, the same CIDR in the global table. `vrfRef` declared but not
yet resolved matches **neither**, and the engine waits rather than adopting the global prefix
and then `PATCH`ing a VRF onto somebody else's row.

`vrf_id__isnull=true` is pinned rather than omitted, and on this kind that is load-bearing
rather than tidy: the whole point of per-VRF prefixes is that the same `10.0.20.0/24` can
exist in several of them at once, so a lookup that merely left `vrf_id` out would match all of
them and adopt an arbitrary one. See
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted).

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`,
`lastAppliedHash`, `lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each field means and when it is
cleared. Nothing is cleared on failure: `status.id` in particular survives, which is what lets
a failing object keep reconciling by id rather than re-deriving an identity.

`status.provenance` is stamped in full: `ipam.Prefix` is a `PrimaryModel`, so it carries both
`tags` and `custom_fields` ([provenance](../operations/provenance.md)).

`status.naturalKey` is worth reading on this kind in particular. It records which of the two
candidates ran, filter by filter, so `{"prefix": "10.0.20.0/24", "vrf_id__isnull": "true"}`
tells you the engine treated the object as a global prefix, and a `vrf_id` there tells you
which VRF it went looking in.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the prefix exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefKindUnavailable`, `RefDenied`, `RefAmbiguous`, `RefCycle` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals are shared across every object kind; see
[errors and retries](../concepts/errors-and-retries.md). The two that mean something
particular here:

- **`Protected`** on `Deleting`: NetBox refuses to delete a prefix while addresses or child
  objects still reference it (`on_delete=PROTECT` on the referring side). The operator keeps
  retrying and the delete completes on its own once they are gone — no manual step, and
  `status.deletionAttempts` counts the tries.
- **`Conflict`** on `Ready`: more than one prefix matched. On this kind that is a legitimate
  NetBox state rather than proof of a mistake; see [natural keys](#natural-keys).

## Kind-specific behaviour

### There is no `siteRef`, and that is the feature

The cheapest possible fix for the `netbox-populator` bug would be to accept `siteRef` and
translate it into `scope`. That was rejected. A field named `siteRef` on a Prefix reads as the
foreign key NetBox no longer has; it invites the same mistake in every downstream tool and
generated manifest, and it makes the wrong mental model expressible forever. **The API surface
is the enforcement mechanism.** A user who wants a site-scoped prefix writes
`scope: {siteRef: {name: home}}`, where the nesting says what the relationship actually is.

The `site` key never appears in any request body sent to `ipam/prefixes` — asserted by a
recording stub across create, adopt, update and lookup rather than by inspecting the code
(`internal/controller/ipam_prefix_controller_test.go`, `sitedColumns`).

### A prefix has no parent

`ipam.Prefix` carries no `parent` foreign key at all. Its place in the hierarchy is not
stored: NetBox computes it from the prefix value itself using a Postgres `inet` GiST index
(`docs/netbox-schema.md` → `ipam.Prefix`, `meta.indexes`) and caches the answer in `_depth`
and `_children`, both `_`-prefixed and therefore read-only. `10.0.20.0/24` is a child of
`10.0.0.0/16` because of what it *is*, not because anything says so.

So there is no `parentRef` on this kind, and inventing one would produce a field with nothing
to write to — accepted by the API, dropped by NetBox, reported as success. Nesting a prefix
means writing a prefix inside another prefix's range; there is nothing else to do.

### The scope moves as one pair

`(scope_type, scope_id)` is diffed as a **unit** ([drift](../concepts/drift.md)). Moving a
prefix from a Region to a Site is one entry in the diff and one `PATCH` carrying both keys,
not two independent diffs that a partial write could apply inconsistently — leaving
`scope_type: dcim.site` against a Region's primary key, which NetBox would happily store.
Clearing the scope sets both columns to `null` in one write.

### The cached columns are never written and never diffed

`_region`, `_site_group`, `_site` and `_location` are denormalised caches NetBox maintains
from the pair, and `_depth` / `_children` are the hierarchy counters. All six come back on
every read and all six are in the descriptor's read-only list. Writing one does not fail —
NetBox ignores it — which is precisely why they have to be declared: an ignored write produces
a difference the next reconcile finds again, and `PATCH`es forever. `Descriptor.Validate`
refuses at boot to register a scoped kind whose cache columns are not declared read-only, so
this cannot be got wrong one kind at a time.

### An unscoped prefix produces no drift

Both scope columns are nullable, so a global prefix is a normal object rather than an
unfinished one. It declares no scope, NetBox returns `scope_type: null, scope_id: null`, and
there is nothing to do — across every reconcile. This is the empty-union-versus-null case, and
it is exactly where an enthusiastic "always send the scope" implementation starts a `PATCH`
loop.

### Ownership and cascade

`scope` is this kind's **containment reference**
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4): the scope target gets a
non-controller owner reference when that is legal, so `kubectl delete netboxsite home` also
removes the prefixes scoped to it.

Exactly one, and `vrfRef` is deliberately not a second. Kubernetes garbage collection deletes
a dependent only once *every* owner is gone, so two containment references would silently turn
"delete the site **or** the VRF and the prefix goes" into "delete both". `vlanRef` and
`roleRef` contribute nothing either — a catalogue reference is not a parent.

A cross-namespace scope target, or one referenced by raw `id`, cannot legally be an owner. The
operator sets `CascadeUnavailable` naming the field rather than silently skipping it.

### Renaming changes identity

`prefix` participates in both natural keys, so editing `spec.prefix` does not renumber the
NetBox prefix — it changes what the CR is looking for. The next reconcile finds nothing at the
new CIDR and creates a second prefix, leaving the first behind. This is what a natural key
means and is not specific to prefixes. Renumber in NetBox and in the manifest together, or
delete and re-create the CR.

The same is true of `vrfRef`: moving a prefix between VRFs changes which candidate applies.
`status`, `description`, `comments`, `isPool`, `markUtilized`, `scope`, `vlanRef` and `roleRef`
are all safe to edit — none is part of an identity.

## Printer columns

```console
$ kubectl get nbprefix
NAME        PREFIX          VRF        SCOPE   STATUS   ID    READY   AGE
home-lan    10.0.20.0/24    vrf-home   41      active   118   True    4m
guest       10.0.30.0/24               41      active   119   True    4m
transit     fd00:10::/64                        active   120   True    4m
lab         10.0.40.0/24    vrf-lab            active         False   4m
```

| Column | JSONPath |
|---|---|
| `PREFIX` | `.spec.prefix` |
| `VRF` | `.spec.vrfRef.name` |
| `SCOPE` | `.status.naturalKey.scope` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`VRF` reads the *intent* rather than the resolved id, so it is visible next to an empty `ID`
while a reference is still unresolved — which is the pair you want side by side while
diagnosing a `WaitingForRef`. An empty `SCOPE` on a prefix that is meant to be scoped is the
observability half of the populator bug: it is now visible in one command.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message naming host bits | admission, nothing stored | `spec.prefix` has host bits set | Write the network address. `10.0.20.5/24` → `10.0.20.0/24`. |
| `kubectl apply` rejected, "must be a CIDR" | admission | a bare address, a mask out of range, or not an address at all | Add the prefix length; check the family. |
| `kubectl apply` rejected, "at most one of regionRef…" | admission | two members of `spec.scope` | A prefix has one scope. Pick one. |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `scope` | reconcile | the scope target is unresolvable | `RefKindUnavailable` on `siteGroupRef` / `locationRef` is expected in this build. Otherwise check the target CR exists and is Ready. |
| `Ready=False`, `Reason=WaitingForKey`, `vrfRef` set | reconcile, zero writes | the VRF does not exist yet, so neither candidate applies | Expected while the VRF is being created. Apply it; the prefix re-enqueues on its own. |
| `Ready=False`, `Reason=Conflict` | reconcile, zero writes | more than one NetBox prefix matched | Legitimate if the VRF has `enforce_unique: false`. `status.naturalKey` shows what was searched; the message names every candidate id. Use `id` mode to pin one. |
| `Ready=False`, `Reason=Invalid` | reconcile, long backoff | NetBox returned a `400` | The message is NetBox's own field error, verbatim. Fix the spec; the operator will not retry a payload it already knows is refused. |
| The prefix exists in NetBox but `scope_type` is `null` | `Ready=False`, `RefsResolved=False` | the scope was left out of the payload because it did not resolve | This is the loud version of the populator bug. Resolve the reference; do not add a `site` key anywhere. |
| A second prefix appeared after an edit | — | `spec.prefix` or `spec.vrfRef` was changed | See [renaming changes identity](#renaming-changes-identity). |
| Terminating forever, `Deleting` `Reason=Protected` | finalizer | addresses or child objects still reference the prefix | Delete them, or switch to `deletionPolicy: Retain` to drop the finalizer without asking NetBox. |
| `deletionPolicy` was not set and the prefix is gone | — | the envelope default is `Delete` | Set `deletionPolicy: Retain`. See [`spec.deletionPolicy`](#specdeletionpolicy). |

## Related

- [Generic references](../concepts/generic-refs.md#the-scope-pair) — the scope pair in full,
  including why writing `site` returns `201` and sets nothing
- [`ScopeRef`](genericref.md#scoperef) — the union's shape in a spec
- [Lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted) — why
  `vrf_id` is pinned rather than omitted
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Drift detection](../concepts/drift.md) — the comparison rules the choice column, the
  booleans and the scope pair each go through
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — the
  containment reference and the cascade
- [ADR-0004: claims-first allocation](../decisions/0004-claims-first-allocation.md) — why
  `spec.prefix` is required and there is no `prefixLength`
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
