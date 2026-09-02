# `NetBoxCustomLink`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCustomLink` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcl` |
| Status subresource | yes |

A `NetBoxCustomLink` is one `extras.CustomLink` in NetBox: a button NetBox renders on an
object's page, whose text and URL are Jinja2 templates rendered with that object as context.

The Descriptor is the same shape as [`NetBoxTag`](netboxtag.md)'s — one unique scalar key, no
foreign keys, one content-type list. What makes it worth having is that it is a NetBox **UI**
object rather than a network one: the link from a VM's page to its Grafana dashboard is
configuration too, and it belongs in Git with everything else.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCustomLink
metadata:
  name: grafana
  namespace: default
spec:
  endpointRef: homelab
  name: Grafana dashboard
  objectTypes:
    - virtualization.virtualmachine
  linkText: "Grafana: {{ object.name }}"
  linkUrl: "https://grafana.example.com/d/vm?var-host={{ object.name }}"
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCustomLink
metadata:
  name: grafana
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  # The natural key; unique across NetBox.
  name: Grafana dashboard

  # Django ContentType strings, not references.
  objectTypes:
    - virtualization.virtualmachine

  # Jinja2, rendered with the NetBox object as `object`.
  linkText: "Grafana: {{ object.name }}"
  linkUrl: "https://grafana.example.com/d/vm?var-host={{ object.name }}"

  groupName: Observability
  buttonClass: blue
  enabled: true
  newWindow: true
  weight: 100
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

The natural key. Column-unique in NetBox, so one candidate identifies at most one link — and,
since the CRD is namespaced and NetBox's constraint is not, two namespaces claiming this name
are claiming one link and the second gets `Ready=False, Reason=Conflict`.

### `spec.objectTypes`

| Type | `[]string`, 1–256 items, each up to 100 characters matching `^[a-z_]+\.[a-z0-9_]+$` |
|---|---|
| Required | yes |
| NetBox column | `object_types`, `ManyToManyField -> contenttypes.ContentType` |

The NetBox models this link appears on, as Django ContentType strings — `dcim.device`,
`virtualization.virtualmachine`. Required by NetBox: the serializer carries no
`required=False`.

**Not references.** The values are `app_label.model` strings, so the descriptor declares this
a `ClassObjectTypeList` and the resolver never sees it. It is compared as an order-independent
string set.

Not checked against this operator's kind registry, deliberately: NetBox's own `ContentTypeField`
is scoped to `ObjectType.objects.with_feature('custom_links')`, so a type that does not exist
and a type that cannot carry links both come back as `Invalid content type` and arrive as
`Reason=Invalid`. A registry check would reject a link on `dcim.device` in a cluster whose
operator cannot manage devices, which is a perfectly reasonable thing to want.

### `spec.linkText`

| Type | `string`, 1–10000 characters |
|---|---|
| Required | yes |
| NetBox column | `link_text`, `TextField REQ` |

Jinja2 template code for the button's label: `Open {{ object.name }} in Grafana`. Required, and
required by NetBox — the column has no `blank=True`. A template that renders to the empty string
hides the button, which is NetBox's documented way to make a link conditional.

### `spec.linkUrl`

| Type | `string`, 1–10000 characters |
|---|---|
| Required | yes |
| NetBox column | `link_url`, `TextField REQ` |

Jinja2 template code for the button's target. Not validated as a URL here: it is a template, so
before rendering it is frequently not one.

`linkUrl` → `link_url` and `linkText` → `link_text` are the entries that earn the Descriptor's
explicit field map. No camelCase convention gets `linkUrl` to `link_url` and `buttonClass` to
`button_class` without an acronym list, and NetBox ignores a field name it does not know rather
than rejecting it — so a wrong one writes nothing and reports success.

### `spec.groupName`

| Type | `string`, up to 50 characters |
|---|---|
| Required | no |
| NetBox column | `group_name`, `CharField len=50` |

Collapses links sharing it into one dropdown menu. Omit it to leave NetBox's own value alone;
set it to `""` to clear it ([field ownership](../concepts/field-ownership.md)).

### `spec.buttonClass`

| Type | `string`, `Enum=default;blue;indigo;purple;pink;red;orange;yellow;green;teal;cyan;gray;black;white;ghost-dark` |
|---|---|
| Required | no |
| Default | `default` |
| NetBox column | `button_class`, `CharField len=30 choices=CustomLinkButtonClassChoices` |

The button's colour; the first link in a group decides the dropdown's. The choice class
inherits from `ButtonColorChoices` and adds one member of its own, so the values come from two
files: thirteen from `netbox/netbox/choices.py:85-117` plus `ghost-dark` from
`netbox/extras/choices.py:135-142`. `grey` is deliberately absent — it is a Python alias for
`gray` on the same value, not a distinct choice.

### `spec.enabled` / `spec.newWindow`

| | `enabled` | `newWindow` |
|---|---|---|
| Type | `bool` pointer | `bool` pointer |
| Required | no | no |
| Default | `true` | `false` |
| NetBox column | `enabled` | `new_window` |

`enabled` shows the link; disabling it is how a link is retired without losing it.
`newWindow` opens it in a new browser window.

Both are pointers with explicit defaults rather than plain bools: `omitempty` on a plain bool
drops a deliberate `false` out of the payload, so the operator could never turn the link off
again.

### `spec.weight`

| Type | `int32` pointer, 0–32767 |
|---|---|
| Required | no |
| Default | `100` |
| NetBox column | `weight`, `PositiveSmallIntegerField` |

Orders links within a group, lowest first — `meta.ordering: ['group_name', 'weight', 'name']`.

## What is deliberately absent

- **`tags` and `customFields`.** The bases are `CloningMixin, ExportTemplatesMixin, OwnerMixin,
  ChangeLoggedModel`: neither mixin. The descriptor states `Taggable: false,
  CustomFieldable: false` rather than omitting them, because NetBox ignores a column it does not
  know rather than rejecting it — a wrongly-declared `tags` would vanish on write and be
  `PATCH`ed forever.
- **A registry check on `objectTypes`.** See [`spec.objectTypes`](#specobjecttypes): NetBox
  validates them against its own feature-scoped queryset, and a check here would only forbid
  legitimate links.
- **URL validation on `linkUrl`.** It is a template, not a URL.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate. `name CharField REQ UNIQUE len=100`, so the filter identifies at most one link:
no conditional constraint to express as a second candidate and no parent to pin to null.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

**`status.provenance` stays empty on this kind.** With neither `TagsMixin` nor
`CustomFieldsMixin` there is nowhere on the NetBox object to put a stamp, so a managed custom
link carries no provenance at all — which is the case [provenance](../operations/provenance.md)
calls out and which `NetBoxSweep` must never delete.

## `deletionPolicy` defaults to `Delete`

A custom link is presentation rather than data. Nothing in NetBox depends on one, so deleting
it destroys nothing: no `PROTECT` to trip over, no data-loss guard, and no reason to prefer
`Retain` ([deletion](../concepts/deletion.md)).

If you want a link gone from the UI but kept in NetBox, set `enabled: false` rather than
deleting the CR.

## Printer columns

```console
$ kubectl get nbcl
NAME       ENABLED   ID   READY   AGE
grafana    true      17   True    4m
runbook    false     18   True    4m
```

| Column | JSONPath |
|---|---|
| `ENABLED` | `.spec.enabled` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | an `objectTypes` entry is not `app_label.model` | Lowercase, one dot: `dcim.device`. Not the CRD Kind name |
| Rejected by `kubectl apply` | none — admission | `buttonClass: grey` | `gray`. `grey` is a Python alias, not a choice |
| Rejected by `kubectl apply` | none — admission | `linkText` or `linkUrl` empty | Both are `REQ` in NetBox. To hide the button, template it to render empty |
| `Ready=False`, `Reason=Invalid`, "Invalid content type" | `Ready` | the type does not exist, or cannot carry custom links | NetBox scopes them to `with_feature('custom_links')` |
| `Ready=False`, `Reason=Conflict` | `Ready` | a link with this `name` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt`; `status.naturalKey` shows what was searched |
| `Ready=True` but no button in NetBox | none | `enabled: false`, or `linkText` renders empty for this object | Both are working as designed. Check the template against a real object |
| The button renders but the URL is broken | none | the template is wrong; NetBox renders it at page load, not at write | The operator writes template text and never renders it |
| `enabled: false` keeps coming back as `true` | none | `enabled` was removed from the manifest rather than set | The default is `true`. Write `enabled: false` explicitly |

## Related

- [`NetBoxTag`](netboxtag.md) — the same Descriptor shape, and the shared envelope in full
- [`NetBoxExportTemplate`](netboxexporttemplate.md) — the other Jinja2-carrying UI kind, and the
  one whose `name` is *not* unique
- [`NetBoxSavedFilter`](netboxsavedfilter.md) — the third UI kind in this milestone
- [Provenance](../operations/provenance.md) — why a managed object can carry no stamp at all
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
