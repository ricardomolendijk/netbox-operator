"""Regression test for the schema extraction pipeline (NBO-067, NBO-070, NBO-071).

Runs extract-netbox-schema.py, digest-netbox-schema.py, extract-netbox-endpoints.py and
classify-scope.py over test/fixtures/netbox-models -- a hand-written miniature of a NetBox
source tree -- and asserts each of the five NBO-067 defects stays fixed, plus NBO-070 (a
model's inherited columns are merged in and attributed, and no model's meta.constraints names
a column absent from its own field list) and the six NBO-071 defects. The two NBO-071 defects
that now stop the run get their own deliberately-broken tree, test/fixtures/netbox-models-bad.
It deliberately does not need a NetBox checkout, which is what makes it runnable in CI:

    python3 hack/test_digest.py
"""
import ast, json, os, re, subprocess, sys

HACK = os.path.dirname(os.path.abspath(__file__))
FIXTURE = os.path.join(os.path.dirname(HACK), 'test', 'fixtures', 'netbox-models')
BAD = FIXTURE + '-bad'

def run(script, *args):
    """(stdout, stderr) of a run that must succeed."""
    r = subprocess.run([sys.executable, os.path.join(HACK, script), *args], capture_output=True, text=True)
    assert r.returncode == 0, f"{script} failed: {r.stderr}"
    return r.stdout, r.stderr

def fails(script, *args):
    """stderr of a run that must NOT succeed. A defect that corrupts a field list in silence is
    worse than one that stops the pipeline, so two of these are exits rather than warnings."""
    r = subprocess.run([sys.executable, os.path.join(HACK, script), *args], capture_output=True, text=True)
    assert r.returncode != 0, f"{script} was expected to fail over {args[-1]}, but exited 0"
    return r.stderr

def rows(digest, model):
    """The lines of one `## app.Model` block."""
    out, inside = [], False
    for line in digest.splitlines():
        if line.startswith('## '):
            inside = line.startswith(f"## {model} ")
            continue
        if inside and line.strip(): out.append(line)
    return out

def field(digest, model, name):
    for line in rows(digest, model):
        if line.startswith(f"     {name} ") or line.strip().startswith(name + ' '): return line
    raise AssertionError(f"{model}.{name} missing from digest")

models, extract_err = run('extract-netbox-schema.py', FIXTURE)
schema = json.loads(models)
models_path = os.path.join(os.environ.get('TMPDIR', '/tmp'), 'nbo067-models.json')
with open(models_path, 'w', encoding='utf-8') as fh: fh.write(models)
digest, _ = run('digest-netbox-schema.py', models_path)
endpoints, _ = run('extract-netbox-endpoints.py', FIXTURE)
endpoint_models = {line.split()[0]: line.split()[1] for line in endpoints.splitlines()}

# --- defect 1: meta.constraints emitted in full, wrapped, never truncated ----------------
src = open(os.path.join(FIXTURE, 'ipam', 'models', 'vlans.py'), encoding='utf-8').read()
assert 'unique_site_vid' in src, 'fixture no longer has a long multi-constraint Meta'
vlan = rows(digest, 'ipam.VLAN')
constraints = ' '.join(line.strip() for line in vlan if 'meta.constraints:' in line or line.startswith('      '))
for name in ('unique_group_vid', 'unique_group_name', 'unique_qinq_svlan_vid', 'unique_qinq_svlan_name', 'unique_site_vid'):
    assert name in constraints, f"meta.constraints truncated before {name}"
assert constraints.rstrip().endswith("site.')))"), f"meta.constraints does not end at the closing paren: {constraints[-80:]}"
assert max(len(line) for line in digest.splitlines()) <= 120, 'a digest line is too long to wrap into the doc'

# --- defect 2: on_delete recorded per FK, for every flavour Django accepts ----------------
assert 'on_delete=PROTECT'     in field(digest, 'ipam.VLAN', 'site')
assert 'on_delete=CASCADE'     in field(digest, 'ipam.VLAN', 'group')
assert 'on_delete=SET_NULL'    in field(digest, 'ipam.VLAN', 'role')
# Passed positionally rather than as a kwarg.
assert 'on_delete=SET_DEFAULT' in field(digest, 'ipam.RouteTarget', 'tenant')
fks = [f for v in schema.values() for f in v['fields'] if f['type'] in ('ForeignKey', 'OneToOneField')]
assert fks and all('on_delete' in f for f in fks), 'an FK reached the digest with no on_delete'

# --- defect 3: symbolic field lengths resolved, or flagged -- never emitted raw -----------
assert 'len=21' in field(digest, 'ipam.VRF', 'rd'), 'VRF_RD_MAX_LENGTH not resolved to 21'
assert 'len=21' in field(digest, 'ipam.RouteTarget', 'name')
assert 'len=33' in field(digest, 'ipam.VLAN', 'name'), 'a constant local to the model module was not resolved'
# AMBIGUOUS_MAX_LENGTH is 64 in ipam and 100 in dcim: guessing would mis-size the column.
assert 'len=UNRESOLVED:AMBIGUOUS_MAX_LENGTH' in field(digest, 'ipam.VLAN', 'description')
for line in digest.splitlines():
    for tok in line.split():
        if not tok.startswith('len='): continue
        assert tok[4:].isdigit() or tok.startswith('len=UNRESOLVED:'), f"raw symbolic length emitted: {tok}"

# --- defect 4: REQ suppressed on generic relations, derived on generic FKs ----------------
assert ' REQ' not in field(digest, 'ipam.VLAN', 'l2vpn_terminations'), 'GenericRelation marked REQ'
assert ' REQ' not in field(digest, 'dcim.Site', 'prefixes'), 'GenericRelation marked REQ'
# scope_type is nullable, so an unscoped (global) prefix is legal.
assert ' REQ' not in field(digest, 'ipam.Prefix', 'scope'), 'GenericForeignKey over a nullable pair marked REQ'
# ...but requiredness is *derived*, not blanket-suppressed: this pair is NOT NULL.
assert ' REQ' in field(digest, 'ipam.PrefixAttachment', 'parent'), 'GenericForeignKey over a required pair lost its REQ'

# --- defect 5: a column-less kind still gets a Models entry ------------------------------
for name in ('dcim.Manufacturer', 'dcim.RackGroup'):
    assert f"## {name} " in digest, f"{name} has an endpoint but no Models entry"
    assert 'no own columns' in ' '.join(rows(digest, name)), f"{name} does not say where its fields come from"
# The real acceptance check: every endpoint's model is described somewhere in the digest.
for line in endpoints.splitlines():
    model = line.split()[-1]
    assert f"## {model} " in digest, f"endpoint {line.split()[0]} maps to {model}, which has no Models entry"

# --- NBO-070: inherited columns are merged in, and attributed to the class declaring them -
# The shared abstract bases (netbox/models/) were never scanned, so a model's inherited
# columns appeared nowhere: RackRole listed only `color`, Region only its GenericRelation.
def declaring(digest, model, name):
    """The class a row is attributed to, or None when the model declares it itself."""
    line = field(digest, model, name).strip()
    return line.split('(', 1)[1].split(')')[0] if line.split()[1].startswith('(') else None

for name in ('name', 'slug', 'description'):
    assert declaring(digest, 'dcim.RackRole', name) == 'OrganizationalModel', f"RackRole.{name} not attributed"
assert declaring(digest, 'dcim.RackRole', 'color') is None, 'a declared column was marked inherited'
assert 'len=100' in field(digest, 'dcim.RackRole', 'name'), 'a base-module constant was not resolved before merging'
for name in ('name', 'slug', 'parent'):
    assert declaring(digest, 'dcim.Region', name) == 'NestedGroupModel', f"Region.{name} not attributed"
# Two hops up: CustomFieldsMixin is a base of NetBoxModel, which is a base of the base.
assert declaring(digest, 'dcim.Manufacturer', 'custom_field_data') == 'CustomFieldsMixin'
# A mixin alongside the abstract base, in the app's own models package.
for name in ('weight', 'weight_unit'):
    assert declaring(digest, 'dcim.DeviceType', name) == 'WeightMixin', f"DeviceType.{name} not attributed"
for name in ('description', 'comments'):
    assert declaring(digest, 'ipam.VRF', name) == 'PrimaryModel', f"VRF.{name} not attributed"
# A generic-FK pair that arrives entirely by inheritance still gets its REQ derived, not guessed.
assert declaring(digest, 'ipam.VLANGroup', 'scope_type') == 'CachedScopeMixin'
assert ' REQ' not in field(digest, 'ipam.VLANGroup', 'scope'), 'inherited GenericForeignKey over a nullable pair marked REQ'
# TagsMixin declares a TaggableManager, not a column: nothing to merge, and nothing invented.
assert not any(f['name'] == 'tags' for v in schema.values() for f in v['fields']), 'a through-table manager was merged as a column'
# Every attribution names a class the run actually saw.
classes = {v['name'] for v in schema.values()}
for key, v in schema.items():
    for f in v['fields']:
        assert f.get('declared_by', v['name']) in classes, f"{key}.{f['name']} attributed to unknown class {f['declared_by']}"
# Django lets a subclass shadow an inherited field. The declared one must win, and the loser
# must be reported -- silent shadowing is how a merge goes wrong invisibly.
assert 'len=UNRESOLVED:AMBIGUOUS_MAX_LENGTH' in field(digest, 'ipam.VLAN', 'description'), 'an inherited column overwrote a declared one'
assert schema['ipam.VLAN']['shadowed'] == ['description (PrimaryModel)'], f"shadowing not reported: {schema['ipam.VLAN'].get('shadowed')}"
assert 'shadowed' not in schema['dcim.Manufacturer'], 'a diamond in the base graph was reported as a conflict'

# --- NBO-070, the regression test that matters ------------------------------------------
# A Meta constraint is the natural key the engine looks an object up by, so a constraint
# naming a column the same entry does not list is a self-contradiction in the field truth --
# and it is exactly what dcim.Region did. Asserted over every fixture model, not one case.
def cited_columns(expr, meta_key):
    """Column names a meta.constraints / unique_together expression names."""
    out = set()
    if meta_key == 'unique_together':
        groups = ast.literal_eval(expr)
        if groups and isinstance(groups[0], str): groups = (groups,)
        for g in groups: out.update(g)
        return out
    for node in ast.walk(ast.parse(expr, mode='eval')):
        # UniqueConstraint(fields=(...)); a `condition=Q(...)` lookup is not a column list.
        if isinstance(node, ast.keyword) and node.arg == 'fields':
            out.update(ast.literal_eval(node.value))
    return out

checked = 0
for key, v in sorted(schema.items()):
    have = {f['name'] for f in v['fields']}
    for meta_key in ('constraints', 'unique_together'):
        if meta_key not in v['meta']: continue
        # `_name__isnull` and friends are lookups on a column, so compare the column half.
        missing = sorted({c for c in cited_columns(v['meta'][meta_key], meta_key) if c.split('__')[0] not in have})
        assert not missing, f"{key}.meta.{meta_key} names {missing}, absent from its own field list"
        checked += 1
assert checked >= 4, f"only {checked} Meta constraint blocks in the fixture: the invariant is barely exercised"

# --- NBO-071 defect 1: an M2M and a GenericRelation are not columns, so never REQ --------
# An M2M has no NOT NULL column and Django ignores null= on it. Nine real rows carried a
# spurious REQ; dcim.Interface.vdcs made the CRD demand a VDC the user cannot supply.
assert ' REQ' not in field(digest, 'dcim.Rack', 'tagged_vlans'), 'ManyToManyField marked REQ'
for line in digest.splitlines():
    if 'ManyToManyField' in line or 'GenericRelation' in line:
        assert ' REQ' not in line, f"a row that is not a column is marked REQ: {line}"
# Nor does an M2M have an on_delete: its second positional argument is related_name.
assert 'on_delete' not in field(digest, 'dcim.Rack', 'tagged_vlans'), 'on_delete invented for an M2M'

# --- NBO-071 defect 2: every FK target reads app.Model ----------------------------------
# `to=Site` unparsed to a bare `Site`, leaving the app to be guessed -- and a guess by class
# name silently picks the wrong app's model when two apps share a name.
assert '-> dcim.Site '     in field(digest, 'dcim.Rack', 'site'), 'a bare positional FK target was not qualified'
assert '-> dcim.RackRole ' in field(digest, 'dcim.Rack', 'role'), 'a bare to= FK target was not qualified'
# `to='self'` names the model the column ends up on, which for an inherited column is the
# subclass: qualifying it where it is declared would make every nested group `netbox.self`.
assert '-> dcim.RackGroup ' in field(digest, 'dcim.RackGroup', 'parent'), 'inherited self-FK not resolved to the inheriting model'
assert '-> dcim.Region '    in field(digest, 'dcim.Region', 'parent')
assert '-> netbox.NestedGroupModel ' in field(digest, 'netbox.NestedGroupModel', 'parent')
# A class from outside the ten scanned apps (django.contrib.auth's User) has no app to be
# qualified with, so it is flagged -- never mislabelled dcim.User.
assert '-> UNRESOLVED:User ' in field(digest, 'dcim.RackReservation', 'user')
for key, v in schema.items():
    for f in v['fields']:
        if not f.get('to') or f.get('to_unresolved'): continue
        assert re.fullmatch(r'[a-z_]+\.\w+', f['to']), f"{key}.{f['name']} target is not app.Model: {f['to']}"

# --- NBO-071 defect 2, second half: classify-scope.py's byname fallback ------------------
# It matched a bare class name in whatever app happened to declare it, so the wrong edge could
# quietly move a Kind in or out of the cluster-scoped set. It is gone; what replaced it warns,
# and it can only fire for a target the extractor itself flagged as unresolvable.
scope_out, scope_err = run('classify-scope.py', models_path)
assert set(re.findall(r"unqualified FK target '([^']+)'", scope_err)) == \
    {f['to'] for v in schema.values() for f in v['fields'] if f.get('to_unresolved')}, \
    f"classify-scope guessed, or warned about a target the extractor had qualified: {scope_err}"
assert 'STABLE cluster-scoped set' in scope_out, 'classify-scope no longer produces a scope set'

# --- NBO-071 defect 3: a symbolic default is not a string literal ------------------------
assert 'def=UNRESOLVED:RackStatus.ACTIVE' in field(digest, 'dcim.Rack', 'status'), 'a symbol was quoted into a literal'
assert "def='front-to-rear'" in field(digest, 'dcim.Rack', 'airflow'), 'a real string default lost its quotes'
assert 'choices=RackAirflow' in field(digest, 'dcim.Rack', 'airflow')
# A callable default is symbolic too: `default=dict` is not the string 'dict'.
assert 'def=UNRESOLVED:dict' in field(digest, 'dcim.Manufacturer', 'custom_field_data')

# --- NBO-071 defect 4: DecimalField precision is emitted --------------------------------
assert 'decimal(4,1)' in field(digest, 'dcim.Rack', 'position'), 'declared DecimalField shows no precision'
assert 'decimal(8,2)' in field(digest, 'dcim.DeviceType', 'weight'), 'inherited DecimalField shows no precision'
for line in digest.splitlines():
    if 'DecimalField' in line:
        assert 'decimal(' in line, f"a DecimalField row has no precision: {line}"

# --- NBO-071 defect 5: a router.register the extractor cannot parse stops the run --------
# Double-quoted, and a viewset with no `views.` prefix: both were skipped in silence, and a
# missing endpoint row is a Kind that never gets a CRD at all.
assert endpoint_models.get('dcim/racks') == 'dcim.Rack', 'a double-quoted register was skipped'
assert endpoint_models.get('dcim/rack-reservations') == 'dcim.RackReservation', 'a viewset with no views. prefix was skipped'
err = fails('extract-netbox-endpoints.py', BAD)
assert 'cannot parse' in err and 'router.register(RACKS' in err, f"an unparsable register line did not fail loudly: {err}"

# --- NBO-071 defect 6: two same-named classes in one app stop the run --------------------
# `if key in out and not fields: continue` kept whichever file the glob reached first and
# dropped the other class's entire field list, in either direction, silently.
err = fails('extract-netbox-schema.py', BAD)
assert 'dcim.Rack is declared twice' in err, f"a same-named class pair did not fail the run: {err}"
# The cross-app case stays a warning by choice: one class name in two apps is legitimate, and
# all that is lost is base attribution, which is already said out loud. Intra-app, one model's
# entire field list is replaced by another model's.

# --- NBO-071: the two silent losses found while fixing the six ---------------------------
# `ast.walk` also reached each model's nested `class Meta(PrimaryModel.Meta)`, whose base name
# contains "Model": every one of those became a phantom `app.Meta` entry, and the second
# collapsed into the first. The fixture has two such Metas.
assert 'dcim.Meta' not in schema and '## dcim.Meta' not in digest, 'a nested class became a model entry'
assert schema['dcim.Rack']['meta']['ordering'] == "('site', 'position')", 'a nested Meta stopped being read'
# A field class the whitelist does not know is a column missing from the entry, and a property
# missing from any CRD derived from it. It is still dropped -- but no longer in silence, and
# that warning is the only thing the extractor has to say about a healthy tree.
assert not any(f['name'] == 'outer_unit' for f in schema['dcim.Rack']['fields'])
assert extract_err.split() == '!! dcim.Rack.outer_unit: unknown field type RackUnitField, column omitted'.split(), \
    f"unexpected extractor stderr: {extract_err}"

# --- NBO-071 "also worth checking": blank is form-level, not SQL -------------------------
# A ForeignKey(null=False, blank=True) column really is NOT NULL...
assert ' REQ' in field(digest, 'dcim.Rack', 'role'), 'blank=True suppressed REQ on a NOT NULL FK'
# ...while a CharField(blank=True) takes '' instead, so it stays optional.
assert ' REQ' not in field(digest, 'dcim.Site', 'facility'), 'blank=True stopped meaning optional on a CharField'
assert ' REQ' not in field(digest, 'ipam.VLAN', 'site'), 'a null=True FK became required'

print(f"ok: 5/5 NBO-067 defects + NBO-070 inherited columns + 6/6 NBO-071 defects covered over "
      f"{len(schema)} fixture models, {len(endpoints.splitlines())} endpoints, "
      f"{checked} Meta constraint blocks")
