# `NetBoxCluster`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Kind | `NetBoxCluster` |
| Scope | Namespaced ([ADR-0002](../decisions/0002-crd-scoping.md)) |
| Short names | `nbcluster` |
| Status subresource | yes |

A `NetBoxCluster` is one `virtualization.Cluster` in NetBox: a set of hosts that virtual
machines run on.

It is the second scoped kind after [`NetBoxPrefix`](netboxprefix.md), and the one
`netbox-populator` gets wrong.

## There is no `siteRef`, and that is the point

`../reconcile.go:270` writes `"site": siteID` to `virtualization/clusters`. Since NetBox 4.2
that field does not exist: `virtualization.Cluster` is scoped through `CachedScopeMixin`'s
`scope_type` / `scope_id` pair, and `_site` is a read-only denormalised cache NetBox maintains
itself (`docs/netbox-schema.md` → `dcim.CachedScopeMixin`).

NetBox's `ClusterSerializer` has no `site` member at all, and DRF **drops a key it does not
know** rather than rejecting it. So that write returns `201`, creates an unscoped cluster, and
never drifts — the object reports itself synced forever while sitting in no site. Nobody
noticed.

This kind cannot express the mistake:

- there is no `siteRef` field, not even as sugar that expands into `scope.siteRef`;
- `site` is listed in the descriptor's `ReadOnly`, so a `siteRef → site` field added in a hurry
  fails at boot rather than at the first silent no-op;
- `_site`, `_region`, `_site_group` and `_location` are read-only too, and are never compared —
  a cache treated as a column is not an error, it is a `PATCH` loop
  ([drift](../concepts/drift.md));
- the regression is asserted on **recorded request bodies**, not on resulting state
  (`internal/controller/virtualization_cluster_controller_test.go`). A stub can be written to
  agree with the bug; a request body cannot.

See [generic references](genericref.md) and
[`docs/concepts/generic-refs.md`](../concepts/generic-refs.md) for the union's shape.

## Minimal example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCluster
metadata:
  name: proxmox-ams
  namespace: default
spec:
  endpointRef: homelab
  name: proxmox-ams
  typeRef:
    name: proxmox
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxCluster
metadata:
  name: proxmox-ams
  namespace: default
spec:
  endpointRef: homelab
  onConflict: Fail            # Fail | Adopt | AdoptOnly
  deletionPolicy: Delete      # Delete | Retain

  name: proxmox-ams
  typeRef:
    name: proxmox
  groupRef:
    name: production
  scope:
    siteRef:
      name: home
  tenantRef:
    name: donkerslootstraat
  status: active              # planned | staging | active | decommissioning | offline
  description: Three-node Proxmox cluster in the Amsterdam rack
  comments: Managed by netbox-operator.
```

## `spec`

Every kind shares the envelope — `endpointRef`, `onConflict`, `deletionPolicy`,
`driftMode` overrides, `tags`, `customFields`. See [`NetBoxTag`](netboxtag.md#spec).

### `spec.name`

| Type | `string`, 1–100 characters |
|---|---|
| Required | yes |
| NetBox column | `name`, `CharField REQ len=100` |

**Not globally unique**, and the shape of its non-uniqueness decides this kind's identity.
`meta.constraints` is `(('group','name'), ('_site','name'))` — two separate constraints rather
than one composite — and both are partial in practice:

- a cluster scoped to a **Region** has `_site = NULL`, so the second constrains nothing; two
  region-scoped clusters called `proxmox` are a legal NetBox state;
- two clusters with the same name in the same **site** collide *even in different groups*,
  because the group constraint is a separate constraint. NetBox answers with a `400`, which the
  operator surfaces as `Ready=False, Reason=Invalid` carrying NetBox's own field error and a
  long backoff — retrying an invalid payload fast is pointless.

### `spec.typeRef`

| Type | [`ObjectRef`](genericref.md) → `NetBoxClusterType` |
|---|---|
| Required | **yes** |
| NetBox column | `type`, `ForeignKey REQ -> virtualization.ClusterType on_delete=PROTECT` |

Required by the API server, because the column is `REQ`. "Required" means the field is *set*,
not that it has resolved: until the target has an id the object reports
`RefsResolved=False` naming this field and `type` is left out of the payload rather than sent as
null.

**Not a containment reference.** Deleting a cluster type must not delete the clusters using it,
and NetBox's `PROTECT` would refuse the delete anyway — so this reference adds no owner
reference ([ADR-0003](../decisions/0003-ownership-and-references.md) §4).

Pointing at a shared catalogue namespace needs a [`NetBoxRefGrant`](netboxrefgrant.md) there.

### `spec.groupRef`

| Type | [`ObjectRef`](genericref.md) → `NetBoxClusterGroup` |
|---|---|
| Required | no |
| NetBox column | `group`, `ForeignKey -> virtualization.ClusterGroup on_delete=PROTECT` |

Part of the identity: with it set the lookup is `?group_id&name`. Leaving it unset is a
*groupless cluster* — a different identity rather than the same one with a field missing — so
the fallback candidate pins `group_id__isnull=true` rather than omitting the filter
([lookups](../concepts/lookups.md)). No owner reference, for the same `PROTECT` reason as
`typeRef`.

### `spec.scope`

| Type | [`ScopeRef`](genericref.md#scoperef) — at most one of `regionRef`, `siteGroupRef`, `siteRef`, `locationRef` |
|---|---|
| Required | no |
| NetBox columns | `scope_type` + `scope_id` (`CachedScopeMixin`) |

Three states, all instructions:

| Spec | Written | Meaning |
|---|---|---|
| absent | nothing | leave whatever NetBox holds alone |
| `scope: {}` | `scope_type: null`, `scope_id: null` | unscope the cluster |
| one member | `scope_type: "dcim.site"`, `scope_id: 41` | scope it there |

The pair is written and diffed **as a unit**, so moving a cluster from a Region to a Site is one
`PATCH` carrying both columns. A half-changed scope — `scope_type: dcim.site` against a
Region's id — is never sent.

`spec.scope` is also this kind's **containment reference**, the only one
([ADR-0003](../decisions/0003-ownership-and-references.md) §4), matching NetBox's own
`_site on_delete=CASCADE`:

- same-namespace target → a non-controller owner reference on it, so
  `kubectl delete netboxsite home` takes the cluster with it;
- cross-namespace target → **no** owner reference and a `CascadeUnavailable` condition naming
  the namespace, because Kubernetes garbage collection does not cross namespaces.

### `spec.tenantRef`

| Type | [`ObjectRef`](genericref.md) → `NetBoxTenant` |
|---|---|
| Required | no |
| NetBox column | `tenant`, `ForeignKey -> tenancy.Tenant on_delete=PROTECT` |

An attribute, not a container: a cluster outliving its tenant is normal, so no owner reference.
Not part of the identity — neither NetBox constraint mentions `tenant` — so it never appears in
a lookup.

### `spec.status`

| Type | `string`, one of `planned` `staging` `active` `decommissioning` `offline` |
|---|---|
| Default | `active` |
| NetBox column | `status`, `CharField len=50 choices=ClusterStatusChoices` |

Defaulted to NetBox's own default, so the operator manages the field from the first reconcile: a
defaulted field that never reaches a payload is one the operator can never correct.

Not the same set as [`NetBoxSite`](netboxsite.md)'s: a cluster is `offline` where a site is
`retired`, and there is no `staged`.

### `spec.description`, `spec.comments`

Inherited from `PrimaryModel`; `description` is capped at 200 characters and `comments` is a
`TextField` with no cap. Both clearable: omit to leave NetBox's value alone, `""` to clear
([field ownership](../concepts/field-ownership.md)).

### What is deliberately absent

- **`siteRef`** — see above. This is the whole ticket.
- **Inline children.** `plan.md` §7 lists `Cluster` among the kinds that get inline expansion
  but names no lists. A cluster's plausible children are Devices and VirtualMachines, which
  have independent lifecycles and are not components of it; materialising them would make this
  the composite topology kind `plan.md` §2 principle 1 forbids. Decision: no inline children,
  now or later.
- **`vlan_groups`, `contacts`** — `GenericRelation`s, so reverse relations rather than columns.
- **`device_count`, `virtualmachine_count`, `allocated_vcpus`, `allocated_memory`,
  `allocated_disk`** — read-only serializer annotations. NetBox returns them and refuses them.
  They are not in `ReadOnly` either, because that list guards the field map and no spec field
  points at them; that they survive the drift comparison is asserted in
  `internal/registry/virtualization_cluster_test.go`.

## Natural key

Two candidates, tried in order:

| # | Candidate | Query | Applicable when |
|---|---|---|---|
| 1 | `(group, name)` | `?group_id=<id>&name=<name>` | `groupRef` is set **and** resolved |
| 2 | `name`, group pinned null | `?name=<name>&group_id__isnull=true` | `groupRef` was never set |

A `groupRef` that is set but has not resolved matches **neither** candidate, and the engine
waits. Falling through to candidate 2 would find an unrelated groupless cluster of the same
name, adopt it, and then `PATCH` a group onto it — moving every VM and device that hangs off it
([lookups](../concepts/lookups.md#why-a-null-filter-is-pinned-and-never-omitted)).

### The site-scoped candidate NetBox has and this kind does not

NetBox's second constraint is `(_site, name)`, and the matching lookup would be
`?site_id=<id>&name=<name>` — reading the cached `_site`, which is **correct as a lookup**,
since NetBox maintains it, and wrong as a write. That distinction is this kind's whole subject,
and the candidate is still not declared, for a mechanical reason:

the site id lives inside the `scope` union, and the engine writes a resolved polymorphic
reference into the *payload* only, never back into the spec a natural-key filter reads
(`internal/reconciler/refs.go`, `applyGenericFK`). A candidate naming `scope` would be
applicable as soon as the union resolved and would then fail as unfilterable — a stopped
reconcile rather than a lookup.

The consequence is bounded and safe rather than silent: a **scoped, groupless** cluster is
looked up by name alone, so two clusters of one name in two different sites match one lookup and
both CRs report `Ready=False, Reason=Conflict` naming the ids, with no write. Give either one a
`groupRef` and both converge. `ipam.VLANGroup` — unique on `(scope_type, scope_id, slug)` — is
the kind that will make the engine able to express a scoped key; this candidate arrives with it.

## `status`

Identical to every other kind — `id`, `url`, `naturalKey`, `adopted`, `lastAppliedHash`,
`lastSyncTime`, `deletionAttempts`, `provenance`, `observedGeneration`, `conditions`. See
[`NetBoxTag`](netboxtag.md#status).

`virtualization.Cluster` is a `PrimaryModel`, so it carries both `tags` and `custom_fields` and
is stamped in full when the endpoint's [`spec.managedBy`](netboxendpoint.md#specmanagedby) is
set. See [provenance](../operations/provenance.md).

## Conditions

| Type | `True` when | `False` when | Reasons it can carry |
|---|---|---|---|
| `Ready` | the cluster exists in NetBox and matches the spec | anything else | `Synced`, `WaitingForEndpoint`, `WaitingForRef`, `Conflict`, `AdoptOnly`, `Invalid`, `APIError`, `DryRunPending` |
| `Synced` | the last write succeeded, or no drift was found | drift found and not corrected | `NoDrift`, `DriftCorrected`, `DriftDetectedDryRun` |
| `RefsResolved` | `typeRef`, `groupRef`, `tenantRef` and `scope` all resolved | any of them did not | `AllResolved`, `WaitingForRef`, `RefKindUnavailable`, `RefNotFound`, `RefForbidden` |
| `Deleting` | never | while terminating and NetBox is not settled | `Protected`, `WaitingForEndpoint`, `APIError`, `Invalid` |
| `CascadeUnavailable` | — | `scope` points into another namespace | `CrossNamespace` |

## `deletionPolicy` defaults to `Delete`

A cluster is not IPAM: nothing is allocated from it and deleting one destroys no record of who
owned a range (#176 option B, #186 option A). `Delete` is both the expected Kubernetes
behaviour and the correct one.

NetBox makes the destructive case hard to reach anyway — `virtualization.VirtualMachine.cluster`
and `dcim.Device.cluster` are `on_delete=SET_NULL`, so deleting a cluster with VMs in it does
not delete the VMs. Set `deletionPolicy: Retain` on clusters other tooling also owns.

## Printer columns

```
NAME          TYPE      SITE   STATUS   ID   READY   AGE
proxmox-ams   proxmox   home   active   31   True    4m
proxmox-lab   proxmox          active   32   True    4m
```

| Column | JSONPath |
|---|---|
| `TYPE` | `.spec.typeRef.name` |
| `SITE` | `.spec.scope.siteRef.name` |
| `STATUS` | `.spec.status` |
| `ID` | `.status.id` |
| `READY` | `.status.conditions[?(@.type=="Ready")].status` |
| `AGE` | `.metadata.creationTimestamp` |

`SITE` reads the **intent**, and only the site member: a cluster scoped to a Region or a
Location shows nothing there. That is a narrower promise than a column reading "the first member
of the union that happens to be set", and a deliberate one — `kubectl get -o yaml` or NetBox
itself answers "what is this cluster scoped to".

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| Rejected by `kubectl apply`, naming `typeRef` | none — admission | `typeRef` omitted | It is required; NetBox's column is `REQ` |
| Rejected naming `at most one of regionRef, siteGroupRef, siteRef or locationRef` | none — admission | two scope members set | A scope is one object |
| `Ready=False`, `Reason=WaitingForRef` | `RefsResolved` | `typeRef` (or another ref) has no id yet | Apply the target, or check the namespace has a [`NetBoxRefGrant`](netboxrefgrant.md) |
| `Ready=False`, `Reason=Conflict` | `Ready` | the name lookup matched more than one cluster, or one already exists under `onConflict: Fail` | Set `groupRef` to narrow the identity; `status.naturalKey` shows what was searched |
| `Ready=False`, `Reason=Invalid` with a NetBox message about `name` | `Ready` | another cluster of this name is already in this site (`(_site, name)`) | Rename, or move one of them; the backoff is long on purpose |
| The cluster is in NetBox but has no site | none — it reports Ready | the scope was never written | Check `scope` is set and resolved. `site:` is not a field on this kind and never was |
| `CascadeUnavailable` | `CascadeUnavailable` | `scope` points at a target in another namespace | Expected: no owner reference is possible across namespaces. Delete the cluster explicitly |
| `Deleting=False`, `Reason=Protected` | `Deleting` | rare — something NetBox `PROTECT`s still points here | The message names it |

## Related

- [`NetBoxClusterType`](netboxclustertype.md) — the required reference, and why it does not cascade
- [`NetBoxClusterGroup`](netboxclustergroup.md) — why a group makes the lookup unambiguous
- [`NetBoxPrefix`](netboxprefix.md) — the first scoped kind, same union, same bug avoided
- [Generic references](genericref.md) — `ScopeRef` and the `(scope_type, scope_id)` pair
- [Lookups](../concepts/lookups.md) — pinned null filters and ambiguous matches
- [ADR-0003](../decisions/0003-ownership-and-references.md) — containment, and why only `scope` is one
