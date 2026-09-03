# `NetBoxIPSecProposal`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxIPSecProposal` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbipsecprop` |
| Status subresource | yes |

A `NetBoxIPSecProposal` is one `vpn.IPSecProposal` in NetBox: the set of algorithms one peer
offers for the IPSec security association — phase 2. It is written to `vpn/ipsec-proposals`.

It is [`NetBoxIKEProposal`](netboxikeproposal.md)'s counterpart, and reading the two side by side
is the fastest way to see what NetBox means by the two phases. **Nothing here is secret-valued**
(`docs/netbox-schema.md` → `vpn.IPSecProposal`, every column), so unlike
[`NetBoxIKEPolicy`](netboxikepolicy.md) this kind ships complete.

## The one difference from the IKE proposal is nullability

`encryption_algorithm` is `REQ` on `vpn.IKEProposal` and `blank=True, null=True` here, and
`authentication_algorithm` is nullable on both:

| Column | `vpn.IKEProposal` | `vpn.IPSecProposal` |
|---|---|---|
| `encryption_algorithm` | `REQ` — the CRD field rejects `""` | nullable — `""` clears it |
| `authentication_algorithm` | nullable | nullable |
| `authentication_method`, `group` | `REQ` | **not columns on this model** |
| `sa_lifetime` | one column, seconds | two: `sa_lifetime_seconds` and `sa_lifetime_data` |

That is NetBox's asymmetry rather than an oversight: an AH-only association encrypts nothing, and
an AEAD cipher such as `aes-256-gcm` authenticates as it encrypts. Both fields here use the
shared enums' `""` member, and NetBox's own `clean()` is the authority on which combinations are
usable — this schema models no crypto rules.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPSecProposal
metadata:
  name: ipsec-aes256-gcm
  namespace: default
spec:
  endpointRef: homelab
  name: IPSec AES-256-GCM
```

`name` is the only required field: every algorithm column on this model is nullable.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxIPSecProposal
metadata:
  name: ipsec-aes256-gcm
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: IPSec AES-256-GCM
  encryptionAlgorithm: aes-256-gcm

  # No `authenticationAlgorithm`: GCM is an AEAD cipher and supplies its own integrity.

  saLifetimeSeconds: 3600
  saLifetimeData: 4608000     # kilobytes

  description: Phase 2 proposal for the branch-office tunnels
  comments: |
    Rekey on whichever lifetime expires first; both are set.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared envelope
and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref).

| Field | Type | Required | Default | Validation | NetBox column |
|---|---|---|---|---|---|
| `name` | `string` | yes | — | 1–100 | `name CharField REQ UNIQUE len=100` |
| `encryptionAlgorithm` | `string` | no | — | enum: `""`, `aes-128-cbc`, `aes-128-gcm`, `aes-192-cbc`, `aes-192-gcm`, `aes-256-cbc`, `aes-256-gcm`, `3des-cbc`, `des-cbc` | `encryption_algorithm CharField` |
| `authenticationAlgorithm` | `string` | no | — | enum: `""`, `hmac-sha1`, `hmac-sha256`, `hmac-sha384`, `hmac-sha512`, `hmac-md5` | `authentication_algorithm CharField` |
| `saLifetimeSeconds` | `integer` | no | — | 0–2147483647 | `sa_lifetime_seconds PositiveIntegerField` |
| `saLifetimeData` | `integer` | no | — | 0–2147483647 | `sa_lifetime_data PositiveIntegerField` |
| `description` | `string` | no | — | ≤200 | `description (PrimaryModel) CharField len=200` |
| `comments` | `string` | no | — | — | `comments (PrimaryModel) TextField` |

### `spec.name`

Required, 1–100 characters, and this kind's natural key. **Unique across the whole NetBox
install**, so two namespaces cannot both own `IPSec AES-256-GCM` and the loser gets a
`Conflict`.

### `spec.encryptionAlgorithm`

Optional. The phase 2 cipher. Eight members from `netbox/vpn/choices.py:117`
(`EncryptionAlgorithmChoices`), plus `""`.

**Closed**, and that is a fact about the source: a `ChoiceSet` is extensible through a
deployment's `FIELD_CHOICES` only when it declares a `key`
(`netbox/utilities/choices.py:23-35`), and this class declares none
(`hack/testdata/ir-4.6.8.json.gz` → `enums.EncryptionAlgorithmChoices`, `"key": null`).

The same Go type as [`NetBoxIKEProposal`](netboxikeproposal.md#specencryptionalgorithm)'s field,
because it is the same NetBox `ChoiceSet` — one `ChoiceSet` gets exactly one type, so a value
added to one cannot silently widen the other and the two cannot drift apart. What differs is the
nullability, and it is expressed on the field: there is no CEL rule here rejecting `""`.

Omit it to leave NetBox's own value alone; set it to `""` to clear it
([field ownership](../concepts/field-ownership.md)). An emptied value is sent as JSON `null`
rather than `""`, because NetBox returns `null` for an unset choice and a payload of `""` would
differ from the value read back on every pass — a PATCH loop rather than an error
(`registry.Field.EmptyIsNull`, #170).

### `spec.authenticationAlgorithm`

Optional. The phase 2 HMAC. Five members from `netbox/vpn/choices.py:139`, plus `""`, closed,
and cleared as `null` for the reason above.

The mirror case of the field above: an AEAD cipher supplies its own integrity, so a proposal
that names `aes-256-gcm` and no HMAC is an ordinary proposal rather than an incomplete one.

### `spec.saLifetimeSeconds` and `spec.saLifetimeData`

How long a phase 2 security association lives, in seconds, and how much traffic it carries
before it is rekeyed, in kilobytes.

Two **independent** nullable columns: either, both or neither may be set, and the operator writes
what the spec declares without inferring one from the other. Both are pointers, so omitting one
leaves NetBox's value alone rather than clearing it, and `0` is distinguishable from unset.

The upper bound on each is Django's `PositiveIntegerField` ceiling, 2147483647; NetBox declares
no validator of its own on either column, so neither does this schema.

### `spec.description`, `spec.comments`

`MaxLength=200` on the first, none on the second — a `TextField` has no `max_length` to derive
one from. Both inherited from `PrimaryModel`, and both clearable on the three-state terms above.

## Natural key

One candidate, no pin:

| # | Candidate | Query | Backed by |
|---|---|---|---|
| 1 | `name` | `?name=<name>` | the column's own `UNIQUE` |

`vpn.IPSecProposal` declares no `meta.constraints`
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.IPSecProposal.natural_keys`, `[]`), so the identity
comes from `name CharField REQ UNIQUE len=100`. The filter is registered: `name` is in
`IPSecProposalFilterSet.Meta.fields` (NetBox 4.6.8, `netbox/vpn/filtersets.py:221`).

Because the column is unique, more than one match is impossible: a hand-made proposal of this
name is adopted, and a duplicate is NetBox's own 409.

## `status`

Identical to every other kind — see [`NetBoxTag`](netboxtag.md#status).

`vpn.IPSecProposal` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and is
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

`vpn.IPSecPolicy.proposals` is a `ManyToManyField`, and a many-to-many cascades nothing and
`PROTECT`s nothing. This Kind has no containment parent in either direction: the model declares
no foreign key of its own bar `owner`
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

On the cluster side the effect shows up on the *policy*: a
[`NetBoxIPSecPolicy`](netboxipsecpolicy.md#specproposals) whose `proposals` names a CR that no
longer exists withholds the whole list rather than writing a shorter one.

### Renaming changes identity

`name` is the natural key, so editing it does not rename the NetBox proposal — it changes what
the CR is looking for, and the next reconcile creates a second proposal, leaving the first
behind. Every other field is safe to edit.

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A crypto proposal is configuration a manifest
recreates verbatim; nothing is allocated from one. See [deletion](../concepts/deletion.md).

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the `users` app is an excluded endpoint. `tags` and
`customFields` are written by the provenance stamp. Nothing else on this model is writable.

Two of the `vpn` app's ten models are not Kinds at all: `vpn.TunnelTermination` and
`vpn.L2VPNTermination` are deferred, because the identity of each is a generic foreign key
(#59).

## Printer columns

```
$ kubectl get nbipsecprop
NAME               ENCRYPTION    AUTHENTICATION   ID   READY   AGE
ipsec-aes256-gcm   aes-256-gcm                    96   True    2m
ipsec-3des-sha1    3des-cbc      hmac-sha1        97   True    2m
```

| Column | JSONPath |
|---|---|
| `ENCRYPTION` | `.spec.encryptionAlgorithm` |
| `AUTHENTICATION` | `.spec.authenticationAlgorithm` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

An empty `AUTHENTICATION` next to a `-gcm` cipher is the normal shape of an AEAD proposal, not a
missing field.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected on an algorithm | — | A value outside the enum. Both `ChoiceSet`s are closed and no deployment can have extended them. | Check the spelling |
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this name, or one NetBox object matched and `onConflict` is `Fail`. | Pick another name, or `onConflict: Adopt` in the owning namespace |
| `Ready=False`, `Reason=Invalid`, NetBox message about the algorithms | `Ready` | NetBox's `clean()` refused the combination. This schema deliberately models no crypto rules. | Fix the combination |
| A lifetime keeps reappearing in NetBox | `Synced` | The field is absent from the spec, so it is not managed. Absent means "leave NetBox's own value alone". | Declare it, or accept NetBox's |
| A second proposal appeared after an edit | — | `spec.name` was changed. | See [renaming changes identity](#renaming-changes-identity) |

## Related

- [`NetBoxIKEProposal`](netboxikeproposal.md) — the phase 1 counterpart, and the nullability contrast
- [`NetBoxIPSecPolicy`](netboxipsecpolicy.md) — what offers this proposal
- [`NetBoxIPSecProfile`](netboxipsecprofile.md) — where the two policies are bound together
- [`NetBoxTunnel`](netboxtunnel.md) — what ends up using all of it
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
