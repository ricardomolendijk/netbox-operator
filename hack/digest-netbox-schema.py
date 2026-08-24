import json,os,re,sys,textwrap
sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from netbox_schema import sql_required
d=json.load(open(sys.argv[1]))
want=sys.argv[2].split(',') if len(sys.argv)>2 else None
# Width of the type column. Must fit the longest class name the extractor can emit
# (RestrictedGenericForeignKey, 27) or that row's REQ/detail columns shift right and the
# table stops lining up. test_digest.py checks it against FIELD_TYPES and MANAGER_TARGETS
# rather than against whatever the fixture happens to declare -- widening the whitelist is
# what breaks this, and the fixture would not notice.
TYPE_COL = 27
MODULE_PREFIX=re.compile(r'^[\w.]*\.')  # models.PROTECT -> PROTECT, models.SET(x) -> SET(x)

# `req` is `sql_required` in hack/netbox_schema.py: the IR builder needs the same rule, and
# two copies of it is how the two halves of the pipeline drift apart in silence.

def sym(f,k):
    """A resolved kwarg, or the symbol flagged as unresolved -- never a symbol passed off as
    the value it happens to spell."""
    return f"{'UNRESOLVED:' if f.get(k+'_unresolved') else ''}{f.get(k,'?')}"

for k,v in sorted(d.items()):
    if want and not any(k.endswith('.'+w) for w in want): continue
    print(f"## {k}   ({v['file']})")
    print(f"   bases: {', '.join(v['bases'])}")
    # An OrganizationalModel / NestedGroupModel subclass can declare no columns of its own
    # (Manufacturer, RackGroup, ClusterType, ...). Those are real API kinds, so say so
    # explicitly rather than dropping the entry and implying the model does not exist.
    if not [f for f in v['fields'] if not f.get('declared_by')]:
        print(f"   (no own columns — every field is inherited from {', '.join(v['bases']) or 'its base classes'})")
    # Django lets a subclass redeclare an inherited field; the declared one wins. Say which,
    # so a reader can see the base's version was dropped on purpose.
    if v.get('shadowed'):
        print(f"   shadows inherited: {', '.join(v['shadowed'])}")
    by_name={f['name']:f for f in v['fields']}
    for f in v['fields']:
        r=' REQ' if sql_required(f,by_name) else ''
        # `name (OrganizationalModel)` — an inherited column is a real column of this model,
        # but a CRD author needs to know it is not declared here.
        label=f"{f['name']} ({f['declared_by']})" if f.get('declared_by') else f['name']
        extra=[]
        # A TaggableManager declares no column at all, so `tags` -- writable over REST on
        # every PrimaryModel and OrganizationalModel -- appeared in no entry. Say what it is
        # and that it is not a column, or a reader goes looking for a column that is not there.
        if f.get('not_a_column'): extra.append('M2M')
        if f.get('to'): extra.append(f"-> {sym(f,'to')}")
        if f.get('not_a_column'): extra.append(f"(via {f['through'].split('.')[-1]}, not a column)" if f.get('through') else '(not a column)')
        if f.get('on_delete'): extra.append(f"on_delete={MODULE_PREFIX.sub('',str(f['on_delete']))}")
        if f.get('unique'): extra.append('UNIQUE')
        if f.get('max_length'): extra.append(f"len={sym(f,'max_length')}")
        # A DecimalField's precision is the whole of its contract: without it a CRD author
        # cannot tell decimal(8,6) from a free float, which is 14 rows of the schema.
        if 'max_digits' in f or 'decimal_places' in f: extra.append(f"decimal({sym(f,'max_digits')},{sym(f,'decimal_places')})")
        # `def='VLANStatusChoices.STATUS_ACTIVE'` read exactly like the literal 'active'.
        if 'default' in f: extra.append(f"def={sym(f,'default') if f.get('default_unresolved') else repr(f['default'])}")
        if f.get('choices'): extra.append(f"choices={f['choices']}")
        # rstrip so an extra-less row does not pad out to the limit.
        print(f"     {label:38} {f['type']:{TYPE_COL}}{r:4} {' '.join(extra)}".rstrip())
    for mk in ('unique_together','constraints','ordering','indexes'):
        if mk not in v['meta']: continue
        # meta.constraints is the natural key the engine looks an object up by, so it is the
        # one field that must never be truncated: wrap instead.
        print(textwrap.fill(f"meta.{mk}: {v['meta'][mk]}", width=110, initial_indent='   ',
                            subsequent_indent='      ', break_long_words=False, break_on_hyphens=False))
    print()
