# `ScopeRef`

| | |
|---|---|
| API version | `netbox.kubeforge.org/v1alpha1` |
| Go type | `ScopeRef` (`api/v1alpha1/scope.go`) |
| Kind | none — a **shared field type**, not a CRD |
| Appears as | `spec.scope` on every scoped kind |
| Milestone | M3 (NBO-018) |

Not a reference page for a CRD. `ScopeRef` is one field type reused by every kind whose
NetBox model mixes in `CachedScopeMixin` — `ipam.Prefix`, `ipam.VLANGroup`,
`virtualization.Cluster`, `wireless.WirelessLAN` and the rest — so it is documented once here
rather than repeated on each of their pages. The mechanism, and why NetBox needs a union at
all, is [concepts/scopes.md](../concepts/scopes.md).

**"Scope" here is NetBox's, not Kubernetes'.** Every CRD in `v1alpha1` is namespaced
([ADR-0002](../decisions/0002-crd-scoping.md)) and `spec.scope` has nothing to do with that.

> No shipped kind carries `spec.scope` yet. `NetBoxVLANGroup` (NBO-023) and `NetBoxPrefix`
> (NBO-024) are the first. The examples below use `NetBoxPrefix` because that is what the
> field is for; substitute your own scoped kind.

## Minimal example

A prefix with no scope at all — a legal and common NetBox state. Omit the field entirely and
the operator does not manage the scope:

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata:
  name: lab
  namespace: team-a
spec:
  endpointRef: homelab
  prefix: 192.0.2.0/24
```

## Full example

```yaml
apiVersion: netbox.kubeforge.org/v1alpha1
kind: NetBoxPrefix
metadata:
  name: hq-lan
  namespace: team-a
spec:
  endpointRef: homelab
  prefix: 10.0.0.0/24
  scope:
    # Exactly one of regionRef, siteGroupRef, siteRef or locationRef.
    siteRef:
      # The mode to prefer: a sibling CR, which is the only mode the operator can wait on.
      name: hq
      # Defaults to this object's own namespace. Crossing one needs a NetBoxRefGrant.
      namespace: netbox-catalog
```

To clear a scope, set the union to empty rather than deleting the field:

```yaml
spec:
  scope: {}          # sends scope_type: null, scope_id: null
```

## `spec.scope`

| | |
|---|---|
| Type | `object` |
| Required | no |
| Default | none |
| Validation | `at most one of regionRef, siteGroupRef, siteRef or locationRef may be set` |

Selects the NetBox object this one hangs off. Three states, and they are three different
instructions:

| Written | Sent to NetBox | Meaning |
|---|---|---|
| field absent | neither column | do not manage the scope; whatever NetBox holds stays |
| `scope: {}` | `scope_type: null`, `scope_id: null` | no scope — clear it |
| one member set | `scope_type: <type>`, `scope_id: <id>` | scoped to that object |

**If it is wrong.** Two members set is rejected by the API server at `kubectl apply`, quoting
the message above. There is no condition, because the object is never admitted.

## `spec.scope.regionRef`, `.siteGroupRef`, `.siteRef`, `.locationRef`

| | |
|---|---|
| Type | [`ObjectRef`](../concepts/references.md#the-four-modes) |
| Required | no — at most one of the four |
| Default | none |
| Validation | `ObjectRef`'s own five CEL rules |

Each is an ordinary reference and takes all four `ObjectRef` modes:

```yaml
scope: {siteRef: {name: hq}}                       # a sibling NetBoxSite CR
scope: {siteRef: {name: hq, namespace: catalog}}   # ... in another namespace
scope: {siteRef: {slug: hq}}                       # a NetBox site the operator does not manage
scope: {siteRef: {lookup: {facility: "DC1"}}}      # an arbitrary NetBox filter
scope: {siteRef: {id: 5}}                          # a literal NetBox primary key
```

Which member you set decides the `scope_type` string:

| Member | `scope_type` | Target Kind | NetBox model | Exists as a Kind? |
|---|---|---|---|---|
| `regionRef` | `dcim.region` | `NetBoxRegion` | `dcim.Region` | yes |
| `siteGroupRef` | `dcim.sitegroup` | `NetBoxSiteGroup` | `dcim.SiteGroup` | no — NBO-066 (#79) |
| `siteRef` | `dcim.site` | `NetBoxSite` | `dcim.Site` | yes |
| `locationRef` | `dcim.location` | `NetBoxLocation` | `dcim.Location` | no — NBO-048 |

**If it is wrong.** Everything below is reported on `RefsResolved`, with `Ready=False,
Reason=WaitingForRef` alongside it. Nothing is written to NetBox in any of these states, and
in particular no half-written pair: `scope_type` and `scope_id` are sent together or not at
all.

| `RefsResolved` `Reason` | What happened | Retry |
|---|---|---|
| `RefNotReady` | the target CR exists and has no `status.id` yet, or is not `Ready`, or is being deleted | none — the watch on the target re-enqueues this object |
| `RefNotFound` | no such CR (`name`), or nothing in NetBox matches (`slug`, `lookup`, `id`) | `name`: none, the CR's creation is an event. Others: 1 min |
| `RefAmbiguous` | a `slug` or `lookup` matched more than one NetBox object; the message names every id | 10 min |
| `RefDenied` | a cross-namespace `name` reference with no `NetBoxRefGrant` in the target namespace | none — a grant is an event |
| `RefKindUnavailable` | the member's Kind has no `Descriptor` in this build — `siteGroupRef` and `locationRef` today, in **all four modes** | 10 min |
| `RefCycle` | the reference closes a ring of `name` references | none |
| `Invalid` | two members set on an object stored before the CEL rule; the message names both | none |

### `RefKindUnavailable` in every mode, including `slug`

Worth calling out, because it is not obvious. `slug`, `lookup` and `id` resolve against NetBox
and need no CRD — but they do need the target's REST endpoint, and that is a per-kind fact
that lives on the target's `Descriptor` (it is looked up, never derived: `dcim.VirtualChassis`
is at `dcim/virtual-chassis`). With no `Descriptor` there is no endpoint to query, so a
`siteGroupRef` or `locationRef` is `RefKindUnavailable` however it is written:

```
RefsResolved=False, Reason=RefKindUnavailable
scope -> netboxlocation/team-a/rack-3: target kind unavailable
  (no descriptor is registered for netbox.kubeforge.org/v1alpha1, Kind=NetBoxLocation)
```

The manifest is correct and the fix is an operator upgrade, which is why the reason is not
`RefNotFound`. Both members start working in all four modes the moment their Kinds are
registered, with no manifest change.

## What never appears in a request body

| Key | Why |
|---|---|
| `site`, `site_id` | not a column on a scoped model since NetBox 4.2. NetBox ignores an unknown key rather than rejecting it, so writing it returns `201` and sets nothing |
| `_region`, `_site_group`, `_site`, `_location` | read-only caches NetBox maintains from the pair. Writing one is dropped, so the operator would PATCH it again every resync, forever |

There is deliberately no `siteRef` shortcut on a scoped kind, not even sugar that expands into
`scope.siteRef` — a field by that name would read as the foreign key NetBox no longer has. A
descriptor that lists a cache column without marking it read-only fails the manager's boot
(`ErrCachedNotReadOnly`), and a test asserts none of these keys reaches a request body.

## Printer columns

A scoped kind's printer column reads the member the user wrote rather than the resolved pair,
because that is the spelling in the manifest:

```
NAME     PREFIX         SCOPE   ID   READY   AGE
hq-lan   10.0.0.0/24    hq      41   True    3m
```

| Column | JSONPath |
|---|---|
| `SCOPE` | `.spec.scope.siteRef.name` |

## Troubleshooting

| Symptom | Condition | Cause | Fix |
|---|---|---|---|
| `kubectl apply` refused | none — rejected at admission | two members set | set one |
| Object stuck `Ready=False/WaitingForRef` | `RefsResolved=False/RefNotReady` | the target CR has not reached NetBox yet | look at the target; its own message is quoted in this object's |
| Object stuck, message says `target kind unavailable` | `RefsResolved=False/RefKindUnavailable` | `siteGroupRef` or `locationRef` | use `regionRef` or `siteRef`, or wait for the Kind |
| Object stuck, message says `denied` | `RefsResolved=False/RefDenied` | cross-namespace `name` with no grant | create a `NetBoxRefGrant` in the target namespace ([netboxrefgrant.md](netboxrefgrant.md)) |
| The scope will not clear | `Ready=True`, and NetBox still scoped | the field was deleted rather than emptied | write `scope: {}` |
| NetBox object has no scope, operator says `Ready` | `Ready=True` | a hand-written `site` key from another tool | this operator cannot write `site`; check the other tool |

## Related

- [Scopes](../concepts/scopes.md) — the mechanism, and the `netbox-populator` migration note
- [References](../concepts/references.md) — the four modes, grants, cycles, watches
- [The Descriptor](../concepts/descriptor.md) — `GenericFKSpec`, and `registry.ScopeFK`
- [`NetBoxRefGrant`](netboxrefgrant.md) — authorising a cross-namespace scope
- [ADR-0002](../decisions/0002-crd-scoping.md) — why every CRD is namespaced
