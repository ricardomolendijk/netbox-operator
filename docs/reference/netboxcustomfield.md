# `NetBoxCustomField`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCustomField` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcf` |
| Status subresource | yes |
| Lands with | NBO-059 (M10) |

A `NetBoxCustomField` is one `extras.CustomField`: a **column added to NetBox's own schema**,
which every other kind's `spec.customFields` can then write into. It is not data about a
network. It is a change to what NetBox will let you store.

This is the one kind where the operator was already a writer before the CRD existed. The
provenance bootstrap creates `k8s_uid`, `k8s_cluster`, `k8s_owner` and
`k8s_allocation_identity` before a `NetBoxEndpoint` reports `Ready`, and keeps their
`object_types` in step with the kinds the running build carries. Two writers for one object is
not a state the engine can make safe, so a CR naming one of those is **refused and never sent**
— see [reserved names](#a-cr-for-a-provenance-definition-is-refused) below and
[docs/custom-fields.md](../custom-fields.md) for the whole argument.

Two other things about this kind are unlike every other:

- **`spec.type` is immutable**, because NetBox refuses to change it on an existing field.
- **Deleting one is blocked by default**, because NetBox performs the delete and destroys the
  field's values on every object that has them.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCustomField
metadata:
  name: service-tier
  namespace: default
spec:
  endpointRef: homelab
  name: service_tier
  objectTypes:
    - virtualization.virtualmachine
```

`metadata.name` and `spec.name` are independent and have to be: `service_tier` is not a legal
Kubernetes object name. The natural key is `spec.name`.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCustomField
metadata:
  name: service-tier
  namespace: default
  annotations:
    # Not set by default. Without it, deleting this CR blocks on DataLossBlocked.
    netbox.kubeforge.org/allow-data-loss: "false"
spec:
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: service_tier
  objectTypes:
    - virtualization.virtualmachine
    - dcim.device

  type: select                # immutable
  choiceSetRef:
    name: service-tier

  label: Service tier
  groupName: Platform
  description: Which support tier this workload is on
  comments: |
    Owned by the platform team. Do not edit in the UI.

  filterLogic: exact          # disabled | loose | exact   (NetBox default: loose)
  uiVisible: always           # always | if-set | hidden
  uiEditable: "no"            # yes | no | hidden          (NetBox default: yes)

  required: false
  unique: false
  isCloneable: false
  searchWeight: 1000
  weight: 100

  default: '"bronze"'         # a JSON *value*: a string default is a quoted JSON string

  # For a numeric or text field rather than a select; shown for completeness.
  validationMinimum: ""
  validationMaximum: ""
  validationRegex: ""
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`customFields`. See [`NetBoxTag`](netboxtag.md#spec).

Setting `spec.customFields` on *this* kind reports `Ready=False, Reason=Invalid`:
`extras.CustomField` has no `custom_field_data` column, and a custom field cannot carry custom
fields.

### `spec.name`

| Type | `string`, 1–50 characters, `^[a-z0-9]+(_[a-z0-9]+)*$` |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ UNIQUE len=50` |

The field's internal name: the key `spec.customFields` on every other kind writes, and this
kind's natural key.

NetBox's own validators are `^[a-z0-9_]+$` applied **case-insensitively**, plus a second one
forbidding a double underscore (`netbox/extras/models/customfields.py:120-138`). The double
underscore is not cosmetic — NetBox's filters are spelled `?cf_<name>__ic=`, so a name
containing `__` produces an unparseable filter.

The pattern here is stricter than NetBox's in one way: **uppercase is rejected**. NetBox allows
`service_Tier`; this CRD does not. The name is a JSON key written by hand in every manifest
that populates the field, and the wrong case is a 400 on every object of every type rather than
a typo anybody can see.

**If it is wrong.** A name outside the pattern is rejected by `kubectl apply`. A name reserved
by the endpoint's `spec.managedBy` is accepted by admission and refused at reconcile with
`Ready=False, Reason=ReservedByOperator`.

### `spec.objectTypes`

| Type | `[]string`, 1–256 items, each ≤100 characters matching `^[a-z_]+\.[a-z0-9_]+$` |
|---|---|
| Required | **yes** |
| NetBox column | `object_types`, `ManyToManyField -> contenttypes.ContentType` |

The NetBox models this field applies to, as Django ContentType strings: `dcim.device`,
`ipam.prefix`. Lowercased and unpunctuated — `dcim.device`, never `dcim.Device`.

Required because NetBox requires it: no `required=False` on the serializer
(`netbox/extras/api/serializers_/customfields.py:45-48`). A field declared for the wrong set
makes every write to a type outside it a 400.

Not references of any cardinality. The values are strings, so the descriptor declares this an
`ObjectTypeList` and the resolver never sees it — a resolver told to resolve one would go
looking for a CR named `dcim.device`, which cannot exist. Compared as an order-independent
string set, because NetBox does not preserve M2M order.

**Not validated against this operator's kind registry, deliberately.** A custom field on
`dcim.device` is a reasonable thing to want in a cluster whose operator cannot yet manage
devices, so a registry check would reject the useful case in order to catch a typo. NetBox
catches the typo instead, and catches more of them: its `ContentTypeField` is scoped to
`ObjectType.objects.with_feature('custom_fields')`, so both a type that does not exist and a
type that exists without supporting custom fields come back as
`Invalid content type: <what you wrote>` (`netbox/netbox/api/fields.py:102-122`).

> **Narrowing this list destroys data.** Removing a type fires
> `handle_cf_object_types_changed` on `post_remove`, which strips the field's stored value from
> every object of the type removed (`netbox/extras/signals.py:23-49`). Nothing guards it —
> the guard is on deletion only — because this is an ordinary PATCH. Treat an edit here the way
> you would treat a `DROP COLUMN`.

Conversely, *adding* a type back-fills `spec.default` onto every object of it
(`populate_initial_data`).

### `spec.type`

| Type | enum: `text`, `longtext`, `integer`, `decimal`, `boolean`, `date`, `datetime`, `url`, `json`, `select`, `multiselect`, `object`, `multiobject` |
|---|---|
| Required | no |
| Default | `text` (NetBox's own) |
| Validation | `self == oldSelf` — **immutable** |
| NetBox column | `type`, `CharField len=50 choices=CustomFieldTypeChoices` |

The kind of data the field holds. Values from `netbox/extras/choices.py:13-43`.

**Immutable, and the CEL rule is the point.** NetBox's serializer rejects a PATCH whose `type`
differs from the stored one — "Changing the type of custom fields is not supported"
(`netbox/extras/api/serializers_/customfields.py:75-79`). Without the rule, editing it in Git
would be a 400 on every reconcile forever, reported as `Reason=Invalid` and fixable only by
putting the old value back. The API server rejects the edit instead, where the message can say
what to do.

Recreating the CR is not a way round it: the natural key is `spec.name`, so a fresh CR adopts
the same NetBox object. Changing a field's type means deleting the NetBox custom field — which
destroys its values.

**If it is wrong.** An edit is rejected by `kubectl apply` with
`type is immutable: NetBox refuses to change the type of an existing custom field`.

### `spec.choiceSetRef`

| Type | [`ObjectRef`](netboxtag.md#objectref) → `NetBoxCustomFieldChoiceSet` |
|---|---|
| Required | no |
| NetBox column | `choice_set`, `ForeignKey -> extras.CustomFieldChoiceSet on_delete=PROTECT` |

Where a `select` or `multiselect` field draws its values from. See
[`NetBoxCustomFieldChoiceSet`](netboxcustomfieldchoiceset.md).

`PROTECT`, so this is **not** a containment parent: no owner reference is set, and deleting the
choice set while a field still uses it is refused by NetBox — the *choice set* reports
`Deleting=False, Reason=Protected` and clears itself when the last field using it goes.

A strict `ObjectRef`, like every reference in this API version. The column is nullable, so
*clearing* it is a state NetBox has and this field cannot express — omitting the reference means
"do not manage the column" ([field ownership](../concepts/field-ownership.md)). `OptionalRef`
exists for exactly that and no kind uses it yet.

### `spec.relatedObjectType`

| Type | `string`, ≤100 characters, `^([a-z_]+\.[a-z0-9_]+)?$` |
|---|---|
| Required | no |
| NetBox column | `related_object_type`, `ForeignKey -> contenttypes.ContentType on_delete=PROTECT` |

The NetBox model an `object` or `multiobject` field points at, as one content-type string.

Singular and a plain value, not an `ObjectTypeList`: the column holds one content type rather
than a set. It still travels as `app_label.model` in both directions, because that is how
`ContentTypeField` renders a ContentType — which is also why it is not a reference.

Omit it to leave NetBox's own value alone; set it to `""` to clear it. `""` is *sent as JSON
null*, which is the only value NetBox accepts to clear it (`required=False, allow_null=True`,
no `allow_blank`).

### `spec.default`, `spec.relatedObjectFilter`, `spec.validationSchema`

| Type | any JSON document (`x-kubernetes-preserve-unknown-fields`) |
|---|---|
| Required | no |
| NetBox column | `default`, `related_object_filter`, `validation_schema` — all `JSONField` |

- **`default`** is the value NetBox fills in for objects that have none. A JSON *value*, so a
  string default is a quoted JSON string: `default: '"bronze"'`. That is NetBox's rule, not
  this operator's (`netbox/extras/models/customfields.py:183-190`). It is the only one of the
  three with no `type: object` constraint, because a default may legitimately be a scalar.
- **`relatedObjectFilter`** narrows the objects an `object` field may point at:
  `{"status": "active"}`.
- **`validationSchema`** is a JSON Schema document every value of a `json` field must satisfy.

All three are compared as **whole documents** rather than as scalars. The scalar rule unwraps a
JSON object carrying an `id` or a `value` key — that is how NetBox renders a foreign key on
read — so `default: {"id": 3}` compared as a scalar would differ from itself forever. See
[field classes](../concepts/descriptor.md#json-is-not-value-and-the-reason-is-unwrapnested).

Omit them to leave NetBox's own value alone; set one to `null` to clear it.

### `spec.validationMinimum`, `spec.validationMaximum`

| Type | `string`, ≤32 characters, `^(-?[0-9]{1,12}(\.[0-9]{1,4})?)?$` |
|---|---|
| Required | no |
| NetBox column | `validation_minimum`, `validation_maximum` — `DecimalField decimal(16,4)` |

Bounds for an `integer` or `decimal` field. **Strings, not numbers**: NetBox renders a decimal
as a JSON string, so `"1.0000"` is what comes back and a float in the spec would compare
unequal to it forever.

Omit to leave alone; set to `""` to clear. `""` is sent as JSON null, because DRF parses the
empty string as a number and rejects it — without that, the cleared state would be admissible
and unwritable ([#170](https://github.com/ricardomolendijk/netbox-operator/issues/170)).

### `spec.validationRegex`

| Type | `string`, ≤500 characters |
|---|---|
| Required | no |
| NetBox column | `validation_regex`, `CharField len=500` |

A regular expression every value of a text field must match. Use `^` and `$` to anchor it;
NetBox does not.

Not compiled here. It is a *Python* regular expression, validated by NetBox with
`validate_regex` (`netbox/extras/models/customfields.py:222-231`); a Go `regexp.Compile` in an
admission rule would reject expressions Python accepts and accept ones it does not.

### `spec.filterLogic`, `spec.uiVisible`, `spec.uiEditable`

| Field | Enum | Default | NetBox column |
|---|---|---|---|
| `filterLogic` | `disabled`, `loose`, `exact` | `loose` | `filter_logic` |
| `uiVisible` | `always`, `if-set`, `hidden` | `always` | `ui_visible` |
| `uiEditable` | `yes`, `no`, `hidden` | `yes` | `ui_editable` |

All three default to NetBox's own defaults, so the operator manages the column from the first
reconcile — a defaulted field that never reaches a payload is a field the operator can never
correct.

`filterLogic: loose` makes `?cf_<name>=<value>` a **substring** match. The operator's own
provenance definitions are created `exact`, because each of them is an identity and a substring
answer to "which object *is* this one" is a different object. This field defaults to `loose`
anyway, because a user's custom field is not necessarily an identity and the CRD's job is to
default the way NetBox does.

`uiEditable: "no"` is the setting for a field a program owns: it stops somebody correcting a
value in the UI that the next reconcile puts straight back. Quote it in YAML — bare `no` is a
boolean.

### `spec.required`, `spec.unique`, `spec.isCloneable`

| Type | `boolean` (pointer, explicitly defaulted) |
|---|---|
| Defaults | all `false`, matching NetBox |

Pointers with explicit defaults rather than plain bools, because `omitempty` on a plain bool
drops a deliberate `false` out of the payload — so the operator could never turn one back off.

`required: true` has a blast radius worth stating: every writer of every type in `objectTypes`
then has to supply the field, this operator included — and this operator only writes the keys a
manifest names, so a required custom field nobody sets is a 400 on every object of that type.
NetBox does not backfill.

### `spec.label`, `spec.groupName`, `spec.description`, `spec.comments`

| Field | Type | NetBox column |
|---|---|---|
| `label` | `string` ≤50 | `label`, `CharField len=50` |
| `groupName` | `string` ≤50 | `group_name`, `CharField len=50` |
| `description` | `string` ≤200 | `description`, `CharField len=200` |
| `comments` | `string` ≤10000 | `comments`, `TextField` |

Presentation. Empty `label` means NetBox derives one from `name`. `groupName` groups fields
under one heading — the operator's own definitions use `Kubernetes`.

Omit any of them to leave NetBox's own value alone; set to `""` to clear
([field ownership](../concepts/field-ownership.md)).

### `spec.searchWeight`, `spec.weight`

| Field | Type | Default | NetBox column |
|---|---|---|---|
| `searchWeight` | `int32`, 0–32767 | `1000` | `search_weight` |
| `weight` | `int32`, 0–32767 | `100` | `weight` |

`searchWeight` is how heavily the field counts in NetBox's global search; **lower is more
important, and `0` excludes it entirely** (`netbox/extras/models/customfields.py:168-176`).
That zero is why it is a pointer: `omitempty` on a plain `int32` would drop it.

`weight` orders the field within its group in NetBox's forms; higher appears lower.

## A CR for a provenance definition is refused

If `spec.name` matches one of the definitions the CR's endpoint bootstraps — the resolved
`uidField`, `clusterField`, `ownerField` or `allocationIdentityField` of its `spec.managedBy` —
the object is refused and **nothing is sent**:

```console
$ kubectl describe nbcf k8s-uid
Status:
  ID:                          # empty: nothing was located, so nothing was adopted
  Conditions:
    Type:     Ready
    Status:   False
    Reason:   ReservedByOperator
    Message:  this netbox object is written by the operator's own provenance bootstrap: name
              "k8s_uid" is configured on netboxendpoint "homelab" as part of spec.managedBy,
              so the operator writes it and this object writes nothing; rename it, or switch
              that provenance field off with the empty string
              (docs/operations/provenance.md)
```

Not created, not adopted, not PATCHed, and no lookup either — `status.id` stays empty, so
deleting the CR afterwards deletes nothing.

Why a refusal rather than a merge: `object_types` on `k8s_uid` is **derived from the descriptor
registry** and widens with every kind the operator gains, while a CR states it as a literal
list. The two disagree on every upgrade, and the loser is not the CR — narrowing
`object_types` strips that field's stored value from every object of the types removed. One
apply of a manifest a release out of date would unstamp a whole class of objects.

It is scoped per endpoint, so all three of these are ordinary objects:

| Endpoint | `NetBoxCustomField` for `k8s_uid` |
|---|---|
| no `spec.managedBy` | ordinary — there is no other writer |
| `uidField: k8s_id` | ordinary — `k8s_id` is the reserved one |
| `uidField: ""` | ordinary — nothing is bootstrapped under that name |

That last row is the supported way to own a provenance field yourself: switch the operator's
off, then declare your own.

It is a reconcile-time refusal rather than admission validation, because which names are
reserved depends on the endpoint the CR points at — a webhook that guessed would reject the
three legitimate cases above.

## Deleting one is blocked by default

`extras.CustomField` is the only Kind whose descriptor declares `DataLossOnDelete`. NetBox
drops the field's values from every object that has them, on a `pre_delete` signal, and no
`PROTECT` stands in the way — the values live in each object's own `custom_field_data` JSON
rather than in rows that could point back at the definition
(`netbox/extras/signals.py:59-68` calling `netbox/extras/models/customfields.py:387-401`). So
the engine's usual answer — send the DELETE, report NetBox's refusal — cannot fire.

```console
Type:     Deleting
Status:   False
Reason:   DataLossBlocked
Message:  deleting netbox extras/custom-fields/42 destroys this field's stored value on every
          object in netbox that has one, and netbox does not refuse it; the finalizer stays
          on. Set the annotation netbox.kubeforge.org/allow-data-loss=true to accept the
          loss, or spec.deletionPolicy: Retain to keep the netbox object
```

The finalizer stays on, which makes it a decision rather than an outage: the CR is still there
and so is the NetBox object.

| Way out | Effect |
|---|---|
| `netbox.kubeforge.org/allow-data-loss: "true"` | The DELETE goes out. The values are gone. |
| `spec.deletionPolicy: Retain` | The finalizer comes off, NetBox is untouched. |
| `netbox.kubeforge.org/skip-finalizer: "true"` | The finalizer comes off, NetBox is untouched, and the CR stops tracking the object at all. |

Only the exact string `"true"` unblocks. See [deletion](../concepts/deletion.md).

## What is deliberately absent

- **`owner`.** `OwnerMixin` declares a `ForeignKey -> users.Owner` and there is no Kind for a
  NetBox owner, so an `ownerRef` would resolve against nothing. Absent on every kind for the
  same reason.
- **`data_type`.** A read-only `SerializerMethodField` derived from `type`
  (`netbox/extras/api/serializers_/customfields.py:56, 82-94`). In `ReadOnly` on the
  descriptor so a later addition cannot map a spec field onto it.
- **`spec.customFields`.** No `custom_field_data` column on this model. Setting it reports
  `Reason=Invalid` rather than being dropped.
- **`spec.tags`.** No `TagsMixin` either, so this kind carries **no provenance stamp at all** —
  the case [provenance](../operations/provenance.md) calls out and `NetBoxSweep` must never
  delete.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate, no null pin, and an *exact* lookup rather than `__ie`: the column carries
`UNIQUE` with no `Lower('name')` constraint, and the CRD already restricts `spec.name` to
lowercase, so there is no case for a case-insensitive filter to reconcile.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

`status.provenance` is always unset: this model carries neither `tags` nor `custom_fields`, so
there is nothing to stamp.

## `deletionPolicy` defaults to `Delete`

A custom field is configuration, and issue #176 gives `Retain` to the IPAM kinds that hold
allocations. This is not one of them — but the `DataLossBlocked` guard above means the default
never *silently* destroys anything. `Retain` is still the right setting for a field whose
values you never want the operator to be able to drop.

## Printer columns

```
NAME           FIELD          TYPE     ID   READY   AGE
service-tier   service_tier   select   42   True    3m
k8s-uid        k8s_uid        text          False   1m
```

| Column | JSONPath |
|---|---|
| `FIELD` | `.spec.name` |
| `TYPE` | `.spec.type` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `spec.name` has uppercase, a double underscore, or characters outside `^[a-z0-9]+(_[a-z0-9]+)*$` | Lowercase, digits, single underscores |
| Rejected by `kubectl apply` on an edit | none — admission | `spec.type` changed | Immutable: NetBox refuses it. Delete the field and recreate it, accepting the data loss |
| Rejected by `kubectl apply` | none — admission | `spec.objectTypes` is empty | NetBox requires at least one type |
| `Ready=False`, `Reason=ReservedByOperator` | `Ready` | `spec.name` is a provenance definition on this endpoint | Rename it, or set that field to `""` on the endpoint's `spec.managedBy` |
| `Ready=False`, `Reason=Invalid`, message `Invalid content type: …` | `Ready` | an `objectTypes` entry names a type NetBox does not have, or one that cannot carry custom fields | Check the spelling: lowercased and unpunctuated, `dcim.device` not `dcim.Device` |
| `Ready=False`, `Reason=Invalid` on a `select` field | `Ready` | no `choiceSetRef`, or the choice set has neither base nor extra choices | NetBox validates the pair; the message names which |
| `Ready=False`, `Reason=Conflict` | `Ready` | a custom field with this `name` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt`; `status.naturalKey` shows what was searched |
| `RefsResolved=False`, `Reason=RefNotFound` naming `choiceSetRef` | `RefsResolved` | the `NetBoxCustomFieldChoiceSet` does not exist yet | It converges when the choice set becomes `Ready`; nothing is written meanwhile |
| `Deleting=False`, `Reason=DataLossBlocked` | `Deleting` | the default | Annotate `netbox.kubeforge.org/allow-data-loss: "true"`, or switch to `deletionPolicy: Retain` |
| Objects of another Kind report `Reason=Invalid` naming a custom-field key | on *those* objects | the `NetBoxCustomField` is not `Ready` yet | It converges within one `resyncPeriod`; apply the field first to skip the wait |
| A field's values disappeared off some objects | none | a type was removed from `objectTypes` | Not recoverable. NetBox strips the values on `post_remove` |

## Related

- [Custom fields](../custom-fields.md) — the two-writers problem in full, and the ordering rule
- [`NetBoxCustomFieldChoiceSet`](netboxcustomfieldchoiceset.md) — the values a `select` field
  may hold
- [Provenance](../operations/provenance.md) — what the operator bootstraps and how to turn it
  off
- [Deletion](../concepts/deletion.md) — where `DataLossBlocked` sits in the deletion sequence
- [The Descriptor](../concepts/descriptor.md) — `ReservedKeySpec`, `DataLossOnDelete` and the
  `JSON` field class
