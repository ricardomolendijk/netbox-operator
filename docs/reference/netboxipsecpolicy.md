# `NetBoxIPSecPolicy`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPSecPolicy` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbipsecpol` |
| Status subresource | yes |

A `NetBoxIPSecPolicy` is one `vpn.IPSecPolicy` in NetBox: the phase 2 policy a peer offers,
built from one or more [`NetBoxIPSecProposal`](netboxipsecproposal.md)s and optionally a
Diffie-Hellman group for perfect forward secrecy. It is written to `vpn/ipsec-policies`.

It is [`NetBoxIKEPolicy`](netboxikepolicy.md)'s counterpart, and **the one without a secret**:
the whole `vpn` app carries exactly one secret-valued column and it is
`vpn.IKEPolicy.preshared_key` (`docs/netbox-schema.md` → the six crypto and tunnel models). So
this kind ships complete while its IKE twin does not — see
[that page](netboxikepolicy.md#there-is-no-presharedkey-field-and-that-is-deliberate) for what
"does not" means in practice.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPSecPolicy
metadata:
  name: ipsec-branch
  namespace: default
spec:
  endpointRef: homelab
  name: IPSec branch policy
  proposals:
    - name: ipsec-aes256-gcm
```

`proposals` is optional in this CRD and required by NetBox on a *create* — see
[`spec.proposals`](#specproposals).

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPSecPolicy
metadata:
  name: ipsec-branch
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: IPSec branch policy

  proposals:
    - name: ipsec-aes256-gcm

  # Perfect forward secrecy, as a Diffie-Hellman group number. Optional: a policy without PFS
  # is an ordinary policy.
  pfsGroup: 14

  description: Phase 2 policy for the branch-office tunnels
  comments: |
    PFS is on; the branch routers all support group 14.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `proposals` | list of [`ObjectRef`](../concepts/references.md) → `NetBoxIPSecProposal` | no | — | `minItems: 1`, `maxItems: 256`, ref arity CEL per element | `proposals ManyToManyField -> vpn.IPSecProposal` |
| `pfsGroup` | `integer` | no | — | enum: `1`, `2`, `5`, `14`–`34` | `pfs_group PositiveSmallIntegerField` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

### `spec.name`

Required, 1–100 characters, and this kind's natural key. **Unique across the whole NetBox
install**, so two namespaces cannot both own `IPSec branch policy` and the loser gets a
`Conflict`.

### `spec.proposals`

The set of IPSec proposals this policy offers, as a list of references to
[`NetBoxIPSecProposal`](netboxipsecproposal.md).

Field for field the same relation as
[`NetBoxIKEPolicy.proposals`](netboxikepolicy.md#specproposals), and it behaves identically:
NetBox replaces a many-to-many **wholesale** on `PATCH`, so the listed set *is* the set; the ids
are sent sorted and deduplicated; the comparison is order-independent, so **reordering the list
produces zero writes** ([drift](../concepts/drift.md)).

**Optional in the CRD and required by NetBox.** The column carries no `blank=True`, so NetBox
refuses to create a policy without a proposal — but a spec omission means "do not manage this
relation" ([field ownership](../concepts/field-ownership.md)), and a required CRD field would
make adopting a policy somebody else's proposals belong to impossible. `minItems: 1` bounds the
*declared* list rather than requiring one: `proposals: []` would ask NetBox to clear a relation
it refuses to leave empty, so the empty list is rejected at `kubectl apply` instead of becoming
a 400 on every reconcile.

**All or nothing.** If any element cannot be resolved the whole field is left out of the payload
and the object reports `RefsResolved=False` naming the element by index, with `Ready=False,
Reason=WaitingForRef` and zero writes
([references](../concepts/references.md#a-list-resolves-whole-or-not-at-all)). Writing the ones
that did resolve would be a full-list replacement with a shorter list — a deletion, reported as
a success.

**No owner references.** A many-to-many member is not containment
([ADR-0003](../decisions/0003-ownership-and-references.md) §4), so deleting this policy does not
cascade to its proposals.

`maxItems` is the project standard 256
([references](../concepts/references.md#a-list-needs-a-bound)).

### `spec.pfsGroup`

The Diffie-Hellman group used for perfect forward secrecy. An **integer**, because the column is
a `PositiveSmallIntegerField`, and the same 24 members as
[`NetBoxIKEProposal.group`](netboxikeproposal.md#specgroup): 1, 2, 5, and 14 through 34. Not a
range — 3, 4 and 6 through 13 are absent — and closed, because `DHGroupChoices` declares no
`key` (`hack/testdata/ir-4.6.8.json.gz` → `enums.DHGroupChoices`).

The same Go type as that field, because it is the same NetBox `ChoiceSet`: one `ChoiceSet` gets
exactly one type, so the two cannot drift apart and a member added to one cannot silently widen
the other.

**Two states rather than three.** The field is a pointer and an integer has no empty member to
stand for "unset": a policy without PFS is an ordinary policy, and `0` is not a DH group.
Omitting the field leaves NetBox's own value alone; setting it explicitly to `null` clears the
column. There is no `pfsGroup: ""` to write, and no `EmptyIsNull` involved — that mechanism
exists for columns whose empty *value* is `""` on the wire and `null` in NetBox, which is a
string problem (#170).

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`. Omit one to
leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural key

One candidate, no pin:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | the column's own `UNIQUE` |

`vpn.IPSecPolicy` declares no `meta.constraints`
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.IPSecPolicy.natural_keys`, `[]`), so the identity comes
from `name CharField REQ UNIQUE len=100`. The filter is registered: `name` is in
`IPSecPolicyFilterSet.Meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:257`).

Neither `proposals` nor `pfs_group` is part of it: a natural key filters on scalars, a
many-to-many matches a superset rather than an identity, and two policies may legitimately share
a PFS group.

## `status`

Identical to every other kind — see [`NetBoxTag`](netboxtag.md#status).

`vpn.IPSecPolicy` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the policy exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | every element of `proposals` resolved, or the field is unset | any element did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefAmbiguous`, `RefDenied`, `RefTargetFailed` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved=False` forces `Ready=False, Reason=WaitingForRef`.

## Kind-specific behaviour

### Deleting the policy is refused while a profile points at it

`vpn.IPSecProfile.ipsec_policy` is `ForeignKey REQ ... on_delete=PROTECT`, so NetBox refuses to
delete a policy a profile still uses and the CR reports `Deleting=False, Reason=Protected`
naming the blocker. Delete the [`NetBoxIPSecProfile`](netboxipsecprofile.md) first.

Deleting the policy CR does **not** delete its proposals: a many-to-many cascades nothing. There
is no containment parent in either direction.

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox policy — it changes what the
CR is looking for, and the next reconcile creates a second policy, leaving the first behind.
`proposals`, `pfsGroup`, `description` and `comments` are safe to edit.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). See [deletion](../concepts/deletion.md).

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the `users` app is an excluded endpoint. `tags` and
`customFields` are written by the provenance stamp. Nothing else on this model is writable.

Two of the `vpn` app's ten models are not Kinds at all: `vpn.TunnelTermination` and
`vpn.L2VPNTermination` are deferred, because the identity of each is a generic foreign key
(#59).

## Printer columns

```
$ kubectl get nbipsecpol
NAME           PFS GROUP   ID   READY   AGE
ipsec-branch   14          98   True    2m
```

| Column | JSONPath |
|---|---|
| `PFS Group` | `.spec.pfsGroup` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

An empty `PFS GROUP` is a policy without perfect forward secrecy, which is a legal policy rather
than an unfinished one.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected on `pfsGroup` | — | The value is not one of 1, 2, 5, 14–34. The set has holes. | Use a listed group |
| `kubectl apply` rejected, `spec.proposals in body should have at least 1 items` | — | `proposals: []`. NetBox refuses to leave the relation empty. | Remove the field to stop managing the relation |
| `Ready=False`, `Reason=Invalid`, NetBox message names `proposals` | `Ready` | A create with `proposals` omitted. | Declare `proposals` |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved=False` | One element of `proposals` does not exist or is not usable yet; the message names it by index. | Create the proposal; its event re-enqueues this policy |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this name. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A profile still points at this policy — `PROTECT`. | Delete the profile first |
| A second policy appeared after an edit | — | `spec.name` was changed. | See [renaming changes identity](#renaming-changes-identity) |

## Related

- [`NetBoxIPSecProposal`](netboxipsecproposal.md) — what this policy offers
- [`NetBoxIKEPolicy`](netboxikepolicy.md) — the phase 1 counterpart, and the kind with the unmapped secret
- [`NetBoxIPSecProfile`](netboxipsecprofile.md) — what binds this policy to an IKE policy, with `PROTECT`
- [`NetBoxVRF`](netboxvrf.md) — the to-many reference this kind's `proposals` is shaped after
- [References](../concepts/references.md) — the four ref modes, and why a list resolves whole
