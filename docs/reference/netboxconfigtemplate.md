# `NetBoxConfigTemplate`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxConfigTemplate` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbct` |
| Status subresource | yes |

A `NetBoxConfigTemplate` is one `extras.ConfigTemplate` in NetBox: a Jinja2 template NetBox
renders into a device or virtual-machine configuration.

> ### This is the first kind that is **taggable but not custom-fieldable**
>
> The bases are `RenderTemplateMixin, SyncedDataMixin, CustomLinksMixin, ExportTemplatesMixin,
> OwnerMixin, TagsMixin, ChangeLoggedModel` — `TagsMixin` and no `CustomFieldsMixin`. So a
> `NetBoxConfigTemplate` carries **half** a provenance stamp: the tag, and no custom fields.
> The two `Descriptor` flags being independent is why they are two flags, and nothing in the
> engine branches on this case — it is those two booleans. See [`status`](#status).

Like [`NetBoxExportTemplate`](netboxexporttemplate.md), `name` is not unique in NetBox, so two
templates sharing one make the lookup ambiguous. See [natural key](#natural-key).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxConfigTemplate
metadata:
  name: motd
  namespace: default
spec:
  endpointRef: homelab
  name: motd
  templateCode: |
    Welcome to {{ object.name }}.
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxConfigTemplate
metadata:
  name: motd
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  # The natural key -- not unique in NetBox, so two templates with this name report
  # Conflict rather than one of them winning.
  name: motd
  description: Login banner

  # This kind is taggable, so the envelope's `tags` is a real column here.
  tags:
    - name: managed

  templateCode: |
    Welcome to {{ object.name }}.
    This host is managed from Kubernetes. Do not edit by hand.

  # Jinja2 Environment keyword arguments. `autoescape` is ignored here: NetBox forces it
  # off for config templates.
  environmentParams:
    trim_blocks: true

  mimeType: text/plain
  asAttachment: false
  debug: false
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides. See [`NetBoxTag`](netboxtag.md#spec). On this kind `tags` is a real column and
`customFields` is not; see [what is deliberately absent](#what-is-deliberately-absent).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ len=100` — **no `UNIQUE`** |

The natural key, on a column with no `unique=True` and no `meta.constraints`. Identity is a
convention rather than something the database enforces, so two templates sharing a name make the
lookup ambiguous and the object reports `Ready=False, Reason=Conflict` naming both ids rather
than picking one. See [natural key](#natural-key).

### `spec.templateCode`

| Type | `string`, 1–131072 characters |
|---|---|
| Required | yes |
| NetBox column | `template_code` (`RenderTemplateMixin`), `TextField REQ` |

The Jinja2 template body, rendered with the device or virtual machine as context — `object` in
the template.

Bounded at 128 KiB here for the reason
[`NetBoxExportTemplate.templateCode`](netboxexporttemplate.md#spectemplatecode) is: a CR lives in
etcd, whose object limit is about 1.5 MiB, and a spec field with no bound is a CR that can be
created and never updated.

### `spec.environmentParams`

| Type | JSON object (`JSONDocument`, unknown fields preserved) |
|---|---|
| Required | no |
| NetBox column | `environment_params` (`RenderTemplateMixin`), `JSONField` |

Extra keyword arguments for the Jinja2 `Environment`.

**`autoescape` is silently ignored on this kind.** NetBox forces it off, because a config
template renders plain text and an escaping template would be a latent XSS sink if the output
were ever shown as HTML (`netbox/extras/models/configs.py:321-329`). Everything else NetBox
validates against Jinja's own constructor, so an unknown key arrives as `Reason=Invalid`.

**Compared as a whole document** (`registry.ClassJSON`), not as a scalar — the scalar rule would
unwrap an object carrying an `id` or a `value` key out of one and never settle.

Omit it to leave NetBox's own value alone; set it to `{}` to clear it
([field ownership](../concepts/field-ownership.md)).

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description`, `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it. The two are different
instructions ([field ownership](../concepts/field-ownership.md)).

### `spec.mimeType` / `spec.fileName` / `spec.fileExtension`

| | `mimeType` | `fileName` | `fileExtension` |
|---|---|---|---|
| Type | `string`, ≤50 | `string`, ≤200 | `string`, ≤15 |
| Required | no | no | no |
| NetBox column | `mime_type` | `file_name` | `file_extension` |

`mimeType` is the content type of the rendered output; empty means NetBox's own default of
`text/plain; charset=utf-8` (`netbox/extras/constants.py:29`). `fileName` is the base name given
to the download and `fileExtension` is appended to it.

All three are clearable, and omitting is not the same as `""`.

### `spec.asAttachment`

| Type | `bool` pointer |
|---|---|
| Required | no |
| Default | `true` |
| NetBox column | `as_attachment` |

Sends the rendered configuration as a download rather than rendering it in the browser. A
pointer with an explicit default rather than a plain bool, so a deliberate `false` survives
`omitempty` and the operator can turn it off again.

### `spec.debug`

| Type | `bool` pointer |
|---|---|
| Required | no |
| Default | `false` |
| NetBox column | `debug` |

Returns the full Python traceback when the template fails to render, instead of a one-line
message. NetBox's own help text says "not recommended for production use"
(`netbox/extras/models/configs.py:295-301`), and it means it: the traceback is returned to
whoever asked for the render.

## What is deliberately absent

- **`customFields`.** No `CustomFieldsMixin` in the bases, so `custom_fields` is not a column
  here at all — which also keeps this kind out of the `object_types` list the provenance
  bootstrap derives, where a kind that does not carry the container has no business being.
  `tags` *is* present, and the descriptor states `Taggable: true, CustomFieldable: false`
  explicitly.
- **The `SyncedDataMixin` columns** — `dataSource`, `dataFile`, `dataPath`, `autoSyncEnabled`,
  `dataSynced`. Absent for the reason they are absent from
  [`NetBoxExportTemplate`](netboxexporttemplate.md#what-is-deliberately-absent): NetBox
  overwrites `template_code` from a `core.DataSource` itself, so declaring both would be two
  writers for one column. `data_synced` is in the descriptor's `ReadOnly` list; the others are
  not read-only, they are simply not this operator's to write.
- **`objectTypes`.** A config template is not scoped to content types the way an export template
  or a custom link is; NetBox renders it against whatever device or VM points at it.
- **Any inbound reference.** Nothing points at one *yet*. `dcim.Device.config_template` and
  `virtualization.VirtualMachine.config_template` are both
  `ForeignKey -> extras.ConfigTemplate on_delete=PROTECT`, and neither is a field on its Kind's
  spec so far. So this Kind ships with the `ConfigTemplateRef` alias and no user of it: the
  alias is where the target Kind is written down, and a reference added later is then a field on
  a spec rather than a second change to `objectref.go`.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate, on a column NetBox does **not** declare unique — `name CharField REQ len=100`, no
`unique=True`, no `meta.constraints`. Two templates sharing a name are an ambiguous lookup and a
`Ready=False, Reason=Conflict` with zero writes, for the reason
[`NetBoxExportTemplate`](netboxexporttemplate.md#natural-key)'s are: guessing which of two a CR
meant would overwrite somebody's device configuration.

Once `status.id` is set the object is reconciled by id and the natural key is not consulted
again, so the ambiguity only ever bites on first adoption.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

**`status.provenance` records half a stamp, and that is the whole point of this kind.** With
`TagsMixin` and no `CustomFieldsMixin`, the engine writes the provenance **tag** onto the NetBox
object and no provenance custom fields, when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. That is enough for `NetBoxSweep` to
recognise the object as managed — which is what separates this kind from the other four in
NBO-059, none of which carries a stamp at all. See
[provenance](../operations/provenance.md).

## `deletionPolicy` defaults to `Delete`

A config template is the template, not the configuration it renders; the body lives in Git,
which is where it was declared. `Retain` is reserved for the IPAM kinds that hold allocations
and this is not one of them ([deletion](../concepts/deletion.md)).

The delete is safe by construction rather than by policy: `Device.config_template` and
`VirtualMachine.config_template` are both `on_delete=PROTECT`, so NetBox refuses to delete a
template a device or VM still uses. That refusal arrives as `Deleting=False, Reason=Protected`
and clears itself when the last user goes. No data-loss guard.

## Printer columns

```console
$ kubectl get nbct
NAME       ID   READY   AGE
motd       53   True    2m
base-cfg   54   True    2m
```

| Column | JSONPath |
|---|---|
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

No column for `name`: `metadata.name` is already in the default `NAME` column and this kind has
no second identity worth showing.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `templateCode` over 128 KiB | Use NetBox's git sync for a template that large; the two cannot both own the column |
| Rejected by `kubectl apply` | none — admission | `customFields` set | Not a column on this model. `tags` is |
| `Ready=False`, `Reason=Conflict` naming two ids | `Ready`, zero writes | NetBox holds two templates with this `name` | Legitimate — the column is not unique. Rename one in NetBox, or adopt deliberately by `id` |
| `Ready=False`, `Reason=Invalid` naming a Jinja keyword | `Ready` | an `environmentParams` key is not a Jinja `Environment` argument | The message names it; remove it |
| `autoescape: true` has no effect | none | NetBox forces it off for config templates | Working as designed — see [`spec.environmentParams`](#specenvironmentparams) |
| `Deleting=False`, `Reason=Protected` | `Deleting` | a device or VM still uses this template (NetBox `PROTECT`) | Re-point or delete them; the delete then completes on the next pass |
| `templateCode` keeps reverting in NetBox | none | NetBox's git sync owns the column for this template | Two writers, one column. Drop the data source or drop the CR field |
| `status.provenance` shows a tag and no custom fields | none | this kind is taggable and not custom-fieldable | Expected. Half a stamp is the correct stamp here |
| A render returns a Python traceback to a user | none | `debug: true` | Set it back to `false`. NetBox does not recommend it in production either |

## Related

- [`NetBoxExportTemplate`](netboxexporttemplate.md) — the sibling template kind, and the same
  non-unique `name`
- [`NetBoxEndpoint`](netboxendpoint.md) — `spec.managedBy`, which decides whether the tag is
  written at all
- [`NetBoxTag`](netboxtag.md) — the provenance tag's own Kind, and the shared envelope in full
- [Provenance](../operations/provenance.md) — the half-stamped case this kind is the first of
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [The Descriptor](../concepts/descriptor.md) — where `Taggable` and `CustomFieldable` live
