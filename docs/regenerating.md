# Regenerating the NetBox schema reference

`docs/netbox-schema.md` is generated, not hand-written. It is the input to every
CRD type in `api/`, so it must be refreshed whenever the operator targets a new
NetBox minor release.

```sh
git clone --depth 1 --branch v4.6.8 https://github.com/netbox-community/netbox.git /tmp/netbox-src
SRC=/tmp/netbox-src/netbox

python3 hack/extract-netbox-schema.py     "$SRC" > /tmp/models.json
python3 hack/digest-netbox-schema.py      /tmp/models.json  > /tmp/all-models.txt
python3 hack/extract-netbox-endpoints.py  "$SRC"            > /tmp/endpoints.txt
python3 hack/extract-netbox-api-schema.py "$SRC"            > /tmp/api-schema.json
python3 hack/build-netbox-ir.py /tmp/models.json /tmp/endpoints.txt /tmp/api-schema.json > /tmp/ir.json
```

The first three produce the reference document; the last two produce the code generator's IR
(see "One IR for the generator" below). All five are offline and take about five seconds.

The two extractors exit non-zero rather than produce a field list that is quietly wrong, so
run them with `set -e` and read stderr:

- **`app.Class is declared twice`** — two same-named classes in one app. Whichever the glob
  reached first used to win and the other's entire field list was dropped. Nothing downstream
  can tell which entry it got, so the run stops.
- **`cannot parse router.register(...)`** — the endpoint prefix must be a string literal and
  the viewset a `*ViewSet` name. A skipped registration is a Kind with no endpoint row, and a
  Kind with no endpoint row never gets a CRD at all.
- **`unknown field type XField, column omitted`** — a warning, not a failure: NetBox has a
  field class the extractor's whitelist does not list. That column is missing from the entry,
  so add the class to `FIELD_TYPES` and re-run before deriving anything from that model.
- **`base X is declared in more than one app and not in <app>`** — a warning: columns
  inherited from that base cannot be attributed and are left out of the subclass. A base
  resolves within the declaring model's own app first, so this fires only when the subclass's
  own app does not declare the name either — at 4.6.8 nothing does. Before NBO-041 it fired on
  the bare name and so dropped `ComponentModel`'s columns from eleven shipped component Kinds
  (`dcim.Interface`, `dcim.ConsolePort`, …), which lost `name`, `label` and `description`.
- **`!! <where>: <detail>` from the two NBO-041 scripts** — every choice value, serializer field
  or filter parameter that did not resolve. Each one is *also* a row in the IR's `unresolved`
  list, so a consumer can fail on it rather than trust a file that looks clean.

The model name in the endpoint map is the viewset name minus `ViewSet`, which is an
assumption: a viewset not named for its model yields an `app.Model` nothing matches. Check
that before splicing —

```sh
comm -23 <(awk '{print $2}' /tmp/endpoints.txt | sort -u) \
         <(jq -r 'keys[]' /tmp/models.json | sort)
```

Anything it prints is an endpoint whose model name could not be derived from its viewset:
either a genuinely model-less helper endpoint (NetBox has a few) or a Kind about to be missed.

Then splice the two text files into `docs/netbox-schema.md` under the
`## API endpoint -> model map` and `## Models` headings, and bump the version
line at the top of that file.

`digest-netbox-schema.py` takes an optional second argument: a comma-separated
list of bare model names, to print just those (handy while designing one CRD):

```sh
python3 hack/digest-netbox-schema.py /tmp/models.json Prefix,VLAN,IPAddress
```

## Testing the pipeline without a NetBox checkout

`test/fixtures/netbox-models/` is a hand-written miniature of a NetBox source tree:
a few Django model modules whose only job is to pin the behaviour of the extraction
pipeline, plus `test/fixtures/netbox-models-bad/` for the declarations that must *stop* the
run. `make test-schema` runs all four scripts over both trees and asserts the output, so the
extractors stay honest in CI — where cloning NetBox is not worth the minute.

```sh
make test-schema        # ruff check hack/ + python3 hack/test_digest.py
```

`make test-schema` runs `hack/test_digest.py` (the model walk and the digest) and
`hack/test_ir.py` (the API-schema walk and the IR).

Any fix to an extractor belongs with a fixture declaration that reproduces the bug.
The fixture is not a copy of NetBox and should stay small; `test/fixtures/netbox-models/README.md`
lists what each declaration is there to pin.

`docs/netbox-schema.md` is current as of NetBox `v4.6.8` and the post-NBO-093 scripts.
Two things are worth re-running whenever it is regenerated, because they check the merge
rather than the parse:

```sh
# Columns a subclass redeclares over a base's. Django lets the declared version win, and it
# is the one place the inherited-column merge can be wrong quietly.
jq -r 'to_entries[] | select(.value.shadowed) | "\(.key): \(.value.shadowed | join(", "))"' /tmp/models.json
```

At 4.6.8 that lists 16 models, all of them a narrowing rather than a change of type: nine
`*Template` kinds re-point `device_type`, and the rest re-declare an inherited `name`, `slug`,
`description` or `comments` (`ipam.ASNRange`, `ipam.VLANGroup`, `tenancy.TenantGroup`,
`wireless.WirelessLANGroup`, `dcim.RackReservation`, `dcim.VirtualDeviceContext`,
`extras.ConfigContextProfile`). Check such a kind's field list against the live REST schema
before deriving a CRD from it. The other invariant — no `meta.constraints` naming a column
absent from its own field list, which `hack/test_digest.py` asserts over the fixture — holds
over all 183 real models with zero exceptions.

The body also carries `netbox.*` entries for the abstract bases themselves
(`netbox.PrimaryModel`, `netbox.OrganizationalModel`, …). They have no API endpoint and are
not kinds; they are there so a reader can see where an inherited column comes from.

## One IR for the generator

The AST walk gives SQL truth: nullability, FK targets, `on_delete`, `Meta.constraints`, decimal
precision. Three things it cannot see, and every one of them has cost a day of hand-reading:

1. **Choice values.** The digest records `choices=SiteStatusChoices` and
   `def=UNRESOLVED:SiteStatusChoices.STATUS_ACTIVE`; the members live in `<app>/choices.py`.
   A ChoiceSet that declares `key = 'Site.status'` can also be **replaced or extended** by a
   deployment's `FIELD_CHOICES` setting (`utilities/choices.py`, `ChoiceSetMeta.__new__`), so
   its members are a default and not a closed set — 26 of the 89 choice sets the catalogue uses
   are extendable that way, and a CRD that pins one as an enum rejects a value that deployment
   considers legal.
2. **Writable vs read-only**, and the `app_label.model` spelling of a generic-FK type field.
   Both live in the REST serializers (`<app>/api/serializers*`): `Meta.fields` is the write
   path, and `ContentTypeField(queryset=ContentType.objects.filter(model__in=…))` is the only
   place the legal generic-FK targets are written down.
3. **The query parameters a Kind's filterset registers.** A natural key is a *query*, and
   django-filter silently ignores a parameter it does not register: the filter is dropped and
   the request returns the **unfiltered** result set, so the engine adopts the wrong object
   (#206). The registered names are the filterset's declared filters plus `Meta.fields`,
   expanded by the lookup maps in `utilities/constants.py` — and which map applies depends on
   the filter class (`netbox/filtersets.py:_get_filter_lookup_dict`, an *ordered* if-chain).

```sh
python3 hack/extract-netbox-api-schema.py "$SRC" > /tmp/api-schema.json
python3 hack/build-netbox-ir.py /tmp/models.json /tmp/endpoints.txt /tmp/api-schema.json > /tmp/ir.json
```

Nothing in the second script is a copy of NetBox's tables: the lookup maps, the
filter-class-to-map dispatch, `FILTER_DEFAULTS` and `STANDARD_LOOKUPS` are all *read out of the
source*, because a stale hardcoded copy of them is precisely the defect in #206. The one
exception is django-filter's own `FILTER_FOR_DBFIELD_DEFAULTS`, which belongs to the library
(pinned at `==26.1` by NetBox's `requirements.txt`) and is a small documented table in
`build-netbox-ir.py`; a column type absent from both tables is reported, never guessed.

### Where the two disagree

The REST schema wins — the operator talks to the API, not to Postgres — and the disagreement is
**recorded** in the IR's `conflicts` list rather than resolved in silence.

| Fact | Authority | Why the other source is wrong or silent |
|---|---|---|
| field exists on the write path; read-only | serializer `Meta.fields` / `read_only=` | the API is what we POST to |
| choice values and labels | `<app>/choices.py` | the AST gives only `choices=PrefixStatusChoices` |
| generic-FK type spelling (`dcim.site`) | serializer `ContentTypeField` queryset | the AST says `-> contenttypes.ContentType` |
| FK target model | `models.json` `to` | the serializer flattens FKs into nested serializers |
| `on_delete` | `models.json` | absent from the serializer; needed for PROTECT messaging |
| required-on-create | `models.json` (NOT NULL, no default) ∩ serializer `required` | each source marks fields required the other does not |
| unique constraints / natural keys | `models.json` `meta.constraints` | absent from the API surface entirely |
| decimal precision | `models.json` `max_digits`/`decimal_places` | the API types decimals as strings |
| read-only cached columns | both (`_`-prefix and `CounterCacheField`; `read_only=`) | belt and braces — writing one silently no-ops |
| endpoint path | `endpoints.txt` | never derive by pluralising: `virtualization.VMInterface` lives at `virtualization/interfaces` |
| query parameter names and lookups | `<app>/filtersets.py` + `utilities/constants.py` | neither the ORM nor the serializer knows them |

### Reviewing the IR

At 4.6.8 the IR covers 134 kinds, 89 enums, 3356 filter parameters and 85 natural-key
candidates, of which **21 are unusable** — the candidate names a column whose filter parameter
NetBox does not register. Every one of those 21 is a null pin on a foreign key: an FK filter is
a `ModelMultipleChoiceFilter`, which takes `FILTER_NEGATION_LOOKUP_MAP`, so it registers `n` and
nothing else. Neither `?vrf_id__isnull=true` (what the operator emits today) nor
`?vrf_id__empty=true` (#206's proposed fix) is a parameter that exists, and django-filter drops
both without a word. `?scope_id__empty=true` *does* exist, because `scope_id` is numeric and
`empty` maps to `isnull` there; on a char column such as `rd` the same suffix asks about
**string emptiness** instead, which is a different question with the same spelling.

Two lists are worth reading on every regeneration, because they are where a version bump breaks
the generator quietly:

```sh
jq -r '.unresolved[] | "\(.kind)\t\(.where)\t\(.detail)"' /tmp/ir.json
jq -r '.conflicts[] | "\(.fact)\t\(.kind).\(.field)"' /tmp/ir.json | sort | uniq -c
```

### The committed artifacts

`hack/testdata/` holds the version-stamped inputs and the IR, gzipped:

```
models-4.6.8.json.gz      the Django AST walk
endpoints-4.6.8.txt       the endpoint -> model map
api-schema-4.6.8.json.gz  choices, serializers, filtersets, lookup maps
ir-4.6.8.json.gz          the merged IR — the code generator's only input
```

They are committed so the generator runs in CI with no NetBox checkout and no network, and so a
version bump produces a reviewable diff of the inputs first and of the IR second, rather than
one opaque regeneration. Read one with `gzip -dc hack/testdata/ir-4.6.8.json.gz | jq .`

### After a regeneration: re-run the coverage audit

`make coverage` joins the new IR against `internal/registry` and rewrites `docs/coverage.md`
(the audit is `TestCoverage` in `internal/registry/coverage_test.go`). Commit the result: the
same test compares the committed document against a fresh run, so a NetBox release that adds
a model, or a Kind that quietly left the registry, arrives as a reviewable diff rather than
as a number nobody checked.

Four things fail on their own, at any coverage level, and are worth reading before the diff:
a column NetBox requires on create that no spec field writes, an entry in
`hack/coverage-exclusions.yaml` the new schema no longer bears out, a Descriptor whose
endpoint or object type the schema does not have, and a `Taggable`/`CustomFieldable` flag the
new serializers contradict.

### What a live NetBox would still add

Everything above is read from the source, so it is only as good as the reading. `/api/schema/`
would confirm three things this cannot:

- that a parameter the IR calls registered really is (the derivation mirrors
  `BaseFilterSet.get_filters`, but it is a mirror);
- that `?vrf_id=null` — the remaining candidate spelling for an FK null pin — is accepted, since
  that depends on django-filter's `MultipleChoiceFilter.null_value` rather than on NetBox;
- the `required` and `readOnly` flags DRF computes for the `Meta.fields` entries a serializer
  does not declare explicitly, which the IR leaves as "on the write path, requiredness from the
  ORM".

None of the three is settled by reading the IR, so settle them against a live instance. The
[e2e suite](operations/e2e.md) exercises the operator end to end against a real NetBox, but it
proves convergence rather than schema fidelity — for a schema question, ask the instance
directly:

```sh
curl -sH "Authorization: Token $NETBOX_TOKEN" \
  "$NETBOX_URL/api/schema/?format=json" > /tmp/netbox-openapi.json
```

## Emitting the Kinds

`hack/gen-types` turns the IR into Go: three files per Kind plus two shared ones.

```sh
make gen-kinds                      # ir -> emit -> make generate manifests
make gen-check                      # write nothing; fail if any output differs from the tree

go run ./hack/gen-types -kinds ipam.Prefix,ipam.VRF -out /tmp/scratch
```

| Output | Content |
|---|---|
| `api/v1alpha1/<app>_<kind>.go` | Spec/Status/List structs, kubebuilder markers, printer columns |
| `internal/registry/<app>_<kind>.go` | the `Descriptor` literal and its `init()` |
| `internal/controller/<app>_<kind>_controller.go` | the RBAC markers and the one-line `init()` |
| `api/v1alpha1/zz_generated_refs.go` | every typed `ObjectRef` alias, deduplicated by target |
| `api/v1alpha1/zz_generated_enums.go` | every choices class as a Go type and const block |

The two shared files are built from the **whole** catalogue, hand-written Kinds included, and
never from a `-kinds` subset: they are keyed by NetBox name rather than by Kind, so narrowing
them to three Kinds would delete the other hundred's enums.

`-kinds` names a Kind explicitly and emits it even when `overrides.yaml` marks it
`handWritten`. That is how the generator is diffed against a human — point `-out` at a scratch
tree, not at the repository.

### Which Kinds a full run emits

The ones that have a `kinds:` row in `overrides.yaml` and are not marked `handWritten`. Two
things follow from requiring the row, and both are deliberate:

- **A Kind nobody has triaged is not emitted with every default.** Its `kubectl` short name and
  its printer columns are judgements with no schema behind them, so a Kind emitted without a row
  would ship an abbreviation and a `kubectl get` nobody chose.
- **Adding a Kind stays data entry.** The row *is* the work.

So `make gen-kinds` writes **nothing** today, and exits zero: every NetBox Kind in the tree is
hand-written, and the ~100 that are not in the tree have no row yet. It reports what each
untriaged Kind still needs on stderr — that list is the remaining catalogue work, and it is a
report rather than a failure because a Kind nobody has got to yet blocks nothing that *is*
being emitted. The two shared files follow the same rule: they serve the generated Kinds, so a
run that emits no Kind emits neither of them, which is also what keeps a second declaration of
every typed alias out of a package whose aliases are still hand-written.

`gen-check` is not wired into `make verify` for the same reason — there is nothing committed for
it to check. NBO-043 flips the M3/M4 Kinds from `handWritten` to generated one at a time and
wires it in.

### How close the emitters are to the hand-written Kinds

`TestEveryHandWrittenKindEmits` pins this, so it moves as a reviewable test change rather than
as a number in a document nobody rereads. At NetBox 4.6.8, **27 of the 30** shipped NetBox Kinds
emit. All three that do not are gaps in the **IR**, not in the emitter:

| Kind | What the IR cannot supply |
|---|---|
| `ipam.IPAddress` | the permitted object types for `(assigned_object_type, assigned_object_id)`: the serializer's `ContentTypeField(queryset=…)` is built dynamically rather than written as a literal the AST walk can evaluate, and an empty `AllowedTypes` means "the type half accepts anything" — the opposite of what a union is for |
| `tenancy.ContactAssignment` | the same, for `(object_type, object_id)` — the widest union in the catalogue |
| `extras.Tag` | `name` and `slug`. Both are declared on taggit's `TagBase`, which lives **outside** the NetBox source tree, so the AST walk sees neither. Same class of gap as `mptt.MPTTModel`'s `_depth`/`_children`, which `readOnlyExtra` covers — except that these two are the Kind's whole identity |

`extras.Tag` is worth reading as the reason the emitter refuses rather than emits: without the
check, it would produce a `NetBoxTag` with a colour and no name, whose declared `slug` lookup
key reads a field that does not exist — and the first sign of it would be a `Descriptor` that
fails `Validate()` at boot. A lookup candidate naming a spec field no column produces is
therefore a hard failure, and it is the one check that turns a missing out-of-tree base from a
silent wrong CRD into a named refusal.

What emitting is *not* is byte-identical to what a human wrote. That gap is NBO-043's, and it
is almost entirely prose: a hand-written Kind carries cited paragraphs about NetBox that no
reading of the schema produces, and the generator deliberately emits only facts it has
(CONTRIBUTING.md bans a comment that restates the code). Of the mechanical differences, three
are worth knowing before reading a diff:

- **Natural keys.** The derivation makes a candidate out of every column-level `UNIQUE`, so
  `dcim.Manufacturer` comes out with `name` *and* `slug` where the human chose `slug` alone —
  a Kind gets one identity. Fixed per Kind by a `naturalKeys:` row, which replaces the derived
  set outright.
- **Columns the human left out.** `dcim.Manufacturer.comments` and `ipam.VRF.tenant` are
  writable columns no shipped spec field maps; the generator emits both. Those are coverage
  gaps in the hand-written Kind rather than generator noise — `docs/coverage.md` is where they
  are tracked.
- **Receiver names.** The emitter spells every receiver `o`; the hand-written Kinds use the
  Kind's initial.

Of the 27 that emit, 18 differ in their candidate list and 19 in their field list. Four —
`ipam.VLANGroup`, `tenancy.Contact`, `tenancy.ContactGroup` and `virtualization.VirtualDisk` —
differ in **nothing but** prose, header and receiver names, which makes them the cheapest first
entries for the gate.

### What `overrides.yaml` may contain

Three categories, and nothing else. A fourth entry is a bug in the derivation: a fact that
could be read out of NetBox belongs in `hack/build-netbox-ir.py`, and a per-Kind *behaviour*
belongs in the `Descriptor` as data. **No template may name a Kind** —
`TestTemplatesNameNoKind` greps every template for every model name, object type and CRD kind
in the IR, because a per-Kind branch in a template is a `switch` on Kind with extra
indirection and it defeats the reason the generator exists.

| Key | Why it cannot be derived |
|---|---|
| `shortNames` | there is no schema for what a human types; the default `nb` + model is unusable for a long one (ADR-0001) |
| `printerColumns` | which columns matter in `kubectl get` is a judgement |
| `cascades` | half the answer is a `GenericRelation` on the *target* and half an `on_delete=CASCADE` cache column on the referrer (#214) |
| `containmentRef` | which one of several cascading references gets the single owner reference (ADR-0003 rule 4) |
| `naturalKeys` | for a Kind whose identity is a NetBox *convention* rather than a `UNIQUE` — `ipam.Prefix` has no `meta.constraints` at all |
| `readOnlyExtra` | `mptt.MPTTModel` is outside the NetBox tree, so the AST walk never sees `_depth` or `_children` |
| `goTypes` | an `ArrayField`'s element type is a constructor argument the AST walk does not record |
| `inherited` / `omit` | a column the embedded `NetBoxObjectSpec` already supplies, or one deliberately absent |
| `extraCEL` | a rule that is a fact about NetBox rather than about the column, such as `isCIDR` |
| `extraRefs` | an alias no NetBox foreign key produces — nothing in NetBox points at `ipam.Prefix`, but `NetBoxIPAddressClaim` does |
| `deferred` | which columns may be left out of a create payload, beyond the self-references that derive their own — see below |
| `retainOnDelete` | whether a deleted CR leaves the NetBox row alone: an IPAM row is allocation that outlives the CR that asked for it (#176), and nothing in NetBox says so |
| `duplicateSpec` | the spec field that makes several natural-key matches legal rather than a `Conflict`; which duplicates NetBox permits depends on the enclosing VRF |
| `updateStrategy` / `recreateOn` | `dcim.Cable`'s identity lives in its terminations, so its diff cannot be PATCHed (NBO-049). The alternative is a kind switch in the engine |
| `unionTypes` | the shared Go union type behind a polymorphic pair when it is not named after the pair: `(scope_type, scope_id)` defaults to `ScopeRef`, `ipam.IPAddress`'s pair is `IPAssignment` |

Every one of those six is a `Descriptor` field a hand-written Kind already carries, and the
reason they are here rather than derived is the same in each case: they are policy, and the
emitted `Descriptor` may not *lose* a per-Kind fact the hand-written one had. `retainOnDelete`
is the sharp one — dropped, an IPAM Kind starts deleting NetBox rows on `kubectl delete`.

### Which references are deferred, and why one is not

A deferred column is left out of the create payload and applied by a follow-up PATCH. Every
**self-reference** derives `DeferIfUnresolved` with nothing declared: a foreign key to the
Kind's own model cannot be satisfied on create until the parent exists. `IfUnresolved` and never
`Always`, because stripping a *resolved* `parent` would create the object top-level for one
pass, where it can adopt an unrelated top-level object of the same name — and the follow-up
PATCH would then reparent that object.

The derivation excludes a self-reference **any natural-key candidate matches on**, and that
exclusion is what makes it safe rather than merely plausible. `dcim.DeviceRole` keys on
`(parent_id, slug)` and on `slug` with `parent_id` pinned null, so stripping `parent` would
change the identity the lookup had already decided on — `registry.Descriptor.Validate` refuses
that pair outright (`ErrDeferredNaturalKey`), and a child role writes nothing and waits instead
([`docs/reference/netboxdevicerole.md`](reference/netboxdevicerole.md)).

Anything else is an entry in `deferred:`, valued `Always`, `IfUnresolved` or `Never`:

| Entry | Why it is not derivable |
|---|---|
| `virtualization.VirtualMachine: {primary_ip4: Always}` | neither column is a self-reference; the ring is `VM -> IPAddress -> VMInterface -> VM` and no apply order breaks it |
| `dcim.Interface: {qinq_svlan: IfUnresolved}` | points at `ipam.VLAN` rather than at this model, so nothing about its shape says it may be deferred |
| `ipam.VLAN: {qinq_svlan: Always}` | *is* a self-reference, so the derived mode would be `IfUnresolved`; a Q-in-Q service VLAN can never be satisfied on create |
| `ipam.IPAddress: {nat_inside: Never}` | switches the derived deferral off: the engine already omits an unresolved reference, so deferring strips nothing — but it *does* stop `resolver.blocking` following the edge, and a mutual pair would then be created rather than reported as a `RefCycle` |

A `deferred:` key naming a column the Kind does not have is a hard failure, not a no-op. A typo
there is the worst possible outcome, because it looks like it worked: the deferral silently does
not happen and the create carries a reference the server cannot satisfy.

### What it refuses to emit

Every refusal names the Kind, the column and the missing fact, and a full run reports all of
them in one pass rather than one per invocation. Nothing is emitted on a guess:

- a column with no Go type — an `ArrayField` with no `goTypes` entry, or a Django class the
  mapper does not know;
- a null-pinned column whose filter class cannot be decided — `registry.NullColumn` has no zero
  value, so an undeclared class fails `Validate()` at boot;
- a Kind with no natural-key candidate, which would fail `Validate()` with `ErrNoNaturalKey`;
- a polymorphic reference whose permitted object types the IR could not resolve, since an empty
  `AllowedTypes` means the type half accepts anything — the opposite of what a union is for;
- a `CustomFieldable` Kind absent from `stampedObjectTypes` in
  `internal/controller/provenance_test.go`. That list is deliberately **not** generated: it is
  the one independent copy of the set and the only assertion that can catch a Kind dropped from
  the provenance stamp, so emitting it would make the assertion a generator agreeing with
  itself. The emitter checks it and prints the paste-ready lines instead.

### How a null pin is spelled

An FK filter in NetBox is a `ModelMultipleChoiceFilter`, which takes
`FILTER_NEGATION_LOOKUP_MAP` and so registers `n` and nothing else: neither `?vrf_id__isnull`
nor `?vrf_id__empty` exists, and django-filter drops both without a word. The class is decided
from the column's own Django field class and there is no default:

| Column | `registry.NullColumn` | On the wire |
|---|---|---|
| `ForeignKey`, `OneToOneField`, `TreeForeignKey` | `NullColumnRef` | `?parent_id=null` |
| `CharField`, `SlugField`, `TextField`, … | `NullColumnChar` | `?rd=null` |
| any non-FK numeric column | `NullColumnNumeric` | `?scope_id__empty=true` |
| the type half of a polymorphic pair | — | redirected to the paired `_id` half |

The last row is not a shortcut. `scope_type`'s filter is `MultiValueContentTypeFilter`, which
registers neither spelling, and the sentinel there is worse than dropped: it makes the filter
`scope_type__in=[]`, which matches **nothing**, so the engine would create a duplicate instead
of adopting. The `_id` half asks the same question, because NetBox refuses one half of the pair
without the other.

The IR marks every FK null pin `unusable`, which was true until the sentinel landed. The IR is
the authority on *what the filterset registers*; the emitter is the authority on what the
client can spell, and those are different questions.

### An extendable choice set is emitted open

26 of the 89 choice sets declare a FIELD_CHOICES `key`, so a deployment's `FIELD_CHOICES`
setting can **replace or extend** their members: their values are a default, not a closed set.
Those get the Go type and the const block — the documentation and the spelling a manifest should
normally use — and **no `+kubebuilder:validation:Enum` marker**, bounded by `MaxLength` instead.
Pinning them would make the API server reject a value that deployment considers legal, and an
admission rejection is not a state the operator can report its way out of. The other 63 are
pinned.

Every value in an `Enum` marker is quoted, which is load-bearing rather than tidy: unquoted,
controller-gen parses `2.5gbase-t` as the number `2.5` and silently discards the rest. For the
same reason a generated CEL rule uses `[0-9]` and `[.]` rather than `\d` and `\.` — a backslash
inside a double-quoted marker value is read as a Go char escape, and `\d` is not one, so the
whole marker is rejected and no CRD is written at all.

### Clobber protection

Three independent mechanisms, because forgetting is easier than remembering:

1. `handWritten:` in `overrides.yaml` — ingested and validated, emitted nowhere.
2. **The header guard.** Every output path is checked *before* any file is written; if one
   exists and does not begin with `// Code generated by hack/gen-types. DO NOT EDIT.` the whole
   run aborts and writes nothing, because a partial regeneration leaves the tree in a state no
   diff explains.
3. `-check` — regenerate in memory and diff against the working tree, not against git, so a
   tree with legitimately uncommitted work reports exactly the files the emitter would change.

### The golden files

`hack/gen-types/testdata/golden/` holds three Kinds' emitted output, byte for byte:

```sh
go test ./hack/gen-types                              # compare
go test ./hack/gen-types -run Golden -update          # rewrite, then read the diff
```

Bytes rather than properties, because bytes are what NBO-043 gates on: a template change that
reflows a comment is harmless and one that drops a marker is a CRD the API server refuses at
install, and only a byte comparison tells a reviewer which one they are looking at. The three
Kinds are chosen to cover, between them, every part of an emitted file that has gone wrong here
before — `ipam.VRF` for the two to-many references and their `MaxItems` bound, `ipam.VLANGroup`
for a polymorphic union with per-member cascades and an `ArrayField`, and
`virtualization.VMInterface` for two derived self-referential deferrals next to one declared.

The two shared files are deliberately **not** pinned by bytes. They are the whole catalogue's
enums and typed aliases — 200 kB that moves whenever any of 134 Kinds does — so a golden copy
would be repo bulk nobody could review as a diff. Their properties are pinned instead, by
`TestEnumConstantsAreUniquePerType`, `TestExtendableEnumsAreNotPinned` and
`TestEmitIsDeterministic`.

The header carries the IR's **base name** and the SHA-256 of the IR file, and no directory: the
base name is version-stamped (`ir-4.6.8.json.gz`) so it still says which release the file holds,
and dropping the path is what keeps the header identical whether a run spelled `-ir` relatively
or absolutely. A header that changed with the caller's working directory would make `-check`
fail in CI for reasons unrelated to the change.
