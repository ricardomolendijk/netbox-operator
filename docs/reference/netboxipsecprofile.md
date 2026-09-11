# `NetBoxIPSecProfile`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPSecProfile` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbipsecprof` |
| Status subresource | yes |

A `NetBoxIPSecProfile` is one `vpn.IPSecProfile` in NetBox: an IKE policy and an IPSec policy
bound together under one name, plus the IPSec protocol to use. It is written to
`vpn/ipsec-profiles`, and it is what [`NetBoxTunnel`](netboxtunnel.md)'s `ipsecProfileRef` points
at.

It is the join of the crypto catalogue and the first kind in the `vpn` app with references the
engine has to resolve before it can write anything.

## Two required references, and both point at kinds that ship

`ike_policy` and `ipsec_policy` are both `ForeignKey REQ ... on_delete=PROTECT`
(`docs/netbox-schema.md` → `vpn.IPSecProfile`), so a profile cannot exist without both.

Applied in any order the graph converges: a profile whose policies do not exist yet reports
`RefsResolved=False` and waits, and **nothing is written until both resolve** — a required
reference left out of the payload would be a create NetBox refuses with a 400
([references](../concepts/references.md)).

Neither is a containment parent. `PROTECT` means nothing on the server side disappears when a
policy does — NetBox refuses the delete instead — so there is nothing for an owner reference to
mirror ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4), and
`registry.ErrContainmentNotCascade` would refuse the declaration at boot.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPSecProfile
metadata:
  name: branch-esp
  namespace: default
spec:
  endpointRef: homelab
  name: Branch ESP
  mode: esp
  ikePolicyRef:
    name: ike-branch
  ipsecPolicyRef:
    name: ipsec-branch
```

Both references need a CR of the target Kind in this namespace, or a
[grant](netboxrefgrant.md) and a `namespace:` to reach one elsewhere. Every field above is
required; there is no smaller legal profile.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPSecProfile
metadata:
  name: branch-esp
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Branch ESP
  mode: esp                   # esp | ah

  ikePolicyRef:
    name: ike-branch
  ipsecPolicyRef:
    name: ipsec-branch

  description: The pair of policies the branch-office tunnels are protected by
  comments: |
    Referenced by every tunnel in the Branch offices group.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `mode` | `string` | yes | — | enum: `esp`, `ah` | `mode CharField REQ` |
| `ikePolicyRef` | [`ObjectRef`](../concepts/references.md) → `NetBoxIKEPolicy` | yes | — | ref arity CEL | `ike_policy ForeignKey REQ -> vpn.IKEPolicy on_delete=PROTECT` |
| `ipsecPolicyRef` | `ObjectRef` → `NetBoxIPSecPolicy` | yes | — | ref arity CEL | `ipsec_policy ForeignKey REQ -> vpn.IPSecPolicy on_delete=PROTECT` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

### `spec.name`

Required, 1–100 characters, and this kind's natural key. **Unique across the whole NetBox
install**, so two namespaces cannot both own `Branch ESP` and the loser gets a `Conflict`.

### `spec.mode`

Required. Which IPSec protocol the profile uses:

- `esp` — Encapsulating Security Payload: encryption and integrity.
- `ah` — Authentication Header: integrity only.

Two members, `netbox/vpn/choices.py:107` at 4.6.8. Closed: the class declares no `key`, so no
deployment's `FIELD_CHOICES` can add a member this enum would reject
(`hack/testdata/ir-4.6.8.json.gz` → `enums.IPSecModeChoices`).

No `""` member, because the column is `REQ` — there is no clearing intent to express. Undefaulted
as well: NetBox declares no Django default, so choosing one here would put a protocol into every
profile the operator adopted.

### `spec.ikePolicyRef` and `spec.ipsecPolicyRef`

The phase 1 policy and the phase 2 policy this profile binds together. Both required, so both
are bare values rather than pointers, and an absent one is rejected by the API server before the
operator sees the object.

**If either is wrong.** `RefsResolved=False` with `RefNotFound`, `RefNotReady`, `RefDenied`,
`RefAmbiguous` or `RefTargetFailed` naming the field, and `Ready=False, Reason=WaitingForRef` —
and **zero NetBox writes**, because the profile cannot be created without both ids. When the
missing policy arrives, its event re-enqueues this profile and the write completes in one pass.

Neither is in the natural key: `name` alone identifies a profile, and filtering on a policy id
would narrow a lookup that already matches at most one row.

A `PROTECT`-refused deletion of either *target* surfaces on that target as
`Deleting=False, Reason=Protected` naming this profile.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second. Both inherited from `PrimaryModel`, and both
clearable: omit one to leave NetBox's own value alone, set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)).

## Natural key

One candidate, no pin:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | the column's own `UNIQUE` |

`vpn.IPSecProfile` declares no `meta.constraints`
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.IPSecProfile.natural_keys`, `[]`), so the identity comes
from `name CharField REQ UNIQUE len=100`. The filter is registered: `name` is in
`IPSecProfileFilterSet.Meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:287`).

**Deliberately not `(ike_policy, ipsec_policy)`.** Nothing makes that pair unique, so two
profiles may legitimately combine the same two policies under different modes, and a key that
matched both would adopt whichever the API returned first.

Because `name` is column-unique, the two references need not resolve for the *lookup* to be
applicable — but nothing is written until they do, so a profile whose policies are missing
reports `WaitingForRef` rather than creating a half-built row.

## `status`

Identical to every other kind — see [`NetBoxTag`](netboxtag.md#status).

`vpn.IPSecProfile` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the profile exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | both policy references resolve | either does not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### No containment parent, in either direction

Both foreign keys are `PROTECT`, so neither qualifies under
[ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 and this Kind takes no owner
reference from anything. Deleting a `NetBoxIKEPolicy` or a `NetBoxIPSecPolicy` that a profile
still uses is refused by NetBox and reported on the *policy* as
`Deleting=False, Reason=Protected`.

The reference pointing at this Kind is `Tunnel.ipsec_profile`, also `PROTECT`, so deleting a
profile a tunnel still uses is refused too.

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox profile — it changes what the
CR is looking for, and the next reconcile creates a second profile, leaving the first behind (and
leaving every tunnel pointing at the first). `mode`, both references, `description` and
`comments` are safe to edit.

Changing a reference is an ordinary managed-field edit: the profile is PATCHed to point at the
other policy. It is not a way to rename the policy.

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
$ kubectl get nbipsecprof
NAME         MODE   ID   READY   AGE
branch-esp   esp    99   True    2m
```

| Column | JSONPath |
|---|---|
| `MODE` | `.spec.mode` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, message names `ikePolicyRef` or `ipsecPolicyRef` | — | The field is required by the schema, because NetBox's column is `REQ`. | Name both policies |
| `kubectl apply` rejected on `mode` | — | A value other than `esp` or `ah`. The `ChoiceSet` is closed. | Use one of the two |
| `Ready=False`, `Reason=WaitingForRef`, nothing in NetBox | `RefsResolved=False`, `RefNotFound` | A referenced policy does not exist. Nothing is written until both resolve. | Create it, or fix the name |
| `RefsResolved=False`, `Reason=RefDenied` | | A cross-namespace reference with no [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. | Add the grant |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this name. | [ADR-0002](../decisions/0002-crd-scoping.md); pick one owner |
| Deleting a policy is refused | on the *policy*: `Deleting=False`, `Reason=Protected` | This profile still points at it — `PROTECT`. | Delete the profile first |
| `Deleting=False`, `Reason=Protected` on the profile | `Deleting` | A tunnel still points at it. | Delete the tunnels first |
| A second profile appeared after an edit | — | `spec.name` was changed. | See [renaming changes identity](#renaming-changes-identity) |

## Related

- [`NetBoxIKEPolicy`](netboxikepolicy.md) and [`NetBoxIPSecPolicy`](netboxipsecpolicy.md) — the two required references
- [`NetBoxTunnel`](netboxtunnel.md) — what points at this profile
- [References](../concepts/references.md) — resolution, waiting, and cross-namespace grants
- [Ownership](../concepts/ownership.md) and [ADR-0003](../decisions/0003-ownership-and-references.md) — why a `PROTECT` foreign key gets no owner reference
