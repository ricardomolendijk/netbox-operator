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

e2e against a live instance is deferred to #29. Until then:

```sh
curl -sH "Authorization: Token $NETBOX_TOKEN" \
  "$NETBOX_URL/api/schema/?format=json" > /tmp/netbox-openapi.json
```
