import json,re,sys,textwrap
d=json.load(open(sys.argv[1]))
want=sys.argv[2].split(',') if len(sys.argv)>2 else None
GENERIC_FK=('GenericForeignKey','RestrictedGenericForeignKey')
MODULE_PREFIX=re.compile(r'^[\w.]*\.')  # models.PROTECT -> PROTECT, models.SET(x) -> SET(x)

def req(f,by_name,seen=()):
    # Neither half of a generic relation is a column, so neither takes null=: reading the
    # raw kwargs marks every `scope` / `assigned_object` row REQ, which would make an
    # unassigned IP or an unscoped prefix look illegal. A GenericRelation is a reverse
    # relation (never required); a GenericForeignKey inherits its requiredness from the
    # content-type half of its pair.
    if f['type']=='GenericRelation': return False
    if f['type'] in GENERIC_FK:
        ct=by_name.get(f.get('ct_field'))
        if ct is None or ct['name'] in seen: return False
        return req(ct,by_name,seen+(f['name'],))
    return not (f.get('null') or f.get('blank') or 'default' in f)

for k,v in sorted(d.items()):
    if want and not any(k.endswith('.'+w) for w in want): continue
    print(f"## {k}   ({v['file']})")
    print(f"   bases: {', '.join(v['bases'])}")
    # An OrganizationalModel / NestedGroupModel subclass can declare no columns of its own
    # (Manufacturer, RackGroup, ClusterType, ...). Those are real API kinds, so say so
    # explicitly rather than dropping the entry and implying the model does not exist.
    if not v['fields']:
        print(f"   (no own columns — every field is inherited from {', '.join(v['bases']) or 'its base classes'})")
    by_name={f['name']:f for f in v['fields']}
    for f in v['fields']:
        r=' REQ' if req(f,by_name) else ''
        extra=[]
        if f.get('to'): extra.append(f"-> {f['to']}")
        if f.get('on_delete'): extra.append(f"on_delete={MODULE_PREFIX.sub('',str(f['on_delete']))}")
        if f.get('unique'): extra.append('UNIQUE')
        if f.get('max_length'): extra.append(f"len={'UNRESOLVED:' if f.get('max_length_unresolved') else ''}{f['max_length']}")
        if 'default' in f: extra.append(f"def={f['default']!r}")
        if f.get('choices'): extra.append(f"choices={f['choices']}")
        print(f"     {f['name']:28} {f['type']:22}{r:4} {' '.join(extra)}")
    for mk in ('unique_together','constraints','ordering','indexes'):
        if mk not in v['meta']: continue
        # meta.constraints is the natural key the engine looks an object up by, so it is the
        # one field that must never be truncated: wrap instead.
        print(textwrap.fill(f"meta.{mk}: {v['meta'][mk]}", width=110, initial_indent='   ',
                            subsequent_indent='      ', break_long_words=False, break_on_hyphens=False))
    print()
