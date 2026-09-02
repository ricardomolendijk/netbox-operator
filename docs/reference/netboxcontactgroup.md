# `NetBoxContactGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxContactGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcontactgroup` |
| Status subresource | yes |

A `NetBoxContactGroup` is one `tenancy.ContactGroup` in NetBox: a tree of buckets to file
contacts in — `Operations` → `Network`, `Vendors` → `Transit`.

It is the **third `NestedGroupModel`** to ship, and the one that settles that the tree shape
decides nothing about the identity. One base class, three different natural keys:

| Kind | `meta.constraints` | Natural key |
|---|---|---|
| [`NetBoxRegion`](netboxregion.md), [`NetBoxSiteGroup`](netboxsitegroup.md) | `(parent, name)` **and** `(name)` where `parent IS NULL` | `(parent, name)` + the pinned variant |
| [`NetBoxTenantGroup`](netboxtenantgroup.md) | none; column `UNIQUE` on `name` and `slug` | `slug` alone, no `parent` filter |
| **`NetBoxContactGroup`** | `(parent, name)` **only** | `(parent, name)` + a pinned variant no constraint backs |

An MPTT kind is usually assumed to be `(parent, name)` plus a `parent IS NULL` variant. That
is right about the first half here and wrong about the second, and the difference is visible in
NetBox's own source: `dcim.Region` and `dcim.SiteGroup` declare the conditional constraint
(`netbox/dcim/models/sites.py:62-67` and `:133-143`), `tenancy.ContactGroup` declares one
constraint and stops (`netbox/tenancy/models/contacts.py:53-58`).

## Why the key is `name` and not `slug`

`slug` is required here and **not unique**. `NestedGroupModel.slug` is a plain
`SlugField(max_length=100)` with no `unique=True` (`netbox/netbox/models/__init__.py:183-186`),
and this model adds none — unlike `OrganizationalModel.slug`, which is `unique=True` (`:232-236`)
and is why [`NetBoxContactRole`](netboxcontactrole.md) next door *is* keyed on its slug.

A `slug` candidate here would therefore find whichever group came back first and PATCH somebody
else's row. `slug` is still required, because NetBox requires it; it is just not an identity.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxContactGroup
metadata:
  name: operations
  namespace: default
spec:
  endpointRef: homelab
  name: Operations
  slug: operations
---
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxContactGroup
metadata:
  name: operations-network
  namespace: default
spec:
  endpointRef: homelab
  name: Network
  slug: operations-network
  parentRef:
    name: operations
```

Apply them in either order. A child whose parent has no NetBox id yet matches **neither**
candidate, so the engine waits and is woken by the parent becoming `Ready` — not by the
endpoint's resync.

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `name` | `string`, 1–100 | yes | `name` (`NestedGroupModel`), `CharField REQ len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | yes | `slug` (`NestedGroupModel`), `SlugField REQ len=100` |
| `parentRef` | [ref](../concepts/references.md) → `NetBoxContactGroup` | no | `parent`, `TreeForeignKey -> tenancy.ContactGroup on_delete=CASCADE` |
| `description` | `string`, ≤200 | no | `description` (`NestedGroupModel`), `CharField len=200` |
| `comments` | `string` | no | `comments` (`NestedGroupModel`), `TextField` |

`description` and `comments` are clearable: omit one to leave NetBox's own value alone, set it
to `""` to clear it ([field ownership](../concepts/field-ownership.md)).

`_depth`, `_children` and `contact_count` are read-only. NetBox maintains them from the tree and
from the reverse relation, and writing one does not fail — it silently no-ops, so the next read
finds the same difference and the operator PATCHes again forever
(`docs/netbox-schema.md`, preamble on `_`-prefixed columns).

`contacts` is absent: it is `ContactsMixin`'s `GenericRelation`, a read-only reverse view of
somebody else's foreign key. The way to attach a contact is a
[`NetBoxContactAssignment`](netboxcontactassignment.md), and the way to put a contact *in* this
group is [`NetBoxContact.spec.groups`](netboxcontact.md#groups) — which is a many-to-many owned
by the contact, not by the group.

`owner` is absent: `ForeignKey -> users.Owner`, and the `users` app is deferred whole
([coverage](../coverage.md#endpoints)).

## Natural key

| # | Candidate | Query | Applies when |
|---|---|---|---|
| 1 | `(parent, name)` | `?parent_id=<id>&name=<name>` | `parentRef` is declared **and** resolved |
| 2 | `name`, `parent` pinned null | `?parent_id=null&name=<name>` | `parentRef` was never declared |

Both filters are registered:
`parent_id = ModelMultipleChoiceFilter(queryset=ContactGroup.objects.all())`
(NetBox 4.6.8, `netbox/tenancy/filtersets.py:34-38`) and `name` from
`Meta.fields = ('id', 'name', 'slug', 'description')` (`:67`).

The order is not a fallback. A nested group is identified by the first and a top-level one by
the second, and a group whose parent is declared but has not been created yet matches
**neither** — so the engine waits rather than adopting an unrelated top-level group of the same
name and then reparenting somebody else's data.

### The pin, and what it does and does not promise

`?parent_id=null` is the sentinel spelling, because `parent` is a foreign key and its filter is
a model-choice one. A numeric column would need `?parent_id__empty=true` instead; the two are
not interchangeable and the wrong one is registered nowhere, which returns the **unfiltered**
set rather than an error (#206). The column class is declared on the candidate so this cannot be
got wrong per kind.

What the pin is *for* is the same as everywhere: omitting `parent_id` asks "this name under any
parent", so every top-level group would match a nested group of the same name and adopt it
([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

What it does **not** promise, and this is where this Kind differs from
[`NetBoxRegion`](netboxregion.md): candidate 2 is backed by no constraint. Postgres treats
`NULL`s as distinct, so `unique_parent_name` does not fire between two top-level groups and
NetBox will happily hold two called `Operations`. More than one match is therefore real server
state, and it is reported as `Ready=False, Reason=Conflict` with no write rather than resolved
by taking the first.

## `parentRef` is the containment parent

`parent` is `on_delete=CASCADE`, so deleting a group in NetBox deletes its descendants
server-side. By [ADR-0003](../decisions/0003-ownership-and-references.md) rule 4 that makes it
the containment reference: a nested group's CR carries a non-controller owner reference to its
parent's CR, so `kubectl delete` on the parent takes the children with it.

Not a stylistic choice, and not about how catalogue-like the Kind feels — it is *whichever
foreign key the server cascades*, and it is the only foreign key this Kind has, so there is no
tiebreak to make. Without the owner reference the child CR outlives the row NetBox deleted,
finds nothing at `.status.id` on the next reconcile, and the engine's create-if-absent step
recreates a group NetBox deliberately removed.

A parent in a different namespace gets no owner reference and the object reports
`CascadeUnavailable` naming `parentRef` — see [ownership](../concepts/ownership.md).

## No deferral, unlike `NetBoxTenantGroup`

`parent` is matched on by candidate 1, so it cannot be deferred: `DeferAlways` is refused at
boot (`registry.ErrDeferredNaturalKey`) because the lookup would then ask a different question
from the create it decided on. `DeferIfUnresolved` would be dead data, because a declared but
unresolved parent makes neither candidate applicable and the engine never reaches a create.

[`NetBoxTenantGroup`](netboxtenantgroup.md) *does* defer its parent, and the difference is
exactly its identity: keyed on `slug` alone, it stays identifiable with the parent outstanding.

## `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). Deleting a group in NetBox cascades to its
descendant groups; it does **not** delete the contacts in it — `Contact.groups` is a
many-to-many, so the join rows go and the contacts stay.

## Printer columns

```
NAME                 SLUG                 PARENT       ID   READY   AGE
operations           operations                        18   True    3m
operations-network   operations-network   operations   19   True    3m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `PARENT` | `.spec.parentRef.name` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Related

- [`NetBoxContact`](netboxcontact.md) — the many-to-many that puts a contact in this group
- [`NetBoxContactRole`](netboxcontactrole.md) — the same app, the other base class, keyed on
  `slug`
- [`NetBoxRegion`](netboxregion.md) — the same shape *with* the conditional constraint
- [`NetBoxTenantGroup`](netboxtenantgroup.md) — the same base class with global uniqueness
- [Lookups](../concepts/lookups.md) — why a null filter is pinned and never omitted
- [Ownership](../concepts/ownership.md) — containment references and `CascadeUnavailable`
