# `NetBoxConfigContext`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxConfigContext` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcc` |
| Status subresource | yes |
| Lands with | NBO-059 (M10) |

A `NetBoxConfigContext` is one `extras.ConfigContext` in NetBox: a JSON document merged into
the rendered configuration of every object its assignment sets select.

> ### Thirteen to-many references on one kind
>
> The widest many-to-many surface in the catalogue, and the proof that cardinality is data: the
> thirteen sets are thirteen `ClassRefMany` entries on the Descriptor and **no engine code at
> all**. Reordering any of them produces zero API writes. See
> [the assignment sets](#the-assignment-sets).
>
> ### `tags` here is **not** this object's tags
>
> On every other kind `tags` is `TagsMixin` — the object's own tags, and where the operator
> writes its provenance stamp. On `extras.ConfigContext` it is a plain many-to-many selecting
> *which tagged objects the context applies to*. Getting that wrong would silently change which
> objects in NetBox receive the configuration. See [the `tags` trap](#the-tags-trap).

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxConfigContext
metadata:
  name: eu-dns
  namespace: default
spec:
  endpointRef: homelab
  name: EU DNS
  data:
    dns:
      servers:
        - 10.0.0.53
```

`data` is required by NetBox. A context with nothing to contribute is expressed with
`isActive: false`, not with an absent document.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxConfigContext
metadata:
  name: eu-dns
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: EU DNS
  description: Resolvers for everything in the EU region

  data:
    dns:
      servers:
        - 10.0.0.53
        - 10.0.1.53

  # Validated against this profile's JSON Schema by NetBox.
  profileRef:
    name: dns-profile

  weight: 1000                # default: a higher weight is merged later and wins
  isActive: true              # default

  # All thirteen assignment sets. Each is a full replacement, and each is compared as an
  # order-independent id set — reordering one produces no API write.
  regions:
    - name: eu-west
  siteGroups:
    - name: branch-offices
  sites:
    - name: ams1
  locations:
    - name: ams1-hall-a
  deviceTypes:
    - name: c9300-48p
  roles:                      # dcim.DeviceRole: one role serves devices and VMs both
    - name: access-switch
  platforms:
    - name: ios-xe
  clusterTypes:
    - name: vmware-vsphere
  clusterGroups:
    - name: production
  clusters:
    - name: ams-cluster-1
  tenantGroups:
    - name: customers
  tenants:
    - name: acme
  # Which *tagged objects* the context applies to. Not this object's own tags.
  tags:
    - name: production
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`, `driftMode`
overrides. See [`NetBoxTag`](netboxtag.md#spec). `customFields` is absent here, and so is a
spec field for this object's *own* tags; see
[what is deliberately absent](#what-is-deliberately-absent).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ UNIQUE len=100` |

The context's name, and its only natural key. `UNIQUE`, so a `?name=` lookup matches at most
one row: there is no second candidate to fall through to and no ambiguity to report.

**If it is wrong.** An empty or over-long name is rejected by admission. A name another context
already holds is `Ready=False, Reason=Conflict` at reconcile, unless `onConflict: Adopt` takes
the existing context over.

### `spec.data`

| Type | JSON object (`JSONDocument`, unknown fields preserved) |
|---|---|
| Required | **yes** |
| NetBox column | `data`, `JSONField REQ` |

The document merged into the configuration of every selected object.

Required, so it has **no empty state to document**: `{}` is a context that contributes an empty
object, and "contribute nothing" is `isActive: false`.

**Compared as a whole document** (`registry.ClassJSON`), not as a scalar. The scalar rule
unwraps any JSON object carrying an `id` or a `value` key, because that is how NetBox renders a
foreign key and a choice on read — so a `data` document with an `id` key anywhere in it would be
compared as that id against the whole document, never settle, and be `PATCH`ed forever
([drift detection](../concepts/drift.md)).

**If it is wrong.** Admission enforces `type: object`. A document a `profileRef`'s JSON Schema
rejects arrives as `Ready=False, Reason=Invalid` carrying NetBox's own validation sentence.

### `spec.profileRef`

| Type | `ConfigContextProfileRef` |
|---|---|
| Required | no |
| NetBox column | `profile`, `ForeignKey -> extras.ConfigContextProfile on_delete=PROTECT` |

The [`NetBoxConfigContextProfile`](netboxconfigcontextprofile.md) whose JSON Schema NetBox
validates `data` against. One reference, four resolution modes
([references](../concepts/references.md)).

**Not this kind's containment parent**, and no owner reference is taken on it: `PROTECT` means
NetBox does not cascade, so an owner reference would promise a cluster-side cascade the server
refuses to perform ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

**If it is wrong.** A structurally invalid reference is refused by CEL at admission. A reference
naming a profile that does not exist yet is `RefsResolved=False, Reason=RefNotReady` and
**nothing is written** — a partially resolved payload would be a silent half-apply. A reference
into another namespace without a matching [`NetBoxRefGrant`](netboxrefgrant.md) is
`RefsResolved=False, Reason=RefForbidden`.

### The assignment sets

Thirteen fields, all identical in behaviour:

| Field | NetBox column | Target Kind |
|---|---|---|
| `regions` | `regions` | [`NetBoxRegion`](netboxregion.md) |
| `siteGroups` | `site_groups` | [`NetBoxSiteGroup`](netboxsitegroup.md) |
| `sites` | `sites` | [`NetBoxSite`](netboxsite.md) |
| `locations` | `locations` | [`NetBoxLocation`](netboxlocation.md) |
| `deviceTypes` | `device_types` | [`NetBoxDeviceType`](netboxdevicetype.md) |
| `roles` | `roles` | [`NetBoxDeviceRole`](netboxdevicerole.md) |
| `platforms` | `platforms` | [`NetBoxPlatform`](netboxplatform.md) |
| `clusterTypes` | `cluster_types` | [`NetBoxClusterType`](netboxclustertype.md) |
| `clusterGroups` | `cluster_groups` | [`NetBoxClusterGroup`](netboxclustergroup.md) |
| `clusters` | `clusters` | [`NetBoxCluster`](netboxcluster.md) |
| `tenantGroups` | `tenant_groups` | [`NetBoxTenantGroup`](netboxtenantgroup.md) |
| `tenants` | `tenants` | [`NetBoxTenant`](netboxtenant.md) |
| `tags` | `tags` | [`NetBoxTag`](netboxtag.md) |

| Type | `[]<Kind>Ref`, up to 256 items |
|---|---|
| Required | no |

Every one of them:

- Is a **full replacement.** The list is the whole membership, so removing an entry removes the
  assignment. Omit the field to leave NetBox's own value alone; set it to `[]` to clear it. The
  two are different instructions ([field ownership](../concepts/field-ownership.md)).
- Is **written sorted and deduplicated**, and compared as an order-independent id set. NetBox
  does not preserve many-to-many order, so the order a manifest lists them in is not data —
  reordering one produces zero API writes ([drift detection](../concepts/drift.md)).
- **Writes nothing at all when any element fails to resolve.** Writing the entries that did
  resolve would be a full-list replacement with a shorter list, which is a deletion reported as
  a success. The object reports `RefsResolved=False` naming the element that failed.
- Is bounded at **256**. `ObjectRef` carries five CEL rules and the API server costs each at the
  list's maximum length, so an unbounded list of refs is refused at install with "estimated rule
  cost exceeds budget" while `make verify` stays green. 256 is the project standard
  ([a list needs a bound](../concepts/references.md#a-list-needs-a-bound)). Thirteen such lists
  on one CRD is the largest budget draw in the catalogue and it clears: the CRD installs, which
  `internal/controller`'s envtest suite asserts on every run by installing it.

`roles` is worth one note: NetBox spells the column `roles`, not `device_roles`, and points it at
`dcim.DeviceRole` — one role serves devices and virtual machines both
([`docs/netbox-schema.md`](../netbox-schema.md) → `extras.ConfigContext`).

### `spec.weight`

| Type | `int32` pointer, 0–32767 |
|---|---|
| Required | no |
| Default | `1000` |
| NetBox column | `weight`, `PositiveSmallIntegerField def=1000` |

Orders contexts when more than one applies to an object: a higher weight is merged later and
therefore wins. Defaulted, so it is never absent and has no empty state.

### `spec.isActive`

| Type | `bool` pointer |
|---|---|
| Required | no |
| Default | `true` |
| NetBox column | `is_active`, `BooleanField def=True` |

Turns the context off without deleting it. A pointer with an explicit default rather than a
plain bool: `omitempty` on a plain bool drops a deliberate `false` out of the payload, so the
operator could never turn it off again.

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description`, `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it.

## The `tags` trap

`extras.ConfigContext` declares `tags` **itself**, as a
`ManyToManyField -> extras.Tag` with `related_name='+'` and a `SlugRelatedField` serializer, and
it mixes in **no `TagsMixin`** ([`docs/netbox-schema.md`](../netbox-schema.md) →
`extras.ConfigContext`, bases). It is a selector: *which tagged objects does this context apply
to.*

That matters because `tags` is where the provenance stamp goes on every kind where it *is*
`TagsMixin`, and `tags` is a full-replacement list — the stamp appends its tag to whatever the
payload carries. Declaring this kind `Taggable` would therefore add `k8s-managed` to the
selector and silently change which objects in NetBox receive this configuration. That is the
loudest possible way to get a boolean wrong, so:

- The kind is **not** `Taggable`, and `spec.tags` is an ordinary user field the operator never
  touches.
- `internal/registry` **refuses at boot** any descriptor that is `Taggable` *and* maps a spec
  field onto `tags` (`ErrTagsFieldOnTaggableKind`). The two readings cannot both be right on one
  kind, so the mistake fails the build rather than the cluster.
- The coverage audit checks `Taggable` against the IR's record of *where the column comes from*
  (`declared_by: TagsMixin`) rather than against the column merely existing, so a future kind
  with the same shape cannot slip through either.

## No provenance at all

Neither `TagsMixin` nor `CustomFieldsMixin` is in the bases, so neither half of the provenance
stamp has a column to go in. A managed `NetBoxConfigContext` therefore carries **no stamp
whatsoever** and `status.provenance` stays empty. Consequences, all of them documented in
[provenance](../operations/provenance.md):

- [`NetBoxSweep`](netboxsweep.md) cannot see one. It keys on the tag and the cluster stamp, so a
  config context this operator created and then lost the CR for will never be reported.
- Multi-writer detection is blind to one. There is no `k8s_cluster` or `k8s_owner` to compare,
  so [two writers, one NetBox object](../operations/multi-writer.md) cannot fire here — two
  clusters both declaring `EU DNS` will each `PATCH` the other's document with no complaint from
  either.

Both are properties of the NetBox model, not choices. The fix is not to have two clusters
declare one context.

## What is deliberately absent

- **`customFields`, and a spec field for this object's own tags.** Neither mixin is in the
  bases, so neither column exists. Stated in the descriptor rather than merely omitted, because
  NetBox ignores a column it does not know rather than rejecting it — a wrongly-declared
  `custom_fields` would vanish on write and be `PATCH`ed forever.
- **The `SyncedDataMixin` trio** (`dataSource`, `dataFile`, `dataPath`). NetBox overwrites `data`
  from a `core.DataSource` on its own schedule, so declaring both the document and the path it
  is fetched from would be two writers for one column with NetBox winning. Recorded in
  [`hack/coverage-exclusions.yaml`](../../hack/coverage-exclusions.yaml).
- **`owner`.** A `ForeignKey -> users.Owner`, and `users/*` is an excluded app.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `name` | `?name=<name>` |

One candidate, and no `meta.constraints` on the model — none needed. The column's own `UNIQUE`
is stronger than a constraint line, because it holds unconditionally rather than under a
condition the engine would have to reproduce as a filter ([lookups](../concepts/lookups.md)).

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `children`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status).

`status.provenance` **stays empty**, for the reason above. `children` stays empty: this kind
declares no inline lists.

Nothing in `status` is cleared on a failed reconcile. A `Ready=False` object keeps the `id` and
`naturalKey` of the object it last matched.

## Conditions

| Type | `True` when | `False` when | Reasons |
|---|---|---|---|
| `Ready` | the context exists in NetBox and matches the spec | anything refused or deferred the write | `Synced`, `Conflict`, `Invalid`, `Unreachable`, `WaitingForEndpoint`, `ReservedByOperator` |
| `RefsResolved` | every element of every set, and `profileRef`, resolved to an id | any reference is missing, ambiguous or forbidden | `Synced`, `RefNotReady`, `RefAmbiguous`, `RefForbidden` |
| `ParentOwned` | — | this kind takes no owner reference, so it is not set | — |
| `Deleting` | — | the delete is waiting on the endpoint or on children | `WaitingForEndpoint`, `PendingDependents` |

Reason glossary and retry intervals are shared across every kind and documented once in
[errors and retries](../concepts/errors-and-retries.md). `RefNotReady` re-enqueues on a watch of
the target Kind rather than on a timer, so a context applied before its sites converges as soon
as they exist ([stuck references](../operations/stuck-references.md)).

## `deletionPolicy` defaults to `Delete`

Nothing in NetBox points at a config context, so there is no `PROTECT` to refuse the delete and
no data to destroy: the merged configuration it contributed simply stops being contributed. No
data-loss guard ([deletion](../concepts/deletion.md)).

To retire a context without losing it, set `isActive: false` instead.

## No containment parent

Twelve of this model's foreign keys are to-many assignment sets, and a to-many reference cannot
be a containment parent at all: Kubernetes garbage collection waits for *every* owner, so a list
of parents turns "delete the site" into "delete the site or the tenant" with no upper bound. The
thirteenth, `profile`, is `on_delete=PROTECT` and therefore does not cascade
([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

So `kubectl delete netboxsite ams1` leaves this CR alone, and the context keeps the remaining
members of its `sites` set.

## Printer columns

```console
$ kubectl get nbcc
NAME     ID   READY   AGE
eu-dns   42   True    5m
us-dns   43   False   30s
```

| Column | JSONPath |
|---|---|
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `data` omitted | It is required by NetBox. Use `isActive: false` to disable a context |
| Rejected by `kubectl apply` | none — admission | `data` given as a list or a string | It has to be an object |
| Rejected by `kubectl apply` | none — admission | an assignment set has more than 256 entries | 256 is the CEL cost bound. Split the context, or select by a broader set |
| `RefsResolved=False`, `Reason=RefNotReady` | `RefsResolved` | one member of a set does not exist yet | Nothing to do — it re-enqueues on a watch. The message names the element |
| `RefsResolved=False`, `Reason=RefForbidden` | `RefsResolved` | a set member lives in another namespace with no grant | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace |
| An assignment vanished from NetBox | none | the entry was removed from the manifest | Working as designed: each set is a full replacement |
| Reordering a set produced a `PATCH` | none | it did not — check `status.lastAppliedHash` | Sets are compared order-independently. A write means a member changed |
| `Ready=False`, `Reason=Invalid` | `Ready` | `data` fails the `profileRef` profile's JSON Schema | Read NetBox's sentence in the condition message |
| `Ready=False`, `Reason=Conflict` | `Ready` | a context with this `name` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt` |
| Two clusters keep overwriting one context | none — nothing fires | this kind carries no provenance stamp | [No provenance at all](#no-provenance-at-all). Only one cluster may declare it |
| The provenance tag appeared in `spec.tags`' NetBox column | — | it cannot | `Taggable` is false and the registry refuses the combination — see [the `tags` trap](#the-tags-trap) |

## Related

- [`NetBoxConfigContextProfile`](netboxconfigcontextprofile.md) — the JSON Schema `data` is
  validated against
- [References](../concepts/references.md) — the four resolution modes, and why a list needs a
  bound
- [Drift detection](../concepts/drift.md) — why a set is compared order-independently and a JSON
  document is compared whole
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set, on thirteen lists
- [Provenance](../operations/provenance.md) — what a kind with no stamp gives up
- [Two writers, one NetBox object](../operations/multi-writer.md) — and why it cannot fire here
- [`NetBoxTag`](netboxtag.md) — the shared envelope fields in full
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
