# `NetBoxTunnelGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxTunnelGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbtunnelgroup` |
| Status subresource | yes |

A `NetBoxTunnelGroup` is one `vpn.TunnelGroup` in NetBox: a named grouping of tunnels —
`Branch offices`, `Partners`, `Lab`. It is written to `vpn/tunnel-groups`.

**It is not a hierarchy**, and it is not a set of crypto settings either. A tunnel group holds
no algorithms, no policy and no encapsulation; it is a label, and the only thing that makes it
load-bearing is that [`NetBoxTunnel`](netboxtunnel.md)'s identity is keyed on it — declaring
`groupRef` on a tunnel is what moves that tunnel from one natural-key candidate to the other.

Eight of the `vpn` app's ten models ship as Kinds. `vpn.TunnelTermination` and
`vpn.L2VPNTermination` are **deferred**: the identity of each is a generic foreign key
(`(termination_type, termination_id)` and `(assigned_object_type, assigned_object_id)`), which
is a different piece of machinery from anything on this Kind. What terminates on a tunnel is
set in NetBox until those Kinds ship.

## No columns of its own

The schema entry is short enough to quote in full
(`docs/netbox-schema.md` → `vpn.TunnelGroup`):

```
## vpn.TunnelGroup   (vpn/models/tunnels.py)
   bases: ContactsMixin, OrganizationalModel
   (no own columns — every field is inherited from ContactsMixin, OrganizationalModel)
     contacts (ContactsMixin)           GenericRelation
     name (OrganizationalModel)         CharField   REQ UNIQUE len=100
     slug (OrganizationalModel)         SlugField   REQ UNIQUE len=100
     description (OrganizationalModel)  CharField   len=200
     comments (OrganizationalModel)     TextField
   meta.ordering: ('name',)
```

No `parent`, no MPTT base, no `site`, and **no `meta.constraints`**. So this Kind has no
`parentRef`, no `parent IS NULL` natural-key variant, no tree and nothing for a cycle check to
check — the [`NetBoxRackGroup`](netboxrackgroup.md) shape exactly, one app over. Its identity is
`slug` alone, from `OrganizationalModel`'s column-level `UNIQUE`.

`ContactsMixin` contributes a `GenericRelation` only — a reverse accessor, never on the write
path (`hack/testdata/ir-4.6.8.json.gz` → `vpn.TunnelGroup.fields`, `contacts`) — so it adds no
field here.

#59's ticket footnotes a "schema gap: `vpn.TunnelGroup` has an endpoint but no model entry". At
4.6.8 the entry exists and is the one quoted above; `bases:` is the answer rather than the gap.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTunnelGroup
metadata:
  name: branch-offices
  namespace: default
spec:
  endpointRef: homelab
  name: Branch offices
  slug: branch-offices
```

That is the whole Kind, bar two optional free-text fields.

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxTunnelGroup
metadata:
  name: branch-offices
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Branch offices
  slug: branch-offices
  description: Site-to-site tunnels terminating on the branch routers
  comments: |
    One tunnel per branch. The crypto is shared: see the IPSec profile.
```

## `spec`

`endpointRef`, `onConflict` and `deletionPolicy` come from the shared envelope and behave
identically on every kind — see [`NetBoxTag`](netboxtag.md#specendpointref) for the full
treatment of each.

| Field | Type | Required | Default | NetBox column |
|---|---|---|---|---|
| `name` | `string`, 1–100 | yes | — | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | yes | — | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |
| `description` | `string`, ≤200 | no | — | `description` (`OrganizationalModel`), `CharField len=200` |
| `comments` | `string` | no | — | `comments` (`OrganizationalModel`), `TextField` |

### `spec.name`

Required. The group's name, up to 100 characters, and **column-unique across the whole NetBox**
— there is no parent to scope it by.

It is a candidate key and deliberately not the lookup key: a kind gets one identity and `slug`
is the stable one, so a rename that collides comes back as NetBox's own 409 reported as
`Ready=False, Reason=Invalid` rather than being adopted under the other candidate.

### `spec.slug`

Required. URL-safe identifier, up to 100 characters, matching `^[-a-zA-Z0-9_]+$`. This kind's
natural key, and **globally unique** across the whole NetBox.

It is usable as an identity *because* the base class is `OrganizationalModel`:
`OrganizationalModel.slug` carries `UNIQUE` and `NestedGroupModel.slug` does not. Same
derivation as [`NetBoxRackGroup`](netboxrackgroup.md#the-base-class-decides-not-the-name).

### `spec.description`

Optional free text, up to 200 characters.

Omit the key to leave NetBox's own value alone; set it to `""` to clear it. Absent, empty and
set are three states and the operator tells them apart from `metadata.managedFields` — see
[field ownership](../concepts/field-ownership.md).

### `spec.comments`

Optional long-form notes. A `TextField` rather than a `CharField`, so it has no `max_length` and
there is no length marker to derive from one.

Mapped here, unlike on the six organisational kinds that shipped in M8 and left it out: the
kinds that shipped after `dcim.RackGroup` map it, and adding a field is additive while removing
one is not.

Clearable on the same three-state terms as `description`.

## Natural keys

One candidate, and no conditional variant:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `slug` | `?slug=<slug>` | always |

`vpn.TunnelGroup` declares **no `meta.constraints` at all** — its `Meta` carries only
`ordering: ('name',)`. So the identity does not come from a constraint list; it comes from
`OrganizationalModel`'s *column-level* `UNIQUE` on `slug`. Uniqueness is global, so there is
nothing to pin to null and nothing a candidate could be conditional on.

The filter is registered: `slug` is in `TunnelGroupFilterSet.Meta.fields` (NetBox 4.6.8,
`netbox/vpn/filtersets.py:32`).

Same derivation as [`NetBoxRackGroup`](netboxrackgroup.md),
[`NetBoxRackRole`](netboxrackrole.md) and [`NetBoxContactRole`](netboxcontactrole.md).

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status) for what each field means and when it is cleared.

`vpn.TunnelGroup` is an `OrganizationalModel`, so it carries both `tags` and `custom_fields` and
is stamped in full when the endpoint's
[`spec.managedBy`](netboxendpoint.md#specmanagedby) is set. See
[provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the group exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForKey`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending`, `ReportPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftReported`, `DriftDetectedDryRun` |
| `RefsResolved` | always — this kind holds no references | never | `AllResolved` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |

`RefsResolved` is unconditionally `True` here: there is no state in which this Kind waits for
another object, so no `WaitingForRef` and no `RefCycle` are reachable.

## Kind-specific behaviour

### A hand-made group is adopted, not duplicated

`slug` is column-unique, so the lookup finds an existing row and the engine takes it over:
`status.adopted=true`, and one group in NetBox rather than two. Creating a second one would be
refused by the unique index anyway, so adoption is the only outcome that works.

### Two namespaces claiming one slug is one group

NetBox's uniqueness is a database constraint and a namespace boundary does not partition it. The
first CR to reconcile creates or adopts the group; the second finds it already claimed and
reports `Ready=False, Reason=Conflict` naming the winning namespace
([ADR-0002](../decisions/0002-crd-scoping.md)).

### No containment parent, in either direction

`vpn.TunnelGroup` has **no foreign key at all** bar `owner`, so there is nothing that could be a
containment parent ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

The reference pointing *at* it is `Tunnel.group ForeignKey -> vpn.TunnelGroup
on_delete=PROTECT` (`docs/netbox-schema.md` → `vpn.Tunnel`), so deleting a group that still has
tunnels is **refused** by NetBox rather than cascading: the CR reports
`Deleting=False, Reason=Protected` naming the blocker. Move the tunnels out of the group, or
delete them, first.

That is also why a `NetBoxTunnel` takes no owner reference from its group — see
[`NetBoxTunnel`](netboxtunnel.md#no-containment-parent-anywhere-on-this-kind).

### `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A tunnel group is a label a manifest recreates
verbatim; deleting one frees no resource anybody else can take, which is what `Retain` was
reserved for. See [deletion](../concepts/deletion.md).

### `tunnel_count` is never written

`tunnel_count` is a `CounterCacheField` NetBox maintains from the tunnels pointing at the group,
it is in the serializer's write path and read-only there
(`hack/testdata/ir-4.6.8.json.gz` → `vpn.TunnelGroup.write_path`), and it is in the descriptor's
read-only list. Writing it would not fail — NetBox drops it — which is precisely why it has to
be declared: a dropped write produces a difference the next reconcile finds again, and PATCHes
forever.

### Renaming the slug changes identity

`slug` is the natural key, so editing it does not rename the NetBox group — it changes what the
CR is looking for, and the next reconcile creates a second group, leaving the first behind. It
also moves every tunnel keyed on this group onto a different lookup. `name`, `description` and
`comments` are safe to edit.

### What is not here yet

`owner` is `ForeignKey -> users.Owner` and the whole `users` app is an excluded endpoint
(`hack/coverage-exclusions.yaml`), so there is no Kind to point at. `contacts` is a
`GenericRelation` — the far end of somebody else's foreign key — and a contact on a tunnel group
is written from [`NetBoxContactAssignment`](netboxcontactassignment.md) when that union grows a
member for it. `tags` and `customFields` are written by the provenance stamp and not by a user.

Nothing else is missing: this model has four writable columns of substance and the CRD maps all
four.

## Printer columns

```
$ kubectl get nbtunnelgroup
NAME             SLUG             ID   READY   AGE
branch-offices   branch-offices   91   True    2m
partners         partners         92   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

There is no `PARENT` column, and that is deliberate rather than an omission — see
[no columns of its own](#no-columns-of-its-own).

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `Ready=False`, `Reason=Conflict` | `Ready` | Another namespace already owns this slug, or one NetBox object matched and `onConflict` is `Fail`. `status.naturalKey` shows what was searched. | Pick a different slug, or set `onConflict: Adopt` in the namespace that should own it. |
| `Ready=False`, `Reason=Invalid`, message names `name` | `Ready` | A rename collided with the column-level `UNIQUE` on `name`, which is global here. | Pick another name; `slug` is what identifies the object. |
| `Ready=False`, `Reason=WaitingForEndpoint` | `Ready` | The [`NetBoxEndpoint`](netboxendpoint.md) named by `endpointRef` is not `Ready`. | Fix the endpoint; the group re-enqueues off its event. |
| `Deleting=False`, `Reason=Protected` | `Deleting` | A tunnel still points at this group — `Tunnel.group` is `PROTECT`. | Move or delete those tunnels, or set `deletionPolicy: Retain`. |
| A second group appeared after an edit | — | `spec.slug` was changed. | See [renaming the slug changes identity](#renaming-the-slug-changes-identity). |
| `spec.parentRef` rejected by `kubectl apply` | — | There is no such field. `vpn.TunnelGroup` has no `parent` column. | Group tunnels by one flat label. |

## Related

- [`NetBoxTunnel`](netboxtunnel.md) — the kind that points at this one, and whose identity turns on whether it does
- [`NetBoxRackGroup`](netboxrackgroup.md) — the same `OrganizationalModel`-with-no-columns shape in `dcim`
- [`NetBoxIPSecProfile`](netboxipsecprofile.md) — the other thing a tunnel points at, and the one that carries the crypto
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
- [Deletion](../concepts/deletion.md) — what `PROTECT` does to a delete
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
