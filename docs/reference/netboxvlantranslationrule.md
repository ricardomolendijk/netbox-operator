# `NetBoxVLANTranslationRule`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVLANTranslationRule` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvtr` |
| Status subresource | yes |

A `NetBoxVLANTranslationRule` is one `ipam.VLANTranslationRule` in NetBox: a single VLAN ID
rewrite inside a [`NetBoxVLANTranslationPolicy`](netboxvlantranslationpolicy.md). Four columns,
all declared on the model itself — `policy`, `local_vid`, `remote_vid`, `description`
(`docs/netbox-schema.md` → `ipam.VLANTranslationRule`). Nothing inherited, and no `comments`:
it is a `NetBoxModel` rather than a `PrimaryModel`.

It can be written two ways, and they are equally supported:

- **Standalone**, as below, with a `policyRef`. Use this when a rule needs its own
  `endpointRef`, its own `deletionPolicy`, or a `description` whose *"leave NetBox's own value
  alone"* state has to be expressible.
- **Inline**, as an entry in the policy's
  [`spec.rules`](netboxvlantranslationpolicy.md#specrules). The policy materialises one of these
  CRs per entry, at `<policy>-<localVID>`.

**This is the first Kind in the operator with two natural-key candidates over *different*
columns**, and that is the thing worth reading here: see
[Natural keys](#natural-keys) and
[Why the second candidate exists](#why-the-second-candidate-exists).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLANTranslationRule
metadata:
  name: dc1-to-dc2-voice
  namespace: default
spec:
  endpointRef: homelab
  policyRef:
    name: dc1-to-dc2
  localVID: 110
  remoteVID: 2110
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLANTranslationRule
metadata:
  name: dc1-to-dc2-voice
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  policyRef:
    name: dc1-to-dc2          # a sibling CR; also id / slug / lookup — but see below on slug

  localVID: 110
  remoteVID: 2110
  description: Voice
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `policyRef` | reference | **yes** | — | `policy`, `ForeignKey REQ -> ipam.VLANTranslationPolicy on_delete=CASCADE` |
| `localVID` | `integer`, 1–4094 | **yes** | — | `local_vid`, `PositiveSmallIntegerField REQ` |
| `remoteVID` | `integer`, 1–4094 | **yes** | — | `remote_vid`, `PositiveSmallIntegerField REQ` |
| `description` | `string`, ≤200 | no | — | `description`, `CharField len=200` |

### `spec.policyRef`

The policy this rule belongs to. Required, because NetBox's column is — and it is half of
*both* identities as well as the containment parent.

`localVID: 100` says nothing on its own, so while the reference is unresolved the operator
cannot tell whether this rule exists. It waits at `RefsResolved=False` rather than adopting a
rule out of somebody else's policy. See [lookups](../concepts/lookups.md).

**It can never be deferred.** `validateDeferred` refuses to defer a field a natural key matches
on, and both candidates match on this one. That is correct rather than a limitation: a deferred
reference is by construction unresolved when the lookup runs.

The target has no `slug` column, so `slug` mode matches nothing and reports `NotFound`. Name
the CR, or use `lookup: {name: "DC1 to DC2"}` for a policy the operator does not manage.

### `spec.localVID`, `spec.remoteVID`

The two sides of the rewrite. Both `1`–`4094`: `0` and `4095` are reserved by 802.1Q and are
rejected at admission rather than arriving as a `400` from NetBox three steps later — the same
bounds [`NetBoxVLAN.spec.vid`](netboxvlan.md#specvid) uses.

Both required, and therefore not pointers: the three states an optional field has do not apply
to a column NetBox will not accept as null.

Each is half of one identity. Editing either is an ordinary `PATCH` of that one column —
`UpdateStrategy` is `Patch` and there is no `RecreateOn` — but it is also a change of *what the
CR is looking for*, so if another rule in the policy already holds the new value the write comes
back as a `409` naming the constraint. See
[Editing a VID is a PATCH, and sometimes a Conflict](#editing-a-vid-is-a-patch-and-sometimes-a-conflict).

### `spec.description`

Free text, declared on the model rather than inherited — unlike almost every other
`description` in this API. Omit it to leave NetBox's own value alone; set it to `""` to clear
the value. See [field ownership](../concepts/field-ownership.md).

The inline form in `spec.rules` cannot express that distinction, which is the one place it is
weaker than this CR: a list entry the parent rewrites wholesale has no way to say *"leave
NetBox's alone"*.

## Natural keys

Two candidates. **Neither is a fallback** — they are two separate constraints, both enforced by
the database at once:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(policy, local_vid)` | `?policy_id=<id>&local_vid=<n>` | `policyRef` resolves |
| 2 | `(policy, remote_vid)` | `?policy_id=<id>&remote_vid=<n>` | `policyRef` resolves |

Both come straight out of the committed schema IR, which records them as unconditional and
usable:

```
models.UniqueConstraint(fields=('policy', 'local_vid'),
    name='%(app_label)s_%(class)s_unique_policy_local_vid')
models.UniqueConstraint(fields=('policy', 'remote_vid'),
    name='%(app_label)s_%(class)s_unique_policy_remote_vid')
```

(`hack/testdata/ir-4.6.8.json.gz` → `ipam.VLANTranslationRule.natural_keys`;
`docs/netbox-schema.md` → `ipam.VLANTranslationRule`, `meta.constraints`.) The filters are all
registered on `VLANTranslationRuleFilterSet` (NetBox 4.6.8,
`netbox/ipam/filtersets.py:1184`).

The order matters and follows NetBox's own `meta.ordering: ('policy', 'local_vid')`:
`local_vid` is the side NetBox treats as primary, so it is tried first.

There is no third candidate and no null pin: all three columns in the two keys are required, so
there is no state in which one is missing and a narrower identity applies.

## Why the second candidate exists

The precedent for a Kind with two candidates is
[`NetBoxWirelessLink`](netboxwirelesslink.md), and the shape is borrowed from it — but the
*reason* is different, and the difference is the whole of this Kind.

There, the two candidates are one constraint and its reverse, because Postgres would store
`(a,b)` and `(b,a)` as two distinct rows; the second candidate stops the operator creating a
duplicate of a link somebody declared the other way round.

Here they are two separate constraints, and the second candidate exists to find a row the first
cannot see:

- **Ordinary case.** The rule is looked up by `(policy_id, local_vid)` and found or created.
- **A rule already occupies this policy's `remote_vid`, under a different `local_vid`.**
  Candidate one finds nothing — no rule has this `local_vid` — and candidate two finds the
  offender. Under the default `onConflict: Fail` that is a `Conflict` naming the other rule with
  nothing written; under `Adopt`, one `PATCH` moves the existing rule's numbering.

Without candidate two, that second case is a `POST`, and NetBox answers it with a `409` on
`unique_policy_remote_vid`. Both endings are correct and neither is silent, which is what makes
this a choice rather than a bug fix: the lookup turns the collision into a `Conflict` the
operator can *name* and, on request, resolve — instead of a server error it can only relay.

That is the ticket's design note, honoured rather than duplicated: the constraint is allowed to
surface, and nothing client-side pre-validates it.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`status.naturalKey` records **which candidate matched**, which is the field to read when a rule
reports `Conflict`: it distinguishes "another rule has this local VID" from "another rule has
this remote VID".

`status.provenance` stays empty. `VLANTranslationRuleSerializer.Meta.fields` is
`('id', 'url', 'display', 'policy', 'local_vid', 'remote_vid', 'description')` (NetBox 4.6.8,
`netbox/ipam/api/serializers_/vlans.py:116`) and lists neither `tags` nor `custom_fields`,
however much the `NetBoxModel` base suggests otherwise — so there is no
[provenance](../operations/provenance.md) stamp on this Kind and adoption is by natural key
alone.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the rule exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | `policyRef` resolves | it does not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefForbidden` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### The policy is the containment parent

`policy` is `on_delete=CASCADE` (`docs/netbox-schema.md` → `ipam.VLANTranslationRule`), which
makes it this Kind's containment parent and its single non-controller owner reference
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

It has to be. NetBox deletes a policy's rules with the policy, so a rule CR that outlived its
row would be recreated by the engine's create-if-absent step and the deletion would silently
undo itself (#203). With the owner reference, deleting the policy CR garbage-collects the rule
CRs and nothing is left behind on either side.

Every other reference in this pair is `PROTECT`, and none of those cascades.

### Editing a VID is a PATCH, and sometimes a Conflict

`UpdateStrategy` is `Patch` with no `RecreateOn`, so changing `remoteVID` sends one `PATCH`
carrying one column. `description` and `policy` are not rewritten, which is what keeps a drift
report about a hand-edited field honest.

But a VID is also half of an identity. If the value you are moving to is already held by
another rule in the same policy, NetBox refuses with a `409` naming the constraint and the CR
reports `Ready=False, Reason=Conflict`. A **swap** — two rules exchanging their local VIDs —
cannot be applied in one step for that reason: move one to a spare VID first.

### A duplicate is reported, not written

Two CRs declaring `(policy, local_vid) = (dc1-to-dc2, 100)` is not a case where the operator
picks its own by the provenance stamp — there is no stamp on this Kind at all — and it is not a
case NetBox's data model requires, the way [`NetBoxIPAddress`](netboxipaddress.md)'s duplicates
are. So there is no `allowDuplicate`: the second CR reports `Conflict`, and one row exists.

### `deletionPolicy` defaults to `Delete`

An IPAM kind whose default is `Delete`, matching its policy (#176, #186). A rewrite is
configuration a manifest recreates, and `Retain` would be incoherent here in any case: NetBox
cascades a policy's rules away with the policy, so retaining one leaves a CR pointing at a row
that no longer exists. See [deletion](../concepts/deletion.md).

### Nothing points at a rule

No column in NetBox references `ipam.VLANTranslationRule`, so a rule is never `PROTECT`-blocked
and there is no `<rule>Ref` alias in the API. Interfaces name the *policy*, not a rule.

### What is not here yet

There is no `tags` and no `customFields` field, and that is not a gap waiting on a ticket: the
columns are not on this serializer's write path at all. There is no `comments` either — the
model is a `NetBoxModel` and has no such column.

## Printer columns

```
$ kubectl get nbvtr
NAME              POLICY       LOCAL   REMOTE   ID    READY   AGE
dc1-to-dc2-100    dc1-to-dc2   100     2100     201   True    4m
dc1-to-dc2-101    dc1-to-dc2   101     2101     202   True    4m
dc1-to-dc2-voice  dc1-to-dc2   110     2110     203   True    2m
```

| Column | JSONPath |
|---|---|
| `POLICY` | `.spec.policyRef.name` |
| `LOCAL` | `.spec.localVID` |
| `REMOTE` | `.spec.remoteVID` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

The first two rows above are materialised children of a policy's `spec.rules`; the third was
written by hand. Nothing in `kubectl get` distinguishes them, on purpose — a materialised child
is an ordinary CR. `kubectl get nbvtr -l netbox.kubeforge.org/owner-uid=<policy uid>` selects
the materialised ones.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `RefsResolved=False`, `Reason=RefNotFound` | `RefsResolved` | The policy named by `policyRef` does not exist, or is in another namespace with no [`NetBoxRefGrant`](netboxrefgrant.md). | Create the policy, or grant the reference. Nothing is written to NetBox while this is false. |
| `Ready=False`, `Reason=Conflict`, `status.naturalKey` shows `local_vid` | `Ready` | Another rule in this policy already has this local VID. | Renumber, or set `onConflict: Adopt` on the CR that should own the row. |
| `Ready=False`, `Reason=Conflict`, `status.naturalKey` shows `remote_vid` | `Ready` | Another rule in this policy already translates onto this remote VID. This is the second constraint, found by the second candidate. | Renumber, or `Adopt` to take the other rule's row over. |
| `Ready=False`, `Reason=Invalid` naming `unique_policy_remote_vid` | `Ready` | The same collision, seen from the server: it appeared between the lookup and the write. | Same fix; the engine retries. |
| A swap of two VIDs will not apply | `Ready` | Both halves collide with each other mid-flight. | Move one rule to an unused VID, let it settle, then move the second. |
| A rule reappeared after `kubectl delete` | — | It is a materialised child of a policy's `spec.rules`. | Remove the entry from the policy instead. |
| `status.provenance` is empty on a `managedBy` endpoint | — | Expected: the serializer accepts neither `tags` nor `custom_fields`. | Nothing to fix. |

## Related

- [`NetBoxVLANTranslationPolicy`](netboxvlantranslationpolicy.md) — the parent, and where `spec.rules` lives
- [`NetBoxWirelessLink`](netboxwirelesslink.md) — the other Kind with two candidates, for a different reason
- [`NetBoxVLAN`](netboxvlan.md) — where the same 1–4094 bounds come from
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Inline children](../concepts/inline-children.md) — how a policy materialises these
- [Deletion](../concepts/deletion.md) — what `CASCADE` does to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
