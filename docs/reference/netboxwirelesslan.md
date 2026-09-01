# `NetBoxWirelessLAN`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxWirelessLAN` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbwlan` |
| Status subresource | yes |
| Lands with | NBO-050 (M9) |

A `NetBoxWirelessLAN` is one `wireless.WirelessLAN` in NetBox: an SSID, optionally filed under a
group, bridged onto a VLAN, and attached to a Region, SiteGroup, Site or Location.

> ### The fourth kind on `CachedScopeMixin`, so `scope` and **never** `siteRef`
>
> `wireless.WirelessLAN` mixes in `CachedScopeMixin` (`netbox/wireless/models.py:80`), so the
> writable foreign key is `scope_type` + `scope_id` and `_region`, `_site_group`, `_site` and
> `_location` are read-only caches NetBox maintains from it.
>
> Writing `site` to this model **does not fail**. NetBox drops the unknown key, returns `201`,
> and the SSID is created unscoped while every subsequent read agrees with the spec — so it
> reports `Ready=True` forever and never drifts. That is the bug `netbox-populator` shipped, and
> the reason no scoped kind here has a `siteRef`, not even as sugar. See
> [what never reaches a request body](#what-never-reaches-a-request-body).
>
> ### And NetBox enforces no identity for it at all
>
> `wireless.WirelessLAN` declares no `meta.constraints` — only indexes. Two identical SSIDs in
> one scope are legal server state, so `(ssid, scope, tenant)` is a **lookup convention** and
> more than one match is a `Conflict` rather than a guess. See [natural keys](#natural-keys).

Companion kinds: [`NetBoxWirelessLANGroup`](netboxwirelesslangroup.md), which this one points at
through `groupRef`, and [`NetBoxWirelessLink`](netboxwirelesslink.md), which is the backhaul
rather than the SSID.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxWirelessLAN
metadata:
  name: donkersloot
  namespace: default
spec:
  endpointRef: homelab
  ssid: Donkersloot
```

An unscoped, untenanted SSID. Legitimate — both scope columns are nullable — but read
[natural keys](#natural-keys) before relying on it: with the scope and the tenant both absent,
the identity is `ssid` alone against a table that has no unique constraint at all.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxWirelessLAN
metadata:
  # A DNS-1123 label, and unrelated to spec.ssid below.
  name: donkersloot
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab

  # Shared-envelope defaults, written out.
  onConflict: Fail
  deletionPolicy: Delete

  ssid: Donkersloot

  groupRef:
    name: donkerslootstraat-wifi

  # NetBox's own default, written out. A defaulted field that never reaches a payload is a
  # field the operator can never correct.
  status: active

  vlanRef:
    name: iot

  # NetBox's scope, not Kubernetes'. There is no siteRef on this Kind. At most one member;
  # omit the block for an unscoped SSID, write it empty to clear a scope.
  scope:
    siteRef:
      name: home

  # on_delete=PROTECT, and the third term of the natural key.
  tenantRef:
    name: donkerslootstraat

  authType: wpa-personal
  authCipher: aes

  # There is deliberately no authPSK and no authPSKSecretRef yet. See the PSK section.

  description: Household IoT network
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy`, the `driftMode` override, `tags` and
`customFields` come from the shared envelope and behave identically on every kind — see
[`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `ssid` | `string` | **yes** | — | `ssid`, `CharField len=32` |
| `groupRef` | [ref](../concepts/references.md) → `NetBoxWirelessLANGroup` | no | — | `group`, `ForeignKey on_delete=SET_NULL` |
| `status` | enum | no | `active` | `status`, `CharField len=50 choices=WirelessLANStatusChoices` |
| `vlanRef` | [ref](../concepts/references.md) → `NetBoxVLAN` | no | — | `vlan`, `ForeignKey on_delete=PROTECT` |
| `scope` | [`ScopeRef`](genericref.md#scoperef) union | no | — | `scope_type` + `scope_id`, both nullable |
| `tenantRef` | [ref](../concepts/references.md) → `NetBoxTenant` | no | — | `tenant`, `ForeignKey on_delete=PROTECT` |
| `authType` | enum | no | — | `auth_type`, from `WirelessAuthenticationBase` |
| `authCipher` | enum | no | — | `auth_cipher`, from `WirelessAuthenticationBase` |
| `description` | `string` | no | — | `description`, `CharField len=200` |
| `comments` | `string` | no | — | `comments`, `TextField` |

### `spec.ssid`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=32` |

The network name. 32 is IEEE 802.11-2007's limit, and NetBox's:
`SSID_MAX_LENGTH = 32  # Per IEEE 802.11-2007` (`netbox/wireless/constants.py:1`, applied at
`netbox/wireless/models.py:84-87`).

Part of this kind's identity, but **only together with the scope and the tenant** — see
[natural keys](#natural-keys) for why an SSID alone does not identify a wireless LAN.

**If it is wrong:** admission enforces the length. There is no reconcile-time failure: NetBox
has no uniqueness constraint to refuse a duplicate.

### `spec.groupRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxWirelessLANGroup`](netboxwirelesslangroup.md) |
| Required | no |

Files the SSID under a group (`group ForeignKey -> wireless.WirelessLANGroup
on_delete=SET_NULL`, `netbox/wireless/models.py:88-94`).

**`SET_NULL` and not `CASCADE`, which is why this is not the containment reference.** Deleting
the group in NetBox leaves the SSID behind with no group, so an owner reference here would
delete a CR describing a row that still exists
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

**If it is wrong:** an unresolved group is `Ready=False, Reason=WaitingForRef` with
`RefsResolved` naming `groupRef`. `group` is not part of the identity, so the SSID is **not**
created while it waits — the whole payload waits, as it does for any unresolved reference on a
kind with no deferral for it.

### `spec.status`

| | |
|---|---|
| Type | `string` (`WirelessLANStatus`) |
| Required | no |
| Default | `active` |
| Validation | `Enum=active;reserved;disabled;deprecated` |

The SSID's lifecycle state. Values read from `WirelessLANStatusChoices`
(`netbox/wireless/choices.py:16-29`) in the 4.6.8 tree, because the schema digest records the
choice *class* and not its members.

**Four values, not three.** Unlike [`NetBoxVLAN`](netboxvlan.md)'s this set has a `disabled`
state *as well as* `deprecated` — exactly the sort of near-miss a shared status enum would have
papered over, which is why each kind declares its own.

Defaulted to NetBox's own default (`netbox/wireless/models.py:95-100`) so the operator manages
the column from the first reconcile. A defaulted field that never reaches a payload is a field
the operator can never correct.

The field carries no *omit-versus-empty* note, and that is deliberate rather than an oversight:
it has a `+kubebuilder:default`, so it is never absent, and
`TestClearableFieldsDocumentBothStatesInTheSchema` refuses a note whose schema contradicts it.

**If it is wrong:** a value outside the enum is rejected at admission.

### `spec.vlanRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxVLAN`](netboxvlan.md) |
| Required | no |

Bridges the SSID onto a VLAN (`vlan ForeignKey -> ipam.VLAN on_delete=PROTECT`,
`netbox/wireless/models.py:101-107`).

`PROTECT`, so a `NetBoxWirelessLAN` holding this reference **blocks deletion of that VLAN** in
NetBox: the VLAN's own CR reports `Deleting=False, Reason=Protected` naming this object
([deletion](../concepts/deletion.md)). Not a containment reference for the same reason —
`PROTECT` cascades nothing.

### `spec.scope`

| | |
|---|---|
| Type | [`ScopeRef`](genericref.md#scoperef) union |
| Required | no |
| Validation | at most one of `regionRef`, `siteGroupRef`, `siteRef`, `locationRef` |

Attaches the SSID to a Region, SiteGroup, Site or Location, and is **part of this object's
identity** — an SSID is only unique within its scope, and only by convention even there. It is
what keeps two houses' `Donkersloot` SSIDs apart.

Written as the `(scope_type, scope_id)` pair and diffed as a unit, so moving an SSID from a Site
to a Region is one change and one `PATCH` carrying both keys
([why the pair is atomic](../concepts/generic-refs.md#why-the-pair-is-atomic)).

The three states, and here the first two select different natural-key candidates:

| Written as | Means | Candidate |
|---|---|---|
| omitted entirely | do not manage the scope | 3 or 4 — `scope_id` pinned null |
| `scope: {}` | clear it: both columns `null` | 3 or 4 |
| one member, **resolved** | attach it there | 1 or 2 |
| one member, **not resolved** | nothing is written at all | none applicable; the engine waits |

Drift is keyed on the pair **only**. A change to the cached `_site` NetBox recomputed is not a
difference the operator can see or correct, because all four caches are in the descriptor's
`ReadOnly` list.

**If it is wrong:**

- Two members → rejected at admission. Nothing is stored.
- The member's target Kind, namespace or grant is wrong → `RefsResolved=False` naming the
  **member's** path (`scope.siteRef`), and no lookup and no write at all — the identity cannot
  be built. All four members have a Descriptor as of NBO-066, so `RefKindUnavailable` is not a
  state this union reaches any more.

### `spec.tenantRef`

| | |
|---|---|
| Type | `ObjectRef` → [`NetBoxTenant`](netboxtenant.md) |
| Required | no |

Assigns the SSID to a tenant (`tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT`,
`netbox/wireless/models.py:108-114`), and is **the third term of the natural key**.

`PROTECT`, so this reference blocks deletion of the tenant in NetBox and contributes no owner
reference.

### `spec.authType`, `spec.authCipher`

| | `authType` | `authCipher` |
|---|---|---|
| Type | `WirelessAuthType` | `WirelessAuthCipher` |
| Required | no | no |
| Default | **none** | **none** |
| Validation | `Enum=open;wep;wpa-personal;wpa-enterprise` | `Enum=auto;tkip;aes` |

From `WirelessAuthenticationBase` (`netbox/wireless/models.py:21-46`), declared once in
`api/v1alpha1/wireless_auth.go` and shared with
[`NetBoxWirelessLink`](netboxwirelesslink.md) — the second kind to use them restates nothing,
or the two copies drift and the losing one is invisible.

Values read from `WirelessAuthTypeChoices` (`netbox/wireless/choices.py:460-472`) and
`WirelessAuthCipherChoices` (`:474-483`). Note the wire value is `wpa-personal` while the label
NetBox renders is "WPA Personal (PSK)": the operator sends and compares the **value**, never the
label ([drift detection](../concepts/drift.md)).

**Undefaulted, unlike `status`.** Both columns are nullable with no Django default, so
defaulting either would assert a security posture nobody described.

### There is no `authPSK`, and no `authPSKSecretRef` yet

`wireless.WirelessAuthenticationBase` carries a third column, `auth_psk`. **No spec field maps
onto it**, and that is a deliberate, load-bearing omission rather than an oversight.

A pre-shared key may never be inline in a spec: that would put it in plain text in etcd and in
whatever git repository the manifest lives in. The required shape is `authPSKSecretRef` → a key
of a Secret, and that needs the engine to source one payload value from a Secret rather than
from the spec — a new field class, a Secret read in the payload path, and a Secret informer
scoped to the object's own namespace rather than to the deploy-time credential namespaces
[RBAC](../operations/gitops.md) grants today. Shared machinery and an RBAC decision, not
descriptor data.

Omitting the field is the safe half of that deferral. A spec omission means *do not manage this
column* ([field ownership](../concepts/field-ownership.md)), so:

- NetBox keeps whatever PSK it holds, on a create and on every subsequent pass;
- a column no spec field maps onto **cannot reach a payload at all** — asserted from the outside
  by a test that walks every recorded request body;
- the log half is already built and needs nothing: `auth_psk` is in `internal/netbox/do.go`'s
  `secretFields`, so it is masked in every request and response line at every level.

It is recorded as a [coverage](../coverage.md) note — a gap with a sentence, not an excuse.

### `spec.description`, `spec.comments`

Both have all three states: omit to leave NetBox's own value alone, set to `""` to clear it, set
to a string to write it ([field ownership](../concepts/field-ownership.md)).

## Natural keys

**Four** candidates, because `scope` and `tenant` are *independent* optional terms and each has
three states — resolved, cleared-or-absent, or declared-and-not-yet-resolved. That is a fuller
matrix than [`NetBoxVLAN`](netboxvlan.md)'s three, where `group` and `site` are alternatives
rather than independent.

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(scope_type, scope_id, tenant, ssid)` | `?scope_type=dcim.site&scope_id=<id>&tenant_id=<id>&ssid=<ssid>` | both **resolve** |
| 2 | `(scope_type, scope_id, ssid)` + `tenant IS NULL` | `?scope_type=…&scope_id=…&ssid=…&tenant_id=null` | scope resolves, `tenantRef` never declared |
| 3 | `(tenant, ssid)` + `scope IS NULL` | `?tenant_id=<id>&ssid=…&scope_id__empty=true` | tenant resolves, `scope` never declared |
| 4 | `ssid` + both `IS NULL` | `?ssid=…&scope_id__empty=true&tenant_id=null` | neither declared |

### The order is not a fallback chain

`NaturalKey.Applicable` matches only on a **resolved** field and pins only a **never-declared**
one, so exactly one candidate applies to any given spec — and a term that is declared but
unresolved makes *every* candidate inapplicable. The engine then waits rather than adopting the
SSID of the same name in some other scope and `PATCH`ing this scope onto it.

### None of them is backed by a constraint

`wireless.WirelessLAN` declares **no `meta.constraints`** — only two indexes, on `(ssid, id)`
for the default ordering and on `(scope_type, scope_id)` (`netbox/wireless/models.py:118-125`;
`docs/netbox-schema.md` → `wireless.WirelessLAN`). NetBox will happily store two identical SSIDs
in one scope.

So the key above is a convention, and more than one match is a real server state. The engine has
one answer for that and it is not per-kind: the client returns an `AmbiguousError` naming every
match, the engine reports `Ready=False, Reason=Conflict`, and **nothing is written**
([why ambiguity is an error](../concepts/errors-and-retries.md#why-ambiguity-is-an-error)). Same
shape as [`NetBoxIPAddress`](netboxipaddress.md); there is no per-kind helper, because "several
matches is ambiguous" is decided once in the client and no kind gets to decide it again.

### The filters and the pins

Every filter was checked rather than assumed — django-filter *ignores* a parameter it does not
recognise, so a guessed name is a lookup that returns the **unfiltered** set and the engine
adopts the first SSID in NetBox ([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206)):

| Filter | Declaration |
|---|---|
| `ssid` | `WirelessLANFilterSet.Meta.fields` (`netbox/wireless/filtersets.py:86-88`) |
| `scope_id` | same `Meta.fields` |
| `scope_type` | `MultiValueContentTypeFilter`, from `ScopedFilterSet` (`netbox/dcim/base_filtersets.py:18`) |
| `tenant_id` | `TenancyFilterSet` |

The **pins follow the column class**, which is the one thing about a null pin that cannot be
guessed ([#206](https://github.com/ricardomolendijk/netbox-operator/issues/206)):

- `scope_id` is a `PositiveBigIntegerField`, so `?scope_id__empty=true`.
- `tenant_id` is a foreign key, so the `?tenant_id=null` sentinel.
- **`scope_type` is pinned by nobody.** It is an FK to `contenttypes.ContentType` behind
  `MultiValueContentTypeFilter`, which registers neither spelling, and the sentinel is worse
  than dropped — it makes the request match *nothing at all*. Pinning the paired `_id` asks the
  same question, because NetBox rejects one half of the pair without the other. Same reading as
  [`NetBoxVLANGroup`](netboxvlangroup.md#the-unscoped-candidate-pins-one-scope-column-not-both).

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status).

**Nothing is cleared on failure**, `status.id` included.

`status.provenance` is stamped in full: `wireless.WirelessLAN` is a `PrimaryModel`, which mixes
in both `TagsMixin` and `CustomFieldsMixin` ([provenance](../operations/provenance.md)).

`status.naturalKey` is the only record of *which* of the four identities was used, and on this
kind it is the first thing to read when an SSID was not adopted. A
`{"scope_type": "dcim.site", "scope_id": "12", "ssid": "Donkersloot", "tenant_id": "null"}` says
candidate 2; a `{"ssid": "Donkersloot", "scope_id__empty": "true", "tenant_id": "null"}` says
candidate 4 — a global SSID, and the widest search this kind can make.

**No PSK ever appears in `status`, in a condition or in an Event**, because no spec field carries
one.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the SSID exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | every declared reference resolved | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefTypeNotAllowed`, `RefKindUnavailable` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | the resolved scope member's CR owns this one | it cannot | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`DeferredFieldPending` is absent: this kind declares no deferred fields.

Reason glossary and retry intervals are shared; see
[errors and retries](../concepts/errors-and-retries.md). The three that mean something
particular here:

- **`Conflict`** on `Ready`: more than one SSID matched. On this kind that is legitimate NetBox
  state rather than proof of a mistake — see
  [none of them is backed by a constraint](#none-of-them-is-backed-by-a-constraint).
- **`WaitingForRef`** on `Ready` with `scope` named: the scope is declared and unresolved, so
  **no lookup happened**. The object is waiting for an identity it cannot build yet, which is
  the designed outcome and not a stall. Do not remove `scope` to unblock it — that changes the
  object's identity.
- **`Protected`** on `Deleting`: nothing points *at* a wireless LAN with a `PROTECT`, so this is
  rare here. It is the *VLAN's* and the *tenant's* `Deleting` condition that goes `Protected`
  because of this object.

## Kind-specific behaviour

### What never reaches a request body

| Key | Why |
|---|---|
| `site`, `site_id` | not a column on `wireless.WirelessLAN`. NetBox ignores an unknown key rather than rejecting it, so writing it returns `201`, creates the SSID **unscoped**, and compares clean on every subsequent read — `Ready=True` forever against a scope that was never set |
| `_region`, `_site_group`, `_site`, `_location` | read-only caches NetBox maintains from the pair (`netbox/dcim/models/mixins.py:41-89`). Writing one is dropped, so the operator would `PATCH` it again every resync, forever |
| `auth_psk` | no spec field maps onto it — see [the PSK section](#there-is-no-authpsk-and-no-authpsksecretref-yet) |

A descriptor listing a cache column without marking it read-only fails the manager's boot
(`ErrCachedNotReadOnly`), and a test asserts none of these keys reaches a request body on this
kind in particular. `GET /api/wireless/wireless-lans/?scope_id__empty=true` is the query that
tells you how many SSIDs a `netbox-populator`-era `site:` silently failed to scope.

### `scope` is the containment parent, and all four members cascade

Every scope member cascades, by **two different mechanisms** that have to be read separately —
and this kind is one of the two that needs both halves:

- `dcim.Region` and `dcim.SiteGroup` carry a `wireless_lans` `GenericRelation` pointed at
  `scope_type` / `scope_id` (`netbox/dcim/models/sites.py:51-56` and `:122-127`), so deleting
  either deletes the SSIDs scoped to it.
- `dcim.Site` and `dcim.Location` carry **no** such `GenericRelation` — and need none, because
  `CachedScopeMixin` declares `_site` and `_location` with `on_delete=CASCADE`
  (`netbox/dcim/models/mixins.py:63-74`). The cached column *is* the cascade for those two.

The other two caches are `on_delete=SET_NULL` on the same mixin (`:75-89`), and the comment
there says why: they cache an *ancestor* of the actual scope, so deleting that ancestor must not
delete this object — and the `GenericRelation` handles the case where the Region or SiteGroup
**is** the scope. Reading only the `SET_NULL` half and concluding "region and site group do not
cascade" is how `virtualization.Cluster` came to have no containment parent at all
([#214](https://github.com/ricardomolendijk/netbox-operator/issues/214)).

So all four cascade, and `scope` is the containment reference: the SSID's CR carries a
non-controller owner reference to whichever of Region, SiteGroup, Site or Location it actually
resolved through, decided per pass from that member. `groupRef`, `vlanRef` and `tenantRef` are
`SET_NULL`, `PROTECT` and `PROTECT`, so none of them is even eligible — one cascading reference,
one slot, no tiebreak.

### The scope moves as one pair

Moving the SSID from a Region to a Site is **one** `PATCH` carrying both `scope_type` and
`scope_id`. A `scope_id` sent without its `scope_type` is rejected by NetBox at best and
interpreted against the old type at worst — the object would point at whatever row of the
*previous* model happens to share that primary key.

### Renaming changes identity

Editing `spec.ssid`, `spec.scope` or `spec.tenantRef` changes what this CR *is*. Once
`status.id` is set the object is reconciled by id, so the edit is a `PATCH` on the same row and
the natural key is not consulted again. Before the first successful reconcile, an edit is simply
a different lookup — and on a table with no unique constraint, that is how a second SSID gets
created.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete`
([deletion](../concepts/deletion.md#the-default-depends-on-the-kind)).

## Printer columns

```
NAME          SSID          VLAN   STATUS   ID   READY   AGE
donkersloot   Donkersloot   iot    active   61   True    5m
guest         Donkersloot   guest  active   62   True    5m
```

| Column | JSONPath |
|---|---|
| `SSID` | `.spec.ssid` |
| `VLAN` | `.spec.vlanRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`VLAN` reads `.spec.vlanRef.name`, so it is empty for a VLAN referenced by `id` or `slug`.
`SCOPE` is deliberately not a column: it is a union, and no single JSONPath reads one.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, `ssid` too long | admission, nothing stored | over 32 characters | IEEE 802.11-2007's limit, and NetBox's |
| `kubectl apply` rejected, "at most one of regionRef…" | admission | two members of `spec.scope` | An SSID has one scope. Pick one |
| `kubectl apply` rejected, `status` not in enum | admission | a value outside `active/reserved/disabled/deprecated` | Four values here, not `NetBoxVLAN`'s three |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `scope.<member>` | reconcile, **zero writes and zero lookups** | the scope target does not exist or is not `Ready` | Expected while the site is being created. Apply it; the SSID re-enqueues. Do **not** remove `scope` — that changes the identity |
| `Ready=False`, `Reason=WaitingForRef`, `RefsResolved` names `groupRef` or `vlanRef` | reconcile, zero writes | the group or VLAN is missing | Apply it. Neither is in the identity, but neither is deferred either, so the whole payload waits |
| `Ready=False`, `Reason=Conflict` | reconcile, zero writes | more than one SSID matched | Legitimate: no constraint exists. `status.naturalKey` shows what was searched. Narrow it with a scope or a tenant, or adopt by `id` |
| NetBox shows the SSID **unscoped** and the spec has a scope | none — `Ready=True` | a `site:` field from a `netbox-populator`-era manifest | There is no `siteRef` on this kind and never will be. Use `scope: {siteRef: …}`. Find the rest with `?scope_id__empty=true` |
| The scope will not clear | none | the field was removed from the manifest rather than emptied | Absent means "do not manage". Write `scope: {}` |
| `_site` was edited in NetBox and nothing happened | none | it is a read-only cache | Correct. Drift is keyed on `(scope_type, scope_id)` only |
| A `NetBoxVLAN` will not delete | `Deleting=False`, `Reason=Protected` on the **VLAN** | this SSID holds `vlanRef`, and `vlan` is `PROTECT` | Remove `vlanRef` here, or delete this SSID first |
| A second SSID appeared after an edit | none | `ssid`, `scope` or `tenantRef` changed before the first successful reconcile | See [renaming changes identity](#renaming-changes-identity) |
| The PSK is not being set | none | no spec field maps `auth_psk` | By design. Set it in the NetBox UI; the operator will not touch it. See [the PSK section](#there-is-no-authpsk-and-no-authpsksecretref-yet) |
| `ParentOwned=False`, `Reason=CascadeUnavailable` | reconcile | the scope target is in another namespace, or referenced by `id` | Expected for a shared site. An owner reference cannot cross a namespace ([ADR-0003](../decisions/0003-ownership-and-references.md)) |

## Related

- [`NetBoxWirelessLANGroup`](netboxwirelesslangroup.md) — the group this points at, and why that
  reference cascades nothing
- [`NetBoxWirelessLink`](netboxwirelesslink.md) — the backhaul rather than the SSID, and the same
  two auth enums
- [`ScopeRef`](genericref.md#scoperef) — the union's shape in a spec, and the keys that never
  reach a request body
- [Generic references](../concepts/generic-refs.md) — the scope pair, why it is atomic, and the
  `app_label.model` spelling rule
- [`NetBoxVLANGroup`](netboxvlangroup.md) — the other scoped kind whose identity contains the
  pair, and the same one-half null pin
- [`NetBoxPrefix`](netboxprefix.md) — the scoped kind that *does* carry the cached columns
- [`NetBoxVLAN`](netboxvlan.md) — the one kind that writes `site` as a real foreign key, and why
  this one must never
- [Lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted) — why the null
  columns are pinned rather than omitted
- [Errors and retries](../concepts/errors-and-retries.md#why-ambiguity-is-an-error) — why more
  than one match is an error
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [ADR-0003: ownership and references](../decisions/0003-ownership-and-references.md) — decision
  2, the scope-versus-`site` bug, and rule 4
