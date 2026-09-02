# `NetBoxConfigContextProfile`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxConfigContextProfile` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbccp` |
| Status subresource | yes |

A `NetBoxConfigContextProfile` is one `extras.ConfigContextProfile` in NetBox: a JSON Schema
that [`NetBoxConfigContext`](netboxconfigcontext.md) documents are validated against.

> ### The one kind whose provenance stamp does not follow its bases
>
> The digest has `extras.ConfigContextProfile` as a `PrimaryModel`, which mixes in both
> `TagsMixin` and `CustomFieldsMixin` — so from the AST it should carry a whole stamp. The REST
> serializer disagrees: its write path is `name, description, schema, tags, owner, comments,
> data_*, created, last_updated`, with **no `custom_fields`**. See
> [half a stamp, for an unusual reason](#half-a-stamp-for-an-unusual-reason).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxConfigContextProfile
metadata:
  name: dns-profile
  namespace: default
spec:
  endpointRef: homelab
  name: DNS settings
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxConfigContextProfile
metadata:
  name: dns-profile
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: DNS settings
  description: Shape of the dns block every config context contributes
  comments: |
    Owned by the platform team. Widen the schema before widening a context.

  # A JSON Schema document. NetBox validates that it *is* legal JSON Schema and rejects one
  # that is not, so the operator is a pipe rather than a second validator.
  schema:
    type: object
    properties:
      dns:
        type: object
        properties:
          servers:
            type: array
            items:
              type: string
    required:
      - dns
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

The profile's name, and its only natural key. `UNIQUE`, so unlike the template kinds in this
app the database enforces the identity and a `?name=` lookup can never be ambiguous.

**If it is wrong.** An empty or over-long name is rejected by admission against
`minLength`/`maxLength`. A name another profile already holds is a `Conflict` at reconcile:
`Ready=False, Reason=Conflict` with `status.naturalKey` showing what was searched, unless
`onConflict: Adopt` takes the existing profile over.

### `spec.schema`

| Type | JSON object (`JSONDocument`, unknown fields preserved) |
|---|---|
| Required | no |
| NetBox column | `schema`, `JSONField` with `blank=True, null=True` |

The JSON Schema a config context's `data` is validated against.

Omit it to leave NetBox's own value alone; set it to `{}` to clear it. The two are different
instructions ([field ownership](../concepts/field-ownership.md)).

**Compared as a whole document** (`registry.ClassJSON`), not as a scalar. The scalar rule
unwraps any JSON object carrying an `id` or a `value` key, because that is how NetBox renders a
foreign key and a choice on read — and a JSON Schema may legitimately contain either as a
property name, so compared as a scalar it would never settle and the operator would `PATCH` it
forever ([drift detection](../concepts/drift.md)).

**If it is wrong.** Admission enforces only `type: object`. Whether the document is legal JSON
Schema is NetBox's question, and a malformed one arrives as `Ready=False, Reason=Invalid`
carrying NetBox's own sentence.

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description`, `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

### `spec.comments`

| Type | `string`, up to 8192 characters |
|---|---|
| Required | no |
| NetBox column | `comments`, `TextField` |

The long-form note NetBox renders as Markdown. Bounded here rather than in NetBox: a CR lives
in etcd, whose object limit is about 1.5 MiB, and an unbounded text field is a CR that can be
created and never updated.

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

## What is deliberately absent

- **`tags` and `customFields`.** `tags` is a real column here and the operator writes the
  provenance tag into it, but there is no *spec* field for user tags on any kind yet — the one
  systematic gap `docs/coverage.md` records. `custom_fields` is not in the serializer's write
  path at all; see below.
- **The `SyncedDataMixin` trio** (`dataSource`, `dataFile`, `dataPath`). NetBox overwrites
  `schema` from a `core.DataSource` on its own schedule, so declaring both the document and the
  path it is fetched from would be two writers for one column with NetBox winning. Recorded in
  [`hack/coverage-exclusions.yaml`](../../hack/coverage-exclusions.yaml).
- **`owner`.** A `ForeignKey -> users.Owner`, and `users/*` is an excluded app, so there is no
  Kind to reference.

## Half a stamp, for an unusual reason

Every other kind's `Taggable` and `CustomFieldable` follow straight from its bases. This one's
do not. The AST digest lists `extras.ConfigContextProfile` as a `PrimaryModel`
([`docs/netbox-schema.md`](../netbox-schema.md) → `extras.ConfigContextProfile`, bases), which
mixes in `CustomFieldsMixin` — but the REST serializer's write path carries no `custom_fields`
key. This is precisely the API-versus-AST gap [`docs/regenerating.md`](../regenerating.md) warns
about for the sixteen models that shadow an inherited column, and this model is one of them: it
shadows `description`.

The flag follows the API, because NetBox **ignores a column it does not know rather than
rejecting it**. A `CustomFieldable: true` here would build a `custom_fields` object into every
payload, NetBox would drop it silently, the operator would report `Ready=True`, and
`status.provenance` would claim a stamp that is not there. So the profile carries the tag and no
custom fields — the same half-stamp state as
[`NetBoxConfigTemplate`](netboxconfigtemplate.md), arrived at from the opposite direction.

`internal/registry/coverage_test.go` is what holds this: it asserts both flags against the IR's
own write path, so a descriptor that believes the bases over the serializer fails the build.

## `deletionPolicy` defaults to `Delete`

A profile validates and stores nothing, so deleting one destroys no data and needs no guard.
`ConfigContext.profile` is `on_delete=PROTECT`
([`docs/netbox-schema.md`](../netbox-schema.md) → `extras.ConfigContext`), so NetBox refuses the
delete while any config context still names the profile. That arrives as `Deleting=False,
Reason=Protected` with NetBox's message verbatim, and it clears itself when the last context
stops pointing at it ([deletion](../concepts/deletion.md)).

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `children`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status).

`status.provenance` records the tag and **no custom fields**, for the reason above. `children`
stays empty: this kind declares no inline lists.

Nothing in `status` is cleared on a failed reconcile. A `Ready=False` object keeps the `id` and
`naturalKey` of the object it last matched, which is what makes the failure diagnosable.

## Conditions

| Type | `True` when | `False` when | Reasons |
|---|---|---|---|
| `Ready` | the profile exists in NetBox and matches the spec | anything refused or deferred the write | `Synced`, `Conflict`, `Invalid`, `Unreachable`, `WaitingForEndpoint`, `ReservedByOperator` |
| `RefsResolved` | this kind has no references, so `True` with `Synced` | never | `Synced` |
| `Deleting` | — | the delete is refused or waiting | `Protected`, `WaitingForEndpoint`, `PendingDependents` |

Reason glossary and retry intervals are shared across every kind and documented once in
[errors and retries](../concepts/errors-and-retries.md). `Conflict` and `Invalid` retry at the
endpoint's `resyncPeriod`; `Unreachable` retries with capped backoff.

## Printer columns

```console
$ kubectl get nbccp
NAME          ID   READY   AGE
dns-profile   31   True    5m
```

| Column | JSONPath |
|---|---|
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `schema` given as a list or a string | It has to be an object. NetBox rejects anything else anyway |
| Rejected by `kubectl apply` | none — admission | `name` empty or over 100 characters | The column is `len=100` |
| `Ready=False`, `Reason=Conflict` | `Ready` | a profile with this `name` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt` |
| `Ready=False`, `Reason=Invalid` | `Ready` | `schema` is not legal JSON Schema | Read NetBox's sentence in the condition message; it names the offending keyword |
| `Deleting=False`, `Reason=Protected` | `Deleting` | a config context still names this profile | Delete or re-point the context, or set `deletionPolicy: Retain` |
| `status.provenance` shows no custom fields | none | working as designed | The serializer has no `custom_fields` on this endpoint — see [above](#half-a-stamp-for-an-unusual-reason) |

## Related

- [`NetBoxConfigContext`](netboxconfigcontext.md) — the kind whose `data` this validates
- [`NetBoxConfigTemplate`](netboxconfigtemplate.md) — the other half-stamped kind in `extras`
- [Provenance](../operations/provenance.md) — what a partial stamp costs
- [Regenerating the schema](../regenerating.md) — the API-versus-AST gap this kind is an
  instance of
- [Drift detection](../concepts/drift.md) — why a JSON document is compared whole
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
