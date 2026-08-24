"""Regression test for the API-schema extractor and the IR builder (NBO-041).

Runs extract-netbox-api-schema.py and build-netbox-ir.py over test/fixtures/netbox-models --
the same hand-written miniature of a NetBox source tree hack/test_digest.py uses -- and asserts
the three facts the Django AST walk cannot see:

  1. choice *values*, and whether a deployment's FIELD_CHOICES can change them
  2. writable-vs-read-only, and the `app_label.model` spelling of a generic-FK type field
  3. the query parameters a Kind's filterset actually registers -- so a natural key is a query
     that exists rather than one django-filter drops in silence (#206)

...plus the two defects in extract-netbox-schema.py that building the IR uncovered, which have
their own fixture declarations (dcim.SpecialRackKind and dcim.RackPort). No NetBox checkout and
no network, which is what makes it runnable in CI:

    python3 hack/test_ir.py
"""
import json, os, subprocess, sys

HACK = os.path.dirname(os.path.abspath(__file__))
FIXTURE = os.path.join(os.path.dirname(HACK), 'test', 'fixtures', 'netbox-models')
TMP = os.environ.get('TMPDIR', '/tmp')

def run(script, *args):
    r = subprocess.run([sys.executable, os.path.join(HACK, script), *args],
                       capture_output=True, text=True)
    assert r.returncode == 0, f"{script} failed: {r.stderr}"
    return r.stdout, r.stderr

def write(name, text):
    path = os.path.join(TMP, name)
    with open(path, 'w', encoding='utf-8') as fh:
        fh.write(text)
    return path

models_out, _ = run('extract-netbox-schema.py', FIXTURE)
endpoints_out, _ = run('extract-netbox-endpoints.py', FIXTURE)
api_out, api_err = run('extract-netbox-api-schema.py', FIXTURE)
api = json.loads(api_out)
ir_out, ir_err = run('build-netbox-ir.py',
                     write('nbo041-models.json', models_out),
                     write('nbo041-endpoints.txt', endpoints_out),
                     write('nbo041-api-schema.json', api_out))
ir = json.loads(ir_out)
K = ir['kinds']

def field(kind, name):
    for f in K[kind]['fields']:
        if f['name'] == name:
            return f
    raise AssertionError(f"{kind}.{name} missing from the IR")

def nk(kind, *columns):
    """The natural-key candidate over exactly these columns, in order."""
    want = list(columns)
    for c in K[kind]['natural_keys']:
        if [f['column'] for f in c['fields']] == want:
            return c
    have = [[f['column'] for f in c['fields']] for c in K[kind]['natural_keys']]
    raise AssertionError(f"{kind} has no natural key over {want}; it has {have}")

# --- the extractors read NetBox's own tables rather than copying them -------------------
# A hardcoded copy of a lookup map is exactly the defect in #206, so the maps, the
# filter-class-to-map dispatch and FILTER_DEFAULTS all come out of the source.
assert api['lookup_maps']['FILTER_NUMERIC_BASED_LOOKUP_MAP']['empty'] == 'isnull'
assert api['lookup_maps']['FILTER_CHAR_BASED_LOOKUP_MAP']['empty'] == 'empty', \
    "`empty` means string emptiness on a char filter and SQL NULL on a numeric one"
assert 'empty' not in api['lookup_maps']['FILTER_NEGATION_LOOKUP_MAP'], \
    "an FK filter registers negation only: there is no __empty and no __isnull"
assert [a['map'] for a in api['lookup_dispatch']][0] == 'FILTER_NUMERIC_BASED_LOOKUP_MAP', \
    "the dispatch table is an ordered if-chain; numeric must be tried before char"
assert api['filter_defaults']['SlugField'] == 'MultiValueCharFilter'
assert api['standard_lookups'] and 'exact' in api['standard_lookups']

# --- gap 1: choice values, and whether a deployment can change them --------------------
# The digest can only say `choices=PrefixStatusChoices` and `def=UNRESOLVED:...STATUS_ACTIVE`.
ps = ir['enums']['PrefixStatusChoices']
assert [v['value'] for v in ps['values']] == ['container', 'active', 'reserved']
assert [v['label'] for v in ps['values']] == ['Container', 'Active', 'Reserved'], "_() not unwrapped"
assert ps['values'][0]['color'] == 'gray', 'the third tuple element is a colour, not a member'
# The fact that matters for a CRD: `key` means FIELD_CHOICES can replace or extend the set, so a
# closed enum in the CRD can reject a value the deployment considers legal.
assert ps['extendable'] is True and ps['key'] == 'Prefix.status'
assert ir['enums']['RoleKindChoices']['extendable'] is False, 'no key means no deployment override'
# A grouped ChoiceSet is flattened -- a CRD enum has no optgroup -- and says that it was.
assert [v['value'] for v in ir['enums']['RoleKindChoices']['values']] == ['copper', 'fibre', 'virtual']
assert ir['enums']['RoleKindChoices']['grouped'] is True
# Members built by arithmetic on class constants, which ast.literal_eval will not take.
assert [v['value'] for v in api['choices']['VLANWidthChoices']['values']] == [60, 720, 1440]
# ...and a label built at import time is flagged, never passed off as a literal.
assert api['choices']['VLANWidthChoices']['values'][2]['label'] == \
    {'unresolved': "_('{n} minutes').format(n=1440)"}
# A star-unpack splices another set in, wherever in the file that set is declared.
assert [v['value'] for v in api['choices']['ExtendedRoleKindChoices']['values']] == \
    ['copper', 'fibre', 'virtual', 'bridge']
# The abstract base's `CHOICES = list()` is legitimately empty, not a parse failure, and a
# metaclass is not a choice set at all.
assert api['choices']['ChoiceSet']['values'] == []
assert 'ChoiceSetMeta' not in api['choices']
# A choice set a field names but no <app>/choices.py declares is reported, not silently dropped.
assert field('dcim.Rack', 'airflow')['enum'] == 'RackAirflow'
assert field('dcim.Rack', 'airflow')['enum_unresolved'] is True
assert any(u['kind'] == 'enum' and 'RackAirflow' in u['where'] for u in ir['unresolved'])

# --- gap 2: writable vs read-only, and the app_label.model spelling --------------------
# `ContentType.objects.filter(model__in=PREFIX_SCOPE_TYPES)` is the only place the legal
# generic-FK targets are written, and they are written as bare model names.
assert field('ipam.Prefix', 'scope_type')['object_types'] == ['dcim.region', 'dcim.site'], \
    'the generic-FK target set was not qualified to app_label.model'
assert field('ipam.Prefix', 'scope_type')['class'] == 'GenericFKType'
assert field('ipam.Prefix', 'scope')['class'] == 'GenericFK'
# A column absent from the serializer's Meta.fields is not on the write path, whatever the ORM
# says -- and the disagreement is recorded, not resolved in silence.
assert field('ipam.Prefix', 'scope_id')['in_write_path'] is False
assert any(c['kind'] == 'ipam.Prefix' and c['field'] == 'scope_id'
           and c['fact'] == 'column absent from the write path' for c in ir['conflicts'])
# read_only on a declared serializer field, and Meta.read_only_fields naming the whole list.
assert field('ipam.Prefix', 'scope')['read_only'] is True
assert field('ipam.VLANGroup', 'name')['read_only'] is True, 'Meta.read_only_fields = fields ignored'
# A CounterCacheField's read-only-ness lives inside the field class, so belt and braces: the
# serializer not marking it read-only is a conflict, and the IR still calls it read-only.
assert field('dcim.DeviceType', 'interface_template_count')['read_only'] is True
# A GenericRelation is a reverse accessor: never writable, and not a disagreement either.
assert field('ipam.VLAN', 'l2vpn_terminations')['class'] == 'ReverseRelation'
assert field('ipam.VLAN', 'l2vpn_terminations')['read_only'] is True
assert not any(c['field'] == 'l2vpn_terminations' for c in ir['conflicts']), \
    'a reverse accessor was reported as a write-path disagreement'
# `tags` is an M2M onto the tag model through a through table, not a list of content types.
assert field('ipam.VLAN', 'tags')['class'] == 'M2M'
# Requiredness is the intersection of NOT NULL-with-no-default and the request serializer.
assert field('ipam.Prefix', 'prefix')['sql_required'] is True
assert field('ipam.Prefix', 'status')['required'] is False, 'a column with a default is not required'
assert field('ipam.Prefix', 'scope')['required'] is False, 'a nullable generic FK is not required'

# --- gap 3: the query parameters the filterset actually registers ----------------------
P = K['ipam.Prefix']['filters']
# The headline. Every null-pinned natural key the operator ships today emits `?<fk>__isnull=`;
# #206 proposed `?<fk>__empty=`. Both are wrong: an FK filter is a ModelMultipleChoiceFilter,
# which takes FILTER_NEGATION_LOOKUP_MAP, so *neither parameter exists* and django-filter drops
# it in silence -- returning the unfiltered result set.
assert P['vrf_id']['lookups'] == {'n': 'exact'}, P['vrf_id']
assert P['vrf_id']['lookup_map'] == 'FILTER_NEGATION_LOOKUP_MAP'
# A numeric column does register `empty`, and there it really does mean SQL NULL.
assert P['scope_id']['lookups']['empty'] == 'isnull'
# On a char column the same suffix asks about string emptiness instead. Not the same question,
# and the difference is invisible in the response.
assert K['ipam.VRF']['filters']['rd']['lookups']['empty'] == 'empty'
# A filter with a method= gets no suffixes at all, however char-shaped its column is.
assert P['prefix']['lookups'] == {} and P['prefix']['method'] == 'filter_prefix'
assert 'method' in P['prefix']['no_lookups_because']
# ...as does one with a non-standard lookup_expr.
assert P['mask_length']['lookups'] == {} and 'STANDARD_LOOKUPS' in P['mask_length']['no_lookups_because']
# Declared filters are inherited down the filterset MRO; Meta.fields is not.
assert P['q']['declared_by'] == 'NetBoxModelFilterSet'
assert P['id']['from'] == 'meta.fields', 'Django\'s implicit pk is not in the class body'
# A generic-FK type filter takes `app_label.model` strings rather than IDs (#35).
assert K['ipam.VLANGroup']['filters']['scope_type']['filter_class'] == 'MultiValueContentTypeFilter'
assert K['ipam.VLANGroup']['filters']['scope_type']['lookup_map'] == 'FILTER_CHAR_BASED_LOOKUP_MAP', \
    'a MultiValueCharFilter subclass must be recognised as the base the dispatch chain names'
# The custom-field filters NetBoxModelFilterSet adds from the database cannot be enumerated
# statically. Recorded as dynamic rather than implied absent.
assert 'cf_<custom field name>' in K['ipam.Prefix']['dynamic_filters']

# --- natural keys are data, with each column resolved to a real parameter --------------
# An FK column's API parameter is `<column>_id`, and every candidate is checked against the
# registered set rather than assumed.
assert [f['filter'] for f in nk('ipam.VLAN', 'group', 'vid')['fields']] == ['group_id', 'vid']
assert nk('ipam.VLAN', 'group', 'vid')['unusable'] is None
# `Lower('name')` is a case-insensitive column: a case-sensitive `name=` fails to find `DNS`
# for `dns` and the engine then creates a duplicate.
rack_port = nk('dcim.RackPort', 'name', 'rack')
assert [f['lookup'] for f in rack_port['fields']] == ['ie', '']
# dcim.RackPort has no filterset in the fixture, so no parameter resolves -- and the candidate
# is marked unusable rather than emitted as a query that silently matches everything.
assert rack_port['unusable'] and 'no registered filter parameter' in rack_port['unusable']
# The null pin, end to end: `condition=Q(group__isnull=True)` becomes a null field, and its
# parameter does not exist because `group_id` is an FK filter.
pin = nk('ipam.VLAN', 'site', 'vid')
assert [p['column'] for p in pin['null_fields']] == ['group']
assert pin['null_fields'][0]['filter'] is None
assert 'registers no `empty` suffix' in pin['null_fields'][0]['reason']
assert pin['unusable'], 'a candidate whose null pin cannot be expressed must be marked unusable'
# A natural key may legitimately name a read-only column, filtered under a different name --
# virtualization.Cluster's `_site` is filtered as `site_id`. Pinned here on the scope pair,
# which is the same shape: a column the operator must never write but must be able to filter on.
assert [f['filter'] for f in nk('ipam.VLANGroup', 'scope_type', 'scope_id', 'name')['fields']] == \
    ['scope_type', 'scope_id', 'name']
# A kind with no Meta.constraints has no derivable natural key, and that is said out loud
# rather than left as an empty list a generator would read as "nothing to look up by".
assert K['ipam.Prefix']['natural_keys'] == []
assert any(u['kind'] == 'naturalkey' and u['where'] == 'ipam.Prefix' for u in ir['unresolved'])

# --- the endpoint join, and provenance ------------------------------------------------
# The endpoint path is looked up, never derived by pluralising a model name.
assert K['dcim.RackReservation']['endpoint'] == 'dcim/rack-reservations'
assert K['ipam.VLANGroup']['object_type'] == 'ipam.vlangroup'
# A model with no endpoint is not a kind -- excluded by the join, not by a name list.
assert 'netbox.PrimaryModel' not in K and 'netbox.PrimaryModel' in ir['models_without_endpoint']
assert ir['netbox_version'] == '0.0.0-fixture', 'the IR carries no NetBox version stamp'
assert set(ir['inputs']) == {'nbo041-models.json', 'nbo041-endpoints.txt', 'nbo041-api-schema.json'}
assert all(len(h) == 64 for h in ir['inputs'].values()), 'inputs are SHA-256-stamped'

# --- determinism: the IR is the input to a golden test, so it must be byte-stable ------
again, _ = run('build-netbox-ir.py',
               os.path.join(TMP, 'nbo041-models.json'),
               os.path.join(TMP, 'nbo041-endpoints.txt'),
               os.path.join(TMP, 'nbo041-api-schema.json'))
assert again == ir_out, 'the IR is not byte-identical across runs'

# --- NBO-041 defect 1: a field-less subclass is still a model -------------------------
# `class CircuitType(BaseCircuitType)` has a docstring for a body, and the inclusion test asked
# whether a base's *name* contained "Model", "Component" or "Template". It did not, so both
# circuits.CircuitType and circuits.VirtualCircuitType -- two shipped API endpoints -- had no
# schema entry at all. Whether a class is a model is reachability, not a substring.
assert 'dcim.SpecialRackKind' in K, 'a field-less subclass of a model still has no entry'
assert [f['name'] for f in K['dcim.SpecialRackKind']['fields'] if f['name'] in ('color', 'name', 'slug')] \
    == ['color', 'name', 'slug']
# ...and a class in a models module that is not a model, and reaches none, stays out.
assert 'utilities.ChoiceSet' not in K and not any(k.endswith('.ChoiceSetMeta') for k in K)

# --- NBO-041 defect 2: a base name in two apps resolves to the declaring model's own app --
# `ComponentModel` is declared in both dcim and virtualization in real NetBox 4.6.8. Resolving a
# base by bare class name found it twice, gave up, and dropped every column it declares -- so
# eleven shipped component Kinds (dcim.Interface, dcim.ConsolePort, ...) lost `name`, `label` and
# `description`: their whole identity, and the column every natural key over them starts from.
assert field('dcim.RackPort', 'name')['declared_by'] == 'ComponentModel'
assert field('dcim.RackPort', 'label')['declared_by'] == 'ComponentModel'
assert not any(f['name'] == 'component_kind' for f in K['dcim.RackPort']['fields']), \
    'a dcim model inherited ipam\'s same-named base'
assert field('ipam.PrefixPort', 'component_kind')['declared_by'] == 'ComponentModel'
assert not any(f['name'] == 'label' for f in K['ipam.PrefixPort']['fields'])
# The case that is still not attributable must still say so: TenancyMixin is declared in ipam and
# netbox and in neither dcim app, so its columns stay out -- and the omission is warned about,
# once, on the model that needed the fallback.
assert not any(f['name'] == 'ambiguous_tenant' for f in K['dcim.RackPort']['fields'])
assert 'dcim.RackPort: base TenancyMixin is declared in more than one app' in \
    subprocess.run([sys.executable, os.path.join(HACK, 'extract-netbox-schema.py'), FIXTURE],
                   capture_output=True, text=True).stderr

# --- nothing is dropped in silence ----------------------------------------------------
# Every warning either extractor prints is also a row in the IR's `unresolved` list, so a
# consumer can fail on it instead of trusting a clean-looking file.
printed = {line.split(': ', 1)[1] for line in (api_err + ir_err).splitlines()
           if line.startswith('!! ')}
recorded = {u['detail'] for u in ir['unresolved']}
assert printed <= recorded, f"warned about but not recorded in the IR: {sorted(printed - recorded)}"
assert recorded, 'the fixture exercises no unresolved case at all'

print(f"ok: 3/3 schema gaps + 2/2 NBO-041 extractor defects covered over {len(K)} fixture kinds, "
      f"{len(ir['enums'])} enums, {sum(len(k['filters']) for k in K.values())} filter parameters, "
      f"{sum(len(k['natural_keys']) for k in K.values())} natural-key candidates "
      f"({sum(1 for k in K.values() for n in k['natural_keys'] if n['unusable'])} unusable), "
      f"{len(ir['conflicts'])} conflicts, {len(ir['unresolved'])} unresolved")
