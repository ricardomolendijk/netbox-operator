import json,re,sys,textwrap
d=json.load(open(sys.argv[1]))
want=sys.argv[2].split(',') if len(sys.argv)>2 else None
GENERIC_FK=('GenericForeignKey','RestrictedGenericForeignKey')
# Not columns: nothing here has a NOT NULL to violate, so nothing here can be REQ. A
# TaggableManager is not even a field, so it arrives flagged `not_a_column` rather than
# being named here.
NOT_A_COLUMN=('GenericRelation','ManyToManyField')
# A CounterCacheField *is* a column, but its `default=0` and `editable=False` are set inside
# the field class rather than at the declaration -- so the AST sees neither, and all 35 of
# them came out REQ: a denormalised counter the API returns read-only, demanded on create.
NEVER_REQ=('CounterCacheField',)
# Columns that hold a reference and nothing else -- no empty value to fall back on.
RELATIONS=('ForeignKey','OneToOneField','TreeForeignKey')
# Width of the type column. Must fit the longest class name the extractor can emit
# (RestrictedGenericForeignKey, 27) or that row's REQ/detail columns shift right and the
# table stops lining up. test_digest.py checks it against FIELD_TYPES and MANAGER_TARGETS
# rather than against whatever the fixture happens to declare -- widening the whitelist is
# what breaks this, and the fixture would not notice.
TYPE_COL = 27
MODULE_PREFIX=re.compile(r'^[\w.]*\.')  # models.PROTECT -> PROTECT, models.SET(x) -> SET(x)

def req(f,by_name,seen=()):
    # Neither half of a generic relation is a column, so neither takes null=: reading the
    # raw kwargs marks every `scope` / `assigned_object` row REQ, which would make an
    # unassigned IP or an unscoped prefix look illegal. A GenericRelation is a reverse
    # relation (never required); a GenericForeignKey inherits its requiredness from the
    # content-type half of its pair.
    # A ManyToManyField is a through table, not a column on this model: it has no NOT NULL
    # to violate and Django ignores null= on it entirely. `REQ` there makes the CRD demand a
    # value the user has no way to supply -- dcim.Interface.vdcs, where a VDC assignment is
    # optional, is one of nine such rows.
    if f['type'] in NOT_A_COLUMN or f['type'] in NEVER_REQ or f.get('not_a_column'): return False
    if f['type'] in GENERIC_FK:
        ct=by_name.get(f.get('ct_field'))
        if ct is None or ct['name'] in seen: return False
        return req(ct,by_name,seen+(f['name'],))
    # `blank` is a form-level flag, not SQL. It stands in for optional on a CharField,
    # whose NOT NULL column takes '' instead, but a ForeignKey(null=False, blank=True) has
    # no such empty value: that column really must be supplied.
    if f['type'] in RELATIONS: return not (f.get('null') or 'default' in f)
    return not (f.get('null') or f.get('blank') or 'default' in f)

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
        r=' REQ' if req(f,by_name) else ''
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
