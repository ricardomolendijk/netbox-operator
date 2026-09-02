# Custom fields: two writers, one object

NetBox custom fields are the one place where this operator is both the tool you are configuring
and a user of the thing you are configuring. It creates four of them for itself before it will
touch anything else, and `NetBoxCustomField` lets you create your own. That is two writers for
one class of NetBox object, and the interesting question is not how the CRD works — it is what
happens where they meet.

The short version:

| You write | The operator does |
|---|---|
| A `NetBoxCustomField` for one of your own fields | Creates it, keeps it in sync, blocks a delete until you say the loss is acceptable |
| A `NetBoxCustomField` for `k8s_uid` (or any name the endpoint's `spec.managedBy` configures) | **Nothing at all.** `Ready=False, Reason=ReservedByOperator` |
| Nothing, and rely on the operator's own fields | Bootstraps them, derives their `object_types` from the kinds this build carries, and widens that list on upgrade |

## What the operator already writes

Before a `NetBoxEndpoint` reports `Ready`, its controller bootstraps the provenance schema:
the `k8s-managed` tag, and the `k8s_uid`, `k8s_cluster`, `k8s_owner` and
`k8s_allocation_identity` custom fields. It looks each one up first and creates only what is
absent, so re-running changes nothing. See [provenance](operations/provenance.md) for the
whole sequence and for how to turn it off.

Two things about those four fields matter here.

**Their `object_types` is derived, not written down.** `extras.CustomField.object_types` is a
required column, and the operator fills it from its own descriptor registry: every registered
Kind whose NetBox model mixes in `CustomFieldsMixin` contributes its `app_label.model` string.
That list **grows on every release that adds a Kind**, and the bootstrap PATCHes the existing
definition to cover the new types on the next reconcile. A hand-maintained list would be
correct exactly until the next Kind landed, and the failure would surface as a hundred
identical 400s on that Kind and nowhere else.

**Everything depends on them.** `k8s_uid` is how the operator recognises objects it owns:
`NetBoxSweep` keys on it, multi-writer detection reads it, and an IP claim refuses to allocate
without `k8s_allocation_identity`. They are not decoration on a few objects, they are the
identity layer under all of them.

## Why a CR for `k8s_uid` is refused rather than honoured

Suppose a `NetBoxCustomField` named `k8s_uid` were an ordinary CR. It would adopt the
definition the bootstrap made — same name, same natural key — and from then on it would be the
writer. Three things follow, and none of them is recoverable by fixing the CR afterwards.

1. **`objectTypes` on a CR is a static list.** The bootstrap derives its list; a manifest states
   one. The two disagree the moment a release adds a Kind, and after that the CR PATCHes its
   list back over the derived one on every resync while the bootstrap PATCHes the derived one
   back over the CR's. Whichever runs last wins until the other runs.

2. **Narrowing `object_types` destroys data.** This is not a NetBox warning you can click
   through. Removing a type from a custom field fires a signal that strips the field's stored
   value from every object of that type:
   `handle_cf_object_types_changed` on `post_remove` calls
   `remove_stale_data`, which issues `custom_field_data = custom_field_data - <name>` over the
   lot (`netbox/extras/signals.py:23-49`,
   `netbox/extras/models/customfields.py:387-401`). So one apply of a manifest whose
   `objectTypes` is a release behind silently unstamps every object of the types it omits.

3. **Deleting it takes provenance away from the whole cluster.** Delete the CR and, with the
   default `deletionPolicy`, the operator deletes the NetBox object — and NetBox strips
   `k8s_uid` off every object that had one (`netbox/extras/signals.py:59-68`). Nothing in
   NetBox then says the operator owns anything. `NetBoxSweep` finds no managed objects,
   multi-writer detection goes blind, and claims stop allocating.

There is no version of "be a careful second writer" that avoids these. Merging is not
available — a CR's spec is the whole desired state for the columns it declares, so there is no
partial intent to merge. Silently dropping `objectTypes` from the payload would be worse: the
object would report itself `Synced` while NetBox held something else. Reporting a conflict and
then writing anyway is the same outcome with a condition attached.

So the operator refuses. A CR naming a reserved field is reported and **never sent**:

```console
$ kubectl describe nbcf k8s-uid
Status:
  ID:                          # empty -- nothing was located and nothing was adopted
  Conditions:
    Type:     Ready
    Status:   False
    Reason:   ReservedByOperator
    Message:  this netbox object is written by the operator's own provenance bootstrap:
              name "k8s_uid" is configured on netboxendpoint "homelab" as part of
              spec.managedBy, so the operator writes it and this object writes nothing;
              rename it, or switch that provenance field off with the empty string
              (docs/operations/provenance.md)
```

`status.id` stays empty, which is the load-bearing part: the operator never looked the object
up, so it cannot have taken it over. Deleting the CR afterwards deletes nothing in NetBox —
with no `status.id` there is nothing it can prove it owns.

### It is per endpoint, not a blocklist

The reserved names are the *resolved* `spec.managedBy` of the endpoint the CR points at, so:

- An endpoint with no `spec.managedBy` reserves nothing. A `NetBoxCustomField` for `k8s_uid`
  against it is an ordinary object, because there is no second writer.
- An endpoint that set `uidField: k8s_id` reserves `k8s_id` and leaves `k8s_uid` free.
- An endpoint that set `uidField: ""` bootstraps nothing under that name, so nothing is
  reserved — that is the supported way to take a provenance field over yourself: switch the
  operator's off, then declare your own.

The same mechanism covers [`NetBoxTag`](reference/netboxtag.md) and its `slug`, because the
bootstrap creates the `k8s-managed` tag too. A `NetBoxTag` claiming that slug was a hole from
the day both shipped.

### What this is not

It is not admission validation. Which names are reserved depends on the endpoint the CR points
at, and admission cannot see that without reading another object; a webhook that guessed would
reject the legitimate cases in the list above. It is a reconcile-time refusal, so `kubectl
apply` succeeds and the CR reports why nothing happened.

## Deleting a custom field destroys data

For the fields you *do* own, the CRD's other unusual behaviour: **the finalizer refuses to
delete by default.**

NetBox drops a custom field's values from every object that has them, irreversibly, and no
`PROTECT` stands in the way — the values live in each object's own `custom_field_data` JSON
rather than in rows that could point back at the definition. So the engine's usual safety net,
issuing the DELETE and reporting NetBox's refusal, cannot fire: there is nothing to refuse.

```console
$ kubectl delete nbcf service-tier
customfield.netbox.kubeforge.org "service-tier" deleted     # blocks

$ kubectl describe nbcf service-tier
Status:
  Conditions:
    Type:     Deleting
    Status:   False
    Reason:   DataLossBlocked
    Message:  deleting netbox extras/custom-fields/42 destroys this field's stored value on
              every object in netbox that has one, and netbox does not refuse it; the
              finalizer stays on. Set the annotation
              netbox.kubeforge.org/allow-data-loss=true to accept the loss, or
              spec.deletionPolicy: Retain to keep the netbox object
              (docs/concepts/deletion.md)
```

Two ways out, and they are not the same:

```yaml
metadata:
  annotations:
    netbox.kubeforge.org/allow-data-loss: "true"   # delete it, values and all
```

```yaml
spec:
  deletionPolicy: Retain                            # forget it, leave NetBox alone
```

Only the exact string `"true"` unblocks. Anything else — the annotation being absent
included — blocks, so a typo is safe in the direction that keeps the data.

Note what is *not* guarded: **narrowing `objectTypes` on a field you own**, which destroys data
by the same mechanism and is an ordinary PATCH. The guard is on deletion because deletion is
where the operator is the one issuing the destructive call. Treat an edit to `objectTypes` the
way you would treat a `DROP COLUMN`.

## Applying a field and the objects that use it, in one go

`spec.customFields` on any Kind writes keys into NetBox's custom-field storage, and NetBox
rejects an unknown key with a 400. So a custom field has to exist before any object that
populates it reconciles.

It also rejects a value of the wrong shape, so the value's JSON type has to match the
field's `type`: `chef_managed: true` on a `boolean`, `extra_disk_1: 500` on an `integer`,
`rack_position: "12"` on a `text`. See [the value's type is
yours to state](concepts/field-ownership.md#the-values-type-is-yours-to-state).

Nothing in this release models that dependency. `spec.customFields` is a map whose keys are
opaque strings rather than references, so there is no edge from the consumer to the
`NetBoxCustomField` that defines the key and nothing to re-enqueue on. What happens today:

- The object reports `Ready=False, Reason=Invalid`, carrying NetBox's own sentence, which names
  the key.
- It retries at the endpoint's `resyncPeriod`. Once the `NetBoxCustomField` is `Ready`, the next
  pass succeeds.

So a `kubectl apply -f` of a directory converges without intervention; it just spends up to one
resync period doing it. Applying the `NetBoxCustomField` first avoids the wait entirely, and is
what [`docs/examples/extras.yaml`](examples/extras.yaml) does.

NBO-059's spec proposes two mechanisms to close the gap — `spec.requiresCustomFields` on the
common envelope as a modelled edge, and a `CustomFieldMissing` reason plus a generation counter
on the endpoint so a blocked object retries the moment a field becomes `Ready`. Neither is in
this change: both are edits to the shared envelope and to every controller's watches, and the
convergence they buy is already there. What they buy is *latency*, and the honest place to
decide that is with a number from a real cluster.

## The Kinds

| Kind | What it is |
|---|---|
| [`NetBoxCustomField`](reference/netboxcustomfield.md) | The field itself |
| [`NetBoxCustomFieldChoiceSet`](reference/netboxcustomfieldchoiceset.md) | The legal values for a `select` or `multiselect` field |

## Related

- [Provenance](operations/provenance.md) — what the operator writes into NetBox, and how to
  stop it
- [Deletion](concepts/deletion.md) — `deletionPolicy`, the finalizer, and the data-loss guard
- [Field ownership](concepts/field-ownership.md) — absent, empty and set are three different
  instructions
- [`docs/examples/extras.yaml`](examples/extras.yaml) — a runnable set of `extras` objects
