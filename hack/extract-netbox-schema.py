import ast, os, sys, json, glob

ROOT = sys.argv[1]
FIELD_TYPES = {
    'CharField','TextField','SlugField','IntegerField','PositiveIntegerField',
    'PositiveSmallIntegerField','PositiveBigIntegerField','BigIntegerField','SmallIntegerField',
    'BooleanField','DateField','DateTimeField','DecimalField','FloatField','JSONField',
    'ForeignKey','OneToOneField','ManyToManyField','GenericForeignKey','GenericRelation',
    'IPAddressField','IPNetworkField','MACAddressField','ASNField','URLField','EmailField',
    'UUIDField','ArrayField','ColorField','CounterCacheField','RestrictedGenericForeignKey',
    # Found by the first run against real 4.6.8 source. TimeZoneField is the one that
    # mattered immediately: dcim.Site.time_zone is a shipped CRD field (NetBoxSite.timeZone)
    # whose schema row did not exist, so it could not be cited.
    'TimeZoneField','BigAutoField','AutoField','ImageField','FilePathField','BinaryField',
    'ChoiceSetField','PathField','DurationField','TimeField','GenericIPAddressField',
    'CachedValueField','WWNField','NaturalOrderingField','GenericIPAddressField',
    # mptt's FK flavour, which is how every NestedGroupModel declares `parent`.
    'TreeForeignKey',
}
FK_TYPES = ('ForeignKey','OneToOneField','ManyToManyField','TreeForeignKey')
# Not fields at all: a manager is an accessor, and the AST walk therefore attributed no
# column from one -- correctly, since there is none. But taggit's TaggableManager is how
# `tags` is declared, and `tags` is a writable REST field on every PrimaryModel and
# OrganizationalModel, so leaving it out left the most-used field in the catalogue absent
# from every entry. Emitted as what it is: an M2M onto the tag model through a through
# table, never a column. Any other manager (`objects = TreeManager()`) is a queryset
# accessor and no part of the API, so it stays out.
# NetBox declares `tags = NetBoxTaggableManagerField(...)`, a subclass of taggit's
# TaggableManager (extras/managers.py). Matching only the base class name meant this fired
# on no real model at all -- the fixture used the base name because the spec did, so the
# omission survived a fixture that was built to catch it. Both names are listed: the
# subclass is what 4.6.8 uses, the base is what a plugin or an older release might.
MANAGER_TARGETS = {
    'NetBoxTaggableManagerField': 'extras.Tag',
    'TaggableManager': 'extras.Tag',
}
GENERIC_FK_TYPES = ('GenericForeignKey','RestrictedGenericForeignKey')
# Kwargs that must be an integer to be usable downstream; the rest of what we keep is
# either boolean or symbolic by nature (choices, default).
SIZE_KWARGS = ('max_length','max_digits','decimal_places')

def lit(n):
    try: return ast.literal_eval(n)
    except Exception:
        return ast.unparse(n)

def is_lit(n):
    """Whether the node is a real literal. `default=VLANStatusChoices.STATUS_ACTIVE`
    unparses to a string the digest would then quote exactly like the literal 'active'."""
    try: ast.literal_eval(n)
    except Exception: return False
    return True

def kwargs_of(call):
    """The keyword *nodes*, unevaluated: once `lit` has unparsed one, a caller can no longer
    tell a symbol from the string literal that happens to spell it."""
    return {kw.arg: kw.value for kw in call.keywords if kw.arg is not None}

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
# `netbox` is not an API app: it is where the shared abstract bases live
# (netbox/models/__init__.py -- PrimaryModel, OrganizationalModel, NestedGroupModel --
# plus netbox/models/features.py and netbox/models/mixins.py). Without it, every column a
# model inherits rather than declares is invisible: RackRole loses name/slug/description,
# Region loses name/slug/parent. Per-app mixins (dcim/models/mixins.py -> WeightMixin) are
# already inside the app globs; they were extracted but never merged. See the merge pass below.
for app in ['circuits','core','dcim','extras','ipam','netbox','tenancy','users','virtualization','vpn','wireless']:
    files = glob.glob(os.path.join(ROOT, app, 'models', '*.py')) + \
            glob.glob(os.path.join(ROOT, app, 'models.py'))
    for f in files:
        tree = parse(f)
        if tree is None: continue
        local = int_consts(tree)
        # A class declared inside another class is not a model. `ast.walk` also reached each
        # model's nested `class Meta(PrimaryModel.Meta)`, whose base name contains "Model", so
        # every one of those became a phantom `app.Meta` entry -- and the second one silently
        # replaced the first. Still a walk rather than a read of tree.body, so a model declared
        # inside a module-level `if` or `try` is not lost either.
        nested = {c for n in ast.walk(tree) if isinstance(n, ast.ClassDef)
                  for c in ast.walk(n) if isinstance(c, ast.ClassDef) and c is not n}
        for node in ast.walk(tree):
            if not isinstance(node, ast.ClassDef) or node in nested: continue
            bases = [ast.unparse(b) for b in node.bases]
            fields = []
            meta = {}
            for stmt in node.body:
                if isinstance(stmt, ast.Assign) and len(stmt.targets)==1 and isinstance(stmt.targets[0], ast.Name):
                    name = stmt.targets[0].id
                    v = stmt.value
                    if isinstance(v, ast.Call):
                        fn = ast.unparse(v.func).split('.')[-1]
                        # A field class the whitelist does not know is a column missing from the
                        # entry, and so a property missing from any CRD derived from it -- the
                        # same silent loss as a missed endpoint. Say it out loud.
                        # MANAGER_TARGETS is checked too: NetBoxTaggableManagerField ends
                        # in "Field" and is admitted below, so warning on it would be a
                        # warning about a column that was kept -- and a warning that fires
                        # on a field it admitted is how people learn to ignore warnings.
                        if fn.endswith('Field') and fn not in FIELD_TYPES and fn not in MANAGER_TARGETS:
                            print(f"!! {app}.{node.name}.{name}: unknown field type {fn}, column omitted",
                                  file=sys.stderr)
                        if fn in FIELD_TYPES or fn in MANAGER_TARGETS:
                            kwn = kwargs_of(v)
                            kw = {k: lit(n) for k, n in kwn.items()}
                            entry = {'name': name, 'type': fn}
                            if fn in FK_TYPES:
                                entry['to'] = target_of(v) or kw.get('to')
                                # on_delete is the referential-integrity contract (PROTECT vs
                                # CASCADE vs SET_NULL) a spec needs to cite; Django also accepts
                                # it as the second positional argument. A ManyToManyField has no
                                # on_delete at all, and its second positional argument is
                                # `related_name`: reading that as one invents a contract.
                                if fn != 'ManyToManyField':
                                    od = kw.get('on_delete')
                                    if od is None and len(v.args) > 1: od = lit(v.args[1])
                                    if od is not None: entry['on_delete'] = od
                                if 'related_name' in kw: entry['related_name'] = kw['related_name']
                            if fn in GENERIC_FK_TYPES:
                                # A GenericForeignKey is an accessor over a (ct_field, fk_field)
                                # pair, not a column: requiredness lives on the pair, so record
                                # which fields those are. NetBox passes them positionally.
                                entry['ct_field'] = kw.get('ct_field') or (lit(v.args[0]) if v.args else 'content_type')
                            if fn in MANAGER_TARGETS:
                                # The target is the manager's, not the declaration's: taggit
                                # reaches the tag model through the through table, which is the
                                # only thing the source names. `not_a_column` is what stops the
                                # digest deriving REQ from kwargs a manager does not have.
                                entry['to'] = MANAGER_TARGETS[fn]
                                entry['not_a_column'] = True
                                if 'through' in kw: entry['through'] = kw['through']
                            for k in ('null','blank','max_length','unique','default','choices','db_index','max_digits','decimal_places'):
                                if k in kw: entry[k] = kw[k]
                            # A symbolic default (`default=VLANStatusChoices.STATUS_ACTIVE`,
                            # `default=dict`) is not the string it unparses to. Flag it as the
                            # sizes are flagged, rather than let the digest quote it into a
                            # literal that does not exist.
                            if 'default' in entry and not is_lit(kwn['default']):
                                entry['default_unresolved'] = True
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
                if key in out:
                    # Whichever file the glob reached first won, and the other class's entire
                    # field list was dropped -- silently, in either direction. A wrong field
                    # list is worse than a missing one, so this stops the run.
                    sys.exit(f"!! {key} is declared twice: {out[key]['file']} and "
                             f"{os.path.relpath(f, ROOT)}. One class's field list would silently "
                             f"replace the other's.")
                out[key] = {'app': app, 'name': node.name, 'file': os.path.relpath(f, ROOT),
                            'bases': bases, 'fields': fields, 'meta': meta}

# A model's class body is only part of its column list. Walk each entry's bases
# left-to-right, depth-first -- Django's own field-resolution order -- and merge in every
# column the model has but does not declare, tagged with the class that does declare it.
# Django lets a subclass shadow an inherited field, so a declared field always wins; the
# inherited loser is recorded in `shadowed` rather than dropped silently, because that is
# where a merge goes wrong invisibly.
BY_NAME = {}
for v in out.values():
    # A bare class name is how a base is written in a subclass's declaration. The same name
    # in two apps cannot be attributed, so it is dropped rather than guessed -- inventing an
    # inherited column is worse than omitting one -- but the omission is said out loud.
    if v['name'] in BY_NAME:
        BY_NAME[v['name']] = None
        print(f"!! {v['name']} is declared in more than one app: columns inherited from it cannot be attributed",
              file=sys.stderr)
    else:
        BY_NAME[v['name']] = v

# An FK target has to read `app.Model`, because anything mapping a target to a Kind can
# otherwise only guess the app -- and a guess by class name picks the wrong app's model when
# two apps share a name, which is exactly what hack/classify-scope.py's `byname` fallback did.
# A bare reference is qualified here, where the declaring model's app is known: with its own
# app if that app declares the class, else with the one app that does. `django.contrib.auth`'s
# User and Permission are referenced bare and are not in the walk at all, so there is no app
# to qualify them with: those are flagged, never mislabelled. `to='self'` is deliberately left
# for after the merge -- it means the model the column ends up on, which for an inherited
# column is the subclass, not the class that declares it.
for v in out.values():
    for f in v['fields']:
        to = f.get('to')
        if not isinstance(to, str) or '.' in to or to == 'self': continue
        e = out.get(f"{v['app']}.{to}") or BY_NAME.get(to)
        if e: f['to'] = f"{e['app']}.{e['name']}"
        else: f['to_unresolved'] = True

def ancestors(v, seen):
    for b in v['bases']:
        e = BY_NAME.get(b.split('[')[0].split('.')[-1].strip())
        if e is None or e['name'] in seen: continue
        seen.add(e['name'])
        yield e
        yield from ancestors(e, seen)

# Every base contributes only the columns it declares itself, so a column is attributed to
# the class that declares it however deep the chain, and the result does not depend on which
# entry the merge loop reaches first.
OWN = {id(v): list(v['fields']) for v in out.values()}

for v in out.values():
    have = {f['name']: None for f in v['fields']}   # value None == declared on this model
    merged, shadowed = [], []
    for e in ancestors(v, {v['name']}):
        for f in OWN[id(e)]:
            entry = dict(f, declared_by=e['name'])
            if entry['name'] not in have:
                have[entry['name']] = entry['declared_by']
                merged.append(entry)
            elif have[entry['name']] != entry['declared_by']:
                # Two *different* classes declare this column: the winner is the one already
                # in `have` (Django's order). Reaching the same declaring class twice is just
                # a diamond in the base graph, not a conflict, so it is not reported.
                shadowed.append(f"{entry['name']} ({entry['declared_by']})")
    v['fields'] += merged
    if shadowed: v['shadowed'] = shadowed

# `to='self'` names the model the column ends up on: NestedGroupModel.parent is
# Region -> Region on Region and RackGroup -> RackGroup on RackGroup. Resolved per entry
# after the merge, so an inherited `parent` does not read `netbox.self`.
for key, v in out.items():
    for f in v['fields']:
        if f.get('to') == 'self': f['to'] = key
print(json.dumps(out, indent=1, default=str))
