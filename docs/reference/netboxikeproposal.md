# `NetBoxIKEProposal`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIKEProposal` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbikeprop` |
| Status subresource | yes |

A `NetBoxIKEProposal` is one `vpn.IKEProposal` in NetBox: the set of algorithms one peer offers
for IKE phase 1 — a cipher, an HMAC, an authentication method and a Diffie-Hellman group. It is
written to `vpn/ike-proposals`.

It is the plainest kind in the `vpn` app: six writable columns, two of them free text, no
reference of any kind, and **no secret**. A proposal names algorithms; the pre-shared key that
`preshared-keys` implies is a column on `vpn.IKEPolicy`, which is where the operator's refusal
to hold a secret inline is documented — see
[`NetBoxIKEPolicy`](netboxikepolicy.md#there-is-no-presharedkey-field-and-that-is-deliberate).

A proposal is offered by a [`NetBoxIKEPolicy`](netboxikepolicy.md), which lists one or more of
them; the policy is bound to a `NetBoxIPSecPolicy` by a
[`NetBoxIPSecProfile`](netboxipsecprofile.md), and that is what a
[`NetBoxTunnel`](netboxtunnel.md) points at.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIKEProposal
metadata:
  name: ike-aes256-sha256
  namespace: default
spec:
  endpointRef: homelab
  name: IKE AES-256 SHA-256
  authenticationMethod: preshared-keys
  encryptionAlgorithm: aes-256-cbc
  group: 14
```

Four required fields, and no reference to wait on: this Kind is always immediately writable.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIKEProposal
metadata:
  name: ike-aes256-sha256
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: IKE AES-256 SHA-256
  authenticationMethod: preshared-keys
  encryptionAlgorithm: aes-256-cbc
  authenticationAlgorithm: hmac-sha256
  group: 14
  saLifetime: 28800

  description: Phase 1 proposal for the branch-office tunnels
  comments: |
    Matches what the branch routers ship with.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `authenticationMethod` | `string` | yes | — | enum: `preshared-keys`, `certificates`, `rsa-signatures`, `dsa-signatures` | `authentication_method CharField REQ` |
| `encryptionAlgorithm` | `string` | yes | — | enum: `aes-128-cbc`, `aes-128-gcm`, `aes-192-cbc`, `aes-192-gcm`, `aes-256-cbc`, `aes-256-gcm`, `3des-cbc`, `des-cbc`; CEL: not `""` | `encryption_algorithm CharField REQ` |
| `authenticationAlgorithm` | `string` | no | — | enum: `""`, `hmac-sha1`, `hmac-sha256`, `hmac-sha384`, `hmac-sha512`, `hmac-md5` | `authentication_algorithm CharField` |
| `group` | `integer` | yes | — | enum: `1`, `2`, `5`, `14`–`34` | `group PositiveSmallIntegerField REQ` |
| `saLifetime` | `integer` | no | — | 0–2147483647 | `sa_lifetime PositiveIntegerField` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

**The combination is not validated here.** Nothing in this repository branches on a cipher or a
DH group: NetBox's own `clean()` is the authority on which cipher, HMAC and group make sense
together, and a CRD rule that guessed would reject configurations a real device accepts.

### `spec.name`

Required, 1–100 characters, and this kind's natural key. **Unique across the whole NetBox
install** (`docs/netbox-schema.md` → `vpn.IKEProposal`, `name CharField REQ UNIQUE len=100`), so
two namespaces cannot both own `IKE AES-256 SHA-256` and the loser gets a `Conflict`
([ADR-0002](../decisions/0002-crd-scoping.md)).

Editing it does not rename the NetBox proposal — see
[renaming changes identity](#renaming-changes-identity).

### `spec.authenticationMethod`

Required. How the two peers prove their identity to each other in phase 1. Four members, read
from `netbox/vpn/choices.py:93` (`AuthenticationMethodChoices`) in the 4.6.8 tree the digest was
taken from.

**A closed enum is safe here, and that is a fact about the source.** A `ChoiceSet` is extensible
through a deployment's `FIELD_CHOICES` only when it declares a `key`
(`netbox/utilities/choices.py:23-35`), and this class declares none
(`hack/testdata/ir-4.6.8.json.gz` → `enums.AuthenticationMethodChoices`, `"key": null`). So no
deployment can add a method this enum would reject.

There is no `""` member: the column is `REQ` with no `blank=True`, so there is no clearing
intent to express.

`preshared-keys` names the *method* and nothing more. The key lives on the policy and the
operator never writes it.

### `spec.encryptionAlgorithm`

Required. The phase 1 cipher. Eight members from `netbox/vpn/choices.py:117`
(`EncryptionAlgorithmChoices`), closed for the reason above
(`hack/testdata/ir-4.6.8.json.gz` → `enums.EncryptionAlgorithmChoices`).

The enum carries `""` as a member and this field rejects it, through a CEL rule rather than a
length marker. The empty member exists because
[`NetBoxIPSecProposal`](netboxipsecproposal.md)'s identically typed field is nullable and this
one is `REQ`: one NetBox `ChoiceSet` gets exactly one Go type, and the nullability difference is
expressed on the field that has it. `encryptionAlgorithm: ""` is rejected by the API server with
*"encryptionAlgorithm is required on an IKE proposal and may not be empty"*.

### `spec.authenticationAlgorithm`

Optional. The phase 1 HMAC. Five members from `netbox/vpn/choices.py:139`, plus `""`, and closed
(`hack/testdata/ir-4.6.8.json.gz` → `enums.AuthenticationAlgorithmChoices`).

Optional because the column is `blank=True, null=True`: an AEAD cipher such as `aes-256-gcm`
authenticates as it encrypts and needs no separate HMAC.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. Those are two different
instructions and the operator tells them apart from `metadata.managedFields`
([field ownership](../concepts/field-ownership.md)). An emptied value is sent as JSON `null`
rather than `""`, because NetBox's serializer returns `null` for an unset choice and a payload of
`""` would differ from the value read back on every pass — a PATCH loop rather than an error
(`registry.Field.EmptyIsNull`, #170).

### `spec.group`

Required. The Diffie-Hellman group number, and an **integer** rather than a string because the
column is a `PositiveSmallIntegerField` (`docs/netbox-schema.md` → `vpn.IKEProposal`,
`group PositiveSmallIntegerField REQ choices=DHGroupChoices`) — NetBox stores and returns a
number and the operator compares a number.

Twenty-four members from `netbox/vpn/choices.py:155` (`DHGroupChoices`): 1, 2, 5, and then 14
through 34. **Not a range** — 3, 4 and 6 through 13 are absent, so a `minimum`/`maximum` pair
would accept group numbers NetBox rejects. Closed, like the rest of the crypto sets
(`hack/testdata/ir-4.6.8.json.gz` → `enums.DHGroupChoices`).

There is no zero value that means "unset": `group: 0` is rejected by the enum rather than treated
as omitted. The same type and the same members appear as
[`NetBoxIPSecPolicy`](netboxipsecpolicy.md#specpfsgroup)'s `pfsGroup`, because it is the same
NetBox `ChoiceSet`.

Nothing about this field relates to [`NetBoxTunnelGroup`](netboxtunnelgroup.md). It is named
`group` because that is the column.

### `spec.saLifetime`

Optional. How long a phase 1 security association lives, in seconds.

A pointer, so omitting it leaves NetBox's value alone rather than clearing it, and so that `0` —
which NetBox accepts as a `PositiveIntegerField` — is distinguishable from unset. The upper
bound is Django's `PositiveIntegerField` ceiling, 2147483647; NetBox declares no validator of its
own on the column, so neither does this schema.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second — a `TextField` has no `max_length` to derive
one from. Both inherited from `PrimaryModel`, and both clearable on the three-state terms
[`spec.authenticationAlgorithm`](#specauthenticationalgorithm) describes.

## Natural key

One candidate, no pin:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | the column's own `UNIQUE` |

`vpn.IKEProposal` declares **no `meta.constraints` at all**
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.IKEProposal.natural_keys`, `[]`), so the identity comes
from the one column that carries a `UNIQUE`: `name CharField REQ UNIQUE len=100`. The
[`NetBoxRouteTarget`](netboxroutetarget.md) derivation.

The filter is registered: `name` is in `IKEProposalFilterSet.Meta.fields` (NetBox 4.6.8,
`netbox/vpn/filtersets.py:143`).

Because the column is unique, more than one match is impossible: a hand-made proposal of this
name is **adopted**, and a duplicate is NetBox's own 409 rather than a silent second row.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

`vpn.IKEProposal` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.
See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the proposal exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

## Kind-specific behaviour

### Nothing protects a proposal a policy is offering

`vpn.IKEPolicy.proposals` is a `ManyToManyField`, and a many-to-many cascades nothing and
`PROTECT`s nothing: deleting a proposal removes it from every policy that listed it rather than
being refused. This Kind has no containment parent in either direction — the model declares no
foreign key of its own bar `owner`
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

On the cluster side the effect is visible on the *policy*: a `NetBoxIKEPolicy` whose
`proposals` names a `NetBoxIKEProposal` CR that no longer exists reports
`RefsResolved=False, Reason=RefNotFound` and withholds the whole list rather than writing a
shorter one — see
[`NetBoxIKEPolicy`](netboxikepolicy.md#specproposals).

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox proposal — it changes what
the CR is looking for, and the next reconcile creates a second proposal, leaving the first
behind (and leaving every policy pointing at the first). Rename in NetBox and in the manifest
together, or delete and re-create the CR.

Every other field is safe to edit.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A crypto proposal is configuration a manifest
recreates verbatim; nothing is allocated from one. See [deletion](../concepts/deletion.md).

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at. `tags` and `customFields` are
written by the provenance stamp and not by a user. Nothing else on this model is writable.

Two of the `vpn` app's ten models are not Kinds at all: `vpn.TunnelTermination` and
`vpn.L2VPNTermination` are deferred, because the identity of each is a generic foreign key
(#59). Neither touches this Kind.

## Printer columns

```
$ kubectl get nbikeprop
NAME                ENCRYPTION    GROUP   ID   READY   AGE
ike-aes256-sha256   aes-256-cbc   14      94   True    2m
```

| Column | JSONPath |
|---|---|
| `ENCRYPTION` | `.spec.encryptionAlgorithm` |
| `GROUP` | `.spec.group` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

Both read the *spec*, so they show the intent even while `ID` is empty.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected on `group` | — | The value is not one of 1, 2, 5, 14–34. The set has holes; 6 through 13 are not members. | Use a listed group. |
| `kubectl apply` rejected, *"encryptionAlgorithm is required on an IKE proposal"* | — | `encryptionAlgorithm: ""`. The empty member belongs to the IPSec proposal, whose column is nullable. | Name a cipher. |
| `kubectl apply` rejected on `authenticationMethod` | — | A value outside the four. The `ChoiceSet` declares no `key`, so no deployment can have added one. | Check the spelling. |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this name, or one NetBox object matched and `onConflict` is `Fail`. | Pick another name, or `onConflict: Adopt` in the owning namespace. |
| `Ready=False`, `Reason=Invalid`, message from NetBox about the algorithm combination | `Ready` | NetBox's `clean()` refused the cipher, HMAC and group together. This schema deliberately models no crypto rules. | Fix the combination. |
| The pre-shared key never appears in NetBox | none; `Ready=True` | There is no key on this Kind. It is a column on `vpn.IKEPolicy` and the operator never writes it. | See [`NetBoxIKEPolicy`](netboxikepolicy.md#there-is-no-presharedkey-field-and-that-is-deliberate). |
| A second proposal appeared after an edit | — | `spec.name` was changed. | See [renaming changes identity](#renaming-changes-identity). |

## Related

- [`NetBoxIKEPolicy`](netboxikepolicy.md) — what offers this proposal, and the kind with the key
- [`NetBoxIPSecProposal`](netboxipsecproposal.md) — the phase 2 counterpart, and the one column that differs
- [`NetBoxIPSecProfile`](netboxipsecprofile.md) — where the two policies are bound together
- [`NetBoxTunnel`](netboxtunnel.md) — what ends up using all of it
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
