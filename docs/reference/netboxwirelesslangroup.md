# `NetBoxWirelessLANGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxWirelessLANGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbwlangroup` |
| Status subresource | yes |

A `NetBoxWirelessLANGroup` is one `wireless.WirelessLANGroup` in NetBox: a named, nestable
folder that SSIDs are filed under.

> ### The third `NestedGroupModel`, and the third identity out of one base class
>
> The tree shape never decides the natural key — the **constraint lines** do, and this
> model's are a third arrangement again. It has *both* mechanisms and lands where
> [`NetBoxTenantGroup`](netboxtenantgroup.md) does: `slug` alone, no `parent` term, and **no
> `parent IS NULL` variant at all**. See [natural key](#natural-key) for why adding one would
> be a bug rather than an extra safety net.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxWirelessLANGroup
metadata:
  name: donkerslootstraat-wifi
  namespace: default
spec:
  endpointRef: homelab
  name: Donkerslootstraat Wi-Fi
  slug: donkerslootstraat-wifi
```

A top-level group. There is nothing to say about that: unlike on
[`NetBoxRegion`](netboxregion.md), a top-level group is not a different identity from a nested
one here.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxWirelessLANGroup
metadata:
  # A DNS-1123 label, and unrelated to spec.name below.
  name: donkerslootstraat-wifi
  namespace: default
spec:
  # The NetBoxEndpoint to write through, in this namespace.
  endpointRef: homelab

  # Shared-envelope defaults, written out.
  onConflict: Fail
  deletionPolicy: Delete

  name: Donkerslootstraat Wi-Fi
  slug: donkerslootstraat-wifi

  # Nesting is allowed and is deliberately outside the natural key, so a child applied in
  # the same batch as its parent is created top-level and PATCHed once the parent resolves.
  parentRef:
    name: homelab-wifi

  description: SSIDs served in this house
  comments: Managed by netbox-operator.
```

## `spec`

`endpointRef`, `onConflict`, `deletionPolicy`, the `driftMode` override, `tags` and
`customFields` come from the shared envelope and behave identically on every kind — see
[`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `name` | `string` | **yes** | `name`, `CharField len=100 unique=True` |
| `slug` | `string` | **yes** | `slug`, `SlugField len=100 unique=True` |
| `parentRef` | [ref](../concepts/references.md) → `NetBoxWirelessLANGroup` | no | `parent`, `TreeForeignKey -> self on_delete=CASCADE` |
| `description` | `string` | no | `description`, `CharField len=200` |
| `comments` | `string` | no | `comments`, `TextField` |

`comments` is present here and absent on the `OrganizationalModel` group kinds — see
[why this one has `comments`](#why-this-one-has-comments).

### `spec.name`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100` |

The group's label in the NetBox UI. **Column-unique across NetBox**
(`netbox/wireless/models.py:53-58`), so two groups may not share a name even under different
parents — which is exactly what makes the `unique(parent, name)` table constraint redundant
rather than identity-bearing.

**If it is wrong:** admission enforces the length. A name another group already holds is
NetBox's own `400` at reconcile, surfacing as `Ready=False, Reason=Invalid` with NetBox's
message verbatim.

### `spec.slug`

| | |
|---|---|
| Type | `string` |
| Required | **yes** |
| Validation | `MinLength=1`, `MaxLength=100`, `Pattern=^[-a-zA-Z0-9_]+$` |

The group's URL-safe identifier, and **this kind's whole natural key**.

NetBox enforces uniqueness on it globally (`netbox/wireless/models.py:59-63`) while this CRD is
namespaced, so two `NetBoxWirelessLANGroup`s in different namespaces claiming one slug are one
group and a `Conflict` — not two groups. Same shape as every other globally-unique slug in this
API; see [`NetBoxTag`](netboxtag.md#a-slug-is-global-and-this-crd-is-not).

**If it is wrong:** the pattern is rejected at admission (`kubectl apply` fails, nothing is
stored). Editing it after a successful reconcile does *not* re-look-up: the object is reconciled
by `status.id` and the edit becomes a `PATCH` renaming the slug on the same row.

### `spec.parentRef`

| | |
|---|---|
| Type | `ObjectRef` → `NetBoxWirelessLANGroup` |
| Required | no |
| Deferred | when unresolved (`DeferIfUnresolved`) |

Nests this group under another one. Self-referential: `parent TreeForeignKey -> self
on_delete=CASCADE` on `NestedGroupModel` (`netbox/netbox/models/__init__.py:171-178`).

**Not part of the natural key**, for the reason in [natural key](#natural-key) — which is
precisely what makes it deferrable. A group whose parent does not exist yet is still
identifiable by its slug, so the engine creates it **top-level** and `PATCH`es `parent` on once
the reference resolves. A parent and a child applied in one batch converge without waiting out
a resync.

`IfUnresolved` and not `Always`: a *resolved* parent belongs in the create payload. Stripping it
unconditionally would leave the object top-level for one pass, which is a visible wrong state in
NetBox for no gain (NBO-015).

**If it is wrong:**

- A cycle — two groups naming each other, or a longer ring — is rejected by the
  [validating webhook](../operations/admission-webhooks.md) before it reaches NetBox. Nothing
  kind-specific makes that work: the webhook serves one path across the whole API group and
  walks the reference graph from the `Descriptor`.
- An unresolved parent is `Ready=False` with `parentRef` in `status.deferredPending` and
  `RefsResolved` naming the field. The group **exists in NetBox** meanwhile, top-level. That is
  the difference from [`NetBoxDeviceRole`](netboxdevicerole.md), where the parent *is* in the
  identity and an unresolved one writes nothing at all.
- A parent in another namespace needs a [`NetBoxRefGrant`](netboxrefgrant.md), and gets no owner
  reference — see [`ParentOwned`](#conditions).

### `spec.description`, `spec.comments`

Both have all three states: omit to leave NetBox's own value alone, set to `""` to clear it, set
to a string to write it ([field ownership](../concepts/field-ownership.md)). `comments` is a
`TextField` with no `max_length`, so there is no `MaxLength` marker to derive.

## Natural key

One candidate. No `parent` term, and **no null pin**:

| # | Candidate | Query |
|---|---|---|
| 1 | `slug` | `?slug=<slug>` |

### Why there is no `parent IS NULL` variant

The three `NestedGroupModel` arrangements shipped so far, each read straight off its model:

| Model | `meta.constraints` | Column-level `UNIQUE` | Candidates |
|---|---|---|---|
| `dcim.Region`, `dcim.SiteGroup`, `dcim.Location` | `(parent, name)`, `(name)` **with `condition=Q(parent__isnull=True)`**, and the same pair for `slug` (`netbox/dcim/models/sites.py:62-82`) | none | two, and a `?parent_id=null` pin |
| `tenancy.TenantGroup` | **none at all** | `name`, `slug` | one, no pin ([#203](https://github.com/ricardomolendijk/netbox-operator/issues/203)) |
| `wireless.WirelessLANGroup` | `(parent, name)` — **with no `condition=` clause of any kind** (`netbox/wireless/models.py:70-75`) | `name` (`:53-58`), `slug` (`:59-63`) | one, no pin |

So this model has *both* mechanisms and still lands where `TenantGroup` does. Two readings do
the work:

- `(parent, name)` is **strictly weaker** than the column-level `UNIQUE` on `name` that already
  makes a name globally unique. It adds nothing to the identity.
- `slug` is column-unique, so it identifies at most one group whatever its parent is.

Adding the `?parent_id__empty=true` pin that plan.md §8.1 asserts every MPTT kind needs would be
wrong the same two ways it would be wrong on a `NetBoxTenantGroup`: it would make a **nested**
group's slug unfindable — the request would match nothing and the engine would create a second
row — and it would express a constraint the database does not have.

`slug` is in `WirelessLANGroupFilterSet`'s `Meta.fields`
(`netbox/wireless/filtersets.py:47-49`). `name` is column-unique too and deliberately is *not* a
second candidate: a kind gets one identity, and `slug` is the one the spec calls the group's
identifier.

## `status`

Identical to every other object kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `deferredPending`, `observedGeneration`,
`conditions`. See [`NetBoxTag`](netboxtag.md#status).

**Nothing is cleared on failure**, `status.id` included — which is what lets a group whose
parent still will not resolve keep reconciling by id.

`status.deferredPending` is the one to watch on this kind: it holds `parentRef` for as long as
the parent is unresolved, and empties on the pass that `PATCH`es it on.

`status.naturalKey` is `{"slug": "<slug>"}` and nothing else. If you ever see a `parent_id` term
in it, the descriptor has acquired a candidate it should not have.

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `WaitingForRef`, `DeferredFieldPending`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun`, `DriftReported` |
| `RefsResolved` | `parentRef` resolved, or is absent | it did not | `AllResolved`, `RefNotFound`, `RefNotReady`, `RefTargetFailed`, `RefAmbiguous`, `RefDenied`, `RefCycle` |
| `DriftDetected` | NetBox differs from the spec | it does not | `NoDrift`, `DriftDetected` |
| `ParentOwned` | the parent group's CR owns this one | it cannot | `ParentOwned`, `CascadeUnavailable`, `ParentOwnershipDisabled` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

Reason glossary and retry intervals are shared; see
[errors and retries](../concepts/errors-and-retries.md). The two that mean something particular
here:

- **`DeferredFieldPending`** on `Ready`: the group is in NetBox, top-level, and the parent is
  owed. Not a stall — the ref watch re-enqueues it the moment the parent goes `Ready`.
- **`Conflict`** on `Ready`: another group already holds this slug and this CR did not create
  it. `slug` is globally unique in NetBox and this CRD is namespaced, so the two CRs are two
  claims on one group.

## Kind-specific behaviour

### `parentRef` is the containment parent

`parent` is the only foreign key on this kind and NetBox cascades through it
(`on_delete=CASCADE`), so by [ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 it
is the containment reference: a nested group's CR carries a non-controller owner reference to
its parent's CR, and `kubectl delete` on the parent takes the children with it.

**It matters here for the same reason it matters on
[`NetBoxTenantGroup`](netboxtenantgroup.md) and not on the dcim nested groups**
([#203](https://github.com/ricardomolendijk/netbox-operator/issues/203)): this kind's single
candidate is `slug` alone, so it stays applicable when the parent is gone, finds nothing, and
create-if-absent re-creates a row NetBox cascade-deleted. The dcim groups are saved from that by
their candidates all reading `parent_id` or pinning it null, which this one does neither of. The
owner reference is what removes the CR before that pass can happen.

An owner reference is only filed when it is legal — same namespace, parent resolved by `name`.
Otherwise `ParentOwned=False, Reason=CascadeUnavailable`
([ownership](../concepts/ownership.md)).

### `_depth` and `_children` are never written

MPTT's two denormalised caches. NetBox maintains them as the tree changes; writing either does
not fail, it silently no-ops, so the next reconcile finds the same difference and `PATCH`es
again forever. Both are in the descriptor's `ReadOnly` list and no spec field maps onto either
([drift detection](../concepts/drift.md)).

### Why this one has `comments`

`wireless.WirelessLANGroup` is a `NestedGroupModel` and its serializer accepts `comments`, so
the field is mapped. The `OrganizationalModel` group kinds
([`NetBoxRegion`](netboxregion.md), [`NetBoxSiteGroup`](netboxsitegroup.md),
[`NetBoxTenantGroup`](netboxtenantgroup.md), the two cluster groupings) deliberately do not map
it — a recorded decision, not an inconsistency, and the reasoning is on
`api/v1alpha1/virtualization_clustertype.go` and in [coverage](../coverage.md).

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` ([deletion](../concepts/deletion.md#the-default-depends-on-the-kind)).
Deleting the CR deletes the group, and NetBox's `SET_NULL` on `WirelessLAN.group` means the
SSIDs filed under it survive with no group rather than blocking the delete — so a delete here is
rarely refused, and rarely as harmless as it looks.

## Printer columns

```
NAME                    SLUG                     PARENT          ID   READY   AGE
homelab-wifi            homelab-wifi                             41   True    9m
donkerslootstraat-wifi  donkerslootstraat-wifi    homelab-wifi    42   True    5m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `PARENT` | `.spec.parentRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` rejected, slug pattern | admission, nothing stored | a space, a dot or a slash in `spec.slug` | `^[-a-zA-Z0-9_]+$` |
| `kubectl apply` rejected, "reference cycle" | admission | `parentRef` closes a ring | The webhook walked the graph. Break the ring |
| `Ready=False`, `Reason=DeferredFieldPending`, `deferredPending: [parentRef]` | reconcile; the group **is** in NetBox, top-level | the parent CR does not exist or is not `Ready` | Expected while a batch converges. Apply the parent; the child re-enqueues on its own |
| `Ready=False`, `Reason=Conflict` | reconcile, zero writes | another group holds this slug and this CR did not create it | `slug` is globally unique in NetBox. Pick another, or adopt deliberately with `onConflict: Adopt` |
| `Ready=False`, `Reason=Invalid` on a rename | reconcile, long backoff | the column-level `UNIQUE` on `name` — another group holds that name | The message is NetBox's own. Pick another name |
| The group is nested in NetBox and `parentRef` is unset | none | absent means "do not manage" | Write `parentRef`, or clear it in the NetBox UI. There is no empty form of a single reference |
| `ParentOwned=False`, `Reason=CascadeUnavailable` | reconcile | the parent is in another namespace, or referenced by `id` | Expected for a shared catalogue tree. An owner reference cannot cross a namespace ([ADR-0003](../decisions/0003-ownership-and-references.md)) |
| A group reappeared after its parent was deleted | none | the owner reference was never filed — see the row above | The containment reference is what normally prevents this. Delete the child CR |
| `_depth` never changes when expected | none | it is server-maintained and `ReadOnly` | NetBox recomputes it as the tree changes |
| The SSIDs lost their group when this was deleted | none | `WirelessLAN.group` is `on_delete=SET_NULL` | Not a cascade. Re-file them, or use `deletionPolicy: Retain` |

## Related

- [`NetBoxWirelessLAN`](netboxwirelesslan.md) — the kind that points here through `groupRef`,
  and why that reference is not *its* containment parent
- [`NetBoxTenantGroup`](netboxtenantgroup.md) — the other `NestedGroupModel` with `slug` alone,
  and the same resurrection risk
- [`NetBoxRegion`](netboxregion.md) — the arrangement this one is contrasted against: `parent` in
  the identity, and the `parent IS NULL` variant
- [`NetBoxDeviceRole`](netboxdevicerole.md) — the `NestedGroupModel` whose parent is in the
  identity, so an unresolved one writes nothing
- [References](../concepts/references.md) — the four modes, grants, cycles, watches
- [Ownership](../concepts/ownership.md) — containment references and `CascadeUnavailable`
- [The admission webhook](../operations/admission-webhooks.md) — the cycle check, and why it
  needs no per-kind entry
- [Field ownership](../concepts/field-ownership.md) — absent, empty and set
- [Deletion](../concepts/deletion.md#the-default-depends-on-the-kind) — why this kind defaults to
  `Delete`
