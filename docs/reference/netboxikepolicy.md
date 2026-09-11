# `NetBoxIKEPolicy`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIKEPolicy` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbikepol` |
| Status subresource | yes |

A `NetBoxIKEPolicy` is one `vpn.IKEPolicy` in NetBox: the phase 1 policy a peer offers, built
from one or more [`NetBoxIKEProposal`](netboxikeproposal.md)s. It is written to
`vpn/ike-policies`.

It is **the one kind in the `vpn` app whose NetBox row can hold a secret**, and the section
below is the one to read before anything else on this page.

## There is no `presharedKey` field, and that is deliberate

`docs/netbox-schema.md` → `vpn.IKEPolicy` declares `preshared_key TextField`, it is writable on
the serializer (`hack/testdata/ir-4.6.8.json.gz` → `vpn.IKEPolicy.write_path`), and this API
exposes **neither `spec.presharedKey` nor `spec.presharedKeySecretRef`**.

**A secret may never be inline in a spec.** A CRD field holding the key would put it in an
object every reader of the namespace can `get`, in every `kubectl get -o yaml`, in whatever git
repository the manifest lives in, and in every etcd backup. So the only permitted shape is
`spec.presharedKeySecretRef` → a key of a `Secret`.

That shape does not exist yet either, and the reason is engine-shaped rather than kind-shaped:
reading a Secret into a NetBox payload field is a capability the engine does not have. There is
no `FieldClass` for it and `internal/reconciler/payload.go` has nowhere to fetch one from. The
mechanism — the field class, a Secret informer scoped to the object's own namespace, and the
RBAC decision that comes with it — is issue **#241**, and it is a change to shared logic, which
[adding a Kind is not allowed to be](../concepts/descriptor.md).

So the column is left **unmapped**, and four things follow from that, all of them observable:

- **The operator never writes it.** There is no `Field` entry for `preshared_key` on the
  descriptor, and an unmapped column cannot reach a payload.
- **The operator therefore never clears it.** Whatever key NetBox holds is untouched on every
  reconcile, including the first one that adopts a hand-made policy. "Not managed" here means
  not written and not compared, not "written empty".
- **It is in no diff and no condition.** An unmapped column is not compared, so it can never
  produce drift, a `Synced=False` or a PATCH loop.
- **It is redacted in logs.** `preshared_key` is in `internal/netbox/do.go`'s redaction set
  regardless of any of the above, because NetBox *returns* it on every read of this endpoint and
  a debug-level response log would otherwise print it.

Set the key in the NetBox UI or by API and leave it there. `spec.version`, `spec.mode` and
`spec.proposals` are managed as usual: a choice column and a relation say nothing secret.

**Adding the field later is purely additive.** When #241 lands, `spec.presharedKeySecretRef`
becomes a new optional field on this spec; an existing manifest that does not set it keeps
today's behaviour exactly — the column stays unmanaged — and no stored object needs rewriting.
Nothing in this page's identity, defaults or conditions changes with it.

The gap is recorded as a gap rather than an excuse, in `hack/coverage-exclusions.yaml`'s `notes`
and in [coverage](../coverage.md), next to the two columns that got there first:
[`ipam.FHRPGroup.auth_key`](netboxfhrpgroup.md) and
[`wireless.WirelessLAN.auth_psk`](netboxwirelesslan.md).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIKEPolicy
metadata:
  name: ike-branch
  namespace: default
spec:
  endpointRef: homelab
  name: IKE branch policy
  proposals:
    - name: ike-aes256-sha256
```

`proposals` is optional in this CRD and required by NetBox on a *create* — see
[`spec.proposals`](#specproposals). A policy applied without it, against a NetBox that holds no
such policy yet, comes back as NetBox's own 400.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIKEPolicy
metadata:
  name: ike-branch
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: IKE branch policy
  version: 2                  # default: IKEv2

  # `mode` is IKEv1 only, so an IKEv2 policy leaves it unset. NetBox's own clean() is the
  # authority on the combination; this schema models no crypto rules.

  proposals:
    - name: ike-aes256-sha256

  # There is deliberately no `presharedKey` and no `presharedKeySecretRef`. See above.
  description: Phase 1 policy for the branch-office tunnels
  comments: |
    The pre-shared key is set in NetBox by hand and is not managed from here.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `version` | `integer` | no | `2` | enum: `1`, `2` | `version PositiveSmallIntegerField def=IKEVersionChoices.VERSION_2` |
| `mode` | `string` | no | — | enum: `""`, `aggressive`, `main` | `mode CharField` |
| `proposals` | list of [`ObjectRef`](../concepts/references.md) → `NetBoxIKEProposal` | no | — | `minItems: 1`, `maxItems: 256`, ref arity CEL per element | `proposals ManyToManyField -> vpn.IKEProposal` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

`preshared_key` is the one writable column on this model with no row in that table. It is not an
omission — see [above](#there-is-no-presharedkey-field-and-that-is-deliberate).

### `spec.name`

Required, 1–100 characters, and this kind's natural key. **Unique across the whole NetBox
install**, so two namespaces cannot both own `IKE branch policy` and the loser gets a
`Conflict`.

### `spec.version`

Which IKE version this policy speaks: `1` or `2`. An **integer** rather than a string, because
the column is a `PositiveSmallIntegerField` — NetBox stores and returns a number and the
operator compares a number. Two members, `netbox/vpn/choices.py:73` at 4.6.8, and closed: the
class declares no `key` (`hack/testdata/ir-4.6.8.json.gz` → `enums.IKEVersionChoices`).

**Defaulted to `2`**, which is NetBox's own default, so the operator manages the field from the
first reconcile: a defaulted field that never reaches a payload is a field the operator can
never correct. NetBox returns it as `{"value":2,"label":"IKEv2"}` and accepts the bare value; the
differ compares the value ([drift](../concepts/drift.md)).

### `spec.mode`

The phase 1 negotiation mode, and **IKEv1 only**. Two members, `netbox/vpn/choices.py:83` at
4.6.8, plus `""` because the column is `blank=True, null=True`. Closed
(`hack/testdata/ir-4.6.8.json.gz` → `enums.IKEModeChoices`).

Undefaulted, and deliberately: IKEv2 has no mode at all, so a policy that names one is an IKEv1
policy, and defaulting it here would assert a negotiation style nobody described. Setting it on
an IKEv2 policy is NetBox's `clean()` to refuse, not this schema's.

Omit it to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)). An emptied value is sent as JSON `null`
rather than `""`, because NetBox returns `null` for an unset choice and the two would differ on
every pass (#170).

### `spec.proposals`

The set of IKE proposals this policy offers, as a list of references to
[`NetBoxIKEProposal`](netboxikeproposal.md).

A to-many reference, and it behaves exactly like
[`NetBoxVRF`](netboxvrf.md#specimporttargets-and-specexporttargets)'s route-target lists: NetBox
replaces a many-to-many **wholesale** on `PATCH` — there is no add or remove verb — so the
listed set *is* the set. The ids are sent sorted ascending and deduplicated, the comparison is an
order-independent set compare ([drift](../concepts/drift.md)), and **reordering the list produces
zero writes**.

**Optional in the CRD and required by NetBox, and the gap is deliberate.** The column is
`proposals ManyToManyField -> vpn.IKEProposal` with no `blank=True`, so NetBox's serializer
refuses to *create* a policy without one. It is still optional here, because a spec omission
means "do not manage this relation" ([field ownership](../concepts/field-ownership.md)) and a
policy the operator adopts should keep the proposals somebody else set. A create with the field
omitted is NetBox's own 400, surfaced as `Ready=False, Reason=Invalid`; a required CRD field
would instead make adoption impossible.

| Spec | Payload | Meaning |
|---|---|---|
| field absent | key omitted | do not manage; NetBox keeps whatever proposals it has |
| `proposals: []` | — | **rejected by the API server**: `minItems: 1` |
| `proposals: [a, b]` | `"proposals": [3, 7]` | manage it, exactly these |

That middle row is the one place this field differs from a VRF's `importTargets`, where `[]` is
a legitimate clear. Here `[]` would ask NetBox to empty a relation it refuses to leave empty, so
the empty list is rejected at `kubectl apply` instead of becoming a 400 on every reconcile.
`minItems: 1` bounds the *declared* list; it does not make the field required.

**All or nothing.** If any element cannot be resolved the whole field is left out of the payload
and the object reports `RefsResolved=False` naming the element by its index —
`proposals[1] -> netboxikeproposal/team-a/ike-missing: not found` — with `Ready=False,
Reason=WaitingForRef` and zero writes. Writing the ones that did resolve would be a full-list
replacement with a shorter list: a deletion, reported as a success
([references](../concepts/references.md#a-list-resolves-whole-or-not-at-all)). When the missing
proposal arrives, its event re-enqueues this policy and the write completes in one pass.

**No owner references.** A many-to-many member is not containment: two policies may offer one
proposal and neither owns it ([ADR-0003](../decisions/0003-ownership-and-references.md) §4). So
deleting this policy does not cascade to its proposals.

`maxItems` is the project standard 256 ([references](../concepts/references.md#a-list-needs-a-bound)):
an `ObjectRef` carries five CEL rules and the API server costs a rule on a list item at the
list's maximum length, so an unbounded list of refs is rejected outright.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`, and both
clearable: omit one to leave NetBox's own value alone, set it to `""` to clear it.

## Natural key

One candidate, no pin:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | the column's own `UNIQUE` |

`vpn.IKEPolicy` declares no `meta.constraints`
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.IKEPolicy.natural_keys`, `[]`), and
`name CharField REQ UNIQUE len=100` is what identifies a policy. The filter is registered:
`name` is in `IKEPolicyFilterSet.Meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:187`).

`proposals` is deliberately not part of the key, and could not be: a natural key filters on
scalars, a many-to-many matches a superset rather than an identity, and the ids are not known
until every element resolves.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

**No status field holds the pre-shared key**, and none can: it is in no payload and in no diff.

`vpn.IKEPolicy` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the policy exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | every element of `proposals` resolved, or the field is unset | any element did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefDenied`, `RefTargetFailed` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved=False` forces `Ready=False, Reason=WaitingForRef`, so a withheld many-to-many
cannot pass a readiness check.

## Kind-specific behaviour

### Adopting a hand-made policy keeps its key

This is the practical shape of the section at the top. Point a CR at a policy somebody created
in the NetBox UI with a pre-shared key set: the lookup matches on `name`, `status.adopted=true`,
the operator PATCHes `version`, `mode` and `proposals` as the spec declares them, and
`preshared_key` is not in the payload — so the key survives the adoption and every reconcile
after it.

### Deleting the policy is refused while a profile points at it

`vpn.IPSecProfile.ike_policy` is `ForeignKey REQ ... on_delete=PROTECT`, so NetBox refuses to
delete a policy a profile still uses and the CR reports `Deleting=False, Reason=Protected`
naming the blocker. Delete the [`NetBoxIPSecProfile`](netboxipsecprofile.md) first.

Deleting the policy CR does **not** delete its proposals: a many-to-many cascades nothing.

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox policy — it changes what the
CR is looking for, and the next reconcile creates a second policy, leaving the first behind.
`version`, `mode`, `proposals`, `description` and `comments` are safe to edit.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). See [deletion](../concepts/deletion.md).

### What is not here yet

- **`preshared_key`**, for the reason this page opens with (#241).
- **`owner`.** `ForeignKey -> users.Owner`, and `users/*` is an excluded endpoint.
- `tags` and `customFields` are written by the provenance stamp and not by a user.

Two of the `vpn` app's ten models are not Kinds at all: `vpn.TunnelTermination` and
`vpn.L2VPNTermination` are deferred, because the identity of each is a generic foreign key
(#59).

## Printer columns

```
$ kubectl get nbikepol
NAME         VERSION   ID   READY   AGE
ike-branch   2         95   True    2m
```

| Column | JSONPath |
|---|---|
| `VERSION` | `.spec.version` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

There is no column for the key, the mode or the proposals. The first cannot be shown, the second
is empty on every IKEv2 policy, and the third is a list.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `spec.presharedKey` rejected by `kubectl apply` | — | There is no such field, and there is no `presharedKeySecretRef` either. | Set the key in NetBox. See [above](#there-is-no-presharedkey-field-and-that-is-deliberate) and #241 |
| The key disappeared from NetBox | — | Not from here. The column is unmapped, so the operator never writes it and never clears it. | Look for another writer; `preshared_key` is redacted in this operator's logs but its writes would still be absent |
| `Ready=False`, `Reason=Invalid`, NetBox message names `proposals` | `Ready` | A create with `proposals` omitted. NetBox refuses a policy with no proposals; the CRD leaves the field optional so adoption stays possible. | Declare `proposals` |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved=False` | One element of `proposals` does not exist or is not usable yet. The message names it by index. | Create the proposal; its event re-enqueues this policy |
| `kubectl apply` rejected, `spec.proposals in body should have at least 1 items` | — | `proposals: []`. NetBox refuses to leave the relation empty, so the empty list is refused here instead. | Remove the field to stop managing the relation |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this name. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner |
| `kubectl apply` rejected on `mode` or `version` | — | A value outside the enum. Both `ChoiceSet`s are closed and cannot be extended. | Use a listed value |
| A second policy appeared after an edit | — | `spec.name` was changed. | See [renaming changes identity](#renaming-changes-identity) |

## Related

- [`NetBoxIKEProposal`](netboxikeproposal.md) — what this policy offers
- [`NetBoxIPSecPolicy`](netboxipsecpolicy.md) — the phase 2 counterpart, and the one without a secret
- [`NetBoxIPSecProfile`](netboxipsecprofile.md) — what binds this policy to one, with `PROTECT`
- [`NetBoxFHRPGroup`](netboxfhrpgroup.md) and [`NetBoxWirelessLAN`](netboxwirelesslan.md) — the other two kinds with a deliberately unmapped secret column
- [`NetBoxVRF`](netboxvrf.md) — the to-many reference this kind's `proposals` is shaped after
- [References](../concepts/references.md) — the four ref modes, and why a list resolves whole
- [The Descriptor](../concepts/descriptor.md) — why a Secret-reading field class is engine surgery
