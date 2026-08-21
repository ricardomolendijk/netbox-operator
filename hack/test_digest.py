"""Regression test for the schema extraction pipeline (NBO-067).

Runs extract-netbox-schema.py, digest-netbox-schema.py and extract-netbox-endpoints.py over
test/fixtures/netbox-models -- a hand-written miniature of a NetBox source tree -- and asserts
each of the five NBO-067 defects stays fixed. It deliberately does not need a NetBox checkout,
which is what makes it runnable in CI:

    python3 hack/test_digest.py
"""
import json, os, subprocess, sys

HACK = os.path.dirname(os.path.abspath(__file__))
FIXTURE = os.path.join(os.path.dirname(HACK), 'test', 'fixtures', 'netbox-models')

def run(script, *args):
    r = subprocess.run([sys.executable, os.path.join(HACK, script), *args], capture_output=True, text=True)
    assert r.returncode == 0, f"{script} failed: {r.stderr}"
    return r.stdout

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

models = run('extract-netbox-schema.py', FIXTURE)
schema = json.loads(models)
models_path = os.path.join(os.environ.get('TMPDIR', '/tmp'), 'nbo067-models.json')
with open(models_path, 'w', encoding='utf-8') as fh: fh.write(models)
digest = run('digest-netbox-schema.py', models_path)
endpoints = run('extract-netbox-endpoints.py', FIXTURE)

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

print(f"ok: 5/5 NBO-067 defects covered over {len(schema)} fixture models, {len(endpoints.splitlines())} endpoints")
