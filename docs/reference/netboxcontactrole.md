# `NetBoxContactRole`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxContactRole` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcontactrole` |
| Status subresource | yes |

A `NetBoxContactRole` is one `tenancy.ContactRole` in NetBox: the capacity a contact is
attached to something in — `Technical`, `Billing`, `Escalation`, `Site Access`.

It is an `OrganizationalModel` with **no columns of its own** (`docs/netbox-schema.md` →
`tenancy.ContactRole`, "no own columns"), so every field here is inherited and the whole Kind
is four of them.

## Why the role is not decoration

The role is the third column of a contact assignment's **identity**. NetBox's constraint is
`(object_type, object_id, contact, role)`
([`NetBoxContactAssignment`](netboxcontactassignment.md#natural-key)), so the role is what
lets the same contact be attached to the same object twice — once as `Technical` and once as
`Billing` — and what keeps the operator from treating the second assignment as drift on the
first.

Roles are cheap and the identity depends on them. Create the ones you mean.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxContactRole
metadata:
  name: technical
  namespace: default
spec:
  endpointRef: homelab
  name: Technical
  slug: technical
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `name` | `string`, 1–100 | yes | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | yes | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |
| `description` | `string`, ≤200 | no | `description` (`OrganizationalModel`), `CharField len=200` |
| `comments` | `string` | no | `comments` (`OrganizationalModel`), `TextField` |

`description` and `comments` are clearable: omit one to leave NetBox's own value alone, set it
to `""` to clear it ([field ownership](../concepts/field-ownership.md)).

`owner` is absent. It is `ForeignKey -> users.Owner`, and the `users` app is deferred whole
([coverage](../coverage.md#endpoints)), so there is no Kind to point at.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `slug` | `?slug=<slug>` |

Column-level `UNIQUE` on `slug`, no `meta.constraints` on the model, so one candidate and no
null pin. The filter is registered:
`Meta.fields = ('id', 'name', 'slug', 'description')` on `ContactRoleFilterSet`
(NetBox 4.6.8, `netbox/tenancy/filtersets.py:74`).

`name` is `UNIQUE` too and is deliberately **not** a candidate. A Kind gets one identity, and
`slug` is the one the spec calls the role's identifier — so a rename that collides comes back
as NetBox's own `409`, reported as `Ready=False, Reason=Invalid`, rather than being adopted
under the other candidate.

Unique across the whole NetBox while these CRs are namespaced: two namespaces claiming
`technical` are claiming one role, and the second gets `Ready=False, Reason=Conflict`.

## The contrast worth knowing: its neighbour cannot use `slug` at all

[`NetBoxContactGroup`](netboxcontactgroup.md) is in the same app, the same NetBox source
file and has almost the same fields — and its natural key is `(parent, name)`, because
`NestedGroupModel.slug` carries no `UNIQUE`
(`netbox/netbox/models/__init__.py:183-186`) while `OrganizationalModel.slug` does (`:232-236`).
The **base class** decides the identity, not the app and not how catalogue-like the Kind feels.

## `status`, conditions and provenance

Identical to [`NetBoxTag`](netboxtag.md#status). An `OrganizationalModel` mixes in both
`TagsMixin` and `CustomFieldsMixin`, so this Kind is stamped in full.

## No containment parent, and no cascade in either direction

`tenancy.ContactRole` has **no foreign keys at all**, so there is nothing that could be a
containment parent ([ADR-0003](../decisions/0003-ownership-and-references.md) rule 4).

The reference pointing *at* it is
`ContactAssignment.role ForeignKey REQ -> tenancy.ContactRole on_delete=PROTECT`
(`docs/netbox-schema.md` → `tenancy.ContactAssignment`), so deleting a role that is in use is
**refused** by NetBox rather than cascading: the CR reports
`Deleting=False, Reason=Protected` naming it. Delete the assignments first.

## `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). A role is cheap to recreate; what it protects
is the assignments, and `PROTECT` is what does that.

## Printer columns

```
NAME        SLUG        ID   READY   AGE
technical   technical   14   True    2m
billing     billing     15   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Related

- [`NetBoxContactAssignment`](netboxcontactassignment.md) — why the role is part of an
  assignment's identity
- [`NetBoxContact`](netboxcontact.md) — the thing being assigned, and a lookup key no
  constraint backs
- [`NetBoxContactGroup`](netboxcontactgroup.md) — the same app, the other base class, a
  different identity
- [Lookups](../concepts/lookups.md) — candidates, ambiguity and `Conflict`
