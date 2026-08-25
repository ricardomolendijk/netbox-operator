# `NetBoxCustomFieldChoiceSet`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCustomFieldChoiceSet` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcfcs` |
| Status subresource | yes |
| Lands with | NBO-059 (M10) |

A `NetBoxCustomFieldChoiceSet` is one `extras.CustomFieldChoiceSet` in NetBox: the list of values
a `select` or `multiselect` custom field may hold. Like
[`NetBoxCustomField`](netboxcustomfield.md) it is schema rather than data, so the convention is
that it lives in the same shared namespace the custom fields pointing at it do. It is also the
first kind with a `JSONField` column, and the reason `registry.ClassJSON` exists — see
[`spec.choiceColors`](#specchoicecolors).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCustomFieldChoiceSet
metadata:
  name: service-tier
  namespace: default
spec:
  endpointRef: homelab
  name: service-tier
  extraChoices:
    - ["gold", "Gold"]
    - ["silver", "Silver"]
```
## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCustomFieldChoiceSet
metadata:
  name: service-tier
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain
  name: service-tier
  description: Service tiers for the platform team
  # `[value, label]` pairs, in the order NetBox should offer them. The order is data.
  extraChoices:
    - ["gold", "Gold"]
    - ["silver", "Silver"]
  # Choice value -> one of NetBox's own colour names.
  choiceColors:
    gold: yellow
    silver: gray
  orderAlphabetically: false
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides; see [`NetBoxTag`](netboxtag.md#spec). `tags` and `customFields` are absent here — see
[what is deliberately absent](#what-is-deliberately-absent).

### `spec.name`

| Type | `string`, 1–100 characters, required |
|---|---|
| NetBox column | `name`, `CharField REQ UNIQUE len=100` |

The natural key. NetBox enforces uniqueness globally while this CRD is namespaced, so two of
these in different namespaces claiming one name are claiming *one* choice set and the second
gets `Ready=False, Reason=Conflict`.

### `spec.description`

| Type | `string`, up to 200 characters, optional |
|---|---|
| NetBox column | `description`, `CharField len=200` |

Omit it to leave NetBox's value alone; `""` clears it. Two different instructions
([field ownership](../concepts/field-ownership.md)).

### `spec.baseChoices`

| Type | `string`, `Enum="";IATA;ISO_3166;UN_LOCODE`, optional |
|---|---|
| NetBox column | `base_choices`, `CharField len=50 choices=CustomFieldChoiceSetBaseChoices` |

One of NetBox's predefined sets, whose members are prepended to `extraChoices`; the three values are
read from `netbox/extras/choices.py:85-95`. `""` is in the enum because the column is `null=True`,
and it is how this API spells "no base set" — the descriptor marks it `EmptyIsNull`, so `""` is
*sent* as JSON `null`, the only value NetBox accepts to clear it (the serializer leaves
`allow_blank` false and `to_internal_value` rejects the empty string).

### `spec.extraChoices`

| Type | `[][]string`, ≤1024 items; each exactly 2 strings of ≤100 characters, optional |
|---|---|
| NetBox column | `extra_choices`, `ChoiceSetField` — an `ArrayField` of `[value, label]` pairs |

A two-element list rather than a struct with `value` and `label`, because that is what the column is
and what the API takes (`netbox/extras/fields.py:17-23`); NetBox ignores a shape it does not
recognise rather than rejecting it, so a friendlier struct would write nothing and report success.
Compared as an **ordered** array (`registry.ClassArray`) — NetBox concatenates the base set and
these in the order given and only re-sorts at read time when `orderAlphabetically` is set, so an
order-independent compare would ignore a reordering you asked for. `[]` clears it, though NetBox
refuses that unless `baseChoices` is set.

### `spec.choiceColors`

| Type | JSON object (`JSONDocument`, unknown fields preserved), optional |
|---|---|
| NetBox column | `choice_colors`, `JSONField def=dict` |

Maps a choice value to the colour NetBox renders it in: `{"active": "green"}`. A JSON document
rather than a `map[string]string`, because the column is a `JSONField` and the operator has to write
whatever NetBox accepts there — the legal values are NetBox's own colour names
(`netbox/extras/choices.py:98-131`). **Compared as a whole document** (`registry.ClassJSON`), not as
a scalar: the scalar rule unwraps an object carrying an `id` or a `value` key — how NetBox renders a
foreign key and a choice on read — so a map keyed on a choice value called `id` would be compared as
that colour against the whole document and `PATCH`ed forever. Omitting differs from `{}`.

### `spec.orderAlphabetically`

| Type | `bool` pointer, optional, default `false` |
|---|---|
| NetBox column | `order_alphabetically` |

Sorts the combined list by value when NetBox reads it back. A pointer with an explicit default
rather than a plain bool: `omitempty` on a plain bool drops a deliberate `false` out of the
payload, so the operator could never turn the flag back off.

## What is deliberately absent

- **`tags` and `customFields`.** The bases are `CloningMixin, ExportTemplatesMixin, OwnerMixin,
  ChangeLoggedModel` — neither mixin. The descriptor states both flags `false` rather than omitting
  them: NetBox ignores a column it does not know rather than rejecting it, so a wrongly-declared
  `tags` would vanish on write and be `PATCH`ed forever.
- **The `clean()` rules.** NetBox refuses a set with neither a base nor extra choices, a duplicate
  value inside `extraChoices`, and a colour keyed on a value no choice declares. All three are left
  to NetBox: a schema enforcing one of three related rules invites you to believe it enforces all of
  them. The `400` arrives as `Ready=False, Reason=Invalid` carrying NetBox's own sentence.
- **`choices_count`.** Read-only on the serializer, and in `ReadOnly` so a later addition cannot
  map a spec field onto it.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate and no null pin: the column carries `UNIQUE`, so the filter identifies at most one
object — no conditional constraint for a second candidate, and no parent to pin to null.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status). **`status.provenance` stays empty, and that is correct:** with
neither mixin there is nowhere to put a stamp, so a managed choice set carries no provenance at all
— the case [provenance](../operations/provenance.md) calls out, and one `NetBoxSweep` never deletes.

## `deletionPolicy` defaults to `Delete`

A choice set is schema, not allocated state; `Retain` is reserved for the IPAM kinds that hold
allocations and this is not one ([deletion](../concepts/deletion.md)). The delete is safe by
construction rather than by policy: `CustomField.choice_set` is `on_delete=PROTECT`
(`netbox/extras/models/customfields.py:236-243`), so NetBox refuses to delete a set any custom
field still uses. That arrives as `Deleting=False, Reason=Protected` and clears itself when the
last custom field goes — which is why this kind has no data-loss guard while
[`NetBoxCustomField`](netboxcustomfield.md) does.

## Printer columns

```console
$ kubectl get nbcfcs
NAME           BASE       ID   READY   AGE
service-tier              41   True    3m
country        ISO_3166   42   True    3m
```

| Column | JSONPath |
|---|---|
| `BASE` | `.spec.baseChoices` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | an `extraChoices` entry has one or three elements | Every entry is exactly `[value, label]` |
| `Ready=False`, `Reason=Invalid`, "must define base or extra choices" | `Ready` | `extraChoices: []` with no `baseChoices` | NetBox's rule, not this API's |
| `Ready=False`, `Reason=Invalid` naming a colour key | `Ready` | an unknown colour, or a key no choice declares | NetBox's colour names, keyed on real choice values |
| `Ready=False`, `Reason=Conflict` | `Ready` | a set with this `name` exists and `onConflict: Fail` | Adopt deliberately with `onConflict: Adopt`; `status.naturalKey` shows what was searched |
| `Deleting=False`, `Reason=Protected` | `Deleting` | a custom field still uses this set (`PROTECT`) | Delete or re-point them; the delete completes on the next pass |
| A custom field reports `RefsResolved=False` naming its choice set | on the *custom field* | this CR is in another namespace with no grant | Add a [`NetBoxRefGrant`](netboxrefgrant.md) here |

## Related

- [`NetBoxCustomField`](netboxcustomfield.md) — what points here, and the kind with the guard
- [Custom fields](../custom-fields.md) — the pair in use, end to end
- [Provenance](../operations/provenance.md) — why a managed object can carry no stamp at all
- [`NetBoxRefGrant`](netboxrefgrant.md), [The Descriptor](../concepts/descriptor.md)
