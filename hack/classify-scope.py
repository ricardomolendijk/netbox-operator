import json,sys
d=json.load(open(sys.argv[1]))
byname={}
for k,v in d.items(): byname.setdefault(v['name'],k)
def norm(t,app):
    if not t or not isinstance(t,str): return None
    t=t.strip().strip('"\'')
    if t=='self': return 'SELF'
    if '.' in t:
        a,m=t.split('.',1)
        if a in ('contenttypes','auth','users','django'): return None
        for k,v in d.items():
            if v['app']==a and v['name'].lower()==m.lower(): return k
        return None
    for k,v in d.items():
        if v['app']==app and v['name'].lower()==t.lower(): return k
    # There used to be a `byname.get(t)` fallback here: match the class name in whatever app
    # happens to declare it. That silently picks the wrong model when two apps share a class
    # name, and the wrong edge quietly moves a Kind in or out of the cluster-scoped set.
    # extract-netbox-schema.py now emits every FK target as `app.Model` (NBO-071), so this is
    # only reachable from a models.json written before that -- say so instead of guessing.
    print(f"!! unqualified FK target {t!r} declared in {app}: re-run extract-netbox-schema.py",
          file=sys.stderr)
    return None

# Curated seed: base-class catalogues PLUS conceptual catalogues that NetBox
# models as PrimaryModel only because they carry description/comments/custom fields.
SEED_BARE = """
Region SiteGroup Manufacturer Platform DeviceRole RackRole RackGroup InventoryItemRole
Tag TenantGroup ContactGroup ContactRole RIR Role ASNRange VLANGroup
ClusterType ClusterGroup TunnelGroup WirelessLANGroup CircuitGroup
DeviceType RackType ModuleType ModuleTypeProfile VirtualMachineType
CircuitType VirtualCircuitType ServiceTemplate
ConsolePortTemplate ConsoleServerPortTemplate PowerPortTemplate PowerOutletTemplate
InterfaceTemplate FrontPortTemplate RearPortTemplate ModuleBayTemplate DeviceBayTemplate
InventoryItemTemplate
CustomField CustomFieldChoiceSet ConfigTemplate ExportTemplate CustomLink SavedFilter
Webhook EventRule
IKEProposal IKEPolicy IPSecProposal IPSecPolicy IPSecProfile
""".split()
cand={byname[n] for n in SEED_BARE if n in byname}
missing=[n for n in SEED_BARE if n not in byname]
if missing: print("!! not found in schema:", missing)

# Out-of-scope kinds count as "not cluster-scoped" only if they are
# NetBox objects we model at all; core.DataSource/DataFile are out of scope entirely
# and their FKs are optional, so treat them as neutral.
NEUTRAL={'core.DataSource','core.DataFile','core.ObjectType','core.Job'}

edges={}
for k,v in d.items():
    out=[]
    for f in v['fields']:
        if f['type'] not in ('ForeignKey','OneToOneField','ManyToManyField'): continue
        if f['name'].startswith('_'): continue
        tgt=norm(f.get('to'), v['app'])
        if tgt in (None,'SELF',k) or tgt in NEUTRAL: continue
        # Required-ness, derived the same way the digest derives it (NBO-071): an M2M has no
        # NOT NULL column at all, and `blank` is form-level, not SQL -- every field here is a
        # relation, so there is no '' for a blank column to fall back on. Getting this wrong
        # in either direction moves a Kind in or out of the cluster-scoped set.
        out.append((f['name'],tgt,f['type']!='ManyToManyField' and not (f.get('null') or 'default' in f)))
    edges[k]=out

s=set(cand); rounds=0
while True:
    drop=[]
    for k in sorted(s):
        for fname,tgt,req in edges[k]:
            if req and tgt not in s: drop.append((k,fname,tgt)); break
    if not drop: break
    rounds+=1
    print(f"\nround {rounds} demotions:")
    for k,fname,tgt in drop:
        print(f"   {k:34} required {fname} -> {tgt}")
        s.discard(k)
print(f"\nSTABLE cluster-scoped set ({len(s)}) after {rounds} round(s):")
for k in sorted(s): print("   ",k)

print("\n--- optional FKs from the stable set into namespaced kinds (need a namespace-qualified ref) ---")
for k in sorted(s):
    for fname,tgt,req in edges[k]:
        if not req and tgt not in s:
            print(f"   {k.split('.')[1]:24} .{fname:18} -> {tgt}")
