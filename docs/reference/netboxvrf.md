# `NetBoxVRF`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVRF` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvrf` |
| Status subresource | yes |
| Lands with | NBO-022 (M3) |

A `NetBoxVRF` is one `ipam.VRF` in NetBox: a routing table that gives prefixes and addresses
somewhere to be unique. Without it, every house using `10.0.0.0/24` collides in NetBox's
global table — so this kind is what makes `NetBoxPrefix` and `NetBoxIPAddress` identifiable
at all.

It is also the **first kind with a real to-many reference**. `importTargets` and
`exportTargets` are lists of [`NetBoxRouteTarget`](netboxroutetarget.md) references, and
everything unusual about this page is downstream of that.

> ⚠️ **`spec.name` is not unique in NetBox.** `ipam.VRF` carries no `UNIQUE` on the column
> and declares no `meta.constraints` at all, so a name-only lookup can legitimately match
> more than one VRF. Set `spec.rd` on every VRF you can — see
> [natural keys](#natural-keys).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVRF
metadata:
  name: donkerslootstraat
  namespace: default
spec:
  endpointRef: homelab
  name: Donkerslootstraat (RTM)
  rd: "65000:10"
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRouteTarget
metadata:
  name: rt-65000-10-import
  namespace: default
spec:
  endpointRef: homelab
  name: "65000:10"
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxRouteTarget
metadata:
  name: rt-65000-11-export
  namespace: default
spec:
  endpointRef: homelab
  name: "65000:11"
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVRF
metadata:
  name: donkerslootstraat
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Retain      # default *on this kind* -- see below

  name: Donkerslootstraat (RTM)
  rd: "65000:10"
  enforceUnique: true

  importTargets:
    - name: rt-65000-10-import
  exportTargets:
    - name: rt-65000-11-export

  description: Per-house VRF, so identical 10.0.x.0/24 ranges do not collide
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared
envelope and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Validation | `+kubebuilder:validation:MinLength=1`, `+kubebuilder:validation:MaxLength=100` |

The VRF's label in the NetBox UI.

**Not unique.** `docs/netbox-schema.md` → `ipam.VRF` gives `name CharField REQ len=100` — no
`UNIQUE` — and the model declares no `meta.constraints`, so NetBox happily holds two VRFs
called `Donkerslootstraat (RTM)`. This is the one fact on this kind that will bite you, and
the two natural keys below are how the operator refuses to guess.

**If it is wrong.** Empty or over 100 characters is rejected at admission. Changed after the
fact, it changes what the CR is looking for — see
[renaming changes identity](#renaming-changes-identity).

### `spec.rd`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `+kubebuilder:validation:MaxLength=21` |

The route distinguisher, `<asn>:<value>` — `65000:10`.

Column-unique in NetBox (`docs/netbox-schema.md` → `ipam.VRF`,
`rd CharField UNIQUE len=21`), which makes it the only field here that identifies a VRF on its
own, and therefore this kind's natural key whenever it is set. The 21-character cap is
NetBox's `VRF_RD_MAX_LENGTH`, defined in `netbox/ipam/constants.py`.

Globally unique over namespaced CRDs, exactly like a `NetBoxSite`'s `slug`: two namespaces
cannot both own `65000:10`, and the loser gets `Ready=False, Reason=Conflict`. Same routine
failure mode, same mitigation — `onConflict: Fail` for anything shared, so a collision is
reported rather than silently taken over.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. Those are different
instructions ([field ownership](../concepts/field-ownership.md)) — but note that an
explicitly-emptied `rd` leaves this object with **no applicable natural key**, and it will
report `Ready=False, Reason=WaitingForKey` and write nothing. That is the safe answer: see
[an emptied `rd` is no identity](#an-emptied-rd-is-no-identity).

### `spec.enforceUnique`

| | |
|---|---|
| Type | `*bool` |
| Required | no |
| Default | none in the CRD; NetBox's own column default is `true` |

Asks NetBox to refuse duplicate prefixes and duplicate addresses inside this VRF.

**A pointer, and the pointer is the point.** `docs/netbox-schema.md` → `ipam.VRF` gives
`enforce_unique BooleanField def=True`. With a plain `bool`, "omitted" and "`false`" would be
the same Go value, and the operator would send `false` on every VRF it ever adopted —
silently turning NetBox's own default off. So:

| Spec | Payload | Meaning |
|---|---|---|
| field absent | key omitted | do not manage; NetBox keeps its own value |
| `enforceUnique: false` | `"enforce_unique": false` | duplicates permitted in this VRF |
| `enforceUnique: true` | `"enforce_unique": true` | duplicates refused |

The same rule applies to every defaulted boolean in the catalogue — `ipam.Prefix.is_pool`,
`ipam.Prefix.mark_utilized`, `ipam.IPRange.mark_populated`.

It is deliberately **not** defaulted in the CRD either. Defaulting it to NetBox's `true`
would write the field on every VRF the operator touches, which is the opposite of "spec
omission means do not manage".

**What `false` costs downstream.** With uniqueness off, NetBox permits duplicate prefixes and
addresses inside this VRF, so `NetBoxPrefix`'s `(prefix, vrf)` and `NetBoxIPAddress`'s
`(address, vrf)` natural keys stop being unique and their lookups can match more than one
row. That is a documented consequence of this field rather than a bug in those kinds. The
operator models no uniqueness logic of its own; the flag is written and NetBox decides
(issue #177).

### `spec.importTargets` and `spec.exportTargets`

| | |
|---|---|
| Type | `[]RouteTargetRef` — a list of [`ObjectRef`](../concepts/references.md)s |
| Required | no |
| Target kind | [`NetBoxRouteTarget`](netboxroutetarget.md) |

The route targets imported into, and exported from, this VRF. Two independent relations: the
same route target may appear in both, and each resolves and is written on its own.

Both are `ManyToManyField -> ipam.RouteTarget` declared on `ipam.VRF`
(`docs/netbox-schema.md` → `ipam.VRF`), which is why they are here and not on
`NetBoxRouteTarget`. All writes to the relation go through the VRF.

#### The three states

NetBox replaces a many-to-many **wholesale** on `PATCH` — there is no add or remove verb — so
the listed set *is* the set:

| Spec | Payload | Meaning |
|---|---|---|
| field absent | key omitted | do not manage; NetBox keeps whatever route targets it has |
| `importTargets: []` | `"import_targets": []` | manage it, and clear it |
| `importTargets: [a, b]` | `"import_targets": [3, 7]` | manage it, exactly these |

An absent field must stay absent. A field defaulted to `[]` would strip the route targets off
the first hand-configured VRF the operator ever touched, and report success.

The middle row needs `metadata.managedFields` to be readable, because Go's `omitempty` erases
an explicitly-empty list on the way in. The API server has tracked which fields each client
set since field management went GA, and the engine reads that — see
[field ownership](../concepts/field-ownership.md), including what happens when there is no
ownership metadata at all.

#### Sorted, deduplicated, order-independent

The ids are sent **sorted ascending and deduplicated**:

- NetBox does not preserve the order it was given, so the order you write is not data.
  Carrying it would advertise an ordering nothing downstream honours.
- The comparison is therefore an order-independent set compare
  ([drift detection](../concepts/drift.md)), which is what makes **reordering the list produce
  zero writes**.
- The request still has to be deterministic, or `status.lastAppliedHash` changes every pass
  and the short-circuit never fires.
- Listing the same route target twice is not an error. A relation is a set, and that is what
  NetBox stores either way.

#### All or nothing

If **any** element cannot be resolved, the whole field is left out of the payload and the
object reports:

```
RefsResolved  False  RefNotFound  importTargets[1] -> netboxroutetarget/team-a/rt-missing: not found (no such object in the cluster)
Ready         False  WaitingForRef
```

with **zero writes**, and no returned error — an element waiting for its target is a state, not
a failure. This is structural rather than a policy the engine has to remember: resolution is
keyed by field, and a field is present only when every element resolved, so "three of five"
has no representation.

The element is named **by its index**, counted from zero against the order this manifest lists
the route targets in, and the target it pointed at is rendered beside it — a reference written
by `slug` names a NetBox row and no CR, so the index alone would not say what was looked for
([references](../concepts/references.md#a-list-resolves-whole-or-not-at-all)).

It has to be that way round. Writing the two that resolved would be a full-list replacement
with a shorter list — a deletion of the three that did not, reported as a successful write.
When the missing route target arrives, its event re-enqueues this VRF and the write completes
in one pass.

Each unresolved element keeps its own reason and its own retry interval, so a not-found
element and a not-ready one are reported and retried differently
([errors and retries](../concepts/errors-and-retries.md)).

#### No owner references

An M2M member contributes **no** owner reference. The containment list in
[ADR-0003](../decisions/0003-ownership-and-references.md) §4 covers single-valued relations
only, and a shared many-to-many is by definition not containment: two VRFs may import one
route target and neither owns it. Deleting this VRF therefore does not cascade to its route
targets.

The reverse *is* containment: a `NetBoxPrefix` or `NetBoxIPAddress` with a `vrfRef` acquires a
non-controller owner reference on the VRF, so `kubectl delete netboxvrf` cascades to them.

### `spec.description`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `+kubebuilder:validation:MaxLength=200` |

Free text, inherited from `PrimaryModel`. Omit it to leave NetBox's own value alone; set it to
`""` to clear it.

### `spec.comments`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | none — a `TextField` has no `max_length` |

Long-form notes. Same three states as `description`.

### What is deliberately absent

`ipam.VRF.tenant` is a `ForeignKey -> tenancy.Tenant on_delete=PROTECT`
(`docs/netbox-schema.md` → `ipam.VRF`) and there is **no `tenantRef` field yet**.
`NetBoxTenant` lands with NBO-021; a field that is accepted and does nothing would report
success and write nothing. The `TENANT` printer column arrives with it.

## Natural keys

Two candidates, tried in this order:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `rd` | `?rd=65000:10` | `spec.rd` holds a value |
| 2 | `name` with a null `rd` | `?name=<name>&rd=null` | `spec.rd` was **never declared** |

Unlike `dcim.Region`'s pair, these do not come from `meta.constraints` — `ipam.VRF` declares
none. They come from the one column that carries `UNIQUE` and from the fact that the other one
does not.

**Candidate 1 sends no `name` filter at all.** That is deliberate: `name` is not unique, so a
lookup carrying it can match a VRF somebody else owns.

**Candidate 2 pins `rd=null` rather than leaving the filter out.** Candidates are
tried in order and the engine falls through when one matches nothing, so a name-only second
candidate would be reached by a VRF that *does* declare an `rd` whose NetBox object does not
exist yet. It would find an unrelated VRF of the same name, adopt it, and `PATCH` its own `rd`
onto it — silently reparenting every prefix and address keyed on that VRF, which is the worst
outcome available on this kind. The pin makes candidate 2 the identity of a *different*
object: the VRF of this name with no route distinguisher. Same reasoning as a top-level
`dcim.Region` ([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

**The pin does not make `name` unique.** Two RD-less VRFs sharing a name still both match, and
that is reported as `Ready=False, Reason=Conflict` naming both candidate ids, with zero
writes. The operator does not take the first row: adopting the wrong VRF is unrecoverable in a
way that waiting is not.

So: **set `rd`.** It is the only identity this kind has that NetBox actually enforces.

## `deletionPolicy` defaults to `Delete`

`NetBoxVRF` is an IPAM kind holding state rather than configuration, and issue #176 decided
(option B) that those default to **`Retain`**: deleting a VRF destroys the record of which
routing table every prefix and address in it belonged to, and re-creating it does not restore
the changelog. `ipam.VRF`'s foreign keys are `on_delete=PROTECT` anyway, so NetBox refuses
many of these deletions and the operator reports rather than retries them.

There is no CRD marker carrying the default, and there cannot be one: `deletionPolicy` is
declared once on `NetBoxObjectSpec`, the envelope **every** kind embeds, so a
`+kubebuilder:default` there is the same answer for all of them -- which since
[#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) is exactly what is
wanted, and the engine supplies it. So
`kubectl explain netboxvrf.spec.deletionPolicy` prints no default and
[deletion](../concepts/deletion.md#the-two-policies) is where the table lives.
Set `deletionPolicy: Delete` explicitly where `kubectl delete` really should remove the VRF.

[`NetBoxRouteTarget`](netboxroutetarget.md#deletionpolicy-defaults-to-delete) is `Delete`, and
that is not an inconsistency: #176's rationale is irreversible loss of *allocated state*, and
nothing is allocated from a route target.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.naturalKey` is worth reading on this kind in particular: it records which of the two
candidates ran, filter by filter, so `{"name": "...", "rd": "null"}` tells you the
engine treated the object as the RD-less VRF of that name — which is the lookup that can be
ambiguous.

`ipam.VRF` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is stamped
in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the VRF exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun` |
| `RefsResolved` | every route target in both lists resolved, or neither list is set | any element did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefDenied`, `RefTargetFailed` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved=False` forces `Ready=False, Reason=WaitingForRef`, so a withheld many-to-many
cannot pass a readiness check.

## Kind-specific behaviour

### An emptied `rd` is no identity

`rd: ""` asks for the route distinguisher to be cleared. It also leaves the object with no
applicable natural key: candidate 1 has no value to filter on, and candidate 2 asserts the
field was never declared. Neither applies, so the engine reports
`Ready=False, Reason=WaitingForKey` and performs zero writes.

That is the safe outcome, not a gap. The alternative is looking a VRF up by a name that is not
unique and adopting whichever row came first.

To actually remove an `rd` from a VRF the operator already manages: clear it in NetBox, then
drop the field from the manifest so candidate 2 applies.

### Renaming changes identity

`name` participates in candidate 2 and `rd` is candidate 1, so editing either does not rename
the NetBox VRF — it changes what the CR is looking for. The next reconcile finds nothing and
creates a second VRF, leaving the first behind. Rename in NetBox and in the manifest together,
or delete and re-create the CR.

`enforceUnique`, `description`, `comments` and both route-target lists are safe to edit:
none is part of a natural key.

### Reordering a route-target list writes nothing

```console
$ kubectl patch netboxvrf donkerslootstraat --type=merge \
    -p '{"spec":{"importTargets":[{"name":"rt-b"},{"name":"rt-a"}]}}'
```

produces no NetBox request. The ids are sorted before the payload is built, so the payload is
byte-identical, `status.lastAppliedHash` is unchanged and the reconcile short-circuits.

### The relation direction

Every write to VRF ↔ route target goes through this kind. `NetBoxRouteTarget` has no
many-to-many field at all — see
[that page](netboxroutetarget.md#the-relation-is-written-from-the-vrf-side-only) for why a
reverse field would be a `PATCH` war rather than a convenience.

## Printer columns

```
NAME                RD         ID   READY   AGE
donkerslootstraat   65000:10   41   True    3m
loopbacks                           False   3m
```

| Column | JSONPath |
|---|---|
| `RD` | `.spec.rd` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

An empty `RD` next to an empty `ID` is the pair worth having side by side: it is the shape of a
VRF relying on the non-unique fallback key.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `rd` over 21 characters, or `name` empty or over 100 | Both caps are NetBox's own columns |
| `Ready=False`, `Reason=Conflict`, `rd` set | `Ready` | another CR or namespace already owns this `rd` | `rd` is globally unique in NetBox. Pick another, or adopt deliberately with `onConflict: Adopt` |
| `Ready=False`, `Reason=Conflict`, no `rd` | `Ready` | two NetBox VRFs share this `name` and neither has an `rd` | Give this VRF an `rd`; the message names both candidate ids so you can tell them apart |
| `Ready=False`, `Reason=WaitingForKey` | `Ready` | `rd` is declared but empty | See [an emptied `rd` is no identity](#an-emptied-rd-is-no-identity) |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved` | one route target in a list does not exist or is not usable yet | The message names the element, `importTargets[1]`. Create it; its event re-enqueues this VRF |
| Route targets in NetBox are ignored | none | the field is absent from the spec | Absent means "do not manage". Add the list, or `[]` to clear |
| The last route target will not come off | none | removing every element leaves the field absent after `omitempty` | Write `importTargets: []` explicitly. It needs `metadata.managedFields` to be readable — see [field ownership](../concepts/field-ownership.md) |
| A second VRF appeared after an edit | none | `name` or `rd` was changed | See [renaming changes identity](#renaming-changes-identity) |
| `Deleting=False`, `Reason=Protected` | `Deleting` | prefixes, addresses or VLANs still reference this VRF | `ipam` foreign keys are `PROTECT`. Delete the dependents; the retry then succeeds |

## Related

- [`NetBoxRouteTarget`](netboxroutetarget.md) — the other end of both relations
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set, and how `[]` survives `omitempty`
- [References](../concepts/references.md) — the four ref modes and what the API server rejects
- [Lookups](../concepts/lookups.md) — why a null filter is pinned rather than omitted
- [Drift detection](../concepts/drift.md) — the order-independent set compare
- [ADR-0003](../decisions/0003-ownership-and-references.md) — why a many-to-many member takes no owner reference
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
