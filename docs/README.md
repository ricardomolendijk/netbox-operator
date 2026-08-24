# Documentation

Start with [the root README](../README.md) for what this operator is. This page is the
index of everything under `docs/`.

Docs ship in the same pull request as the code they describe — a feature PR that touches
neither `docs/` nor `README.md` is incomplete ([`CONTRIBUTING.md`](../CONTRIBUTING.md),
definition of done). Every kind gets a reference page; every concept gets a concept page.

## Concepts

How the engine behaves, and why.

| Page | Answers |
|---|---|
| [The Descriptor](concepts/descriptor.md) | What per-kind facts the engine needs, why they are data rather than code, and how natural keys establish identity before a `status.id` exists |
| [Deletion](concepts/deletion.md) | What `deletionPolicy: Delete` and `Retain` each do, which kinds default to `Retain` and why, why the finalizer goes on before the first write and comes off after the last one, what a `PROTECT`-blocked delete looks like and how to get out of it |
| [Drift detection](concepts/drift.md) | Why what NetBox returns is not what you wrote, and the eight comparison rules that stop a reconcile loop from PATCHing forever |
| [Field ownership](concepts/field-ownership.md) | The three states of an optional field -- absent, empty, set -- how to write each, how `metadata.managedFields` tells them apart, and what happens when there is no ownership metadata to read |
| [Generic references](concepts/generic-refs.md) | What a polymorphic foreign key is, why the `app_label.model` spelling is written down once, why the schema digest's `REQ` on a `GenericForeignKey` row must be ignored, how a `*_type` / `*_id` pair is kept atomic, and NetBox's scope pair — including why writing `site` returns `201` and sets nothing |
| [Errors and retries](concepts/errors-and-retries.md) | Which NetBox failure becomes which typed error, what gets retried and where, and why more than one lookup match is an error rather than a guess |
| [Lookups](concepts/lookups.md) | How a natural key becomes a query string, why `?name__ie=` exists, why a null filter is pinned rather than omitted, and what `allowDuplicate` does to the natural key of an address that may legitimately exist twice |
| [Ownership](concepts/ownership.md) | When the operator sets an owner reference and when it deliberately does not, why an owner reference may never cross a namespace and how the `ParentOwned` condition tells you which happened, and what the operator will never do to an owner reference somebody else set |
| [References](concepts/references.md) | How one object points at another, the four resolution modes, what the API server rejects before a bad reference reaches the operator, what it takes to cross a namespace, and why a namespace does not imply a tenant |

## Reference

One page per CRD: every field, every condition, every way it fails. Plus one page per **shared
field type** — a type reused by many kinds, documented once instead of repeated on each of
their pages.

| Page | Answers |
|---|---|
| [`NetBoxEndpoint`](reference/netboxendpoint.md) | How to point the operator at a NetBox: URL, token Secret, TLS, dry run, rate limit, the provenance stamp, and the `>=4.2, <5.0` version gate |
| [`NetBoxRegion`](reference/netboxregion.md) | The first kind whose identity depends on a reference: two natural keys, why a top-level region is a different identity rather than a missing filter, and why a child region waits instead of guessing |
| [`NetBoxTag`](reference/netboxtag.md) | The first NetBox object kind: `slug` as a natural key, adoption and `Conflict`, `objectTypes` as content-type strings, and what happens when two namespaces claim one slug |
| [`NetBoxSite`](reference/netboxsite.md) | A choice column and two decimals that need no per-kind handling, a globally-unique slug over namespaced CRDs, and which of `dcim.Site`'s foreign keys are deliberately absent |
| [`NetBoxTenantGroup`](reference/netboxtenantgroup.md) | The second `NestedGroupModel`, with the opposite identity from the first: no `meta.constraints` at all, so `slug` alone and no `parent_id` filter of any kind -- and why that is what makes a self-reference safe to defer |
| [`NetBoxTenant`](reference/netboxtenant.md) | The kind almost every IPAM object points at: a group that is part of the identity, `group_id=null` pinned rather than omitted, why `tenantRef` does not cascade, and what a `PROTECT`-refused delete looks like |
| [`NetBoxSiteGroup`](reference/netboxsitegroup.md) | The same NetBox model as `NetBoxRegion` under a different name: the functional site hierarchy, the `parent IS NULL` natural key, and the first of the two scope-union members that had no Descriptor |
| [`NetBoxLocation`](reference/netboxlocation.md) | The first kind with a **required** reference: why its identity is a pair, why an unresolved `siteRef` writes nothing at all, why `siteRef` and not `parentRef` is the containment parent, and why `tenant` is absent |
| [`NetBoxPrefix`](reference/netboxprefix.md) | The kind NetBox 4.2's scope change broke in `netbox-populator`: why there is no `siteRef` and no `parentRef`, how `(scope_type, scope_id)` moves as one pair, a two-candidate natural key on a model with no `meta.constraints` at all, why `vrf_id` is pinned to null rather than omitted, and why an IPAM object wants `deletionPolicy: Retain` |
| [`NetBoxVRF`](reference/netboxvrf.md) | The first kind with a real to-many reference: `importTargets`/`exportTargets` as absent-versus-empty-versus-set, why the ids are sorted and deduplicated, why a partially resolvable list writes nothing, and why a `name`-only lookup on a non-unique column is a `Conflict` rather than a guess |
| [`NetBoxRouteTarget`](reference/netboxroutetarget.md) | The far end of `NetBoxVRF`'s two many-to-many relations: why it has no reverse field, why two VRFs sharing one route target take no owner reference on it, and why its `deletionPolicy` default differs from the VRF's |
| [`NetBoxVLAN`](reference/netboxvlan.md) | The one kind in M3 that writes `site` as a real foreign key, and why the kind next to it must never write it at all: a three-candidate natural key of which only the first is a database constraint, why `group_id` is pinned to null rather than omitted, a deferred self-reference, and a `status` enum that is *nearly* `NetBoxPrefix`'s |
| [`NetBoxVLANGroup`](reference/netboxvlangroup.md) | The first kind whose identity is a **generic FK pair**: `(scope_type, scope_id, slug)`, why `slug` is not globally unique here when it is on every other `OrganizationalModel`, why two globally-scoped groups sharing a slug is a `Conflict` the database will not prevent, and the scope pair without any cached columns |
| [`NetBoxClusterType`](reference/netboxclustertype.md) | The smallest kind in the catalogue: a model with no columns of its own, `slug` as its only natural key, and why a `PROTECT`ed required reference makes it safe to default to `deletionPolicy: Delete` |
| [`NetBoxClusterGroup`](reference/netboxclustergroup.md) | Field for field the same kind again, and why they are two Kinds rather than one with a discriminator -- plus why setting a cluster's group is what makes that cluster's lookup unambiguous |
| [`NetBoxCluster`](reference/netboxcluster.md) | The second kind NetBox 4.2's scope change broke in `netbox-populator`, and the one it broke silently: why there is no `siteRef`, why `site` is an explicit deny rather than an omission, how the two `meta.constraints` become two lookup candidates -- and the site-scoped candidate the engine cannot express yet |
| [`NetBoxIPAddress`](reference/netboxipaddress.md) | The first Kind with a polymorphic foreign key and the first whose identity no database constraint backs: host bits preserved where `NetBoxPrefix` masks them, `role` as a string where its neighbours have a reference, the two natural keys and why the global-table one pins `vrf_id`, and what `allowDuplicate` does to identity when NetBox permits two rows to hold one address |
| [Generic references](reference/genericref.md) | The union shape a polymorphic foreign key takes in a spec: one member per legal target, `<= 1` versus `== 1`, why an empty union clears the reference while an absent one does not, and the two unions that ship: `IPAssignment` and `ScopeRef` |
| [`NetBoxRefGrant`](reference/netboxrefgrant.md) | The kind that describes no NetBox object: which namespaces may reference into this one, the wildcard and selector forms that keep one grant per catalogue namespace, why `NetBoxEndpoint` is the one exception, and why a grant is not NetBox authorisation |

### The shape of a reference page

Around 112 CRDs will follow, so the shape is settled here rather than after twenty pages
have diverged. [`reference/netboxendpoint.md`](reference/netboxendpoint.md) is the
template — copy its headings in this order:

1. **Header table** — API version, kind, scope, short names, status subresource, milestone.
2. **Minimal example** — the fewest fields that actually work, valid YAML, with any Secret
   or prerequisite object it needs.
3. **Full example** — every field set, with defaults written out explicitly and commented
   as defaults.
4. **`spec`** — one subsection per field, keyed by full path (`spec.tokenSecretRef.key`),
   each with a table giving type, required, default *taken from the `+kubebuilder:default`
   marker*, and validation *quoted from the `+kubebuilder:validation:` marker*; then one
   sentence on what it does; then a **"If it is wrong"** paragraph naming the condition
   type, `Reason` constant and message the user will actually see, and separating what
   admission rejects from what fails later at reconcile.
5. **`status`** — a table of field, type, what populates it, and when. Say explicitly which
   fields are *not* cleared on failure.
6. **Conditions** — a table of type, when `True`, when `False`, and every `Reason` it can
   carry; then a reason glossary; then retry intervals.
7. **Kind-specific behaviour** — the one or two things about this kind that are not
   obvious. Cite `docs/netbox-schema.md` or a NetBox source path for every NetBox claim.
8. **Printer columns** — real `kubectl get <kind>` output, plus a table mapping column to
   JSONPath.
9. **Troubleshooting** — symptom → condition → cause → fix, driven off the `Reason`
   constants, since those enumerate the real failure modes.
10. **Related** — links to the concept pages and ADRs that explain the *why*.

Document only what is in the code. If a spec and the code disagree, the code wins and the
divergence gets reported.

## Decisions

Dated records of decisions that are expensive to reverse. Index and status:
[`decisions/README.md`](decisions/README.md).

| Page | Answers |
|---|---|
| [0001 — API group and kind naming](decisions/0001-api-group-and-kind-naming.md) | Why the group is `netbox.kubeforge.org` and every kind is prefixed `NetBox` |
| [0002 — CRD scoping](decisions/0002-crd-scoping.md) | Why every kind is namespaced in `v1alpha1`, what that costs, and what would have to change to revisit it |
| [0003 — Ownership and references](decisions/0003-ownership-and-references.md) | How a NetBox foreign key differs from a Kubernetes owner reference, where the operator adds each, what cross-namespace containment gives up as a result, and why inline child sugar is in `v1alpha1` on terms that let `v1beta1` drop it |
| [0004 — Claims-first allocation](decisions/0004-claims-first-allocation.md) | Why "allocate me an address" is a separate kind rather than a mode of `NetBoxIPAddress`, why the inline form is sugar over a real claim, and why an exhausted pool waits rather than failing |
| [0005 — Coexisting with Flux and Argo CD](decisions/0005-gitops-coexistence.md) | Why Git is authoritative, why a NetBox UI edit is drift rather than a competing opinion, and why there is no write-back |

## Operations

| Page | Answers |
|---|---|
| [Coexisting with Flux and Argo CD](operations/gitops.md) | Why the operator never writes a `spec` and how that is enforced, the Argo CD `ignoreDifferences` and Flux `Kustomization` snippets that make it quiet, the three `driftMode` values, the cluster-rebuild and NetBox-restore walkthroughs, and the NetBox permission model |
| [Provenance](operations/provenance.md) | What `spec.managedBy` writes into every NetBox object the operator manages, how the tag and custom-field definitions get bootstrapped, why stamping is not mandatory, what stops working when you turn it off, and why two clusters sharing one NetBox are never serialised |
| [Observability](operations/observability.md) | Every metric with its labels, cardinality and what to alert on; which Events fire and when; the log levels and the stable key set, with `kubectl logs \| jq` recipes |
| [Stuck references](operations/stuck-references.md) | Which condition says why an object is waiting for another one, what the reference metrics mean together, how to find an object's referrers by hand, and which references nothing will ever wake |
| [NetBox schema reference](netbox-schema.md) | The authoritative field list every CRD is derived from: 159 models, 138 endpoints, machine-extracted from NetBox 4.6.8. Grep it; do not read it |
| [Regenerating the schema](regenerating.md) | How to retarget a newer NetBox release, how to test the extraction pipeline without a NetBox checkout, and how to cross-check the AST walk against a live instance |

## Examples

| Page | Answers |
|---|---|
| [Examples](examples/README.md) | Runnable manifests, and which milestone each one becomes real in |
