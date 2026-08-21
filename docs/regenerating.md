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
pipeline. `make test-schema` runs all three scripts over it and asserts the output,
so the extractors stay honest in CI — where cloning NetBox is not worth the minute.

```sh
make test-schema        # == python3 hack/test_digest.py
```

Any fix to an extractor belongs with a fixture declaration that reproduces the bug.
The fixture is not a copy of NetBox and should stay small; `test/fixtures/netbox-models/README.md`
lists what each declaration is there to pin.

> **The committed `docs/netbox-schema.md` predates NBO-067.** It was produced by the
> pre-fix scripts, so it still truncates `meta.constraints` at 400 characters, carries
> no `on_delete`, prints unresolved length symbols, marks generic relations `REQ`, and
> omits the column-less organisational kinds. Re-running the commands above against a
> NetBox 4.6.8 checkout is what refreshes it.

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
