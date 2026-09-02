# `NetBoxClusterType`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxClusterType` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbctype` |
| Status subresource | yes |
| Lands with | NBO-028 (M4) |

A `NetBoxClusterType` is one `virtualization.ClusterType` in NetBox: the technology a cluster
runs on — vSphere, Proxmox VE, Hyper-V, Nutanix.

It is the smallest kind in the catalogue. `virtualization.ClusterType` is a pure
`OrganizationalModel` with **no columns of its own** (`docs/netbox-schema.md` →
`virtualization.ClusterType`), so `name`, `slug` and `description` are the whole of it, and the
descriptor is three field-map entries and one natural key.

It ships with [`NetBoxCluster`](netboxcluster.md) because a cluster cannot exist without a
type: NetBox's `type` column is `REQ` and `PROTECT`ed.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxClusterType
metadata:
  name: proxmox
  namespace: default
spec:
  endpointRef: homelab
  name: Proxmox VE
  slug: proxmox
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxClusterType
metadata:
  name: proxmox
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Adopt           # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: Proxmox VE
  slug: proxmox
  description: Debian-based hypervisor, KVM plus LXC
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name` (`OrganizationalModel`), `CharField REQ UNIQUE len=100` |

Column-unique in NetBox, so it identifies a type on its own. The natural key is `slug`
anyway — see below.

### `spec.slug`

| Type | `string`, 1–100 characters, `^[-a-zA-Z0-9_]+$` |
|---|---|
| Required | yes |
| NetBox column | `slug` (`OrganizationalModel`), `SlugField REQ UNIQUE len=100` |

The natural key. Unique across the whole NetBox while these CRs are namespaced, so two
namespaces claiming `proxmox` are claiming *one* object and the second gets
`Ready=False, Reason=Conflict`. Keep cluster types in one catalogue namespace and reference
them across it with a [`NetBoxRefGrant`](netboxrefgrant.md).

### `spec.description`

| Type | `string`, up to 200 characters |
|---|---|
| Required | no |
| NetBox column | `description` (`OrganizationalModel`), `CharField len=200` |

Omit it to leave NetBox's own value alone; set it to `""` to clear it. The two are different
instructions ([field ownership](../concepts/field-ownership.md)).

### What is deliberately absent

- **`comments`.** `OrganizationalModel` declares it and NetBox's serializer accepts it, but
  NBO-028 names three fields and the group kinds that shipped before this one —
  [`NetBoxSiteGroup`](netboxsitegroup.md), [`NetBoxRegion`](netboxregion.md) — leave it out
  too. Adding a field later is additive; removing one is not.
- **`clusters`.** There is no inline child list and no reverse field. A cluster names its type,
  not the other way round.
- **`cluster_count`.** A read-only `RelatedObjectCountField` on the serializer, returned and
  never accepted.

## Natural key

| # | Candidate | Query |
|---|---|---|
| 1 | `slug` | `?slug=<slug>` |

One candidate and no null pin: the column carries `UNIQUE`, so the filter identifies at most
one object. `name` is equally unique and deliberately is not a second candidate — a kind gets
one identity, and a fallback on a unique column would only ever be reached when the object does
not exist and should be created.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

An `OrganizationalModel` mixes in both `TagsMixin` and `CustomFieldsMixin`, so this kind is
stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

## `deletionPolicy` defaults to `Delete`

A cluster type is configuration, not allocated state, and since [#304](https://github.com/ricardomolendijk/netbox-operator/issues/304) every kind defaults to
`Delete` regardless ([deletion](../concepts/deletion.md#the-two-policies)).

The delete is safe by construction rather than by policy: `virtualization.Cluster.type` is
`on_delete=PROTECT`, so NetBox refuses to delete a type any cluster still uses. The CR reports
`Deleting=False, Reason=Protected` naming the cluster and retries; nothing is lost.

## Printer columns

```
NAME      SLUG      ID   READY   AGE
proxmox   proxmox   12   True    2m
vsphere   vsphere   13   True    2m
```

| Column | JSONPath |
|---|---|
| `SLUG` | `.spec.slug` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply` | none — admission | `slug` has characters outside `^[-a-zA-Z0-9_]+$` | Slugs are NetBox slugs: lowercase, hyphens, underscores |
| `Ready=False`, `Reason=Conflict` | `Ready` | a cluster type with this `slug` exists and `onConflict: Fail` | Adopt it deliberately with `onConflict: Adopt`; `status.naturalKey` shows what was searched |
| `Deleting=False`, `Reason=Protected` | `Deleting` | a cluster still uses this type (NetBox `PROTECT`) | Delete or re-type the clusters first; the delete then completes on the next pass |
| A cluster reports `RefsResolved=False` naming `typeRef` | on the *cluster* | this CR is in another namespace with no grant | Add a [`NetBoxRefGrant`](netboxrefgrant.md) in this namespace |

## Related

- [`NetBoxCluster`](netboxcluster.md) — what points at this kind, and why it is not a container
- [`NetBoxClusterGroup`](netboxclustergroup.md) — the other half of a cluster's catalogue
- [`NetBoxRefGrant`](netboxrefgrant.md) — referencing a shared catalogue namespace
- [The Descriptor](../concepts/descriptor.md) — where this kind's per-kind facts live
