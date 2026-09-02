# `NetBoxL2VPN`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxL2VPN` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbl2vpn` |
| Status subresource | yes |

A `NetBoxL2VPN` is one `vpn.L2VPN` in NetBox: a layer 2 overlay — a VXLAN EVPN fabric, a VPLS
instance, an Ethernet private line — and the route targets that import and export it. It is
written to `vpn/l2vpns`.

It is the **second kind in the catalogue with real to-many references**, after
[`NetBoxVRF`](netboxvrf.md), and they are the same relation to the same model: `importTargets`
and `exportTargets` are lists of [`NetBoxRouteTarget`](netboxroutetarget.md) references. Everything
those lists do here, they do there, for the same reasons and with the same wording — two
`ClassRefMany` entries on the descriptor and no engine code of any kind.

Two things are worth knowing before the field list.

## Identity is `slug`, and there is no `name` fallback

`vpn.L2VPN` declares **no `meta.constraints`** and carries a column-level `UNIQUE` on *both*
`name` and `slug` (`docs/netbox-schema.md` → `vpn.L2VPN`), so either would identify one row on
its own. `slug` is the candidate, for the reason it is on every `OrganizationalModel`: a kind gets
one identity and the slug is the stable one, so renaming an L2VPN updates the object NetBox
already holds rather than orphaning it.

There is deliberately **no second candidate on `name`**. Candidates are tried in order and the
engine falls through when one matches nothing, so a `name` fallback would be reached exactly when
the slug has changed — and it would adopt the object whose slug disagrees and PATCH this slug
onto it, renaming somebody else's L2VPN. See [Natural key](#natural-key).

## Terminations are not here

`vpn.L2VPNTermination` is a separate NetBox model, and it is **not** part of this release: its
identity is `(assigned_object_type, assigned_object_id)` over a generic foreign key, which is a
different piece of machinery from anything on this spec.

Eight of the `vpn` app's ten models ship as Kinds. The two that do not are
`vpn.L2VPNTermination` and `vpn.TunnelTermination`, deferred by the pull request for #59 for
exactly that reason.

An L2VPN declared here is a complete, adoptable `vpn.L2VPN`: what terminates on it — the VLANs
and interfaces attached to the overlay — is set in NetBox until that Kind ships, and the operator
neither writes nor removes those rows. `terminations` is not a column on this model; it is the
reverse accessor of `L2VPNTermination.l2vpn`.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxL2VPN
metadata:
  name: campus-evpn
  namespace: default
spec:
  endpointRef: homelab
  name: Campus EVPN
  slug: campus-evpn
  type: vxlan-evpn
```

Three required fields and no reference to wait on: an L2VPN with no route targets is an ordinary
L2VPN.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRouteTarget
metadata:
  name: rt-65000-1000
  namespace: default
spec:
  endpointRef: homelab
  name: "65000:1000"
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxL2VPN
metadata:
  name: campus-evpn
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Campus EVPN
  slug: campus-evpn
  type: vxlan-evpn
  status: active              # default
  identifier: 65000

  # Two independent relations onto ipam.RouteTarget. The same target may appear in both.
  importTargets:
    - name: rt-65000-1000
  exportTargets:
    - name: rt-65000-1000

  tenantRef:
    name: acme

  description: Campus-wide VXLAN EVPN overlay
  comments: |
    Terminations are set in NetBox: vpn.L2VPNTermination is not a Kind yet (#59).
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `slug` | `string` | yes | — | 1–100, `^[-a-zA-Z0-9_]+$` | `slug SlugField REQ UNIQUE len=100` |
| `type` | `string` | yes | — | enum: `vpws`, `vpls`, `vxlan`, `vxlan-evpn`, `mpls-evpn`, `pbb-evpn`, `evpn-vpws`, `epl`, `evpl`, `ep-lan`, `evp-lan`, `ep-tree`, `evp-tree`, `spb` | `type CharField REQ len=50` |
| `status` | `string` | no | `active` | enum: `active`, `planned`, `decommissioning` | `status CharField len=50` |
| `identifier` | `integer` | no | — | — | `identifier BigIntegerField` |
| `importTargets` | list of [`ObjectRef`](../concepts/references.md) → `NetBoxRouteTarget` | no | — | `maxItems: 256`, ref arity CEL per element | `import_targets ManyToManyField -> ipam.RouteTarget` |
| `exportTargets` | list of `ObjectRef` → `NetBoxRouteTarget` | no | — | `maxItems: 256`, ref arity CEL per element | `export_targets ManyToManyField -> ipam.RouteTarget` |
| `tenantRef` | `ObjectRef` → `NetBoxTenant` | no | — | ref arity CEL | `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

### `spec.name`

Required, 1–100 characters, and **column-unique across the whole NetBox**. A candidate key and
deliberately not the lookup key — see [above](#identity-is-slug-and-there-is-no-name-fallback).

A rename that collides comes back as NetBox's own 409, reported as
`Ready=False, Reason=Invalid`.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and **globally unique over namespaced CRs**: two namespaces cannot both own
`campus-evpn`, and the loser gets `Ready=False, Reason=Conflict`
([ADR-0002](../decisions/0002-crd-scoping.md)).

Editing it does not rename the NetBox L2VPN — see
[renaming the slug changes identity](#renaming-the-slug-changes-identity).

### `spec.type`

Required. Which layer 2 VPN technology this is. Fourteen members from
`netbox/vpn/choices.py:217` at 4.6.8. NetBox renders them in two option groups — the
VPLS/VPWS/EVPN family, then the MEF service types — which is presentation only: the flat set of
*values* is what goes over the wire, and the operator sends and compares the value rather than the
label ([drift](../concepts/drift.md)).

Closed: `L2VPNTypeChoices` declares no `key`, so no deployment's `FIELD_CHOICES` can add a member
this enum would reject (`hack/testdata/ir-4.6.8.json.gz` → `enums.L2VPNTypeChoices`).

**Required here, and `required=False` on the serializer.** The IR records
`"api": {"required": false}` against `"sql_required": true`
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.L2VPN.type`), so an omitted type is accepted by DRF and
then refused by the database. The stricter of the two readings is the one this schema takes,
because it turns the failure into a `kubectl apply` error instead of a 500. The enum carries no
empty member, so `""` is refused by the enum itself.

### `spec.status`

The L2VPN's lifecycle state, defaulted to NetBox's own default so the operator manages the field
from the first reconcile — a defaulted field that never reaches a payload is a field the operator
can never correct.

Three members, `netbox/vpn/choices.py:272` at 4.6.8: `active`, `planned`, `decommissioning`.

**This `ChoiceSet` is extensible, and the enum here is not.** `L2VPNStatusChoices` declares
`key = 'L2VPN.status'` (`hack/testdata/ir-4.6.8.json.gz` → `enums.L2VPNStatusChoices`), so a
deployment can add values through `FIELD_CHOICES`; a value added there **needs this CRD's enum
widened**, because a CRD cannot read a NetBox setting. Same statement as
[`NetBoxTunnel`](netboxtunnel.md#specstatus) makes about `Tunnel.status`,
[`NetBoxRack`](netboxrack.md#specstatus) about `Rack.status` and
[`NetBoxVLAN`](netboxvlan.md) about `VLAN.status`. The symptom is a `kubectl apply` rejected on
`status` naming a value your NetBox accepts in the UI.

These are **not** `NetBoxTunnel`'s three: `decommissioning` is not `disabled`, and `planned` is
the only value the two sets share — which is exactly the near-miss one shared enum would have
papered over.

### `spec.identifier`

The L2VPN's numeric identifier — a VNI for VXLAN, a VC ID for VPLS.

A pointer, so omitting it leaves NetBox's value alone rather than clearing it, and so that `0` is
distinguishable from unset. A **signed** `BigIntegerField` rather than a positive one, so there is
no `minimum` marker: NetBox accepts a negative identifier, and a bound this schema invented would
refuse a value NetBox stores.

Not part of the identity: no constraint names it, and `meta.ordering: ('name', 'identifier')` is
an ordering rather than a uniqueness claim.

### `spec.importTargets` and `spec.exportTargets`

The route targets imported into, and exported from, this L2VPN. Two **independent** relations: the
same route target may appear in both, and each resolves and is written on its own.

Both are `ManyToManyField -> ipam.RouteTarget` declared on `vpn.L2VPN`, which is why they are here
and not on [`NetBoxRouteTarget`](netboxroutetarget.md#the-relation-is-written-from-the-vrf-side-only)
— all writes to the relation go through this side.

#### The three states

NetBox replaces a many-to-many **wholesale** on `PATCH` — there is no add or remove verb — so the
listed set *is* the set:

| Spec | Payload | Meaning |
|---|---|---|
| field absent | key omitted | do not manage; NetBox keeps whatever route targets it has |
| `importTargets: []` | `"import_targets": []` | manage it, and clear it |
| `importTargets: [a, b]` | `"import_targets": [3, 7]` | manage it, exactly these |

An absent field must stay absent. A field defaulted to `[]` would strip the route targets off the
first hand-configured L2VPN the operator ever touched, and report success. The middle row needs
`metadata.managedFields` to be readable, because Go's `omitempty` erases an explicitly-empty list
on the way in — see [field ownership](../concepts/field-ownership.md).

`[]` is legal here and rejected on a policy's `proposals`
([`NetBoxIKEPolicy`](netboxikepolicy.md#specproposals)), and the difference is the column: these
two are `blank=True` (`hack/testdata/ir-4.6.8.json.gz` → `vpn.L2VPN.import_targets`), so an L2VPN
with no route targets is an ordinary L2VPN.

#### Sorted, deduplicated, order-independent

The ids are sent **sorted ascending and deduplicated**. NetBox does not preserve the order it was
given, so the order you write is not data; the comparison is an order-independent set compare
([drift](../concepts/drift.md)), which is what makes **reordering a list produce zero writes**;
and the request still has to be deterministic, or `status.lastAppliedHash` changes every pass and
the short-circuit never fires. Listing the same route target twice is not an error — a relation is
a set.

#### All or nothing

If **any** element cannot be resolved, the whole field is left out of the payload and the object
reports `RefsResolved=False` naming the element by its index —
`exportTargets[1] -> netboxroutetarget/team-a/rt-missing: not found` — with `Ready=False,
Reason=WaitingForRef` and zero writes. Writing the ones that did resolve would be a full-list
replacement with a shorter list: a deletion of the rest, reported as a successful write
([references](../concepts/references.md#a-list-resolves-whole-or-not-at-all)). When the missing
route target arrives, its event re-enqueues this L2VPN and the write completes in one pass.

The two lists resolve independently of each other, but each resolves whole.

#### No owner references

A many-to-many member contributes **no** owner reference: the containment list in
[ADR-0003](../decisions/0003-ownership-and-references.md) §4 covers single-valued relations only,
and two L2VPNs may import one route target while neither owns it. Deleting this L2VPN does not
cascade to its route targets.

### `spec.tenantRef`

The tenant this L2VPN belongs to. Ordinary reference, `PROTECT`, and not part of the identity:
`slug` is unique across the whole install, so a tenant filter would narrow a lookup that already
matches at most one row.

**If it is wrong.** `RefsResolved=False` naming the field, `Ready=False, Reason=WaitingForRef`,
and nothing written. See [`NetBoxTenant`](netboxtenant.md) for why a tenant reference cascades
nothing, and [references](../concepts/references.md) for why a namespace does not imply a tenant.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second — a `TextField` has no `max_length`. Both
inherited from `PrimaryModel`, and both clearable: omit one to leave NetBox's own value alone, set
it to `""` to clear it ([field ownership](../concepts/field-ownership.md)).

## Natural key

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

The identity does not come from a constraint list — `vpn.L2VPN` declares none
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.L2VPN.natural_keys`, `[]`) — it comes from the
column-level `UNIQUE` on `slug`. Uniqueness is global, so there is nothing to pin to null and
nothing a candidate could be conditional on.

The filter is registered: `slug` is in `L2VPNFilterSet.Meta.fields` (NetBox 4.6.8,
`netbox/vpn/filtersets.py:332`).

`name` is unique too and is deliberately **not** a second candidate — see
[above](#identity-is-slug-and-there-is-no-name-fallback). It is the
[`NetBoxVRF`](netboxvrf.md#natural-keys) reasoning with the same conclusion and no pin to soften
it: there, `rd` and `name` describe two different objects and the second candidate pins `rd=null`
to say so; here both columns describe the same object, so the second candidate would only ever
adopt the wrong one.

Because `slug` is column-unique, more than one match is impossible: a hand-made L2VPN of this slug
is **adopted** (`status.adopted=true`), and a duplicate is NetBox's own 409.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

`vpn.L2VPN` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is stamped in
full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the L2VPN exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | every element of both lists resolved and `tenantRef` is unset or resolves | any did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefDenied`, `RefTargetFailed` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved=False` forces `Ready=False, Reason=WaitingForRef`, so a withheld many-to-many cannot
pass a readiness check.

## Kind-specific behaviour

### Reordering a route-target list writes nothing

```console
$ kubectl patch netboxl2vpn campus-evpn --type=merge \
    -p '{"spec":{"importTargets":[{"name":"rt-b"},{"name":"rt-a"}]}}'
```

produces no NetBox request. The ids are sorted before the payload is built, so the payload is
byte-identical, `status.lastAppliedHash` is unchanged and the reconcile short-circuits. Exactly as
on [`NetBoxVRF`](netboxvrf.md#reordering-a-route-target-list-writes-nothing).

### Renaming the slug changes identity

`slug` is the natural key, so editing it does not rename the NetBox L2VPN — it changes what the CR
is looking for, and the next reconcile creates a second L2VPN, leaving the first behind. Rename in
NetBox and in the manifest together, or delete and re-create the CR.

`name`, `type`, `status`, `identifier`, both route-target lists, `tenantRef`, `description` and
`comments` are safe to edit: none is part of the natural key.

### No containment parent, in either direction

`tenant` is `PROTECT` and the two route-target relations are many-to-many, so nothing on this
model qualifies as a containment parent under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 and this Kind takes no owner
reference from anything.

The cascade that does exist runs the other way and is invisible from here:
`vpn.L2VPNTermination.l2vpn` is `on_delete=CASCADE`, so deleting an L2VPN takes its terminations
with it — rows no CR describes yet, because that Kind is deferred.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). An L2VPN is configuration a manifest recreates
verbatim; nothing is allocated from one, which is what `Retain` was reserved for. See
[deletion](../concepts/deletion.md).

The `CASCADE` above is the reason to override it: on an L2VPN whose terminations were set by hand,
`deletionPolicy: Retain` is the safe default until `vpn.L2VPNTermination` ships.

### What is not here yet

- **`vpn.L2VPNTermination`.** Not a Kind and not an inline list — see
  [terminations are not here](#terminations-are-not-here) (#59).
- **`contacts`** is a `GenericRelation`, the far end of somebody else's foreign key.
- **`owner`.** `ForeignKey -> users.Owner`, and `users/*` is an excluded endpoint.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

Nothing else on this model is missing: every remaining column in the serializer's write path is
mapped or read-only (`hack/testdata/ir-4.6.8.json.gz` → `vpn.L2VPN.write_path`).

## Printer columns

```
$ kubectl get nbl2vpn
NAME          TYPE         STATUS   ID    READY   AGE
campus-evpn   vxlan-evpn   active   103   True    3m
lab-vpls      vpls         planned  104   True    3m
```

| Column | JSONPath |
|---|---|
| `TYPE` | `.spec.type` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

Both read the *spec*, so they show the intent even while a route target is unresolved and `ID` is
empty.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `type` | — | The field is required by this schema even though the serializer calls it optional; the column is `NOT NULL`. | Name one of the fourteen |
| `kubectl apply` rejected on `status` naming a value the NetBox UI accepts | — | `L2VPNStatusChoices` declares `key = 'L2VPN.status'`, so your deployment extended it through `FIELD_CHOICES`. A CRD cannot read a NetBox setting. | Widen this CRD's enum, or use a listed value |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved=False` | One route target in a list does not exist or is not usable yet; the message names the element, `importTargets[1]`. | Create it; its event re-enqueues this L2VPN |
| Route targets in NetBox are ignored | none | The field is absent from the spec, and absent means "do not manage". | Add the list, or `[]` to clear |
| The last route target will not come off | none | Removing every element leaves the field absent after `omitempty`. | Write `importTargets: []` explicitly — it needs `metadata.managedFields` to be readable ([field ownership](../concepts/field-ownership.md)) |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this slug. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner |
| `Ready=False`, `Reason=Invalid`, message names `name` | `Ready` | A rename collided with the column-level `UNIQUE` on `name`, which is global. | Pick another name; `slug` is what identifies the object |
| A second L2VPN appeared after an edit | — | `spec.slug` was changed. | See [renaming the slug changes identity](#renaming-the-slug-changes-identity) |
| Terminations vanished after `kubectl delete` | — | `vpn.L2VPNTermination.l2vpn` is `CASCADE`, and no CR describes those rows. | Use `deletionPolicy: Retain` on L2VPNs whose terminations are hand-made |
| `spec.terminations` rejected by `kubectl apply` | — | There is no such field. | See [terminations are not here](#terminations-are-not-here) |

## Related

- [`NetBoxRouteTarget`](netboxroutetarget.md) — the far end of both relations
- [`NetBoxVRF`](netboxvrf.md) — the first kind with these two lists, and the page that derives their behaviour in full
- [`NetBoxTunnel`](netboxtunnel.md) — the other `vpn` kind that ships, and the other whose terminations are deferred
- [`NetBoxTenant`](netboxtenant.md) — the `PROTECT`ed tenant
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set, and how `[]` survives `omitempty`
- [Drift detection](../concepts/drift.md) — the order-independent set compare
- [ADR-0003](../decisions/0003-ownership-and-references.md) — why a many-to-many member takes no owner reference
