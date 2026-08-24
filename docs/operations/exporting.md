# Exporting a live NetBox as manifests

`nbctl export` reads a NetBox instance and writes CR manifests to a directory. It is how a
NetBox that nobody generated from a file becomes desired state: run it, read the diff,
commit it.

It is the **only** supported answer to "I want NetBox's current contents to become desired
state", and the shape of that answer is deliberate. See
[there is no mode where NetBox wins](gitops.md#there-is-no-mode-where-netbox-wins).

## What it does not do

| It does not | Because |
|---|---|
| Write to NetBox | The exporter is handed a one-method `Lister` interface. There is no `Create`, `Patch` or `Delete` to call, so a coding mistake here cannot mutate NetBox — and the client is built in `DryRun` mode as well |
| Write to a cluster | It emits files. It reads no kubeconfig and needs no cluster to run |
| Write to Git | **The operator never holds repository credentials** ([ADR-0005 §4](../decisions/0005-gitops-coexistence.md), issue #22). `export` writes a working tree; you review it and you commit it |
| Run as a controller | Promoting a NetBox edit into a CR `spec` would make the operator a second writer to desired state, which [ADR-0005 §1](../decisions/0005-gitops-coexistence.md) forbids. There is no controller that can do this and there is not going to be one |
| Export a log | Change records, journal entries and object-change history are a log rather than a state |
| Invent claims | An address that exists is a `NetBoxIPAddress`. A `NetBoxIPAddressClaim` is a statement of intent NetBox does not record, and exporting one would make a re-apply allocate a second address |

## Running it

```sh
export NETBOX_TOKEN=...            # never a flag: a flag value is in every `ps` listing
nbctl export \
  --url https://netbox.example.com \
  --endpoint homelab \
  --namespace homelab \
  -o manifests/
```

Then the part no tool does for you:

```sh
git diff manifests/         # read it
git add manifests/ && git commit -m 'adopt netbox contents'
kubectl apply -f manifests/
```

### Flags

| Flag | Default | What it does |
|---|---|---|
| `--url` | *required* | NetBox base URL to read from. The token comes from `NETBOX_TOKEN` |
| `--endpoint` | *required* | The `NetBoxEndpoint` name written into every object's `spec.endpointRef` |
| `-n`, `--namespace` | *required* | The namespace every object is written into |
| `-o` | *required* | Output directory, created if absent |
| `--kinds` | every registered Kind | Comma-separated Kinds, e.g. `NetBoxSite,NetBoxPrefix` |
| `--split` | `kind` | `kind` writes one file per Kind; `single` writes `export.yaml` |
| `--full` | off | Keep fields whose value is empty |
| `--id-refs` | off | Emit every reference as a NetBox id rather than as a CR name |
| `--include-managed` | off | Also export objects the operator already manages |
| `--managed-tag` | `k8s-managed` | The provenance tag, from `NetBoxEndpoint.spec.managedBy.tag` |
| `--dry-run` | off | List the files that would be written and write nothing |
| `--force` | off | Overwrite manifests that are already there |

`--endpoint` is required and is **not** derived from `--url`. `spec.endpointRef` names a
`NetBoxEndpoint` in the destination cluster, which is a property of that cluster and not of
NetBox; a guessed default would produce manifests that apply cleanly and then wait forever
for an endpoint nobody created.

`-o` refuses to overwrite an existing file unless `--force` is given, and it checks every
file before writing the first one. A run that failed halfway would leave a directory half
from one export and half from another, which is the one output nobody can review.

## References: names, not ids

A NetBox object carries `site: {id: 3}`. A CR can carry `siteRef: {name: …}`,
`{slug: …}`, `{lookup: …}` or `{id: 3}` ([references](../concepts/references.md)). The
export picks by name, and falls back to the id:

- **Target inside the export set** → `{name: <its CR name>}`. This is the only mode that
  expresses a dependency the operator can wait on, so a directory applied in any order
  converges, and the only one that survives being applied to a different NetBox — ids are
  per-instance, so a manifest full of them can only be applied back to the machine it came
  from.
- **Target outside the export set** → `{id: N}`, and the run reports each one on stderr.
  There is no CR to name. This is what a reference to a Kind that has no CRD yet looks
  like: a prefix's `roleRef` points at `ipam.Role`, which has no Kind, so it is exported as
  the id it is.
- **`--id-refs`** → `{id: N}` everywhere, for a mechanical dump nobody is going to review.

**The trade this makes.** By-name refs cost nothing extra at export time — every
exportable Kind is paged once in the index pass anyway, so turning an id into a name is a
map lookup rather than a second fetch. What they do cost is a rename: an object renamed in
NetBox exports under a new CR name, and the old CR has to be deleted. Id refs have the
opposite profile — stable against renames, useless in Git, and unreviewable. `slug` refs
are deliberately not used: a slug is not globally unique for every Kind
(`ipam.VLANGroup` is unique on `(scope_type, scope_id, slug)`, see
[`NetBoxVLANGroup`](../reference/netboxvlangroup.md)), and a slug ref resolves straight
against NetBox rather than against a CR, so it silently opts out of the dependency graph
the operator uses to order writes.

## Names, and what happens when two objects want the same one

A CR name has to be a DNS subdomain. A NetBox name does not: `Home Lab / Rack 3` and
`10.0.20.0/24` are both ordinary NetBox values and neither is a legal object name. So the
name is derived, in this order:

1. The object's **`slug`**, if its Descriptor maps one — that is already the identifier
   NetBox itself puts in a URL.
2. Its **`name`**, sanitised.
3. Its **natural key**'s scalar fields, joined — which is what identifies a prefix
   (`10.0.20.0/24` → `10-0-20-0-24`).

Sanitising lowercases, replaces anything else with `-`, collapses runs and trims each
label, so every emitted name matches the pattern `ObjectRef.name` itself is validated
against. A name over 253 characters is cut to fit and ends in a digest of the object, so
two objects sharing a long prefix cannot collapse onto one name.

**Collisions are reported, never silently merged.** Two NetBox objects can legitimately
reduce to one CR name — the same CIDR in two VRFs, the same VLAN name in two sites. Objects
are walked in NetBox id order, the first keeps the plain name, and every later one takes a
`-<8 hex>` suffix derived from its object type and id. Both are stable across runs, and
each collision is printed:

```
note: NetBoxVLAN "mgmt (10)" collides on name "mgmt" with another object; exported as "mgmt-4f7a1c02"
```

One file holding two documents with the same `metadata.name` is a manifest whose second
`kubectl apply` overwrites the first object's spec, so this is a case worth a suffix and a
line of output rather than a quiet win for whichever object came last.

## Objects the operator already manages

By default an object carrying the operator's provenance is **skipped**, and the count is
reported. Three signals, any one of which is enough
([provenance](provenance.md)):

- the `k8s_uid` custom field (or any other field in `spec.managedBy`) holds a value;
- the object carries the `--managed-tag` tag;
- the object **is** that tag — `internal/provenance`'s bootstrap creates and maintains it,
  so a `NetBoxTag` CR describing it would be a second writer for the operator's own
  bookkeeping, and deleting that CR would delete the tag every stamped object depends on.

The reason is not tidiness. A managed object's desired state already lives in Git; a second
CR claiming the same NetBox object is a `Conflict`, not a backup. `--include-managed`
exports them anyway, for when you want the export to be a snapshot of everything.

A skipped object is outside the export set, so a reference **to** it is emitted as
`{id: N}` and reported. That is the honest answer rather than a shortcoming: there is a CR
for it somewhere in Git, but the export does not know what it is called, and inventing a
name would produce a reference that never resolves. Replace those ids with the real CR
names as part of reviewing the diff, or re-run with `--include-managed` to see the whole
graph by name.

Nothing the operator writes about itself is ever exported, in either mode: `k8s_uid`,
`k8s_cluster`, `k8s_owner` and `k8s_allocation_identity` are excluded from
`spec.customFields`. The allocation identity especially — putting one claim's private
bookkeeping into a manifest would pin an allocation into Git and then have the operator
argue with it ([ADR-0005 §3](../decisions/0005-gitops-coexistence.md)).

## What is never exported

Only fields a Descriptor's field map names are emitted, which makes the rule structural
rather than a filter somebody has to keep in step: the registry already refuses a spec
field mapped onto a read-only column, so `id`, `url`, `display`, `created`,
`last_updated`, every `_`-prefixed cached column and every counter cache have no spec field
to be emitted into.

That includes the cached scope columns, and they are the case worth stating outright.
`ipam.Prefix`, `ipam.VLANGroup`, `virtualization.Cluster` and `wireless.WirelessLAN` carry
`(scope_type, scope_id)` plus the read-only caches `_region`, `_site_group`, `_site` and
`_location` ([generic references](../concepts/generic-refs.md)). The export reads the pair
and emits a `scope:` union; it never reads `_site`. A prefix scoped to a **Location** also
carries `_site`, so believing the cache would export `siteRef` for an object that is not
scoped to a site — which then round-trips as a silent no-op forever, because the column the
spec names does not exist. `ipam.VLAN.site` *is* a real foreign key and does emit
`siteRef`.

## `--full` and what "minimal" drops

The default drops fields whose value is empty: a null, an empty string, an empty list. An
omitted spec field means "do not manage this column"
([field ownership](../concepts/field-ownership.md)), and for a column that is already empty
that is the same state as writing the empty value into it — so dropping it costs nothing
and turns 3000 lines of `comments: ""` into a file somebody will actually read. `--full`
keeps them, for when the export is meant as a backup.

`false` and `0` are **never** dropped, in either mode. NetBox's own column defaults are not
in the registry, so "this value equals the default" is not a question the export can answer,
and guessing would drop a deliberately-false `is_pool` and hand the column back to whatever
NetBox happens to hold.

## A truncated read is a failure, not a smaller export

Every list follows NetBox's pagination to the end. If it hits the client's page cap, the
whole run **fails** and nothing is written:

```
nbctl: listing ipam/prefixes: list of ipam/prefixes truncated at the 1000-page cap after 250000 objects; results would be incomplete
```

A partial export is indistinguishable from a complete one once it is in Git, and the next
`kubectl apply --prune` against it is a request to delete everything the export missed.
Narrow the export with `--kinds`, or raise the page cap.

Nothing is written until every Kind has been read successfully, for the same reason.

## Determinism

Two exports of an unchanged NetBox produce byte-identical files. There are no timestamps
and no hostnames in the output, Kinds are emitted in Kind order, objects in NetBox id
order, to-many references sorted, object-type lists sorted, and keys sorted by the YAML
encoder. The diff between two runs is the change in NetBox and nothing else, because that
diff is the thing a human is being asked to read.

## Not implemented yet

`export` covers the Kinds that have Descriptors. These parts of the original design need
tickets of their own and are deliberately absent rather than half-built:

- **`--adopt`** (NBO-039): annotating each object with the NetBox id it came from. The
  annotation names are that ticket's to define.
- **`--selector` / `--filter`**: NetBox-side filtering. `--kinds` is the subset knob that
  exists; add these when a NetBox is large enough to need them.
- **`--catalog-namespace` and `NetBoxRefGrant` emission** (NBO-014): everything lands in
  one namespace, so no reference crosses one and no grant is needed. A split needs a
  per-Kind statement of which namespace a Kind belongs in, which no Descriptor carries yet.
- **`--pending-dir`**: only registered Kinds are exported, so nothing can be emitted for a
  CRD that does not exist.
- **`tags`**: no Kind's spec has a `tags` field yet (NBO-073), so there is nothing to
  export tags into.
- **`--concurrency`**: Kinds are listed one after another.

## Related

- [Coexisting with Flux and Argo CD](gitops.md) — why Git is authoritative and why there is
  no write-back
- [ADR-0005 — Coexisting with Flux and Argo CD](../decisions/0005-gitops-coexistence.md) —
  §1 no spec writes, §3 the allocation identity, §4 no Git write-back
- [Provenance](provenance.md) — what the operator stamps on the objects it manages
- [References](../concepts/references.md) — the four resolution modes a ref can take
- [Generic references](../concepts/generic-refs.md) — the scope pair, and why `_site` is
  never read
