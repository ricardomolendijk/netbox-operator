# `NetBoxClusterGroup`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxClusterGroup` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcgroup` |
| Status subresource | yes |

A `NetBoxClusterGroup` is one `virtualization.ClusterGroup` in NetBox: an administrative
grouping of clusters — `Production`, `Lab`, `Amsterdam DC`.

Field for field it is [`NetBoxClusterType`](netboxclustertype.md), and that is the honest
description rather than a missing abstraction: `bases: ContactsMixin, OrganizationalModel`
(`docs/netbox-schema.md` → `virtualization.ClusterGroup`), and the model's only own entries are
two `GenericRelation`s — `vlan_groups` and `contacts` — which are reverse relations rather than
columns, so neither is writable and neither becomes a field.

They are two Kinds rather than one "cluster catalogue" Kind with a discriminator because they
are two NetBox models at two endpoints with independent ids. A cluster carries a `type` *and* a
`group`, and a shared Kind would make that reference ambiguous exactly where NetBox's `PROTECT`
makes a wrong answer unrecoverable.

## Why the group matters more than its size suggests

`(group, name)` is the first of `virtualization.Cluster`'s two unique constraints
(`docs/netbox-schema.md` → `virtualization.Cluster`, `meta.constraints`). Setting a cluster's
`groupRef` is therefore what makes that cluster's **lookup** unambiguous:

- with `groupRef` set, a cluster is found by `?group_id=<id>&name=<name>`;
- without it, by `?name=<name>&group_id__isnull=true`, which can legitimately match more than
  one cluster and is then a `Conflict` with no write
  ([`NetBoxCluster`](netboxcluster.md#natural-key)).

Groups are cheap. Use them.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxClusterGroup
metadata:
  name: production
  namespace: default
spec:
  endpointRef: homelab
  name: Production
  slug: production
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

| Field | Type | Required | NetBox column |
|---|---|---|---|
| `name` | `string`, 1–100 | yes | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |
| `slug` | `string`, 1–100, `^[-a-zA-Z0-9_]+$` | yes | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |
| `description` | `string`, ≤200 | no | `description` (`OrganizationalModel`), `CharField len=200` |

`description` is clearable: omit it to leave NetBox's own value alone, set it to `""` to clear
it ([field ownership](../concepts/field-ownership.md)).

`comments`, `vlan_groups`, `contacts` and `cluster_count` are absent, for the reasons
[`NetBoxClusterType`](netboxclustertype.md#what-is-deliberately-absent) gives.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `slug` | `?slug=<slug>` |

Column-level `UNIQUE` on `slug`, no `meta.constraints` on the model, so one candidate and no
null pin. Unique across the whole NetBox while these CRs are namespaced: two namespaces
claiming `production` are claiming one object, and the second gets
`Ready=False, Reason=Conflict`.

## `status`, conditions and provenance

Identical to [`NetBoxClusterType`](netboxclustertype.md#status). An `OrganizationalModel` mixes
in both `TagsMixin` and `CustomFieldsMixin`, so this kind is stamped in full.

## `deletionPolicy` defaults to `Delete`

Not an IPAM kind, so `Delete` (#176 option B). `virtualization.Cluster.group` is
`on_delete=PROTECT`, so NetBox refuses to delete a group any cluster still belongs to and the
CR reports `Deleting=False, Reason=Protected` naming it.

## Printer columns

```
NAME         SLUG         ID   READY   AGE
production   production   21   True    2m
lab          lab          22   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Related

- [`NetBoxCluster`](netboxcluster.md) — why `groupRef` is part of a cluster's identity
- [`NetBoxClusterType`](netboxclustertype.md) — the same shape, and the required half
- [Lookups](../concepts/lookups.md) — why `group_id__isnull` is pinned rather than omitted
