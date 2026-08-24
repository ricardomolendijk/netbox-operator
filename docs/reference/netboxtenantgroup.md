# `NetBoxTenantGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxTenantGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbtenantgroup` |
| Status subresource | yes |
| Lands with | NBO-021 (M3) |

A `NetBoxTenantGroup` is one `tenancy.TenantGroup` in NetBox: a nestable grouping of
tenants, and `tenancy.Tenant`'s only foreign key.

It is the **second `NestedGroupModel`** the operator carries, and it is here to show that
the tree shape is not what decides a natural key. `dcim.Region`
([`NetBoxRegion`](netboxregion.md)) has the same base, the same self-referential `parent`,
and a completely different identity.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTenantGroup
metadata:
  name: houses
  namespace: default
spec:
  endpointRef: homelab
  name: Houses
  slug: houses
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTenantGroup
metadata:
  name: rotterdam
  namespace: netbox-catalog
spec:
  endpointRef: homelab

  onConflict: Fail        # the default
  deletionPolicy: Delete  # the default

  name: Rotterdam
  slug: rotterdam
  description: Houses in Rotterdam

  # Nests this group. Sent in the create payload when it resolves, PATCHed on afterwards
  # when it does not.
  parentRef:
    name: houses
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy` and `customFields` come from the shared
envelope and behave identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Validation | `MinLength=1`, `MaxLength=100` |

The group's label in the NetBox UI.

**Globally unique**, unlike a `NetBoxRegion`'s: `docs/netbox-schema.md` →
`tenancy.TenantGroup` records `name CharField REQ UNIQUE len=100`, a column-level index
rather than a per-parent constraint. Two groups may not share a name even under different
parents.

**If it is wrong.** Empty or over 100 characters is rejected at admission. A name already
taken by another group comes back from NetBox as a 400 and the object reports
`Ready=False, Reason=Invalid` carrying NetBox's own message. It is not retried with the same
payload — the spec has to change first
([errors and retries](../concepts/errors-and-retries.md)).

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Validation | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

URL-safe identifier, and **this kind's natural key**. Also column-unique
(`slug SlugField REQ UNIQUE len=100`).

**If it is wrong.** `Not A Slug` is rejected at admission by the pattern — the controller is
never involved. A slug another group already holds is found by the natural-key lookup, and
`spec.onConflict` decides what happens next; see
[Two namespaces, one slug](#two-namespaces-one-slug).

### `spec.parentRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxTenantGroup` |
| Required | no |
| Validation | the `ObjectRef` CEL rules: exactly one of `name`, `slug`, `lookup`, `id` |

Nests this group under another one. Self-referential:
`parent (NestedGroupModel) TreeForeignKey -> tenancy.TenantGroup on_delete=CASCADE`.

Written to NetBox as `parent`. It is **not** filtered on, because it is not part of this
kind's identity — see [Natural keys](#natural-keys).

`on_delete=CASCADE` is worth knowing: deleting a parent group in NetBox deletes its children
too. That is NetBox's behaviour, not the operator's, and it happens whether or not the
children have CRs.

**If it is wrong.** A malformed ref is rejected at admission. A ref naming a CR that does not
exist yet reports `RefsResolved=False, Reason=RefNotFound` and lists `parentRef` in
`status.deferredPending`; the group is still created, top-level, and the parent is applied by
a follow-up PATCH once the reference resolves. See
[A parent applied in the same batch converges](#a-parent-applied-in-the-same-batch-converges).

### `spec.description`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `MaxLength=200` |

Free text shown next to the group. Omit it to leave NetBox's own value alone; set it to `""`
to clear the value in NetBox. Those are two different instructions and the operator can tell
them apart ([field ownership](../concepts/field-ownership.md)).

**If it is wrong.** Over 200 characters is rejected at admission.

`tenancy.TenantGroup` also carries `comments`, which this CRD deliberately does not expose
yet — same as [`NetBoxRegion`](netboxregion.md). A field that is accepted and does nothing is
worse than one that is not there.

## Natural keys

**One candidate: `slug` alone.**

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

That is a genuine divergence from the shape the design document asserts for every MPTT kind,
and it is read straight off the schema:

| Kind | `meta.constraints` | Column-level `UNIQUE` | Natural keys |
|---|---|---|---|
| `dcim.Region` | `(parent, name)`, `(name)` where `parent IS NULL`, `(parent, slug)`, `(slug)` where `parent IS NULL` | none | two, one pinning `parent_id=null` |
| `tenancy.TenantGroup` | **none** | `name`, `slug` | one, `slug` |

So there is **no `parent_id` filter of any kind** in this kind's lookup — neither a matched
value nor a null pin. Adding one would be wrong twice over: a nested group's slug would stop
being findable, and the query would assert a constraint the database does not have. The
regression test is
[`TestTenantGroupIsKeyedOnSlugAloneWithNoParentFilter`](../../internal/registry/tenancy_tenant_test.go).

`name` is column-unique too, and is deliberately *not* a candidate. A kind gets one identity,
and `slug` is the one the spec calls the group's identifier.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `deferredPending`, `provenance`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each field means and when it is
cleared.

`status.deferredPending` is the one to read on this kind: it holds `["parentRef"]` while the
group exists in NetBox without the parent the spec asks for.

`status.provenance` is the full stamp — `tenancy.TenantGroup` is a `NestedGroupModel`, so it
carries both `tags` and `custom_fields` and is stamped when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | `parentRef` is unset, or resolved | `parentRef` is set and does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefDenied`, `RefAmbiguous`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals: [`NetBoxTag`](netboxtag.md#conditions) and
[errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### A parent applied in the same batch converges

`parentRef` is declared in `Descriptor.Deferred` with mode `IfUnresolved`
(`internal/registry/tenancy_tenantgroup.go`), and that is only safe *because* `parent` is
outside the natural key. Apply a parent and a child together and the child plays out like
this:

1. Pass one. `parentRef` does not resolve, so `parent` is left out of the payload. Candidate
   1 is still applicable — it reads only `slug` — so the group is **created top-level**.
   `Ready=False`, `RefsResolved=False/RefNotFound`, `status.deferredPending: ["parentRef"]`.
2. Pass two, five seconds later. The parent now has a `status.id`, `parentRef` resolves,
   NetBox lacks a `parent` the spec asks for, and one `PATCH` sets it. `Ready=True`.

A `NetBoxRegion` cannot do this, and the difference is instructive. There, `parent` *is* part
of the identity, so creating the object without it would create an object with a different
natural key from the one the lookup asked about — which is why a child region waits at
`WaitingForKey` instead. Here the lookup asks the same question either way, so creating
early cannot adopt the wrong object.

The mode is `IfUnresolved` and not `Always`: a `parentRef` that resolves on the first pass
belongs in the create payload. Stripping it would leave the group visibly top-level in NetBox
for a pass, for no gain.

### Two namespaces, one slug

Every kind is namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) and `slug` is unique
in NetBox's database. A namespace boundary does not partition a database index, so two
`NetBoxTenantGroup`s in different namespaces with `slug: houses` are **one NetBox group**.

The natural-key lookup finds the existing group and `spec.onConflict` decides:

| `onConflict` | What happens |
|---|---|
| `Fail` (**default**) | The second CR reports `Ready=False, Reason=Conflict` naming the namespace and CR that hold the group, and performs **zero writes**. |
| `Adopt` | Both CRs reconcile the same NetBox object. They will fight over every field they disagree on, one PATCH per resync each, forever. |
| `AdoptOnly` | The same, except the CR never creates a group that does not already exist. |

**Do not set `Adopt` on a shared catalogue kind.** Note this is the opposite of the framing
NBO-021's design note uses, which assumes `Adopt` is the default: it is not, `Fail` is
(`api/v1alpha1/netboxobject_types.go`, `+kubebuilder:default=Fail`). So the collision is loud
out of the box, and the footgun is opting *out* of that. This kind and
[`NetBoxTenant`](netboxtenant.md) are the ones most likely to be applied from more than one
namespace, and a slow-motion tug-of-war between two PATCHing controllers is much harder to
diagnose than a `Conflict` condition. The better shape is one catalogue namespace holding the
groups plus a [`NetBoxRefGrant`](netboxrefgrant.md) letting team namespaces point at them.

### Renaming the slug changes identity

`slug` is the natural key, so editing `spec.slug` does not rename the NetBox group — it
changes what the CR is looking for. The next reconcile finds nothing at the new slug and
creates a second group, leaving the first behind. Rename in NetBox and in the manifest
together, or delete and re-create the CR.

`name`, `description` and `parentRef` are safe to edit: none is part of the natural key.

### `_depth` and `_children` are never written

`tenancy.TenantGroup` is an MPTT tree, so NetBox maintains `_depth` and `_children` itself
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns). Both are in the descriptor's
read-only list. Writing either would not fail — NetBox ignores it — which is precisely why it
has to be declared: an ignored write is a difference the next reconcile finds again, and
PATCHes forever.

### Deleting a group is usually allowed

`tenancy.Tenant.group` is `on_delete=SET_NULL`, not `PROTECT` (`docs/netbox-schema.md` →
`tenancy.Tenant`). So deleting a group that tenants are filed under **succeeds** and clears
their `group` column. Any `NetBoxTenant` whose spec still names the group then finds drift and
PATCHes it back on the next pass — against a group that no longer exists, so the reference
stops resolving and the tenant reports `RefsResolved=False`.

That is the opposite of [`NetBoxTenant`](netboxtenant.md#deleting-a-tenant-is-usually-refused),
whose incoming FKs are almost all `PROTECT`. Both behaviours are NetBox's, and the operator
reports each as it comes.

## Printer columns

```
NAME        SLUG        PARENT   ID   READY   AGE
houses      houses               12   True    4m
rotterdam   rotterdam   houses   13   True    3m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `PARENT` | `.spec.parentRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`PARENT` reads the *intent*, so it shows a name even while the reference is unresolved — the
pair you want beside `ID` while diagnosing a pending deferral.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Group created but not nested | `Ready=False, Reason=DeferredFieldPending`, `status.deferredPending: ["parentRef"]` | The parent has no `status.id` yet | Wait; it converges in seconds. If it persists, look at the parent CR. |
| `Reason=RefNotFound` on `parentRef` | `RefsResolved=False` | No CR of that name in the namespace | Fix the name, or use `slug:` to point at a NetBox group with no CR. |
| `Reason=RefDenied` on `parentRef` | `RefsResolved=False` | `parentRef.namespace` crosses a namespace with no grant | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. |
| `Reason=Conflict` | `Ready=False` | Another namespace's CR already holds this slug, and `onConflict: Fail` | Decide who owns it; the message names the winner. |
| `Reason=Invalid` after an edit | `Ready=False` | NetBox refused the write — usually a `name` another group holds | Change `spec.name`. |
| A second group appeared after an edit | — | `spec.slug` was changed | See [Renaming the slug changes identity](#renaming-the-slug-changes-identity). |
| Children vanished when this was deleted | — | `parent` is `on_delete=CASCADE` in NetBox | Not the operator. Delete children first if you want them kept. |

## Related

- [`NetBoxTenant`](netboxtenant.md) — the kind this one groups, and the PROTECT-heavy delete
- [`NetBoxRegion`](netboxregion.md) — the other `NestedGroupModel`, with the opposite identity
- [Lookups](../concepts/lookups.md) — why a null filter is pinned, and why there is none here
- [References](../concepts/references.md) — the four ref modes and crossing a namespace
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
- [Field ownership](../concepts/field-ownership.md) — omitting `description` versus emptying it
- [ADR-0002](../decisions/0002-crd-scoping.md) — why a shared catalogue kind is namespaced
