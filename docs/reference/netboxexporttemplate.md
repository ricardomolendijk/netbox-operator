# `NetBoxExportTemplate`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxExportTemplate` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbet` |
| Status subresource | yes |

A `NetBoxExportTemplate` is one `extras.ExportTemplate` in NetBox: a Jinja2 template NetBox
offers as an export format on a list view.

> ### `name` is **not unique** in NetBox
>
> `extras.ExportTemplate` declares `name CharField REQ len=100` with no `unique=True` and no
> `meta.constraints` at all, so identity here is a convention rather than something the database
> enforces — as it is for `ipam.Prefix`. If NetBox holds two templates called `csv`, the lookup
> is ambiguous and the object reports `Ready=False, Reason=Conflict` naming every match rather
> than picking one. See [natural key](#natural-key).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxExportTemplate
metadata:
  name: vm-inventory-csv
  namespace: default
spec:
  endpointRef: homelab
  name: vm-inventory-csv
  objectTypes:
    - virtualization.virtualmachine
  templateCode: |
    name,status
    {% for vm in queryset %}{{ vm.name }},{{ vm.status }}
    {% endfor %}
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxExportTemplate
metadata:
  name: vm-inventory-csv
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  # The natural key -- and NetBox does *not* enforce uniqueness on it. `table` is a
  # reserved name NetBox refuses.
  name: vm-inventory-csv
  description: One row per virtual machine

  objectTypes:
    - virtualization.virtualmachine

  templateCode: |
    name,status,vcpus,memory
    {% for vm in queryset %}{{ vm.name }},{{ vm.status }},{{ vm.vcpus }},{{ vm.memory }}
    {% endfor %}

  # Jinja2 Environment keyword arguments.
  environmentParams:
    trim_blocks: true

  mimeType: text/csv
  fileName: vm-inventory
  fileExtension: csv
  asAttachment: true
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides. See [`NetBoxTag`](netboxtag.md#spec). `tags` and `customFields` are absent here;
see [what is deliberately absent](#what-is-deliberately-absent).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ len=100` — **no `UNIQUE`** |

The natural key, on a column NetBox does not constrain. See the note above and
[natural key](#natural-key).

`table` is a reserved name NetBox refuses, case-insensitively
(`netbox/extras/models/models.py:498-503`). Not enforced here: NetBox's own message says it
better than a CEL rule would, and it is one of several `clean()` rules on this model — a schema
that enforced one would invite you to believe it enforced all of them.

### `spec.objectTypes`

| Type | `[]string`, 1–256 items, each up to 100 characters matching `^[a-z_]+\.[a-z0-9_]+$` |
|---|---|
| Required | yes |
| NetBox column | `object_types`, `ManyToManyField -> contenttypes.ContentType` |

The NetBox models this template exports, as Django ContentType strings. Required by NetBox: the
serializer carries no `required=False`.

Not references — `app_label.model` strings, declared as a `ClassObjectTypeList` and compared as
an order-independent string set, so the resolver never sees them. NetBox scopes the queryset to
`ObjectType.objects.with_feature('export_templates')`, so a type that cannot be exported is
rejected there rather than here.

### `spec.templateCode`

| Type | `string`, 1–131072 characters |
|---|---|
| Required | yes |
| NetBox column | `template_code` (`RenderTemplateMixin`), `TextField REQ` |

The Jinja2 template body, rendered with the exported queryset as context.

Bounded at 128 KiB here where NetBox's column is unbounded. A CR is stored in etcd, which has a
hard object limit of about 1.5 MiB, and an unbounded text field in a spec is a CR that can be
created and then never updated. A template larger than this is one NetBox's own git sync should
be pulling in.

### `spec.environmentParams`

| Type | JSON object (`JSONDocument`, unknown fields preserved) |
|---|---|
| Required | no |
| NetBox column | `environment_params` (`RenderTemplateMixin`), `JSONField` |

Extra keyword arguments for the Jinja2 `Environment`: `{"trim_blocks": true}`. NetBox validates
the keys against Jinja's own constructor (`netbox/extras/models/mixins.py:143-160`), so an
unknown one arrives as `Reason=Invalid` naming it.

**Compared as a whole document** (`registry.ClassJSON`), not as a scalar — the scalar rule would
unwrap an object carrying an `id` or a `value` key out of one and never settle.

Omit it to leave NetBox's own value alone; set it to `{}` to clear it
([field ownership](../concepts/field-ownership.md)).

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description`, `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.mimeType` / `spec.fileName` / `spec.fileExtension`

| | `mimeType` | `fileName` | `fileExtension` |
|---|---|---|---|
| Type | `string`, ≤50 | `string`, ≤200 | `string`, ≤15 |
| Required | no | no | no |
| NetBox column | `mime_type` | `file_name` | `file_extension` |

`mimeType` is the content type of the rendered output; empty means NetBox's own default of
`text/plain; charset=utf-8` (`netbox/extras/constants.py:29`). `fileName` is the base name given
to the download and `fileExtension` is appended to it.

All three are clearable, and omitting is not the same as `""`
([field ownership](../concepts/field-ownership.md)).

These four plus `templateCode` and `asAttachment` are the entries that earn the Descriptor's
explicit field map: `templateCode` → `template_code`, `mimeType` → `mime_type`,
`fileName` → `file_name`, `fileExtension` → `file_extension`, `asAttachment` → `as_attachment`
are five names a camelCase convention would get wrong in five different ways, and NetBox ignores
a field name it does not know rather than rejecting it.

### `spec.asAttachment`

| Type | `bool` pointer |
|---|---|
| Required | no |
| Default | `true` |
| NetBox column | `as_attachment` |

Sends the rendered output as a download rather than rendering it in the browser. A pointer with
an explicit default rather than a plain bool: `omitempty` on a plain bool drops a deliberate
`false` out of the payload, so the operator could never turn it off again.

## What is deliberately absent

- **The `SyncedDataMixin` columns** — `dataSource`, `dataFile`, `dataPath`, `autoSyncEnabled`,
  `dataSynced`. They are NetBox's own git-sync mechanism: NetBox pulls the template body out of
  a `core.DataSource` and overwrites `template_code` itself, so a CR that declared both would be
  fighting NetBox for one column. Declare the body here or sync it there, not both.
  They are absent from the descriptor's `Fields` and from its `ReadOnly` list alike, and the two
  omissions mean different things: they are not read-only — NetBox accepts a write to
  `data_source` — they are simply not this operator's to write. `data_synced` *is* read-only and
  *is* listed.
- **`tags` and `customFields`.** The bases are `SyncedDataMixin, CloningMixin,
  ExportTemplatesMixin, OwnerMixin, ChangeLoggedModel, RenderTemplateMixin`: neither
  `TagsMixin` nor `CustomFieldsMixin`. Note that `ExportTemplatesMixin` is what makes a model
  *exportable*, not what makes it an export template — an easy pair of names to read the wrong
  way round.
- **The reserved-name check on `name`.** See [`spec.name`](#specname).

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate, on a column NetBox does **not** declare unique. Two templates sharing a name make
the lookup ambiguous: the client raises an `*AmbiguousError` naming every match and the engine
reports `Ready=False, Reason=Conflict` with zero writes.

That is the right outcome. Guessing which of two templates a CR meant would overwrite somebody's
export format. Once `status.id` is set the object is reconciled by id and the natural key is not
consulted again, so the ambiguity only ever bites on first adoption.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

**`status.provenance` stays empty.** With neither `TagsMixin` nor `CustomFieldsMixin` there is
nowhere on the NetBox object to put a stamp, so a managed export template carries no provenance
at all — the case [provenance](../operations/provenance.md) calls out, and one `NetBoxSweep`
must never delete. This is where it differs from
[`NetBoxConfigTemplate`](netboxconfigtemplate.md#status), which is taggable and does get the tag.

## `deletionPolicy` defaults to `Delete`

An export template is presentation rather than data. Nothing in NetBox depends on one, so
deleting it destroys nothing: no `PROTECT` to trip over, no data-loss guard, and no reason to
prefer `Retain` ([deletion](../concepts/deletion.md)). The template body lives in Git, which is
where it was declared.

## Printer columns

```console
$ kubectl get nbet
NAME               ID   READY   AGE
vm-inventory-csv   31   True    2m
site-json          32   True    2m
```

| Column | JSONPath |
|---|---|
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

No column for `name`: `metadata.name` is already in the default `NAME` column and there is no
second identity worth showing, unlike the `SLUG` on a slugged kind.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `templateCode` over 128 KiB | Use NetBox's git sync for a template that large; the two cannot both own the column |
| Rejected by `kubectl apply` | none — admission | an `objectTypes` entry is not `app_label.model` | Lowercase, one dot: `dcim.site`. Not the CRD Kind name |
| `Ready=False`, `Reason=Conflict` naming two ids | `Ready`, zero writes | NetBox holds two templates with this `name` | Legitimate — the column is not unique. Rename one in NetBox, or adopt deliberately by `id` |
| `Ready=False`, `Reason=Invalid`, `table` | `Ready` | `name: table` is reserved, case-insensitively | Pick another name. NetBox's rule, not this API's |
| `Ready=False`, `Reason=Invalid`, "Invalid content type" | `Ready` | the type cannot carry export templates | NetBox scopes them to `with_feature('export_templates')` |
| `Ready=False`, `Reason=Invalid` naming a Jinja keyword | `Ready` | an `environmentParams` key is not a Jinja `Environment` argument | The message names it; remove it |
| `templateCode` keeps reverting in NetBox | none | NetBox's git sync owns the column for this template | Two writers, one column. Drop the data source or drop the CR field |
| `data_synced` never changes | none | it is server-maintained and `ReadOnly` | The operator never sends it and never diffs it |
| A `""` for `mimeType` did nothing visible | none | empty means NetBox's default, `text/plain; charset=utf-8` | Working as designed |

## Related

- [`NetBoxConfigTemplate`](netboxconfigtemplate.md) — the sibling template kind, which *is*
  taggable and whose `name` is equally non-unique
- [`NetBoxCustomLink`](netboxcustomlink.md) — the other Jinja2-carrying UI kind, with a unique
  `name`
- [`NetBoxSavedFilter`](netboxsavedfilter.md) — the third UI kind in this milestone
- [Provenance](../operations/provenance.md) — why a managed object can carry no stamp at all
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
