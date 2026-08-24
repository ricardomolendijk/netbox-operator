# Regenerating the NetBox schema reference

`docs/netbox-schema.md` is generated, not hand-written. It is the input to every
CRD type in `api/`, so it must be refreshed whenever the operator targets a new
NetBox minor release.

```sh
git clone --depth 1 --branch v4.6.8 https://github.com/netbox-community/netbox.git /tmp/netbox-src
SRC=/tmp/netbox-src/netbox

python3 hack/extract-netbox-schema.py    "$SRC" > /tmp/models.json
python3 hack/digest-netbox-schema.py     /tmp/models.json  > /tmp/all-models.txt
python3 hack/extract-netbox-endpoints.py "$SRC"            > /tmp/endpoints.txt
```

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
- **`Name is declared in more than one app`** — a warning: columns inherited from that base
  cannot be attributed and are left out of the subclasses. Same class name in two apps is
  legitimate, so this is not fatal, but the omission is real.

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
make test-schema        # == python3 hack/test_digest.py
```

Any fix to an extractor belongs with a fixture declaration that reproduces the bug.
The fixture is not a copy of NetBox and should stay small; `test/fixtures/netbox-models/README.md`
lists what each declaration is there to pin.

> **The committed `docs/netbox-schema.md` predates NBO-067, NBO-070, NBO-071 and NBO-073.** It was produced
> by the pre-fix scripts, so it lists no inherited column at all (no `name`, `slug`,
> `parent`, `description`, `comments`, `scope_*`, `weight*`, `custom_field_data` on any
> model that inherits them), truncates `meta.constraints` at 400 characters, carries
> no `on_delete`, prints unresolved length symbols, marks generic relations `REQ`, and
> omits the column-less organisational kinds. Pre-NBO-071 it also marks nine
> `ManyToManyField` rows `REQ` that are not required, leaves thirteen FK targets unqualified
> and six reading `-> self`, drops the precision of all fourteen `DecimalField` rows, quotes
> symbolic defaults as though they were string literals, and lets `blank=True` suppress `REQ`
> on `NOT NULL` foreign keys. Pre-NBO-073 it also lists no `tags` row on any kind, and marks
> all 35 `CounterCacheField` rows `REQ`. Re-running the commands above against a
> NetBox 4.6.8 checkout is what refreshes it.
>
> The regenerated body will also gain `netbox.*` entries for the abstract bases themselves
> (`netbox.PrimaryModel`, `netbox.OrganizationalModel`, …). They have no API endpoint and
> are not kinds; they are there so a reader can see where an inherited column comes from.
> Worth running the invariant `hack/test_digest.py` asserts over the fixture — no
> `meta.constraints` naming a column absent from its own field list — over the real
> `models.json` too, while the checkout is still to hand, and reading the models that
> redeclare an inherited column:
>
> ```sh
> jq -r 'to_entries[] | select(.value.shadowed) | "\(.key): \(.value.shadowed | join(", "))"' /tmp/models.json
> ```
>
> Each of those is a subclass narrowing a base's column. The declared version wins, which
> is Django's own behaviour — but it is the one place the merge can be wrong quietly, so
> check the kind's field list against the live REST schema before deriving a CRD from it.

## Cross-checking against a live instance

The AST walk gives the SQL truth (nullability, FK targets, unique constraints).
The REST API adds a second layer: writable-vs-read-only, choice-field values and
the `"app_label.model"` spelling of generic-FK type fields. Pull that from a live
NetBox:

```sh
curl -sH "Authorization: Token $NETBOX_TOKEN" \
  "$NETBOX_URL/api/schema/?format=json" > /tmp/netbox-openapi.json
```

Where the two disagree, the REST schema wins — the operator talks to the API,
not to Postgres.
