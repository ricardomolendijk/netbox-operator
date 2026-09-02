# `NetBoxTenant`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxTenant` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbtenant` |
| Status subresource | yes |

A `NetBoxTenant` is one `tenancy.Tenant` in NetBox: the owner an IPAM or DCIM object is
filed under. `tenant ForeignKey -> tenancy.Tenant on_delete=PROTECT` appears on `ipam.VRF`,
`ipam.VLAN`, `ipam.VLANGroup`, `ipam.Prefix`, `ipam.IPAddress`, `ipam.IPRange`, `ipam.ASN`,
`ipam.ASNRange`, `dcim.Site`, `dcim.Device`, `dcim.Rack`, `virtualization.Cluster`,
`virtualization.VirtualMachine`, `wireless.WirelessLAN` and more
(`docs/netbox-schema.md`). It exists this early because none of those kinds can carry a
`tenantRef` until it does.

Two things about it are not obvious and are both load-bearing:

- **`groupRef` is part of its identity**, so a groupless tenant is a different natural key
  rather than the same key with a filter left out.
- **A `tenantRef` from another kind does not cascade.** Deleting a tenant does not delete its
  prefixes; NetBox refuses the delete instead.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTenant
metadata:
  name: donkerslootstraat
  namespace: default
spec:
  endpointRef: homelab
  name: Donkerslootstraat (RTM)
  slug: donkerslootstraat
```

That is a **groupless** tenant, looked up as
`?group_id=null&slug=donkerslootstraat`.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTenant
metadata:
  name: donkerslootstraat
  namespace: netbox-catalog
spec:
  endpointRef: homelab

  # The default. Do not set Adopt on a shared catalogue kind -- see "Two namespaces, one
  # slug" below.
  onConflict: Fail

  # The default. A tenant is not IPAM, so it is Delete rather than Retain -- and NetBox will
  # refuse the delete while anything still points at it.
  deletionPolicy: Delete

  name: Donkerslootstraat (RTM)
  slug: donkerslootstraat
  description: Donkerslootstraat 155B
  comments: |
    One tenant per physical address. Owns the per-house VRF, VLANs and prefixes.

  # Part of the identity, not just an attribute.
  groupRef:
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

The tenant's label in the NetBox UI.

**Not column-unique** (`docs/netbox-schema.md` → `tenancy.Tenant`,
`name CharField REQ len=100`). `meta.constraints` makes it unique *per group* —
`unique_group_name` on `(group, name)`, plus `unique_name` on `(name)` conditioned on
`group IS NULL` — so two tenants may share a name under different groups, and that is a
legitimate NetBox state rather than a collision.

**If it is wrong.** Empty or over 100 characters is rejected at admission. A name that
collides inside the group comes back from NetBox as a 400 or 409 carrying
`Tenant name must be unique per group.`, and the object reports
`Ready=False, Reason=Invalid` with that message verbatim. It is **not** retried with the same
payload — `ValidationError` gets a long backoff because the spec has to change first
([errors and retries](../concepts/errors-and-retries.md)). See
[Renaming can 409](#renaming-can-409).

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | yes |
| Validation | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

URL-safe identifier, and **this kind's natural key**. Also not column-unique: unique per
group, from `unique_group_slug` and the `group IS NULL` variant.

**If it is wrong.** `Not A Slug` is rejected at admission by the pattern — the controller is
never involved. A slug another tenant already holds *in the same group* is found by the
natural-key lookup, and `spec.onConflict` decides; see
[Two namespaces, one slug](#two-namespaces-one-slug).

### `spec.groupRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md) → `NetBoxTenantGroup` |
| Required | no |
| Validation | the `ObjectRef` CEL rules: exactly one of `name`, `slug`, `lookup`, `id` |

Files this tenant under a [`NetBoxTenantGroup`](netboxtenantgroup.md). Written to NetBox as
`group`, filtered as `group_id` — both spellings appear below because for a foreign key the
write name and the filter name genuinely differ.

`group ForeignKey -> tenancy.TenantGroup on_delete=SET_NULL`, so deleting the group in NetBox
clears this column rather than refusing. The next reconcile finds the drift and PATCHes it
back — against a group that no longer exists, so the reference stops resolving; see
[`NetBoxTenantGroup`](netboxtenantgroup.md#deleting-a-group-is-usually-allowed).

**It is part of the natural key**, which has two consequences worth stating plainly:

- Omitting it makes a *groupless* tenant, which is a different identity. See
  [Natural keys](#natural-keys).
- It cannot be deferred. A create that stripped a resolved `group` would be an object whose
  natural key is not the one the lookup asked about, which the descriptor validator refuses
  outright (`registry.ErrDeferredNaturalKey`).

**If it is wrong.** A malformed ref is rejected at admission. A ref naming a CR that does not
exist yet reports `RefsResolved=False, Reason=RefNotFound` **and** `Ready=False,
Reason=WaitingForRef`, and performs zero writes — which is the designed outcome, not a gap.
See [A declared group that does not resolve waits](#a-declared-group-that-does-not-resolve-waits).

### `spec.description`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | `MaxLength=200` |

Free text shown next to the tenant. Inherited from `PrimaryModel`
(`description (PrimaryModel) CharField len=200`).

Omit it to leave NetBox's own value alone; set it to `""` to clear the value in NetBox. Those
are two different instructions and the operator can tell them apart
([field ownership](../concepts/field-ownership.md)).

**If it is wrong.** Over 200 characters is rejected at admission.

### `spec.comments`

| | |
|---|---|
| Type | `string` |
| Required | no |
| Validation | none |

Long-form notes. Also inherited from `PrimaryModel`, and a `TextField` rather than a
`CharField`: no `max_length`, so there is no length marker to derive.

Omit it to leave NetBox's own value alone; set it to `""` to clear the value in NetBox.

## Natural keys

Two candidates, tried in this order, straight out of `tenancy.Tenant.meta.constraints`:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(group, slug)` — `unique_group_slug` | `?group_id=<id>&slug=<slug>` | `groupRef` **resolves** to an id |
| 2 | `slug` where `group IS NULL` — `unique_slug` | `?group_id=null&slug=<slug>` | `groupRef` was **never declared** |

The order is not a fallback chain. Candidate 2 is not "what to try if 1 fails" — it is the
identity of a *different* object, a groupless tenant.

`group_id=null` is **pinned, never omitted**, and that is the whole point. A query
with `group_id` merely left out asks "this slug in any group", so every groupless tenant
would match every tenant of that slug anywhere, adopt one, and then PATCH the group off it.
See [lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted). This is
the first non-MPTT kind where that matters, and it is covered by
[`TestTenantNaturalKeysPinGrouplessnessRatherThanOmittingIt`](../../internal/registry/tenancy_tenant_test.go).

`meta.constraints` also declares `unique_group_name` and the `name WHERE group IS NULL`
variant, so `name` is a second candidate key in the database. It is deliberately **not** a
lookup candidate — a kind gets one identity and `slug` is the stable one. What that buys is
[Renaming can 409](#renaming-can-409) rather than a silent adoption under the other key.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `deferredPending`, `provenance`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status) for what each field means and when it is
cleared.

`status.naturalKey` is worth reading on this kind in particular: it records which candidate
ran, filter by filter, so `{"group_id": "null", "slug": "donkerslootstraat"}` tells
you the engine treated the tenant as groupless.

`status.deletionAttempts` is the other one. It counts refusals, not passes, so it is how far
into the [PROTECT backoff](#deleting-a-tenant-is-usually-refused) a terminating tenant is.

`status.provenance` is the full stamp — `tenancy.Tenant` is a `PrimaryModel`, so it carries
both `tags` and `custom_fields` and is stamped when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the tenant exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `Truncated`, `DryRunPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | `groupRef` is unset, or resolved | `groupRef` is set and does not resolve | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefDenied`, `RefAmbiguous`, `RefCycle`, `RefDepthExceeded` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals: [`NetBoxTag`](netboxtag.md#conditions) and
[errors and retries](../concepts/errors-and-retries.md).

## Kind-specific behaviour

### `tenantRef` does not cascade

"Delete the tenant, delete its stuff" is a plausible expectation and it is wrong.

[ADR-0003](../decisions/0003-ownership-and-references.md) §4 lists the containment
references — `scopeRef`, `siteRef`, `clusterRef`, `deviceRef`, `vrfRef`, `prefixRef` — and
`tenantRef` is **not** among them. A tenant is an *attribute* of an object, not its
container. Concretely:

- A `NetBoxVRF` created with `tenantRef: {name: donkerslootstraat}` gets **no owner
  reference** to the tenant. `kubectl get netboxvrf -o
  jsonpath='{.metadata.ownerReferences}'` shows nothing pointing at it.
- Deleting the `NetBoxTenant` therefore does not garbage-collect the `NetBoxVRF`, or its
  prefixes, or their addresses.
- The descriptor's `ContainmentRef` is empty, which is what makes that true rather than a
  convention.

What happens instead is the next section.

### Deleting a tenant is usually refused

Almost every incoming `tenant` FK in NetBox is `on_delete=PROTECT` — the schema records it
explicitly on each row (`docs/netbox-schema.md`, e.g. `ipam.VRF`, `ipam.VLANGroup`,
`ipam.Prefix`, `ipam.IPAddress`, `ipam.ASNRange`, `circuits.CircuitGroup`). So a `DELETE` of
a tenant that anything still references comes back **409**, and the operator classifies it as
a `*netbox.ProtectedError` (`internal/netbox/errors.go`) rather than as a generic API failure.

That is a distinct path from a retry loop, and the distinction is deliberate:

| | |
|---|---|
| Error class | `*netbox.ProtectedError` — 409, or a body naming a protected relation on a non-409 status |
| Condition | `Deleting=False, Reason=Protected` |
| Message | NetBox's own body **verbatim**, plus `; attempt <n>, retrying in <d>` |
| Retried in the client? | **No.** The client retries only `TransientError` and `RateLimitError` — a 409 fails identically every time. |
| Requeue | the engine's own backoff: 10s doubling per refusal to a 5-minute cap |
| Returned as a Go `error`? | **No.** An error return would add controller-runtime's backoff on top and log a controller failure for a state only another object's deletion can fix. |
| Event | one `DeleteBlocked` warning at the third refusal, not one per attempt |
| Finalizer | stays on. `status.deletionAttempts` counts the refusals. |

**The refusal is the topological sort.** There is no deletion-ordering table anywhere in the
codebase, because NetBox's opinion about what still references what is more reliable than a
list a human would keep in step with 159 models. Delete the VRF and the next attempt
succeeds on its own ([deletion](../concepts/deletion.md)).

The case that is otherwise impossible to diagnose is a blocker in a **different namespace** —
a `NetBoxVLANGroup` in `team-blue` holding a tenant in `netbox-catalog`. That is exactly why
NetBox's message is carried through verbatim instead of being replaced with "cannot delete":

```
kubectl describe netboxtenant donkerslootstraat
...
  Deleting  False  Protected  cannot delete netbox tenancy/tenants/7: Unable to delete
                              object. The following dependent objects were found:
                              VLAN group Donkerslootstraat (RTM); attempt 4, retrying in 16s
```

To get out of it: delete the blocker, or set `deletionPolicy: Retain` on the tenant and
re-apply. The policy is read fresh on every pass rather than latched when deletion started,
precisely so that second option works
([deletion](../concepts/deletion.md#getting-out-of-a-blocked-delete)).

`deletionPolicy` defaults to `Delete` here. Retaining by default is reserved for the IPAM
kinds, where a NetBox object outliving its CR is often the point; a tenant is a catalogue
object and a CR that creates one and walks away from it is a leak nobody asked for.

### A declared group that does not resolve waits

Set `groupRef` at a group that has no `status.id` yet and the object reports two conditions
that together explain themselves:

```
RefsResolved  False  RefNotFound   groupRef -> catalogue/internal: not ready
                                   (the target has no status.id yet)
Ready         False  WaitingForRef
```

and performs **zero writes**, because a reference the spec declares is a precondition for the
write ([the rule](../concepts/reconciliation.md#a-declared-reference-is-a-precondition-for-the-write)).

Two independent reasons agree here. No natural-key candidate is applicable either: candidate 1
matches on `groupRef`, which requires it *resolved* — it is not; candidate 2 asserts `groupRef`
was never *declared* — it was. Falling through to candidate 2 would look up a groupless tenant
of that slug, find somebody else's, adopt it, and then PATCH `group` onto it — filing data the
manifest never mentioned under a group it never mentioned either. Before
[#195](https://github.com/ricardomolendijk/netbox-operator/issues/195) that was the only reason
and `Ready` reported `WaitingForKey`.

Note the contrast with [`NetBoxTenantGroup`](netboxtenantgroup.md#a-parent-applied-in-the-same-batch-converges),
whose `parentRef` *is* deferrable and whose child is created immediately. That is the one
exception to the rule, and the descriptor has to say so explicitly: `parent` is outside the
natural key there, so creating early cannot adopt the wrong object.

### Two namespaces, one slug

Every kind is namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) and there is no tier
that forces global uniqueness — but NetBox's constraints still do. `unique_group_slug` is a
database constraint, so two `NetBoxTenant`s in different namespaces with
`slug: donkerslootstraat` in the same group are **one NetBox tenant**, and the second to
apply loses.

The natural-key lookup finds the existing tenant and `spec.onConflict` decides:

| `onConflict` | What happens |
|---|---|
| `Fail` (**default**) | The second CR reports `Ready=False, Reason=Conflict` naming the namespace and CR that hold the tenant, and performs **zero writes**. Neither object is corrupted. |
| `Adopt` | Both CRs reconcile the same NetBox object. They fight over every field they disagree on, one PATCH per resync each, forever. |
| `AdoptOnly` | The same, except the CR never creates a tenant that does not already exist. |

**Do not set `Adopt` here.** NBO-021's design note reads as though `Adopt` were the default
and `Fail` the thing to opt into; the code is the other way round
(`api/v1alpha1/netboxobject_types.go`, `+kubebuilder:default=Fail`), because recovering from a
wrong adoption is a restore while opting into one is a field. So the collision is loud out of
the box and the footgun is turning that off. This is the kind most likely to be applied from
more than one namespace, and a slow-motion tug-of-war between two PATCHing controllers is far
harder to diagnose than a `Conflict` condition that names the winner. The better shape is one
catalogue namespace holding the tenants plus a [`NetBoxRefGrant`](netboxrefgrant.md) letting
team namespaces point at them.

Two tenants with *different* slugs never collide, groupless or not — the pinned
`group_id=null` narrows the query and `slug` still distinguishes them.

### Renaming can 409

`slug` is the natural key, so editing `spec.slug` does not rename the NetBox tenant — it
changes what the CR is looking for. The next reconcile finds nothing at the new slug and
creates a second tenant, leaving the first behind.

`name` is different, and this is where the second candidate key earns its keep. It is not
looked up on, so editing `spec.name` really is a rename: one PATCH. But `unique_group_name`
is still a database constraint, so a rename onto a name another tenant in the group already
holds is **refused**. That comes back as `Ready=False, Reason=Invalid` carrying NetBox's
`Tenant name must be unique per group.` and is not retried with the same payload — which is
the right shape, because nothing but a spec change can fix it.

`description`, `comments` and `groupRef` are safe to edit — except that `groupRef` is part of
the natural key, so changing it changes which object the CR is looking for, exactly as
changing `slug` does.

## Printer columns

```
NAME                SLUG                GROUP    ID   READY   AGE
donkerslootstraat   donkerslootstraat   houses   7    True    5m
acme                acme                         8    True    5m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `GROUP` | `.spec.groupRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`GROUP` reads the *intent*, so it shows a name even while the reference is unresolved and
`ID` is empty — which is exactly the pair you want side by side while diagnosing a
`WaitingForRef`.

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Nothing written, `groupRef` set | `Ready=False, Reason=WaitingForRef` + `RefsResolved=False, Reason=RefNotFound` | A declared reference did not resolve, so the write is withheld | Apply the [`NetBoxTenantGroup`](netboxtenantgroup.md), or drop `groupRef` if the tenant really is groupless. |
| Nothing written, no `groupRef` | `Ready=False, Reason=WaitingForKey` | Not expected — check `spec.slug` is non-empty | — |
| `Reason=RefDenied` on `groupRef` | `RefsResolved=False` | `groupRef.namespace` crosses a namespace with no grant | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in the target namespace. |
| `Reason=Conflict` | `Ready=False` | Another namespace's CR already holds this slug in this group, with `onConflict: Fail` | Decide who owns it; the message names the winner. |
| `Reason=Invalid` after editing `spec.name` | `Ready=False` | `unique_group_name` — another tenant in the group holds that name | Pick a different name. See [Renaming can 409](#renaming-can-409). |
| A second tenant appeared after an edit | — | `spec.slug` or `groupRef` was changed | Both are natural-key fields. See [Renaming can 409](#renaming-can-409). |
| CR stuck `Terminating`, `deletionAttempts` climbing | `Deleting=False, Reason=Protected` | Something in NetBox still references the tenant | Read the message — it names the blocker. Delete it, or set `deletionPolicy: Retain`. |
| Deleting the tenant did not delete its prefixes | — | `tenantRef` is not a containment reference | Working as designed. See [`tenantRef` does not cascade](#tenantref-does-not-cascade). |
| `group` keeps being PATCHed back | `Synced=False` | The group was deleted in NetBox (`SET_NULL`) while the CR still names it | Recreate the group, or remove `groupRef`. |

## Related

- [`NetBoxTenantGroup`](netboxtenantgroup.md) — the kind `groupRef` points at
- [Deletion](../concepts/deletion.md) — the finalizer, and a `PROTECT`-blocked delete in full
- [Errors and retries](../concepts/errors-and-retries.md) — which NetBox failure becomes which typed error
- [Lookups](../concepts/lookups.md) — why `group_id=null` is pinned rather than omitted
- [References](../concepts/references.md) — the four ref modes and crossing a namespace
- [Field ownership](../concepts/field-ownership.md) — omitting `description` versus emptying it
- [ADR-0003](../decisions/0003-ownership-and-references.md) — why a NetBox FK is not an owner reference
