# `NetBoxSavedFilter`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxSavedFilter` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbsf` |
| Status subresource | yes |
| Lands with | NBO-059 (M10) |

A `NetBoxSavedFilter` is one `extras.SavedFilter` in NetBox: a named set of query parameters
NetBox offers as a one-click filter in its UI.

> ### This is the first kind with **two independently-unique columns**
>
> `name` and `slug` both carry `UNIQUE`, so this kind has two natural-key candidates for a
> reason no earlier kind had: not a conditional constraint, but a *changed* value. `slug` is
> tried first and `name` second, which is what turns editing a slug into a rename rather than a
> duplicate. See [natural keys](#natural-key).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSavedFilter
metadata:
  name: active-vms
  namespace: default
spec:
  endpointRef: homelab
  name: Active virtual machines
  slug: active-vms
  objectTypes:
    - virtualization.virtualmachine
  parameters:
    status:
      - active
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxSavedFilter
metadata:
  name: active-vms
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Active virtual machines
  # The first natural-key candidate. `name` is the second, which is what makes editing
  # this slug a rename rather than a duplicate.
  slug: active-vms
  description: Everything currently running

  objectTypes:
    - virtualization.virtualmachine

  # NetBox query parameters. Values are lists because a query string can repeat a key.
  parameters:
    status:
      - active

  enabled: true
  shared: true
  weight: 10
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides. See [`NetBoxTag`](netboxtag.md#spec). `tags` and `customFields` are absent here;
see [what is deliberately absent](#what-is-deliberately-absent).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ UNIQUE len=100` |

The filter's name as shown in NetBox, and the *second* natural-key candidate. Unique across
NetBox while this CRD is namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)), so two
namespaces claiming this name are claiming one filter.

### `spec.slug`

| Type | `string`, 1–100 characters, `^[-a-zA-Z0-9_]+$` |
|---|---|
| Required | yes |
| NetBox column | `slug`, `SlugField REQ UNIQUE len=100` |

The URL-safe identifier, and the *first* natural-key candidate. Also unique across NetBox.

Two independently-unique columns, and the second candidate is not dead weight: renaming `slug`
in Git while leaving `name` alone finds nothing under the new slug, falls through to `name`,
adopts the existing filter and `PATCH`es the slug — which is a rename. Without the fallback the
engine would try to create a second filter and NetBox would refuse it on the unique `name`,
leaving the object stuck at `Reason=Invalid` forever.

### `spec.objectTypes`

| Type | `[]string`, 1–256 items, each up to 100 characters matching `^[a-z_]+\.[a-z0-9_]+$` |
|---|---|
| Required | yes |
| NetBox column | `object_types`, `ManyToManyField -> contenttypes.ContentType` |

The NetBox models this filter applies to, as Django ContentType strings. Required by NetBox:
the serializer carries no `required=False`.

Unlike the other `object_types` in this app the queryset is **unrestricted** —
`ObjectType.objects.all()` — so any content type NetBox has is accepted here, where
[`NetBoxCustomLink`](netboxcustomlink.md#specobjecttypes) and
[`NetBoxExportTemplate`](netboxexporttemplate.md#specobjecttypes) are both scoped to a feature.

Not references: `app_label.model` strings, declared as a `ClassObjectTypeList` and compared as
an order-independent string set. The resolver never sees them.

### `spec.parameters`

| Type | JSON object (`JSONDocument`, unknown fields preserved) |
|---|---|
| Required | **yes** |
| NetBox column | `parameters`, `JSONField REQ` |

The filter itself: the NetBox query parameters to apply.

```yaml
parameters:
  status: ["active"]
  tenant: ["acme"]
```

Required to be an *object* rather than any JSON value, because NetBox's `clean()` rejects
anything that is not a dictionary of keyword arguments
(`netbox/extras/models/models.py:588-594`). That is expressible in the schema, so it is —
`type: object` here, rather than a `400` you have to go and read.

The values are lists because that is what a query string is: `?status=active&status=reserved`
is one parameter with two values, and NetBox's own UI writes them that way. A bare string works
too; NetBox normalises it.

**Compared as a whole document** (`registry.ClassJSON`), not as a scalar. The scalar rule
unwraps a JSON object carrying an `id` key — and `{"id": ["3"]}` is an ordinary NetBox filter,
so a saved filter on an id would be compared as that id against the whole document and
`PATCH`ed forever.

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description`, `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it. The two are different
instructions ([field ownership](../concepts/field-ownership.md)).

### `spec.enabled` / `spec.shared`

| | `enabled` | `shared` |
|---|---|---|
| Type | `bool` pointer | `bool` pointer |
| Required | no | no |
| Default | `true` | `true` |
| NetBox column | `enabled` | `shared` |

`enabled` offers the filter in NetBox's UI; disabling it retires the filter without losing it.
A pointer with an explicit default rather than a plain bool: `omitempty` on a plain bool drops
a deliberate `false` out of the payload, so the operator could never turn it off again.

`shared` makes the filter visible to every NetBox user rather than only to its owner. `true` is
NetBox's own default and the only useful value here: a filter declared from Git has no `user`,
and an unshared filter with no owner is one nobody can see.

### `spec.weight`

| Type | `int32` pointer, 0–32767 |
|---|---|
| Required | no |
| Default | `100` |
| NetBox column | `weight`, `PositiveSmallIntegerField` |

Orders filters in NetBox's list, lowest first — `meta.ordering: ('weight', 'name')`.

## What is deliberately absent

- **`user`.** A `ForeignKey -> settings.AUTH_USER_MODEL`, and there is no Kind for a NetBox
  user, so a `userRef` would be a field the resolver could not resolve against anything.
  NetBox's own default for an unset `user` is "not owned by anybody", which combined with
  `shared: true` is exactly what a filter declared from Git should be.
- **`tags` and `customFields`.** The bases are `CloningMixin, ExportTemplatesMixin, OwnerMixin,
  ChangeLoggedModel`: neither mixin. Stated in the descriptor rather than omitted, because
  NetBox ignores a column it does not know rather than rejecting it — a wrongly-declared `tags`
  would vanish on write and be `PATCH`ed forever.
- **Validation of the parameter *keys*.** Only "it is an object" is enforced here. Whether
  `status` is a real filter on `virtualization.virtualmachine` is NetBox's question, and its
  answer changes with its version.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `slug` | `?slug=<slug>` |
| 2 | `name` | `?name=<name>` |

Both columns are required, so **both candidates are always applicable** — this is not a
fallback for an unset field, which is what every earlier two-candidate kind used a second
candidate for. It is a fallback for a *changed* one.

`slug` first, so the usual case is one filtered lookup. `name` second, so that editing `slug`
in Git finds nothing under the new value, falls through, adopts the existing filter and
`PATCH`es the slug. That is a rename, and it is the behaviour you want from a GitOps flow.

Editing `name` alone is a plain `PATCH` found by candidate 1. Editing **both** in one commit is
a new identity: nothing matches either candidate, and the engine creates a second filter. Rename
one at a time.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

`status.naturalKey` is worth reading on this kind, because it records *which* of the two
identities the lookup used — a `{"name": "…"}` after a slug edit is the rename path having
fired.

**`status.provenance` stays empty.** With neither `TagsMixin` nor `CustomFieldsMixin` there is
nowhere on the NetBox object to put a stamp, so a managed saved filter carries no provenance at
all — the case [provenance](../operations/provenance.md) calls out, and one `NetBoxSweep` must
never delete.

## `deletionPolicy` defaults to `Delete`

A saved filter is presentation rather than data. Nothing in NetBox depends on one, so deleting
it destroys nothing: no `PROTECT` to trip over, no data-loss guard, and no reason to prefer
`Retain` ([deletion](../concepts/deletion.md)).

To retire a filter without losing it, set `enabled: false` instead.

## Printer columns

```console
$ kubectl get nbsf
NAME             SLUG             ID   READY   AGE
active-vms       active-vms       23   True    5m
retired-prefix   retired-prefix   24   True    5m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `slug` has characters outside `^[-a-zA-Z0-9_]+$` | Slugs are NetBox slugs: lowercase, hyphens, underscores |
| Rejected by `kubectl apply` | none — admission | `parameters` is a list or a string | It has to be an object. NetBox rejects anything else anyway |
| Rejected by `kubectl apply` | none — admission | `parameters` omitted | It is the filter. There is nothing to save without it |
| `Ready=False`, `Reason=Conflict` | `Ready` | a filter with this `slug` or `name` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt`; `status.naturalKey` shows what was searched |
| A second filter appeared after an edit | none | `name` and `slug` were both changed in one commit | Neither candidate matched. Rename one at a time — see [natural keys](#natural-key) |
| The filter is in NetBox but nobody can see it | none | `shared: false` with no `user` | Leave `shared` at its `true` default |
| `Ready=True` but the filter is not offered | none | `enabled: false` | Working as designed |
| The filter returns nothing in NetBox | none | a parameter key or value is not valid for those `objectTypes` | Not validated here. Reproduce it as a URL query in NetBox first |

## Related

- [`NetBoxCustomLink`](netboxcustomlink.md) — the neighbouring UI kind, and a single-candidate
  natural key for contrast
- [`NetBoxExportTemplate`](netboxexporttemplate.md) — the kind whose `name` is *not* unique, and
  what that costs
- [Provenance](../operations/provenance.md) — why a managed object can carry no stamp at all
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
