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
| [Claims](concepts/claims.md) | What "allocates exactly once" means when a POST can lose its answer, how the deterministic allocation identity gets the same address back after a cluster rebuild and exactly when it does not, why an exhausted pool waits on a timer *and* a watch when neither alone is enough, why deleting a claim frees its address and what that costs, and how the operator reports the allocations it does leave behind, and what `AllocationConflict`, `ReclaimedOutsidePool` and `PoolNotAllocatable` each want you to go and fix |
| [Deletion](concepts/deletion.md) | What `deletionPolicy: Delete` and `Retain` each do, which kinds default to `Retain` and why, why `NetBoxVLANGroup` sits in `ipam` and does not, why `Retain` also refuses a destructive *update* on the one kind that has one, why the finalizer goes on before the first write and comes off after the last one, what a `PROTECT`-blocked delete looks like and how to get out of it |
| [Drift detection](concepts/drift.md) | Why what NetBox returns is not what you wrote, and the nine comparison rules that stop a reconcile loop from PATCHing forever |
| [Field ownership](concepts/field-ownership.md) | The three states of an optional field -- absent, empty, set -- how to write each, how `metadata.managedFields` tells them apart, and what happens when there is no ownership metadata to read |
| [Generic references](concepts/generic-refs.md) | What a polymorphic foreign key is, why the `app_label.model` spelling is written down once, why the schema digest's `REQ` on a `GenericForeignKey` row must be ignored, how a `*_type` / `*_id` pair is kept atomic, and NetBox's scope pair — including why writing `site` returns `201` and sets nothing |
| [Errors and retries](concepts/errors-and-retries.md) | Which NetBox failure becomes which typed error, what gets retried and where, and why more than one lookup match is an error rather than a guess |
| [Inline children](concepts/inline-children.md) | How an inline list becomes real child CRs, how a child's name and its `owned-by-path` are derived and what happens past 253 characters, the three cases pruning tells apart and the blast-radius cap that stops it deleting too much, why a hand-written CR at the derived name is never hijacked, what each of the three "renames" actually does, and the one place the sugar flows back *up* into its parent's payload |
| [Lookups](concepts/lookups.md) | How a natural key becomes a query string, why `?name__ie=` exists, why a null filter is pinned rather than omitted, what `allowDuplicate` does to the natural key of an address that may legitimately exist twice, and what it takes to give a second kind the same field — plus the kinds that must never have it |
| [Ownership](concepts/ownership.md) | When the operator sets an owner reference and when it deliberately does not, why an owner reference may never cross a namespace and how the `ParentOwned` condition tells you which happened, and what the operator will never do to an owner reference somebody else set |
| [Custom fields](custom-fields.md) | What happens when a `NetBoxCustomField` names one of the four fields the operator bootstraps for itself, why `object_types` derived from the kind registry cannot be reconciled with a static list in a manifest, why deleting a custom field is refused by default, and how a `spec.customFields` key applied before its definition converges anyway |
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
| [`NetBoxPrefix`](reference/netboxprefix.md) | The kind NetBox 4.2's scope change broke in `netbox-populator`: why there is no `siteRef` and no `parentRef`, how `(scope_type, scope_id)` moves as one pair, a two-candidate natural key on a model with no `meta.constraints` at all, why `vrf_id` is pinned to null rather than omitted, and why an IPAM object defaults to `deletionPolicy: Retain` |
| [`NetBoxVRF`](reference/netboxvrf.md) | The first kind with a real to-many reference: `importTargets`/`exportTargets` as absent-versus-empty-versus-set, why the ids are sorted and deduplicated, why a partially resolvable list writes nothing, and why a `name`-only lookup on a non-unique column is a `Conflict` rather than a guess |
| [`NetBoxRouteTarget`](reference/netboxroutetarget.md) | The far end of `NetBoxVRF`'s two many-to-many relations: why it has no reverse field, why two VRFs sharing one route target take no owner reference on it, and why its `deletionPolicy` default differs from the VRF's |
| [`NetBoxVLAN`](reference/netboxvlan.md) | The one kind in M3 that writes `site` as a real foreign key, and why the kind next to it must never write it at all: a three-candidate natural key of which only the first is a database constraint, why `group_id` is pinned to null rather than omitted, a deferred self-reference, and a `status` enum that is *nearly* `NetBoxPrefix`'s |
| [`NetBoxVLANGroup`](reference/netboxvlangroup.md) | The first kind whose identity is a **generic FK pair**: `(scope_type, scope_id, slug)`, why `slug` is not globally unique here when it is on every other `OrganizationalModel`, why two globally-scoped groups sharing a slug is a `Conflict` the database will not prevent, the scope pair without any cached columns, and why it is the one kind in `ipam` that defaults to `deletionPolicy: Delete` |
| [`NetBoxClusterType`](reference/netboxclustertype.md) | The smallest kind in the catalogue: a model with no columns of its own, `slug` as its only natural key, and why a `PROTECT`ed required reference makes it safe to default to `deletionPolicy: Delete` |
| [`NetBoxClusterGroup`](reference/netboxclustergroup.md) | Field for field the same kind again, and why they are two Kinds rather than one with a discriminator -- plus why setting a cluster's group is what makes that cluster's lookup unambiguous |
| [`NetBoxCluster`](reference/netboxcluster.md) | The second kind NetBox 4.2's scope change broke in `netbox-populator`, and the one it broke silently: why there is no `siteRef`, why `site` is an explicit deny rather than an omission, how the two `meta.constraints` become two lookup candidates -- and the site-scoped candidate the engine cannot express yet |
| [`NetBoxIPAddress`](reference/netboxipaddress.md) | The first Kind with a polymorphic foreign key and the first whose identity no database constraint backs: host bits preserved where `NetBoxPrefix` masks them, `role` as a string where its neighbours have a reference, the two natural keys and why the global-table one pins `vrf_id`, and what `allowDuplicate` does to identity when NetBox permits two rows to hold one address |
| [`NetBoxIPAddressClaim`](reference/netboxipaddressclaim.md) | The first kind that asks NetBox for something rather than describing it: an immutable `prefixRef`, a `status.address` that is written once and never rewritten, why there is no `deletionPolicy` and no `onConflict`, why the allocating POST is the one write that is never retried, and every refusal with the fix for it |
| [`NetBoxVirtualMachine`](reference/netboxvirtualmachine.md) | The most intricate identity in the catalogue, and the first kind with inline children: four conditional `UniqueConstraint`s over `Lower('name')` and the five ordered candidates they become, why the `name__ie` filter adopts rather than duplicates, `primary_ip4`/`primary_ip6` as unconditionally deferred fields breaking the `VM -> IPAddress -> VMInterface -> VM` ring and how `interfaces[].addresses[].primary` closes that ring in one convergence, why an inline address has no `allowDuplicate`, a decimal-as-string `vcpus`, and why NetBox's own `clean()` -- not the schema -- is what requires a cluster, a site or a device |
| [`NetBoxVMInterface`](reference/netboxvminterface.md) | The Kind that makes `IPAssignment.vmInterfaceRef` resolve for the first time: an identity that is unique per VM rather than globally, a case-*sensitive* name next to the VM's case-insensitive one, two self-references deferred only when they do not resolve, and a to-many VLAN list bounded at 256 refs |
| [`NetBoxVirtualDisk`](reference/netboxvirtualdisk.md) | The smallest kind in the catalogue, and the contrast that makes the descriptor claim concrete: one declared column, one natural key, no deferral -- plus why a VM's `spec.disk` alongside virtual disks is a loud `400` rather than a silent `PATCH` loop, and therefore needs no guard |
| [`NetBoxIPAddressClaim`](reference/netboxipaddressclaim.md) | The first kind that asks NetBox for something rather than describing it: an immutable `prefixRef`, a `status.address` that is written once and never rewritten, why `deletionPolicy` defaults to `Delete` here and `Retain` on `NetBoxIPAddress`, why there is no `onConflict`, why the allocating POST is the one write that is never retried, and every refusal with the fix for it |
| [`NetBoxManufacturer`](reference/netboxmanufacturer.md) | The root of the hardware catalogue, and the plainest identity in it: a model with no columns of its own, no `meta.constraints` at all, `slug` alone -- plus the grant that makes a shared catalogue namespace work, and why a `PROTECT`-refused delete is the normal way one goes |
| [`NetBoxDeviceRole`](reference/netboxdevicerole.md) | The `NestedGroupModel` that really does need a `parent IS NULL` variant, and the constraint condition that proves it: why a child role writes nothing instead of deferring, why `vmRole` is a pointer, and why one role serves devices and virtual machines both |
| [`NetBoxDeviceType`](reference/netboxdevicetype.md) | The kind whose identity needs a **required** reference: why an unresolved `manufacturerRef` writes nothing at all, why `uHeight` is a string, the only real fallback chain among the catalogue kinds, and why eleven counter caches and two image fields are absent |
| [`NetBoxPlatform`](reference/netboxplatform.md) | The `NestedGroupModel` whose uniqueness is not scoped by its own tree: `manufacturer_id__isnull` pinned and `parent` in no candidate at all, which is what makes its self-reference deferrable where `NetBoxDeviceRole`'s is not -- and why deleting one is *not* refused |
| [`NetBoxDevice`](reference/netboxdevice.md) | The first kind with **no containment parent at all**: why every one of `dcim.Device`'s foreign keys is `PROTECT` or `SET_NULL` and the cascade rule therefore produces none, a three-candidate natural key whose strongest entry is a column-level unique rather than a `meta.constraints` line, why `?name__ie=` is the difference between adopting a device and duplicating it, why `site_id` is never omitted, three one-to-ones that are stripped from every create, and the first inline child list in the catalogue: `interfaces` with their `addresses`, the sibling key a LAG is named by, and the two interface names differing only in case that one derived name cannot hold |
| [`NetBoxInterface`](reference/netboxinterface.md) | The kind that makes `IPAssignment.interfaceRef` resolve, and the largest spec in the catalogue with no engine code to go with it: an identity inherited from the parent model and matched case-**sensitively** where the device's is not, three self-referential foreign keys deferred independently, a 207-value enum pinned by a golden file, and twenty read-only columns including the reverse of the union that points at it |
| [`NetBoxIPRange`](reference/netboxiprange.md) | A run of consecutive addresses rather than a subnet: why there is no `size` field when the column is `REQ`, why both endpoints carry the containing prefix's mask, a natural key on a model with no `meta.constraints` whose address filters are exact only because they carry that mask, and why `IPRangeStatusChoices` has no `container` |
| [`NetBoxPrefixClaim`](reference/netboxprefixclaim.md) | "Carve me a /26 out of this container": why `prefixLength` is bounded twice -- statically by CEL and against the resolved parent by the controller -- why a `/16` asked for out of a `/16` is refused rather than allowed to duplicate the container, why there is no `vrfRef`, and why `status: container` is a *precondition* here and a refusal one page up |
| [`NetBoxIPRangeClaim`](reference/netboxiprangeclaim.md) | The one claim kind NetBox does not serialise: there is no `available-ranges` endpoint, so placement is arithmetic over the other ranges and the server arbitrates by refusing an overlap -- what `AllocationContended` means and why it is not `PoolExhausted`, why `alignment: PowerOfTwo` exists, and which two NetBox filters are deliberately not used |
| [`NetBoxContactGroup`](reference/netboxcontactgroup.md) | The third `NestedGroupModel` and the third identity out of one base class: `(parent, name)` with **no** conditional constraint behind the null-pinned variant, why the key is `name` where every `OrganizationalModel` uses `slug`, and why two top-level groups sharing a name is a `Conflict` the database will not prevent |
| [`NetBoxContactRole`](reference/netboxcontactrole.md) | The smallest kind in `tenancy`: no columns of its own, `slug` as its only natural key -- and why the base class rather than the app is what decides that, when its neighbour in the same file cannot use `slug` at all |
| [`NetBoxContact`](reference/netboxcontact.md) | A lookup key **no constraint backs at all** -- only an index -- so two contacts of one name is legal server state and a `Conflict` rather than a guess; plus the one place NetBox spells a group as a many-to-many, and why `PROTECT` means deleting a contact does not remove its assignments |
| [`NetBoxContactAssignment`](reference/netboxcontactassignment.md) | The widest polymorphic union in the catalogue and the first **`REQ`** pair to ship: 11 members over a column NetBox permits 25 types in, a required union with the `== 1` rule, an identity that mixes the pair with `?contact_id=` and `?role_id=` and the filterset lines that prove all four exist, why the role being in the key makes two assignments of one contact legal, and a containment parent that is the union rather than the contact |
| [`NetBoxCable`](reference/netboxcable.md) | The hardest identity in the catalogue, and the model the generic-FK union did **not** survive unchanged: two ends each a *list* of polymorphic references, a pair nested inside a list element instead of two columns, and a natural key built from a **representative** termination because `dcim.Cable` has no `meta.constraints` whatsoever -- so a `Conflict` means "something else is already plugged in there"; plus the only `UpdateStrategy: Recreate` kind, what the non-atomic delete-then-create window costs every `CablePath` through it, why `deletionPolicy: Retain` refuses that write outright, why reordering a termination list is zero API calls, why swapping the ends is a loud 400, and why `profile` cannot be cleared |
| [`NetBoxCableBundle`](reference/netboxcablebundle.md) | The plainest `PrimaryModel` there is, and the third answer to "what is a natural key": `name` backed by a real column `UNIQUE`, where an `OrganizationalModel` would use `slug` and `tenancy.Contact` has only an index -- plus why deleting a bundle destroys no cables and needs no `PROTECT`, and why a `slug`-mode reference to it can never resolve |
| [`NetBoxCustomField`](reference/netboxcustomfield.md) | The one kind the operator was already a writer of before it had a CRD: why a CR for `k8s_uid` is refused rather than adopted, why `type` is immutable in the schema, why `objectTypes` is not checked against the kind registry, and why deleting one is blocked until you say the loss is acceptable |
| [`NetBoxCustomFieldChoiceSet`](reference/netboxcustomfieldchoiceset.md) | The values a `select` custom field may hold: `[value, label]` pairs whose order is data, a `baseChoices` cleared with null rather than the empty string, and a JSON colour map compared as a whole document |
| [`NetBoxCustomLink`](reference/netboxcustomlink.md) | A Jinja2 button on a NetBox object's page: two required template bodies, content-type strings rather than references, and a colour enum assembled from two source files |
| [`NetBoxSavedFilter`](reference/netboxsavedfilter.md) | The first kind with two independently-unique columns, and why the second natural-key candidate is what makes editing a slug a rename rather than a duplicate |
| [`NetBoxExportTemplate`](reference/netboxexporttemplate.md) | A natural key NetBox does *not* enforce, so two templates sharing a name are a `Conflict` rather than a guess -- plus why the `SyncedDataMixin` git-sync columns are deliberately absent from every template kind |
| [`NetBoxConfigTemplate`](reference/netboxconfigtemplate.md) | The first kind that is taggable and not custom-fieldable, so it carries half a provenance stamp -- and a `debug` flag whose own help text says not to use it |
| [`NetBoxConfigContextProfile`](reference/netboxconfigcontextprofile.md) | The one kind whose provenance stamp does not follow its bases: a `PrimaryModel` whose REST serializer carries `tags` and no `custom_fields` at all, why the flag follows the API rather than the AST when NetBox ignores an unknown column instead of rejecting it, and a JSON Schema compared as a whole document |
| [`NetBoxConfigContext`](reference/netboxconfigcontext.md) | Thirteen to-many references on one kind and no engine code to go with them: why `tags` here is a selector rather than this object's own tags and what declaring it taggable would silently do, why thirteen lists bounded at 256 still clear the CEL budget, a required JSON document with no empty state, and why a kind with neither mixin carries no provenance and no multi-writer detection at all |
| [`NetBoxRIR`](reference/netboxrir.md) | The root of the allocation registry, and the smallest shape a kind can have while still mattering: `slug` from a column-level `UNIQUE` on a model with no `meta.constraints`, one `BooleanField` that has to be a pointer because it has a Django default, and no containment parent at all because the model declares no foreign key to cascade from |
| [`NetBoxAggregate`](reference/netboxaggregate.md) | A required reference that is *half the identity*: why an unresolved `rirRef` makes the engine wait instead of adopting the same prefix under another registry, why two CRs with the same `(prefix, rir)` give one `Ready` and one `Conflict` on a table with no uniqueness constraint, and the nullable `DateField` that has to be cleared with `null` rather than `""` |
| [`NetBoxASN`](reference/netboxasn.md) | The one kind with **no `name` and no `slug`**: a number as the whole identity, `int64` because a 4-byte ASN overflows 32 bits, why the required `rirRef` is deliberately *not* in the lookup, and why a `slug`-mode `ASNRef` can never resolve |
| [`NetBoxASNRange`](reference/netboxasnrange.md) | The model that redeclares the two columns it inherits, an `end < start` check that belongs to NetBox rather than to admission, why `(start, end)` is not an identity — and the `PROTECT`ed tenant that makes a `NetBoxTenant` in another namespace refuse to delete |
| [`NetBoxRole`](reference/netboxrole.md) | The kind that disambiguates three things called "role": `ipam.Role` here, `dcim.DeviceRole` behind a different alias and a different endpoint, and `NetBoxIPAddress.role` which is a string — plus why `weight` is a pointer and why `SET_NULL` on every referrer is the reason this kind defaults to `Retain` |
| [`NetBoxFHRPGroup`](reference/netboxfhrpgroup.md) | The kind with a field that is **deliberately absent**: why `auth_key` is neither inline nor a Secret reference yet, what the operator does to the column instead of writing it (nothing, ever), two closed enums with the NetBox source lines that prove they cannot be extended, and a `(protocol, group_id)` identity nothing in the database enforces |
| [`NetBoxFHRPGroupAssignment`](reference/netboxfhrpgroupassignment.md) | The one object kind with **no provenance stamp**: a bare `ChangeLoggedModel`, so no `tags`, no `custom_fields`, and no clearable field either — plus the first containment decision where *two* references genuinely cascade and only one slot exists, with the reasoning and the cost of the choice written down |
| [`NetBoxService`](reference/netboxservice.md) | A polymorphic pair, a many-to-many and an ordered array on one object: why `ports` is compared order-sensitively and `ipAddresses` is not, why `ports` is kept out of the lookup so a reorder can never produce a second row, how the parent pair reaches a query parameter by column name, and why `parent_object_type`'s `PROTECT` is not the cascade that matters |
| [`NetBoxServiceTemplate`](reference/netboxservicetemplate.md) | The same two columns as `NetBoxService` from the same abstract base, with the opposite kind of identity — a real `UNIQUE` instead of a convention — and why its `name` is matched case-*sensitively* where `dcim.Device`'s is not |
| [`NetBoxMACAddress`](reference/netboxmacaddress.md) | The kind NetBox 4.2 created by moving the MAC off the interface: the narrowest polymorphic union in the catalogue and the first that is deliberately *not* a reuse of a wider one, an identity no constraint backs at all so that two MACs on one interface is legal server state, why a declared-but-unresolved union writes nothing here when the same union on an address writes anyway, a CRD pattern narrower than what NetBox accepts because the other spellings cannot converge, and why the reverse half of the pair is absent rather than stubbed |
| [Generic references](reference/genericref.md) | The union shape a polymorphic foreign key takes in a spec: one member per legal target, `<= 1` versus `== 1`, why an empty union clears the reference while an absent one does not, and the six unions that ship: `IPAssignment`, `ScopeRef`, `ContactAssignmentTarget`, `FHRPInterface`, `ServiceParent` and `MACAssignment` |
| [`NetBoxRefGrant`](reference/netboxrefgrant.md) | The kind that describes no NetBox object: which namespaces may reference into this one, the wildcard and selector forms that keep one grant per catalogue namespace, why `NetBoxEndpoint` is the one exception, and why a grant is not NetBox authorisation |
| [`NetBoxSweep`](reference/netboxsweep.md) | The kind that reports rather than reconciles: what a sweep can and cannot see, why the tag and the cluster stamp are load-bearing, why it never deletes and has no flag that makes it, why a `DryRun` or `driftMode: Off` endpoint refuses the run, and why a truncated list is a refusal rather than a partial report |

### The shape of a reference page

Around 111 CRDs will follow, so the shape is settled here rather than after twenty pages
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
5. **M2M fields** — one subsection per many-to-many field, **only when the kind has one**.
   State the three states as a Spec / Payload / Meaning table — field absent means "do not
   manage", `[]` clears the relation, a list replaces it — and say that the middle row needs
   `metadata.managedFields` to be readable, because `omitempty` erases an explicitly-empty
   list on the way in ([field ownership](concepts/field-ownership.md)). Then: the ids are sent
   sorted and deduplicated and compared as a set, so **reordering the list writes nothing**;
   the field resolves **all or nothing**, and an unresolved element is named with its index
   (`importTargets[1]`); and a member contributes **no owner reference**, because a shared
   many-to-many is not containment
   ([ADR-0003](decisions/0003-ownership-and-references.md) §4).
   [`reference/netboxvrf.md`](reference/netboxvrf.md#specimporttargets-and-specexporttargets)
   is the worked example. The rule is not optional prose: NetBox replaces a many-to-many
   wholesale on `PATCH` and has no remove verb, so a page that leaves out the empty state has
   left out the only way to take the last element off.
6. **`status`** — a table of field, type, what populates it, and when. Say explicitly which
   fields are *not* cleared on failure.
7. **Conditions** — a table of type, when `True`, when `False`, and every `Reason` it can
   carry; then a reason glossary; then retry intervals.
8. **Kind-specific behaviour** — the one or two things about this kind that are not
   obvious. Cite `docs/netbox-schema.md` or a NetBox source path for every NetBox claim.
9. **Printer columns** — real `kubectl get <kind>` output, plus a table mapping column to
   JSONPath.
10. **Troubleshooting** — symptom → condition → cause → fix, driven off the `Reason`
    constants, since those enumerate the real failure modes.
11. **Related** — links to the concept pages and ADRs that explain the *why*.

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
| [Installing](install.md) | Helm, `make deploy`, every chart value and what it reaches, which values are the chart's and which belong in a CR, what the chart deliberately does not expose, and why a chart upgrade does not upgrade the CRDs |
| [Coexisting with Flux and Argo CD](operations/gitops.md) | Why the operator never writes a `spec` and how that is enforced, the Argo CD `ignoreDifferences` and Flux `Kustomization` snippets that make it quiet, the three `driftMode` values, the cluster-rebuild and NetBox-restore walkthroughs, the chart values that configure all of it and which of them are per-endpoint rather than per-install, and the NetBox permission model |
| [Exporting a live NetBox](operations/exporting.md) | How `nbctl export` turns a live NetBox into manifests, why references are emitted by CR name and what that trades away, how a CR name is derived and what a collision looks like, why an operator-managed object is skipped, and why a truncated read fails the whole run |
| [Provenance](operations/provenance.md) | What `spec.managedBy` writes into every NetBox object the operator manages, how the tag and custom-field definitions get bootstrapped, why stamping is not mandatory, what stops working when you turn it off, and why two clusters sharing one NetBox are never serialised |
| [The admission webhook](operations/admission-webhooks.md) | What the validating webhook checks and what CEL already covers, the degradation table for when it is down, why its `failurePolicy` is `Ignore`, why there is no defaulting webhook, and why cert-manager is the only certificate path |
| [Two writers, one NetBox object](operations/multi-writer.md) | The three multi-cluster shapes and which are supported, what the operator reports when two writers claim one object, what it deliberately does not call a conflict, the runbook for resolving one, and why writes are never serialised |
| [Observability](operations/observability.md) | Every metric with its labels, cardinality and what to alert on; which Events fire and when; the log levels and the stable key set, with `kubectl logs \| jq` recipes |
| [Sweeps](operations/sweeps.md) | What this cluster has left behind in NetBox: why a sweep reports and never reclaims, the three filters that decide what it can see, why the cluster stamp is what stops it accusing another cluster, the four verdicts and the grace period, the eight reasons it refuses to run, and what one run costs NetBox |
| [Stuck references](operations/stuck-references.md) | Which condition says why an object is waiting for another one, what the reference metrics mean together, how to find an object's referrers by hand, and which references nothing will ever wake |
| [The e2e suite](operations/e2e.md) | How to run the operator against a kind cluster and a real NetBox, what the seven runs of the ordering gate each prove, how to reproduce a seeded failure and read a dump diff, every environment variable, where it runs and where it deliberately does not, and how the next gate reuses the harness |
| [NetBox schema reference](netbox-schema.md) | The authoritative field list every CRD is derived from: 159 models, 138 endpoints, machine-extracted from NetBox 4.6.8. Grep it; do not read it |
| [Coverage](coverage.md) | Which NetBox endpoints have a Kind, which are deliberately excluded and why, which writable columns a shipped Kind does not map, and which natural-key candidates cannot be issued as a query. Generated by `make coverage`; stale is a test failure |
| [Regenerating the schema](regenerating.md) | How to retarget a newer NetBox release, how the Kind generator turns the IR into Go and what `overrides.yaml` may contain, how to test the extraction pipeline without a NetBox checkout, and how to cross-check the AST walk against a live instance |

## Examples

| Page | Answers |
|---|---|
| [Examples](examples/README.md) | Runnable manifests, and which milestone each one becomes real in |
