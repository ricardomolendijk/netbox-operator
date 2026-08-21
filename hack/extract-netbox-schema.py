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

out = {}
for app in ['circuits','core','dcim','extras','ipam','tenancy','users','virtualization','vpn','wireless']:
    files = glob.glob(os.path.join(ROOT, app, 'models', '*.py')) + \
            glob.glob(os.path.join(ROOT, app, 'models.py'))
    for f in files:
        try:
            tree = ast.parse(open(f, encoding='utf-8').read())
        except Exception:
            continue
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
                            if fn in ('ForeignKey','OneToOneField','ManyToManyField'):
                                entry['to'] = target_of(v) or kw.get('to')
                                if 'on_delete' in kw: entry['on_delete'] = kw['on_delete']
                                if 'related_name' in kw: entry['related_name'] = kw['related_name']
                            for k in ('null','blank','max_length','unique','default','choices','db_index','max_digits','decimal_places'):
                                if k in kw: entry[k] = kw[k]
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
