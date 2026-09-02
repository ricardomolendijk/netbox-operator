# `NetBoxVLANTranslationPolicy`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxVLANTranslationPolicy` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbvtp` |
| Status subresource | yes |

A `NetBoxVLANTranslationPolicy` is one `ipam.VLANTranslationPolicy` in NetBox: a **named table
of VLAN ID rewrites**, applied to an interface as a whole rather than to one VLAN. A frame
tagged `100` on one side of a data-centre interconnect arrives tagged `2100` on the other, and
the policy is where that mapping is written down.

Two things about it are worth reading before the field table, because neither is what the
model's base class suggests.

**It carries no tags and no custom fields.** `ipam.VLANTranslationPolicy` is a `PrimaryModel`,
so the *model* mixes in `TagsMixin` and `CustomFieldsMixin` like every other one — but
`VLANTranslationPolicySerializer.Meta.fields` is written out longhand and lists neither
(NetBox 4.6.8, `netbox/ipam/api/serializers_/vlans.py:123`). So this Kind and its rules are the
only object Kinds beside [`NetBoxFHRPGroupAssignment`](netboxfhrpgroupassignment.md) that carry
**no provenance stamp**: the operator recognises its own policy by natural key and by nothing
else. See [`status`](#status).

**Its identity is hand-declared.** The committed schema IR has `natural_keys: []` for this
model, because that list is built from `meta.constraints` alone and this model declares none.
The uniqueness is one level down, on the column. See
[Natural keys](#natural-keys) for the derivation, which is the part of this Kind most worth
checking against `docs/netbox-schema.md` if you are reviewing it.

Two already-shipped Kinds point at it —
[`NetBoxInterface`](netboxinterface.md#specvlantranslationpolicyref) and
[`NetBoxVMInterface`](netboxvminterface.md#specvlantranslationpolicyref) — both with
`on_delete=PROTECT`, so a policy in use cannot be deleted.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLANTranslationPolicy
metadata:
  name: dc1-to-dc2
  namespace: default
spec:
  endpointRef: homelab
  name: DC1 to DC2
```

A policy with no rules is legal and does nothing: it is a table with no rows.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxVLANTranslationPolicy
metadata:
  name: dc1-to-dc2
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: DC1 to DC2
  description: VLAN numbering translation across the DCI
  comments: |
    Owned by the network team. Do not renumber without a change window.

  # Three NetBoxVLANTranslationRule children, materialised as dc1-to-dc2-100,
  # dc1-to-dc2-101 and dc1-to-dc2-102.
  rules:
    - localVID: 100
      remoteVID: 2100
      description: Management
    - localVID: 101
      remoteVID: 2101
      description: Storage
    - localVID: 102
      remoteVID: 2102
      description: vMotion
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | yes | — | `name`, `CharField REQ UNIQUE len=100` |
| `description` | `string`, ≤200 | no | — | `description` (`PrimaryModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`PrimaryModel`), `TextField` |
| `rules` | list of [rule entries](#specrules), ≤128 | no | — | none — child CRs, not a column |

### `spec.name`

The policy's name, and its natural key. Column-unique in NetBox, which does not partition by
namespace: two namespaces cannot both own `DC1 to DC2`, and the loser reports
`Ready=False, Reason=Conflict` ([ADR-0002](../decisions/0002-crd-scoping.md)).

There is **no `slug` column on this model at all**, unlike every `OrganizationalModel` in the
catalogue, so `name` is not a second-best choice — it is the only unique column there is, and
it is the spelling a [`vlanTranslationPolicyRef`](#references-to-this-kind) uses.

Editing it renames the NetBox policy only if the CR still finds it, which it will not: `name`
*is* the key. See [Renaming changes identity](#renaming-changes-identity).

### `spec.description`, `spec.comments`

Free text, both inherited from `PrimaryModel`. Omit either to leave NetBox's own value alone;
set it to `""` to clear the value. The two are different intents and the operator can tell them
apart — see [field ownership](../concepts/field-ownership.md).

### `spec.rules`

Inline children: each entry materialises one
[`NetBoxVLANTranslationRule`](netboxvlantranslationrule.md) CR, owned by this policy and
garbage-collected with it. See [inline children](../concepts/inline-children.md) for the
mechanism and [ADR-0003](../decisions/0003-ownership-and-references.md) rule 5 for the terms
the sugar is allowed on.

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `localVID` | `integer`, 1–4094 | yes | `local_vid`, `PositiveSmallIntegerField REQ` |
| `remoteVID` | `integer`, 1–4094 | yes | `remote_vid`, `PositiveSmallIntegerField REQ` |
| `description` | `string`, ≤200 | no | `description`, `CharField len=200` |

Nothing is hidden. Each entry becomes a real CR at `<policy>-<localVID>` — `dc1-to-dc2-100` —
which appears in `kubectl get nbvtr`, carries its own conditions and writes its own NetBox row.
`kubectl delete nbvtp dc1-to-dc2` takes all of them with it.

There is **no discriminator** in that name, unlike a virtual machine's `dns-disk-scsi0`:
`rules` is the only inline set on this parent, so it owns the policy's whole namespace of keys.

The list is a **map list keyed on `localVID`**, so two entries with the same local VID are
rejected by the API server. That is the identity of a list entry rather than a copy of NetBox's
constraint — two entries with one key would derive one CR name, which is not a state the
operator can represent. `remoteVID` gets no equivalent, deliberately: see
[Two constraints, and only one of them is mirrored](#two-constraints-and-only-one-of-them-is-mirrored).

Omit `rules` and this policy materialises no children. `[]` is the same statement and prunes
the ones a previous revision declared. A hand-written `NetBoxVLANTranslationRule` that points
at this policy is never materialised, never pruned and never appears in `status.children`.

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | always |

**The derivation, because the IR does not carry it.**
`hack/testdata/ir-4.6.8.json.gz` records `natural_keys: []` for `ipam.VLANTranslationPolicy`.
That is not a statement that the model has no identity: the extractor builds that list from
`meta.constraints` alone, and this model declares none — its `Meta` carries only
`ordering: ('name',)`. The uniqueness is one level down, on the column:

```
name  CharField  REQ UNIQUE len=100
```

(`docs/netbox-schema.md` → `ipam.VLANTranslationPolicy`; the same fact is in the IR as
`fields[name].sql.unique: true`.) `internal/registry/coverage_test.go` says this in general
terms — *"the uniqueness `natural_keys` does not carry … a model whose identity is one UNIQUE
column has an empty one"* — and [`NetBoxInterface`](netboxinterface.md) carries a hand-written
key for the neighbouring reason, its constraint living on an abstract base.

The filter is registered: `name` is in `VLANTranslationPolicyFilterSet`'s `Meta.fields`
(`('id', 'name', 'description')`, NetBox 4.6.8 `netbox/ipam/filtersets.py:1167`), as a
`MultiValueCharFilter` with `lookup_expr: exact`.

`description` is deliberately not a second candidate: a Kind gets one identity, `name` is
required and unique, so a lookup that found nothing means the policy does not exist and should
be created.

Getting this wrong would not be a missing feature — it would silently adopt somebody else's
object, which is the class of defect behind #206 and #216. It is asserted in
`internal/registry/ipam_vlantranslation_test.go`.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions` — plus
`children`, which every inline parent carries. See [`NetBoxTag`](netboxtag.md#status) for what
each field means and when it is cleared.

`status.provenance` stays empty, and that is not a bug. The serializer accepts neither `tags`
nor `custom_fields`, so there is nothing for the [provenance](../operations/provenance.md)
stamp to write and nothing to read back — however a `NetBoxEndpoint`'s
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. The practical consequence is that a
[sweep](../operations/sweeps.md) cannot tell an operator-managed policy from a hand-made one,
and that adoption here is by `name` alone.

`status.children` records one entry per materialised rule: its path (`spec.rules[100]`), its
Kind and its derived name. It is what the pruner reads when `spec.rules` is emptied.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the policy exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `ChildrenReady` | every rule this policy declares is `Ready` | one is not, or a prune was blocked | `AllReady`, `PendingChildren`, `PruneBlocked` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### Two constraints, and only one of them is mirrored

`ipam.VLANTranslationRule` carries **two** unique constraints inside one policy —
`(policy, local_vid)` and `(policy, remote_vid)` (`docs/netbox-schema.md` →
`ipam.VLANTranslationRule`, `meta.constraints`). Both are real and both are enforced.

`spec.rules` keys its list on `localVID`, which happens to make one of them unreachable: the
API server rejects a duplicate local VID before the operator sees it. The other is left alone
on purpose. A policy declaring two rules that translate onto **one remote VID** is admitted
here and refused by NetBox, and the second rule's CR reports
`Ready=False, Reason=Conflict` naming `ipam_vlantranslationrule_unique_policy_remote_vid` while
the first stays `Ready`.

That asymmetry is deliberate. The list key exists because two entries with one key derive one
child CR name; it is not a copy of a database constraint. Duplicating the *second* constraint
in the CRD would be a second statement of a NetBox rule that can change under the operator, and
letting it surface as a `Conflict` is one statement instead of two.

A pair that swaps two VIDs — `100 → 200` and `200 → 100` — satisfies both constraints and is
perfectly legal.

### Deleting the policy takes its rules with it, twice over

`ipam.VLANTranslationRule.policy` is `on_delete=CASCADE`, so NetBox deletes a policy's rules
server-side when the policy goes. The CRs have to go with them, or the engine's
create-if-absent step would recreate rows NetBox deliberately deleted (#203).

Two mechanisms do that, and they are independent:

- **Kubernetes garbage collection.** Each materialised rule carries a controller owner
  reference to this policy with `blockOwnerDeletion`, so `kubectl delete nbvtp dc1-to-dc2`
  removes the rule CRs. That reference comes from the rule Descriptor's `containmentRef`
  ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).
- **Pruning.** Removing an entry from `spec.rules` — without deleting the policy — deletes that
  one child and leaves the rest untouched, matched by the owned-by path annotation.

### `deletionPolicy` defaults to `Delete`

An IPAM kind whose default is `Delete`, which is the [`NetBoxVLANGroup`](netboxvlangroup.md)
carve-out rather than an oversight (#176, #186). The rule that table turns on is whether
deletion destroys *state*: a translation policy is a table of rewrites, recreated verbatim from
the manifest, and deleting one frees no address, no VLAN ID and no range. It belongs with the
configuration kinds. See [deletion](../concepts/deletion.md).

### A policy in use cannot be deleted

Both interface kinds point here with `on_delete=PROTECT`, so NetBox refuses the delete while
any interface names the policy, and the CR reports `Deleting=False, Reason=Protected`. Clear
`vlanTranslationPolicyRef` on those interfaces first, or set `deletionPolicy: Retain`.

### `rules` is never written as a column

The serializer's `rules` field is declared
`VLANTranslationRuleSerializer(many=True, read_only=True)`
(`netbox/ipam/api/serializers_/vlans.py:123`). Writing it would not fail — DRF drops it — which
is precisely why it is in the descriptor's read-only list: a dropped write produces a difference
the next reconcile finds again, and PATCHes forever. The rules are written one at a time
through `ipam/vlan-translation-rules`.

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox policy — it changes what
the CR is looking for, and the next reconcile creates a second policy, leaving the first behind
with whatever interfaces still point at it. `description` and `comments` are safe to edit.

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at.

There is no `tags` and no `customFields` field, and unlike on other kinds that is not a gap
waiting on a ticket: the columns are not on the write path at all.

## References to this kind

`vlanTranslationPolicyRef` resolves against this Kind. It is used by:

| Kind | Field | `on_delete` |
|---|---|---|
| [`NetBoxVLANTranslationRule`](netboxvlantranslationrule.md) | `policyRef` (required) | `CASCADE` |
| [`NetBoxInterface`](netboxinterface.md) | `vlanTranslationPolicyRef` | `PROTECT` |
| [`NetBoxVMInterface`](netboxvminterface.md) | `vlanTranslationPolicyRef` | `PROTECT` |

`slug` mode matches nothing here — the model has no `slug` column — and reports `NotFound`.
Name the CR, or use `lookup: {name: "DC1 to DC2"}` for a policy the operator does not manage.
See [references](../concepts/references.md).

## Printer columns

```
$ kubectl get nbvtp
NAME         NAME          ID   READY   AGE
dc1-to-dc2   DC1 to DC2    91   True    4m
```

| Column | JSONPath |
|---|---|
| `NAME` | `.spec.name` |
| `CHILDREN` | `.status.conditions[?(@.type=="ChildrenReady")].status` (priority 1, `-o wide`) |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this name, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Pick a different name, or set `onConflict: Adopt` in the namespace that should own it. |
| `ChildrenReady=False`, `Reason=PendingChildren` | `ChildrenReady` | One rule is not `Ready` — usually a `remoteVID` colliding inside the policy. | `kubectl get nbvtr -l netbox.kubeforge.org/owner-uid=<policy uid>` and read that rule's conditions. |
| A rule reports `Conflict` naming `unique_policy_remote_vid` | on the rule | Two rules in this policy translate onto the same remote VID. | Renumber one of them; NetBox enforces this and the operator deliberately does not pre-check it. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | An interface still names this policy — both interface columns are `PROTECT`. | Clear `vlanTranslationPolicyRef` on those interfaces, or set `deletionPolicy: Retain`. |
| `status.provenance` is empty on a `managedBy` endpoint | — | Expected: neither serializer accepts `tags` or `custom_fields`. | Nothing to fix. Adoption here is by `name`. |
| A second policy appeared after an edit | — | `spec.name` was changed. | See [Renaming changes identity](#renaming-changes-identity). |

## Related

- [`NetBoxVLANTranslationRule`](netboxvlantranslationrule.md) — one row of this table, and the Kind with two identities
- [`NetBoxInterface`](netboxinterface.md) — one of the two Kinds this unblocks
- [`NetBoxVMInterface`](netboxvminterface.md) — the other
- [`NetBoxVLANGroup`](netboxvlangroup.md) — the other IPAM kind that defaults to `Delete`, for the same reason
- [Inline children](../concepts/inline-children.md) — how `spec.rules` becomes CRs
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` and `CASCADE` each do to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
