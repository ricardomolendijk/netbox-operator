import ast, os, sys, json, glob

ROOT = sys.argv[1]
FIELD_TYPES = {
    'CharField','TextField','SlugField','IntegerField','PositiveIntegerField',
    'PositiveSmallIntegerField','PositiveBigIntegerField','BigIntegerField','SmallIntegerField',
    'BooleanField','DateField','DateTimeField','DecimalField','FloatField','JSONField',
    'ForeignKey','OneToOneField','ManyToManyField','GenericForeignKey','GenericRelation',
    'IPAddressField','IPNetworkField','MACAddressField','ASNField','URLField','EmailField',
    'UUIDField','ArrayField','ColorField','CounterCacheField','RestrictedGenericForeignKey',
    'CachedValueField','WWNField','NaturalOrderingField','GenericIPAddressField',
}
FK_TYPES = ('ForeignKey','OneToOneField','ManyToManyField')
GENERIC_FK_TYPES = ('GenericForeignKey','RestrictedGenericForeignKey')
# Kwargs that must be an integer to be usable downstream; the rest of what we keep is
# either boolean or symbolic by nature (choices, default).
SIZE_KWARGS = ('max_length','max_digits','decimal_places')

def lit(n):
    try: return ast.literal_eval(n)
    except Exception:
        return ast.unparse(n)

def kwargs_of(call):
    out = {}
    for kw in call.keywords:
        if kw.arg is None: continue
        out[kw.arg] = lit(kw.value)
    return out

def target_of(call):
    if call.args:
        return lit(call.args[0])
    return None

def parse(path):
    try: return ast.parse(open(path, encoding='utf-8').read())
    except Exception: return None

def int_consts(tree):
    out = {}
    for stmt in tree.body:
        if not (isinstance(stmt, ast.Assign) and len(stmt.targets)==1 and isinstance(stmt.targets[0], ast.Name)): continue
        try: v = ast.literal_eval(stmt.value)
        except Exception: continue
        if isinstance(v, int) and not isinstance(v, bool): out[stmt.targets[0].id] = v
    return out

# NetBox declares column sizes as module constants (`max_length=VRF_RD_MAX_LENGTH`), which
# the AST walk sees as a bare Name. Pre-scan every constants module so those resolve to the
# integer the column really has. A name defined twice with conflicting values is dropped
# rather than guessed: emitting the wrong length would silently mis-size a CRD field.
CONSTS = {}
for dirpath, _, filenames in os.walk(ROOT):
    in_constants_pkg = os.path.basename(dirpath) == 'constants'
    for fn in filenames:
        if not fn.endswith('.py') or not (in_constants_pkg or fn == 'constants.py'): continue
        tree = parse(os.path.join(dirpath, fn))
        if tree is None: continue
        for name, v in int_consts(tree).items():
            CONSTS[name] = None if CONSTS.get(name, v) != v else v

def resolve_size(v, local):
    """Return (value, unresolved). A symbol we cannot pin down is flagged, never passed off as a literal."""
    if not isinstance(v, str): return v, False
    for tbl in (local, CONSTS):
        if tbl.get(v) is not None: return tbl[v], False
    return v, True

out = {}
for app in ['circuits','core','dcim','extras','ipam','tenancy','users','virtualization','vpn','wireless']:
    files = glob.glob(os.path.join(ROOT, app, 'models', '*.py')) + \
            glob.glob(os.path.join(ROOT, app, 'models.py'))
    for f in files:
        tree = parse(f)
        if tree is None: continue
        local = int_consts(tree)
        for node in ast.walk(tree):
            if not isinstance(node, ast.ClassDef): continue
            bases = [ast.unparse(b) for b in node.bases]
            fields = []
            meta = {}
            for stmt in node.body:
                if isinstance(stmt, ast.Assign) and len(stmt.targets)==1 and isinstance(stmt.targets[0], ast.Name):
                    name = stmt.targets[0].id
                    v = stmt.value
                    if isinstance(v, ast.Call):
                        fn = ast.unparse(v.func).split('.')[-1]
                        if fn in FIELD_TYPES:
                            kw = kwargs_of(v)
                            entry = {'name': name, 'type': fn}
                            if fn in FK_TYPES:
                                entry['to'] = target_of(v) or kw.get('to')
                                # on_delete is the referential-integrity contract (PROTECT vs
                                # CASCADE vs SET_NULL) a spec needs to cite; Django also accepts
                                # it as the second positional argument.
                                od = kw.get('on_delete')
                                if od is None and len(v.args) > 1: od = lit(v.args[1])
                                if od is not None: entry['on_delete'] = od
                                if 'related_name' in kw: entry['related_name'] = kw['related_name']
                            if fn in GENERIC_FK_TYPES:
                                # A GenericForeignKey is an accessor over a (ct_field, fk_field)
                                # pair, not a column: requiredness lives on the pair, so record
                                # which fields those are. NetBox passes them positionally.
                                entry['ct_field'] = kw.get('ct_field') or (lit(v.args[0]) if v.args else 'content_type')
                            for k in ('null','blank','max_length','unique','default','choices','db_index','max_digits','decimal_places'):
                                if k in kw: entry[k] = kw[k]
                            for k in SIZE_KWARGS:
                                if k not in entry: continue
                                entry[k], unresolved = resolve_size(entry[k], local)
                                if unresolved: entry[k+'_unresolved'] = True
                            fields.append(entry)
                if isinstance(stmt, ast.ClassDef) and stmt.name == 'Meta':
                    for m in stmt.body:
                        if isinstance(m, ast.Assign) and isinstance(m.targets[0], ast.Name):
                            meta[m.targets[0].id] = ast.unparse(m.value)
            if fields or any('Model' in b or 'Component' in b or 'Template' in b for b in bases):
                key = f"{app}.{node.name}"
                if key in out and not fields: continue
                out[key] = {'app': app, 'name': node.name, 'file': os.path.relpath(f, ROOT),
                            'bases': bases, 'fields': fields, 'meta': meta}
print(json.dumps(out, indent=1, default=str))
