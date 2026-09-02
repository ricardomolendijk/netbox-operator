# `NetBoxVLAN`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVLAN` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvlan` |
| Status subresource | yes |
| Lands with | NBO-023 (M3) |

A `NetBoxVLAN` is one `ipam.VLAN` in NetBox: an 802.1Q VLAN ID and name, optionally on a site,
optionally in a [VLAN group](netboxvlangroup.md), and optionally one half of a Q-in-Q pair.

> ### `site` is a real foreign key, and `scope` is not
>
> **This is the one kind in M3 where writing `site` is correct**, and it sits immediately next
> to the kind where it is wrong. It is the single most confusable pair in this API, so it is
> stated here before anything else.
>
> | | `NetBoxVLAN` | [`NetBoxPrefix`](netboxprefix.md) |
> |---|---|---|
> | NetBox column | `site ForeignKey -> dcim.Site on_delete=PROTECT` | **no `site` column at all** |
> | Where it comes from | declared on `ipam.VLAN` | `scope_type` / `scope_id` from `CachedScopeMixin` |
> | Spec field | `spec.siteRef` | `spec.scope` |
> | Request body carries | `site: <id>` | `scope_type`, `scope_id` |
>
> Both from `docs/netbox-schema.md` → `ipam.VLAN` and → `ipam.Prefix`. `ipam.Prefix` lists
> `bases: ContactsMixin, GetAvailablePrefixesMixin, CachedScopeMixin, PrimaryModel` and no
> `site` row; `ipam.VLAN` lists `site ForeignKey` as its first column.
>
> **Getting either one backwards is silent in both directions.** NetBox drops a field it does
> not know rather than rejecting it, so `POST {"prefix": "…", "site": 3}` returns `201` and
> creates an *unscoped* prefix, and `POST {"vid": 20, "scope_type": "dcim.site"}` returns `201`
> and creates a VLAN with no site. The operator's API cannot express either mistake: there is
> no `scope` on this kind and no `siteRef` on that one. See
> [the failure this prevents](../concepts/generic-refs.md#the-failure-this-prevents).
>
> A *VLAN group* **is** scoped, which is why [`NetBoxVLANGroup`](netboxvlangroup.md) has the
> `scope` union and no `siteRef`. `siteRef` here, `scope` there, in the same API version, is
> deliberate — the two kinds are applied together and the contrast is the cheapest place to
> internalise the rule.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLAN
metadata:
  name: vlan-20-servers
  namespace: default
spec:
  endpointRef: homelab
  vid: 20
  name: SERVERS
```

A VLAN with neither a site nor a group. That is legal — both columns are nullable — but read
[natural keys](#natural-keys) first: such an object is identified by `vid` alone, which is the
widest identity this kind has.

Deleting this CR leaves the NetBox VLAN in place: `deletionPolicy` defaults to `Retain` on this
kind — see [`spec.deletionPolicy`](#specdeletionpolicy).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLAN
metadata:
  # A DNS-1123 label. `spec.name` below keeps the literal NetBox string.
  name: vlan-1-mgmt-default
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab

  # Shared-envelope defaults, written out.
  onConflict: Fail

  # The default *on this kind*. See spec.deletionPolicy.
  deletionPolicy: Retain

  vid: 1
  name: MGMT (Default)

  # A REAL foreign key, written under the key `site` as an integer id. Also this kind's
  # containment parent.
  siteRef:
    name: home

  # The only identity a database constraint backs.
  groupRef:
    name: house-vlans-rtm

  # NetBox's own default, written out.
  status: active

  # ipam.Role is an object, not a choice column. The Kind is NBO-055.
  roleRef:
    slug: management

  # A self-reference, so it is deferred: left out of the create and PATCHed after.
  qinqSVLANRef:
    name: vlan-4000-svlan
  qinqRole: cvlan

  # on_delete=PROTECT. Holding this blocks deletion of that tenant in NetBox.
  tenantRef:
    name: donkerslootstraat

  description: Management VLAN
  comments: Managed by netbox-operator.
```

### `metadata.name` is not `spec.name`

`vid: 1` in `../inventory.yaml` is named `MGMT (Default)` — with a space and parentheses. That
is a NetBox value, not a Kubernetes name. `metadata.name` has to be a DNS-1123 label
(`vlan-1-mgmt-default`) while `spec.name` preserves the literal string. The two are unrelated
and neither constrains the other.

## `spec`

`endpointRef` and `onConflict` come from the shared envelope and behave identically on every
kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full treatment of each.

### `spec.deletionPolicy`

| | |
|---|---|
| Type | `string` (`DeletionPolicy`) |
| Required | no |
| Default | **`Retain`** on this kind |
| Validation | `Enum=Delete;Retain` |

**`Retain`, unlike most kinds.**
[Deletion — the default depends on the Kind](../concepts/deletion.md#the-default-depends-on-the-kind)
lists `NetBoxVLAN` in the `Retain` column. Deleting a VLAN destroys its change log, its journal
entries and every L2VPN termination hanging off it, and a fresh VLAN with the same `vid` is a
different object with a different id. Set `deletionPolicy: Delete` explicitly where
`kubectl delete` really should remove the VLAN.

The default is not a CRD marker. `deletionPolicy` is declared once on the shared envelope every
object kind embeds, so a marker there could only give every kind the same answer — and
redeclaring the field on `NetBoxVLANSpec` makes `controller-gen` emit
`allOf: [{default: Retain}, {default: Delete}]`, which the API server rejects outright. The
per-kind value is data on this kind's Descriptor (`registry.Descriptor.RetainOnDelete`), so
`kubectl explain netboxvlan.spec.deletionPolicy` prints no default.

[`NetBoxVLANGroup`](netboxvlangroup.md#specdeletionpolicy) is the neighbour that
keeps `Delete`, and that is not an inconsistency: a group is an organisational container, not
an allocation, so deleting one frees nothing.

### `spec.vid`

| | |
|---|---|
| Type | `integer` (`int32`) |
| Required | **yes** |
| Validation | `Minimum=1`, `Maximum=4094` |

The 802.1Q VLAN ID. `vid PositiveSmallIntegerField REQ` on `ipam.VLAN`
(`docs/netbox-schema.md` → `ipam.VLAN`).

`0` and `4095` are reserved by the standard and are rejected at admission rather than by NetBox
on write, so the message names the field instead of arriving as a `400` three steps later. `1`
and `4094` are accepted.

**Part of every one of this kind's identities, and never enough on its own.** `vid: 20` exists in
every house in `../inventory.yaml`. See [natural keys](#natural-keys).

**If it is wrong.** Admission, so `kubectl apply` fails and nothing is stored:

```console
$ kubectl apply -f vlan.yaml
The NetBoxVLAN "vlan-0" is invalid: spec.vid: Invalid value: 0: spec.vid in body should be
greater than or equal to 1
```

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=64` |

The VLAN's name. `name CharField REQ len=64` on `ipam.VLAN`
(`docs/netbox-schema.md` → `ipam.VLAN`).

Free text, and the real inventory uses it that way — see
[`metadata.name` is not `spec.name`](#metadataname-is-not-specname).

**Not part of the identity the operator looks up by**, even though `unique_group_name` is a real
constraint. `vid` is the stable identifier a network engineer keys on, and a rename should not
orphan the object. That asymmetry is deliberate: renaming a VLAN is a plain `PATCH`, while
changing its `vid` changes what the CR is looking for.

**If it is wrong.** Length is admission. Renaming a VLAN into a name another VLAN in the same
*group* already holds is a `409` from NetBox (`unique_group_name`), surfaced as
`Ready=False, Reason=Invalid` carrying NetBox's own error verbatim, with a long backoff.

### `spec.siteRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxSite` |
| Required | no |

Ties the VLAN to a site. `site ForeignKey -> dcim.Site on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.VLAN`).

**A real foreign key, not a scope.** It is written under the key `site` as an integer id, and no
`scope_type` / `scope_id` ever appears in a request body for `ipam/vlans`. See
[the box at the top of this page](#site-is-a-real-foreign-key-and-scope-is-not).

Part of the identity when `groupRef` is unset, which is the common case: the lookup then filters
`?site_id=<id>&vid=<vid>`. See [natural keys](#natural-keys).

`siteRef` is also this kind's **containment reference** — see
[ownership and cascade](#ownership-and-cascade).

**If it is wrong.** Unresolvable is `RefsResolved=False` with `RefNotFound`, `RefNotReady` or
`RefDenied`, and `Ready=False, Reason=WaitingForRef`. Crossing a namespace without a
[`NetBoxRefGrant`](netboxrefgrant.md) is `RefDenied`. A `siteRef` that is declared and
unresolved makes candidate 2 inapplicable, so the lookup falls to candidate 3 only if `groupRef`
is also absent — otherwise the engine waits.

### `spec.groupRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → [`NetBoxVLANGroup`](netboxvlangroup.md) |
| Required | no |

Puts the VLAN in a VLAN group. `group ForeignKey -> ipam.VLANGroup on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.VLAN`).

**The only identity of this kind that a database constraint actually backs.** `unique_group_vid`
(`docs/netbox-schema.md` → `ipam.VLAN`, `meta.constraints`) makes `(group, vid)` unique, so with
`groupRef` set the lookup is `?group_id=<id>&vid=<vid>` and can match at most one VLAN.

Note that a `slug`-mode reference to a VLAN group can legitimately match more than one object:
`ipam.VLANGroup` is unique on `(scope_type, scope_id, slug)` rather than on `slug` alone, so
`slug` is not globally unique there ([`spec.slug`](netboxvlangroup.md#specslug)). Prefer naming
the sibling CR.

Not a containment reference: a VLAN outliving its group is a normal state
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

**If it is wrong.** Unresolvable is `RefsResolved=False` with `RefNotFound`, `RefNotReady`,
`RefAmbiguous` or `RefDenied`, and `Ready=False, Reason=WaitingForRef`. Critically, a
`groupRef` that is **declared and unresolved** makes *every* candidate inapplicable, so no lookup
runs at all and the engine waits — see
[the `group_id` pin is load-bearing](#the-group_id-pin-is-load-bearing).

### `spec.tenantRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxTenant` |
| Required | no |

Assigns the VLAN to a tenant. `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.VLAN`).

`PROTECT`, so a VLAN holding this reference blocks deletion of that tenant in NetBox and the
refusal is reported as `Deleting=False, Reason=Protected` on the *tenant*, naming this object
([deletion](../concepts/deletion.md#what-protect-looks-like)).

Not a containment reference and contributes no owner reference.

**If it is wrong.** Same shape as `siteRef`: `RefsResolved=False` and
`Ready=False, Reason=WaitingForRef`.

### `spec.roleRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxRole` |
| Required | no |

Marks what the VLAN is for — management, guest, IoT. `role ForeignKey -> ipam.Role
on_delete=SET_NULL` (`docs/netbox-schema.md` → `ipam.VLAN`).

**An object, not a string.** `ipam.Role` is a real NetBox model with its own slug and weight, and
it is the same model [`NetBoxPrefix.roleRef`](netboxprefix.md#specroleref) points at — which is
what distinguishes both from `NetBoxIPAddress.role`, a choice column of the same name.

`NetBoxRole` is not delivered until NBO-055, and the field is here anyway rather than omitted,
because a field the API accepts and silently drops is worse than one that says why it cannot
resolve. The four reference modes behave differently:

| Mode | Behaviour today |
|---|---|
| `name` (sibling CR) | `RefsResolved=False, Reason=RefKindUnavailable` naming the field and `ipam.Role` |
| `slug` | resolves against NetBox directly, and is written |
| `lookup` | resolves against NetBox directly, and is written |
| `id` | written as given |

The three working modes need only the target's REST endpoint, which the resolver has; `name` mode
needs a Descriptor for the sibling CR, which is what does not exist yet. See
[kinds that do not exist yet](../concepts/generic-refs.md#kinds-that-do-not-exist-yet).

**If it is wrong.** `RefKindUnavailable` on `RefsResolved`, and the field is **left out of the
payload** rather than guessed at. `Ready=False, Reason=WaitingForRef`.

### `spec.qinqSVLANRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxVLAN` |
| Required | no |

The outer service VLAN of a Q-in-Q (802.1ad) pair. `qinq_svlan ForeignKey -> ipam.VLAN
on_delete=PROTECT` (`docs/netbox-schema.md` → `ipam.VLAN`).

**A self-reference, and therefore deferred.** It is in the Descriptor's `Deferred` list with
mode `Always`, so it is left out of the create payload and applied by a follow-up `PATCH`. Two
VLANs that point at each other as service and customer VLAN therefore converge instead of
deadlocking.

`Always` rather than `IfUnresolved`: the cycle is the normal shape of Q-in-Q rather than an
apply-order accident, so there is no ordering under which including it at create time is safe.

The lifecycle is one create, then one `PATCH`, with
`Ready=False, Reason=DeferredFieldPending` in between and the field listed in
`status.deferredPending`. That intermediate state is legitimate and can be long-lived — a
reference that never resolves stays there forever, on purpose.

`(qinq_svlan, vid)` and `(qinq_svlan, name)` are real constraints on this model, but neither can
be a natural key here: a deferred reference is by construction unresolved when the lookup runs.

**If it is wrong.** `Ready=False, Reason=DeferredFieldPending` is the *expected* intermediate
state, not a failure. A reference that never resolves stays `RefsResolved=False` with
`RefNotFound` or `RefNotReady`. A ring through this field is detected as `RefCycle`.

### `spec.qinqRole`

| | |
|---|---|
| Type | `string` (`VLANQinQRole`) |
| Required | no |
| Default | none |
| Validation | `Enum=svlan;cvlan` |

Which half of a Q-in-Q pair this VLAN is. `qinq_role CharField len=50
choices=VLANQinQRoleChoices` (`docs/netbox-schema.md` → `ipam.VLAN`).

The two values are read from `netbox/ipam/choices.py`, `VLANQinQRoleChoices`, in the NetBox 4.6.8
tree the digest was taken from:

| Wire value | Label NetBox renders |
|---|---|
| `svlan` | Service |
| `cvlan` | Customer |

The operator sends and compares the **value**, never the label
([drift detection](../concepts/drift.md)).

Undefaulted, unlike `status`. The column is nullable with no Django default and an ordinary VLAN
is neither half of a Q-in-Q pair, so defaulting it would assert a topology nobody described.

**If it is wrong.** Admission — the enum is generated from the choice set, so a typo or the label
spelling (`Customer`) is rejected before anything is stored.

### `spec.status`

| | |
|---|---|
| Type | `string` (`VLANStatus`) |
| Required | no |
| Default | `active` |
| Validation | `Enum=active;reserved;deprecated` |

The VLAN's lifecycle state. `status CharField len=50
def=UNRESOLVED:VLANStatusChoices.STATUS_ACTIVE choices=VLANStatusChoices`
(`docs/netbox-schema.md` → `ipam.VLAN`) — the choice *class* rather than its members, and a
`def=` the AST walk could not evaluate, so the values are read from `netbox/ipam/choices.py`,
`VLANStatusChoices`, in the same 4.6.8 tree.

**Three values, not four.** A VLAN has no `container` state. `ipam.Prefix` does
([`NetBoxPrefix.status`](netboxprefix.md#specstatus)) and the two sets are otherwise identical,
which is exactly the sort of near-miss a shared enum would have papered over.

Defaulted to NetBox's own default so the operator manages the field from the first reconcile: a
defaulted field that never reaches a payload is a field the operator can never correct.

NetBox returns choice fields as `{"value": "active", "label": "Active"}` and accepts the bare
value. The differ compares the value, so a VLAN NetBox reports that way produces **no drift**.

**If it is wrong.** Admission. `container` is the value to expect a rejection for.

### `spec.description` / `spec.comments`

| | `description` | `comments` |
|---|---|---|
| Type | `string` | `string` |
| Required | no | no |
| Validation | `MaxLength=200` | none |

Both are inherited from `PrimaryModel` rather than declared on `ipam.VLAN`
(`docs/netbox-schema.md` → `ipam.VLAN`, `description (PrimaryModel) CharField len=200`,
`comments (PrimaryModel) TextField`); an inherited column is as writable as a declared one.
`comments` is a `TextField` with no `max_length`, so there is no `MaxLength` marker to derive.

Both are clearable, and the two empty states are different instructions: **omit** the field to
leave NetBox's own value alone, set it to `""` to clear the value in NetBox. The operator can tell
them apart because it reads `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

**If it is wrong.** A `description` over 200 characters is admission. Neither field can fail at
reconcile on its own.

### What is deliberately absent

`l2vpn_terminations` appears in `docs/netbox-schema.md` → `ipam.VLAN` as
`l2vpn_terminations GenericRelation REQ`, and it is **not a column**. It is a Django
`GenericRelation` — a reverse accessor, a read-only view of somebody else's foreign key — so
there is nothing to write. It is absent from the spec struct, absent from the field map, and
absent from `ReadOnly` too, because `ReadOnly` is about columns the operator might otherwise
send.

Its `REQ` in the digest is the extractor artefact NBO-019 describes: a `GenericRelation` takes no
`null=` kwarg, so the AST walk reports it as required. See
[the `REQ` trap](../concepts/generic-refs.md#the-req-trap-in-the-schema-digest).

## Natural keys

Three candidates, tried in this order:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(group, vid)` | `?group_id=<id>&vid=<vid>` | `groupRef` **resolves** to an id |
| 2 | `(site, vid)` where `group IS NULL` | `?site_id=<id>&vid=<vid>&group_id=null` | `siteRef` **resolves** and `groupRef` was **never declared** |
| 3 | `vid` where both `IS NULL` | `?vid=<vid>&group_id=null&site_id=null` | neither `groupRef` nor `siteRef` was declared |

### Only the first is a database constraint

`meta.constraints` on `ipam.VLAN` is

```
meta.constraints: (models.UniqueConstraint(fields=('group', 'vid'),
   name='%(app_label)s_%(class)s_unique_group_vid'), models.UniqueConstraint(fields=('group', 'name'),
   name='%(app_label)s_%(class)s_unique_group_name'), models.UniqueConstraint(fields=('qinq_svlan', 'vid'),
   name='%(app_label)s_%(class)s_unique_qinq_svlan_vid'), models.UniqueConstraint(fields=('qinq_svlan',
   'name'), name='%(app_label)s_%(class)s_unique_qinq_svlan_name'))
```

— `(group, vid)`, `(group, name)`, `(qinq_svlan, vid)` and `(qinq_svlan, name)`. **There is no
`(site, vid)` constraint.** This kind's natural key is usually given as "`(group, vid)` or
`(site, vid)`", as if both came from `Meta.constraints`; only the first does. Candidates 2 and 3
are conventions.

That matters concretely rather than pedantically. **Every VLAN in `../inventory.yaml` has a site
and no group**, so every VLAN the operator creates for the real inventory falls into candidate 2
— the branch nothing in the database enforces. With `group` null the `unique_group_vid`
constraint does not fire either, because Postgres treats NULLs as distinct, so nothing stops two
VLANs with `vid: 20` on the same site.

More than one match is therefore a legitimate server state on this kind. The engine reports
`Ready=False, Reason=Conflict` naming every candidate id and writes nothing
([why ambiguity is an error](../concepts/errors-and-retries.md#why-ambiguity-is-an-error)).
`../reconcile.go:230` uses the same `{vid, site_id}` filter and takes the first match; the
operator must not, and that is the bug this kind exists not to inherit.

The escape hatch is the usual one: once `status.id` is set the object is reconciled by id and the
natural key is not consulted again, so the ambiguity only ever bites on first adoption.

### The `group_id` pin is load-bearing

`group_id=null` on candidates 2 and 3 is pinned rather than omitted, and on this kind
that is not tidiness.

Omitted, a VLAN whose group has not been created yet would match candidate 2 by site and `vid`,
adopt an *ungrouped* VLAN, and the follow-up `PATCH` would move somebody else's VLAN into this
group. Pinned, such a VLAN matches **nothing** — candidate 1 needs the group resolved, 2 and 3
need it never declared — and the engine waits, which is the correct outcome
([NBO-015](../concepts/references.md),
[lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

The same argument applies to `site_id` on candidate 3.

### Why candidate 3 exists at all

`?vid=20` with two pins is a wide net, and it is the whole of what a VLAN with neither a site nor
a group *is*. The alternative is no applicable candidate, which makes the engine wait forever for
an identity that cannot be built — the worst of the three outcomes, because it never resolves and
never reports a cause the user can act on. A `Conflict` is loud; an eternal wait is not.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each field means and when it is
cleared. Nothing is cleared on failure: `status.id` in particular survives, which is what lets a
failing object keep reconciling by id rather than re-deriving an identity.

`status.provenance` is stamped in full: `ipam.VLAN` is a `PrimaryModel`, which mixes in both
`TagsMixin` and `CustomFieldsMixin` ([provenance](../operations/provenance.md)).

`status.deferredPending` is the field to read on this kind. It lists `qinqSVLANRef` while the
follow-up `PATCH` has not happened, and it is a status field rather than only a condition message
because the intermediate state is legitimate, can be long-lived, and has to be greppable across a
namespace ([object lifecycle](../concepts/object-lifecycle.md)).

`status.naturalKey` records which of the three candidates ran, filter by filter, so a
`{"vid": "20", "site_id": "12", "group_id": "null"}` tells you the engine treated the
object as a sited, ungrouped VLAN — the non-unique branch.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the VLAN exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded`, `RefKindUnavailable` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | the containment parent owns this CR | it cannot | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals are shared across every object kind; see
[errors and retries](../concepts/errors-and-retries.md). The four that mean something particular
here:

- **`Conflict`** on `Ready`: more than one VLAN matched. On this kind that is a legitimate NetBox
  state rather than proof of a mistake — see
  [only the first is a database constraint](#only-the-first-is-a-database-constraint).
- **`DeferredFieldPending`** on `Ready`: `qinqSVLANRef` has not been written yet. Expected and
  transient on a create; see [`spec.qinqSVLANRef`](#specqinqsvlanref).
- **`RefKindUnavailable`** on `RefsResolved`: `roleRef` in `name` mode. `NetBoxRole` is NBO-055;
  use `slug`, `lookup` or `id` meanwhile.
- **`Protected`** on `Deleting`: NetBox refuses to delete a VLAN while prefixes, L2VPN
  terminations or Q-in-Q children still reference it. The delete completes on its own once they
  are gone, and `status.deletionAttempts` counts the tries.

## Kind-specific behaviour

### `site` is present and `scope_type` is absent, on the wire

The request body for `ipam/vlans` carries the key `site` as an integer, and never `scope_type` or
`scope_id`. The reverse is true for `ipam/prefixes`. Both are asserted by a recording client
rather than left to review, because the failure mode is a `201` with no error
([the failure this prevents](../concepts/generic-refs.md#the-failure-this-prevents)).

### `ipam.VLAN` has no cached columns

Its `ReadOnly` list is exactly `created`, `last_updated`, `url`, `display` — the four columns
every `ChangeLoggedModel` carries, and nothing else. Unlike
[`NetBoxPrefix`](netboxprefix.md#the-cached-columns-are-never-written-and-never-diffed), there is
no `_site` cache to exclude, because this model holds a real `site` foreign key rather than a
scope pair. There are no hierarchy counters either: a VLAN sits in no tree.

[`NetBoxVLANGroup`](netboxvlangroup.md#the-scope-pair-without-the-cached-columns) has none
either, for a different reason — it declares the scope pair on the model itself. The two scoped-
adjacent kinds in this milestone differ from `ipam.Prefix` in the same direction and by different
routes.

### Ownership and cascade

`siteRef` is this kind's **containment reference**
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4): the site gets a
non-controller owner reference when that is legal, so `kubectl delete netboxsite home` also
removes the VLANs on it.

Exactly one. `groupRef` and `tenantRef` are deliberately not a second or a third — Kubernetes
garbage collection deletes a dependent only once *every* owner is gone, so two containment
references would silently turn "delete the site **or** the group and the VLAN goes" into "delete
both". A catalogue reference is not a parent.

An owner reference is only legal within one namespace, so a VLAN whose site is in a shared
catalogue namespace **gets none, ever**. The operator sets
`ParentOwned=False, Reason=CascadeUnavailable` naming `siteRef` rather than silently skipping it.
The same is true of a site referenced by raw `id`.

### Renaming changes identity

`vid`, `groupRef` and `siteRef` all participate in the natural keys, so editing any of them does
not renumber or move the NetBox VLAN — it changes what the CR is looking for. The next reconcile
finds nothing at the new identity and creates a second VLAN, leaving the first behind. This is
what a natural key means and is not specific to this kind. Renumber in NetBox and in the manifest
together, or delete and re-create the CR.

`name`, `status`, `roleRef`, `tenantRef`, `qinqSVLANRef`, `qinqRole`, `description` and
`comments` are all safe to edit — none is part of an identity. Note that `name` *is* covered by
`unique_group_name` in NetBox and is still not a lookup filter, on purpose.

## Printer columns

```console
$ kubectl get nbvlan
NAME                  VID   NAME             SITE   GROUP             STATUS   ID    READY   AGE
vlan-1-mgmt-default   1     MGMT (Default)   home                     active   301   True    5m
vlan-20-servers       20    SERVERS          home                     active   302   True    5m
vlan-30-clients       30    CLIENTS          home   house-vlans-rtm   active   303   True    5m
vlan-4000-svlan       4000  CARRIER          home                     active   304   True    5m
vlan-50-iot           50    IOT              lab                      active         False   5m
```

| Column | JSONPath |
|---|---|
| `VID` | `.spec.vid` |
| `NAME` | `.spec.name` |
| `SITE` | `.spec.siteRef.name` |
| `GROUP` | `.spec.groupRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`SITE` and `GROUP` read the spec rather than the status, because both are visible intent and both
are worth seeing next to an empty `ID` while a reference is still resolving — which is the pair
you want side by side while diagnosing a `WaitingForRef`. An empty `GROUP` is also the shape of
the non-constraint natural-key branch, so `kubectl get nbvlan` shows at a glance which VLANs are
identified by a convention rather than by a constraint.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, `spec.vid` out of range | admission, nothing stored | `0` or `4095` | 1–4094. Both ends are reserved by 802.1Q |
| `kubectl apply` rejected, `spec.name` too long | admission | over 64 characters | NetBox's own column cap |
| `kubectl apply` rejected, `spec.status` | admission | `container` was used | That is `ipam.Prefix`'s value. A VLAN is `active`, `reserved` or `deprecated` |
| `kubectl apply` rejected, `spec.qinqRole` | admission | the label spelling (`Customer`) | The wire values are `svlan` and `cvlan` |
| `kubectl apply` rejected, `metadata.name` | admission | a space or parentheses copied from `spec.name` | `metadata.name` is a DNS-1123 label; `spec.name` keeps the literal string |
| `Ready=False`, `Reason=DeferredFieldPending` | reconcile | `qinqSVLANRef` has not been PATCHed yet | Expected and transient. `status.deferredPending` names the field |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `groupRef` | reconcile, **zero writes and zero lookups** | the group does not exist yet, so no candidate applies | Expected while the group is being created. Apply it; the VLAN re-enqueues on its own. Do **not** remove `groupRef` to unblock it — that changes the object's identity |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `roleRef` with `RefKindUnavailable` | reconcile | `roleRef` in `name` mode; `NetBoxRole` is NBO-055 | Use `slug`, `lookup` or `id`, which resolve against NetBox directly today |
| `Ready=False`, `Reason=Conflict`, `groupRef` set | reconcile, zero writes | more than one VLAN matched `(group, vid)` | Should not happen — `unique_group_vid` forbids it. Check they are really in the same group; `status.naturalKey` shows what was searched |
| `Ready=False`, `Reason=Conflict`, no `groupRef` | reconcile, zero writes | two VLANs share this `vid` on this site | Legitimate: there is no `(site, vid)` constraint. The message names both candidate ids. Put one in a group, or adopt deliberately by `id` |
| `Ready=False`, `Reason=Invalid` on a rename | reconcile, long backoff | `unique_group_name` — another VLAN in this group holds that name | The message is NetBox's own error, verbatim. Pick another name |
| The VLAN exists but has no site | none | `siteRef` did not resolve, so it was left out of the payload | Resolve the reference. Do **not** add a `scope` block — this kind has none |
| A prefix has no scope but its VLAN has a site | none | the two kinds are not the same shape | Correct and expected. See [the box at the top](#site-is-a-real-foreign-key-and-scope-is-not) |
| `ParentOwned=False`, `Reason=CascadeUnavailable` naming `siteRef` | reconcile | the site is in another namespace, or referenced by `id` | Expected for a shared catalogue site. An owner reference cannot cross a namespace ([ADR-0003](../decisions/0003-ownership-and-references.md)) |
| A second VLAN appeared after an edit | none | `spec.vid`, `spec.groupRef` or `spec.siteRef` was changed | See [renaming changes identity](#renaming-changes-identity) |
| Terminating forever, `Deleting` `Reason=Protected` | finalizer | prefixes, L2VPN terminations or a Q-in-Q child still reference this VLAN | Delete them, or switch to `deletionPolicy: Retain` to drop the finalizer without asking NetBox |
| Deleting the CR left the VLAN in NetBox | `Retained` Event | this kind defaults to `deletionPolicy: Retain` | Set `deletionPolicy: Delete` if removing the VLAN is what you want. See [`spec.deletionPolicy`](#specdeletionpolicy) |

## Related

- [`NetBoxVLANGroup`](netboxvlangroup.md) — the kind `groupRef` points at, whose identity is a
  scope pair plus a slug
- [`NetBoxPrefix`](netboxprefix.md) — the kind this one is most confusable with, and the reason
  the `site`-versus-`scope` box exists
- [Generic references](../concepts/generic-refs.md#the-failure-this-prevents) — why writing
  `site` to a scoped model returns `201` and sets nothing
- [References](../concepts/references.md) — the four ref modes, deferral, and cycle detection
- [Lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted) — why
  `group_id` is pinned rather than omitted
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Drift detection](../concepts/drift.md) — how the choice columns are compared
- [Deletion](../concepts/deletion.md#the-default-depends-on-the-kind) — why this kind defaults
  to `Retain` and its group does not, and what `PROTECT` looks like
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — the
  containment reference and the cascade
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
