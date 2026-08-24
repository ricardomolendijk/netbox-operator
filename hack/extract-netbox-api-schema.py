"""The API half of the schema truth, read from the NetBox source (NBO-041).

`extract-netbox-schema.py` walks the Django models and gives SQL truth. It cannot see three
things the operator needs, and every one of them has cost a day already:

  1. **Choice values.** The model walk records `choices=SiteStatusChoices` and
     `default=UNRESOLVED:SiteStatusChoices.STATUS_ACTIVE`; the members live in
     `<app>/choices.py`. Six agents have read those by hand. A ChoiceSet that declares
     `key = 'Site.status'` can also be *replaced or extended* by a deployment's
     `FIELD_CHOICES` setting (`utilities/choices.py`, ChoiceSetMeta.__new__), so the member
     list is a default, not a closed set -- which is the difference between a CRD enum that
     validates and one that rejects a legitimate value.
  2. **Writable vs read-only, and the `app_label.model` spelling of a generic-FK type
     field.** Both live in the REST serializers (`<app>/api/serializers*`), not in the ORM.
  3. **Filterset parameter names.** A natural key is a *query*. django-filter silently drops
     an unregistered query parameter, so a misspelled filter returns the unfiltered result
     set and the engine adopts the wrong object (#206). The registered names come from the
     filterset's declared filters plus `Meta.fields`, expanded by the lookup maps in
     `utilities/constants.py` -- and which map applies depends on the filter class
     (`netbox/filtersets.py:_get_filter_lookup_dict`).

Nothing here is derived: the lookup maps and the filter-class-to-map table are *read out of
the source*, because a hardcoded copy of them is exactly the bug #206 is about.

    python3 hack/extract-netbox-api-schema.py /tmp/netbox-src/netbox > /tmp/api-schema.json

Like the model walk, this exits non-zero rather than emit something quietly wrong, and
warns on stderr for anything it drops. Every warning is also recorded in the output's
`unresolved` list, so a consumer can fail on it instead of trusting a clean-looking file.
"""
import ast, glob, json, os, sys

ROOT = sys.argv[1]
UNRESOLVED = []

def note(kind, where, detail):
    """Record and say out loud one thing that was not parsed. A column, choice value or
    filter parameter dropped in silence is the failure mode this whole pipeline exists to
    avoid, so there is exactly one way to drop something and it is loud."""
    UNRESOLVED.append({'kind': kind, 'where': where, 'detail': detail})
    print(f"!! {where}: {detail}", file=sys.stderr)

def parse(path):
    try:
        return ast.parse(open(path, encoding='utf-8').read())
    except Exception as e:
        note('parse', rel(path), f"cannot parse: {e}")
        return None

def rel(path):
    return os.path.relpath(path, ROOT)

def at(path, node):
    return f"{rel(path)}:{node.lineno}"

def lit(n):
    try:
        return ast.literal_eval(n)
    except Exception:
        return None

MISSING = object()
# Nodes a constant expression may be built from. NetBox writes `INTERVAL_DAILY = 60 * 24`,
# `INTERVAL_HOURLY * 12` and `CSV_DELIMITERS['comma']` in ChoiceSet bodies, none of which is a
# literal `ast.literal_eval` will take. Restricting the node types keeps this an arithmetic
# evaluator over names already known to be literals, not an exec of the source.
CONST_NODES = (ast.Expression, ast.Constant, ast.Name, ast.BinOp, ast.UnaryOp, ast.Tuple,
               ast.List, ast.Dict, ast.Set, ast.Subscript, ast.Load, ast.Index,
               ast.Add, ast.Sub, ast.Mult, ast.Div, ast.FloorDiv, ast.Mod, ast.Pow,
               ast.USub, ast.UAdd)

def empty_container(node):
    """`list()` / `tuple()` -- an empty collection written as a call. The abstract ChoiceSet's
    `CHOICES = list()` and InterfaceConnectionFilterSet's `fields = tuple()` are both real and
    both legitimately empty; treating them as parse failures cries wolf."""
    return isinstance(node, ast.Call) and not node.args and not node.keywords \
        and ast.unparse(node.func) in ('list', 'tuple', 'set', 'dict')

def const_eval(node, ns):
    """The value of a constant expression, or MISSING. A symbol that does not resolve is
    MISSING rather than the string it unparses to -- the whole point of NBO-071's
    `default_unresolved` flag."""
    if not all(isinstance(n, CONST_NODES) for n in ast.walk(node)):
        return MISSING
    try:
        return eval(compile(ast.Expression(node), '<const>', 'eval'), {'__builtins__': {}}, dict(ns))
    except Exception:
        return MISSING

def kwargs_of(call):
    return {kw.arg: kw.value for kw in call.keywords if kw.arg is not None}

def func_name(call):
    """The trailing name of a call target: `serializers.IntegerField(...)` -> IntegerField."""
    return ast.unparse(call.func).split('(')[0].split('.')[-1]

def top_classes(tree):
    """Module-level classes only. A class nested in another (every `class Meta`) is not one of
    these, which is the mistake that made phantom `app.Meta` model entries in NBO-071."""
    return [n for n in tree.body if isinstance(n, ast.ClassDef)]

def base_names(node):
    return [ast.unparse(b).split('[')[0].split('.')[-1] for b in node.bases]

def assigns(node):
    """(name, value node) for each simple assignment in a class body."""
    for stmt in node.body:
        if isinstance(stmt, ast.Assign) and len(stmt.targets) == 1 and isinstance(stmt.targets[0], ast.Name):
            yield stmt.targets[0].id, stmt.value

def nested_meta(node):
    for stmt in node.body:
        if isinstance(stmt, ast.ClassDef) and stmt.name == 'Meta':
            return stmt
    return None

# Module-level literal constants across the tree, for the symbols a ChoiceSet member or a
# serializer's generic-FK target set is written as (`CSV_DELIMITERS`, `LOCATION_SCOPE_TYPES`).
# A name defined twice with conflicting values is dropped rather than guessed, exactly as
# extract-netbox-schema.py does for column sizes: a wrong value is worse than a missing one.
CONST_LITERALS = {}

def _prescan_constants():
    for f in files('**/*.py'):
        tree = parse(f)
        if tree is None:
            continue
        for stmt in tree.body:
            if not (isinstance(stmt, ast.Assign) and isinstance(stmt.targets[0], ast.Name)):
                continue
            name = stmt.targets[0].id
            v = lit(stmt.value)
            if v is None or not name.isupper():
                continue
            if name in CONST_LITERALS and CONST_LITERALS[name] != v:
                CONST_LITERALS[name] = MISSING
            else:
                CONST_LITERALS.setdefault(name, v)
    for k in [k for k, v in CONST_LITERALS.items() if v is MISSING]:
        del CONST_LITERALS[k]

def files(*patterns):
    out = []
    for p in patterns:
        out += glob.glob(os.path.join(ROOT, p), recursive=True)
    # tests and the test harness declare throwaway serializers and filtersets of their own.
    return sorted(f for f in out if '/tests/' not in f and '/testing/' not in f and '/forms/' not in f)


# ---------------------------------------------------------------------------------------
# Gap 1: choice values, and whether a deployment can change them.
# ---------------------------------------------------------------------------------------

def unwrap_label(n):
    """`_('Active')` -> 'Active'. A label built at import time (`_('{n} inches').format(n=10)`)
    is not a literal and is returned unresolved rather than guessed at: the *values* are what
    a CRD enum is made of, but a wrong label in generated docs is still a wrong label."""
    if isinstance(n, ast.Call) and func_name(n) in ('_', 'gettext', 'gettext_lazy') and n.args:
        return unwrap_label(n.args[0])
    v = lit(n)
    return v if v is not None else {'unresolved': ast.unparse(n)}

def choice_tuple(elt, ns, where):
    """One `(VALUE, label[, colour])` entry, or None with the reason said out loud."""
    if not isinstance(elt, (ast.Tuple, ast.List)) or len(elt.elts) < 2:
        note('choice', where, f"choice entry is not a (value, label) tuple: {ast.unparse(elt)}")
        return None
    value = const_eval(elt.elts[0], ns)
    if value is MISSING:
        note('choice', where, f"choice value does not resolve to a literal: {ast.unparse(elt.elts[0])}")
        return None
    out = {'value': value, 'label': unwrap_label(elt.elts[1])}
    if len(elt.elts) > 2:
        out['color'] = lit(elt.elts[2])
    return out

def extract_choices():
    """Every ChoiceSet in `<app>/choices.py`, with its members and its `key`."""
    out = {}
    for f in files('*/choices.py'):
        tree = parse(f)
        if tree is None:
            continue
        # Module scope first: `COMMA = CSV_DELIMITERS['comma']` reads an imported dict, and a
        # class-body constant may be built from either.
        ns = dict(CONST_LITERALS)
        for stmt in tree.body:
            if isinstance(stmt, ast.Assign) and isinstance(stmt.targets[0], ast.Name):
                v = const_eval(stmt.value, ns)
                if v is not MISSING:
                    ns[stmt.targets[0].id] = v
        for node in top_classes(tree):
            # A metaclass is not a choice set: ChoiceSetMeta(type) has no CHOICES and never will.
            if 'type' in base_names(node):
                continue
            body = dict(assigns(node))
            if 'CHOICES' not in body and not base_names(node):
                continue
            local = dict(ns)
            for k, v in body.items():
                ev = const_eval(v, local)
                if ev is not MISSING:
                    local[k] = ev
            entry = {'source': at(f, node), 'bases': base_names(node),
                     'key': lit(body['key']) if 'key' in body else None}
            # A ChoiceSet that names a `key` is extended or *replaced* wholesale by a
            # deployment's FIELD_CHOICES (utilities/choices.py, ChoiceSetMeta.__new__), so its
            # members are a default rather than a closed set. A CRD that pins an enum here
            # rejects a value the deployment considers legal -- and generating an open string
            # instead is a decision, so it has to be visible.
            entry['extendable'] = entry['key'] is not None
            if 'CHOICES' in body:
                decl = body['CHOICES']
                if not isinstance(decl, (ast.List, ast.Tuple)):
                    # `CHOICES = list()` on the abstract base is an empty set, not a parse
                    # failure; anything else that is not a list display is one.
                    if empty_container(decl):
                        entry['values'] = []
                    else:
                        note('choice', at(f, node), f"{node.name}.CHOICES is not a list display: "
                             f"{ast.unparse(decl)}; members omitted")
                        entry['values'] = None
                else:
                    values, grouped, splices = [], False, []
                    for elt in decl.elts:
                        # `CHOICES = (*ButtonColorChoices.CHOICES, (LINK, _('Link')))` splices
                        # another set in. Deferred to a second pass: the set it names may be
                        # declared later in the file.
                        if isinstance(elt, ast.Starred):
                            splices.append((len(values), ast.unparse(elt.value).split('.')[0]))
                            continue
                        # A grouped ChoiceSet is ('Group', ((v, l), ...)): flattened, since a
                        # CRD enum has no notion of an optgroup, but the fact is recorded.
                        if isinstance(elt, (ast.Tuple, ast.List)) and len(elt.elts) == 2 \
                                and isinstance(elt.elts[1], (ast.Tuple, ast.List)) \
                                and elt.elts[1].elts and isinstance(elt.elts[1].elts[0], (ast.Tuple, ast.List)):
                            grouped = True
                            for sub in elt.elts[1].elts:
                                c = choice_tuple(sub, local, at(f, node))
                                if c: values.append(c)
                            continue
                        c = choice_tuple(elt, local, at(f, node))
                        if c: values.append(c)
                    entry['values'] = values
                    entry['grouped'] = grouped
                    if splices:
                        entry['splices'] = splices
            key = node.name
            if key in out:
                # Two ChoiceSets of one name means one value list silently replaces the other,
                # and a CRD enum built from the loser rejects every value the API accepts.
                sys.exit(f"!! choice set {key} is declared twice: {out[key]['source']} and "
                         f"{at(f, node)}. One value list would silently replace the other.")
            out[key] = entry
    for name, e in list(out.items()):
        # A ChoiceSet with no CHOICES of its own inherits its base's.
        if e.get('values') is None and 'values' not in e or e.get('values') is None:
            for b in e['bases']:
                if b in out and out[b].get('values'):
                    e['values'] = out[b]['values']
                    e['inherited_from'] = b
                    break
            else:
                if e.get('values') is None:
                    note('choice', e['source'], f"{name} declares no usable CHOICES and no base that does")
                    continue
        # ...and one that splices another in gets those members, in place.
        for pos, src in reversed(e.pop('splices', [])):
            if src not in out or not out[src].get('values'):
                note('choice', e['source'], f"{name}.CHOICES splices in {src}.CHOICES, which did not resolve")
                continue
            e['values'][pos:pos] = out[src]['values']
            e.setdefault('spliced_from', []).append(src)
    return out


# ---------------------------------------------------------------------------------------
# Gap 2: writable vs read-only, and the app_label.model spelling of a generic-FK type field.
# ---------------------------------------------------------------------------------------

SERIALIZER_KWARGS = ('read_only', 'write_only', 'required', 'allow_null', 'many', 'nested', 'default')

def extract_serializers():
    """Every `*Serializer` in `<app>/api/serializers*`, with its Meta and its declared fields.

    `Meta.fields` is the API field list -- the write path is what the operator POSTs to, and a
    column absent from it cannot be set however NOT NULL it is in Postgres."""
    out = {}
    for f in files('*/api/serializers.py', '*/api/serializers_/*.py', 'netbox/api/serializers/*.py'):
        tree = parse(f)
        if tree is None:
            continue
        module_consts = {n.targets[0].id: lit(n.value) for n in tree.body
                         if isinstance(n, ast.Assign) and isinstance(n.targets[0], ast.Name)}
        for node in top_classes(tree):
            if not node.name.endswith('Serializer'):
                continue
            entry = {'source': at(f, node), 'app': rel(f).split('/')[0],
                     'bases': base_names(node), 'declared': {}}
            meta = nested_meta(node)
            if meta is not None:
                m = dict(assigns(meta))
                if 'model' in m:
                    entry['model'] = ast.unparse(m['model']).split('.')[-1]
                for k in ('fields', 'brief_fields', 'read_only_fields'):
                    if k not in m:
                        continue
                    # `read_only_fields = fields` names a sibling Meta key.
                    v = entry.get(m[k].id) if isinstance(m[k], ast.Name) else lit(m[k])
                    if v is None:
                        # Not a literal list: the API field list is the write path, so a
                        # guess here is a generated CRD with fields the API does not have.
                        note('serializer', at(f, node), f"{node.name}.Meta.{k} is not a literal "
                             f"list ({ast.unparse(m[k])}); API field list unknown")
                        entry[k] = None
                    else:
                        entry[k] = list(v)
            for name, v in assigns(node):
                if not isinstance(v, ast.Call):
                    continue
                kwn = kwargs_of(v)
                d = {'serializer_field': func_name(v)}
                for k in SERIALIZER_KWARGS:
                    if k not in kwn:
                        continue
                    # `default=None` is a real value, so MISSING rather than None is what marks
                    # a kwarg that did not resolve.
                    v = const_eval(kwn[k], CONST_LITERALS)
                    d[k] = {'unresolved': ast.unparse(kwn[k])} if v is MISSING else v
                if 'choices' in kwn:
                    d['choices'] = ast.unparse(kwn['choices']).split('.')[-1] \
                        if isinstance(kwn['choices'], (ast.Name, ast.Attribute)) else None
                    if d['choices'] is None:
                        note('serializer', at(f, node),
                             f"{node.name}.{name} choices= is not a ChoiceSet name: {ast.unparse(kwn['choices'])}")
                # `ContentTypeField(queryset=ContentType.objects.filter(model__in=LOCATION_SCOPE_TYPES))`
                # is the only place the *set* of legal generic-FK targets is written down, and it
                # is written as bare model names -- the `app_label.model` half is resolved by
                # build-netbox-ir.py, which has models.json and so knows the apps.
                if 'queryset' in kwn and 'ContentType' in ast.unparse(kwn['queryset']):
                    d['content_type_queryset'] = ast.unparse(kwn['queryset'])
                    for kw in ast.walk(kwn['queryset']):
                        if isinstance(kw, ast.keyword) and kw.arg in ('model__in', 'model__in_'):
                            v2 = lit(kw.value)
                            if v2 is None and isinstance(kw.value, ast.Name):
                                v2 = module_consts.get(kw.value.id) or CONST_LITERALS.get(kw.value.id)
                            if v2 is None:
                                note('serializer', at(f, node), f"{node.name}.{name}: cannot resolve "
                                     f"the legal content types in {ast.unparse(kw.value)}")
                            else:
                                d['content_types'] = list(v2)
                entry['declared'][name] = d
            if node.name in out:
                sys.exit(f"!! serializer {node.name} is declared twice: {out[node.name]['source']} "
                         f"and {at(f, node)}. One API field list would silently replace the other.")
            out[node.name] = entry
    return out


# ---------------------------------------------------------------------------------------
# Gap 3: the filter parameters a kind's filterset actually registers.
# ---------------------------------------------------------------------------------------

FILTER_KWARGS = ('method', 'field_name', 'lookup_expr', 'to_field_name', 'exclude', 'null_value')

def extract_lookup_maps():
    """`FILTER_*_LOOKUP_MAP` out of `utilities/constants.py`, and `STANDARD_LOOKUPS` out of
    `netbox/filtersets.py`. Read rather than copied: a stale hardcoded copy of these maps is
    precisely the defect in #206, where the operator emitted `__isnull` -- the ORM lookup --
    where NetBox registers the parameter suffix `empty`."""
    out = {}
    path = os.path.join(ROOT, 'utilities', 'constants.py')
    tree = parse(path)
    if tree is None:
        sys.exit(f"!! {rel(path)}: unreadable; the filter lookup maps are not optional")
    for stmt in tree.body:
        if not (isinstance(stmt, ast.Assign) and isinstance(stmt.targets[0], ast.Name)):
            continue
        name = stmt.targets[0].id
        if not name.startswith('FILTER_') or not name.endswith('_LOOKUP_MAP'):
            continue
        v = stmt.value
        # `dict(n='exact', empty='isnull')` -- a call, not a literal, so literal_eval is out.
        if isinstance(v, ast.Call) and func_name(v) == 'dict':
            out[name] = {kw.arg: lit(kw.value) for kw in v.keywords}
        else:
            out[name] = lit(v)
        if not out[name]:
            sys.exit(f"!! {rel(path)}:{stmt.lineno}: cannot read {name}; every registered "
                     f"filter parameter suffix comes from it")
    if 'FILTER_CHAR_BASED_LOOKUP_MAP' not in out or 'FILTER_NUMERIC_BASED_LOOKUP_MAP' not in out:
        sys.exit(f"!! {rel(path)}: the char and numeric lookup maps are both required")
    return out

def extract_lookup_dispatch():
    """The filter-class-to-lookup-map table, read out of
    `BaseFilterSet._get_filter_lookup_dict` (`netbox/filtersets.py`). An ordered list, because
    the source is an if-chain and `MultiValueNumberFilter` matches the numeric arm before the
    char arm would ever see it."""
    path = os.path.join(ROOT, 'netbox', 'filtersets.py')
    tree = parse(path)
    if tree is None:
        sys.exit(f"!! {rel(path)}: unreadable; the filter-class-to-lookup-map table is not optional")
    fn = next((n for n in ast.walk(tree) if isinstance(n, ast.FunctionDef)
               and n.name == '_get_filter_lookup_dict'), None)
    if fn is None:
        sys.exit(f"!! {rel(path)}: _get_filter_lookup_dict not found -- renamed? Without it every "
                 f"lookup suffix would be a guess.")
    table = []
    for stmt in fn.body:
        if not isinstance(stmt, ast.If) or not isinstance(stmt.test, ast.Call):
            continue
        if func_name(stmt.test) != 'isinstance' or len(stmt.test.args) != 2:
            continue
        arg = stmt.test.args[1]
        classes = [ast.unparse(e).split('.')[-1] for e in
                   (arg.elts if isinstance(arg, (ast.Tuple, ast.List)) else [arg])]
        ret = next((s for s in stmt.body if isinstance(s, ast.Return)), None)
        if ret is None:
            continue
        table.append({'map': ast.unparse(ret.value), 'classes': classes})
    if not table:
        sys.exit(f"!! {rel(path)}: _get_filter_lookup_dict has no isinstance arms to read")
    standard = next((lit(s.value) for s in tree.body
                     if isinstance(s, ast.Assign) and getattr(s.targets[0], 'id', None) == 'STANDARD_LOOKUPS'), None)
    if standard is None:
        sys.exit(f"!! {rel(path)}: STANDARD_LOOKUPS not found; a filter with a non-standard "
                 f"lookup_expr gets no suffixes at all and would otherwise be over-reported")
    return table, list(standard)

def extract_filter_defaults():
    """`BaseFilterSet.FILTER_DEFAULTS.update({...})` -- which filter class a bare `Meta.fields`
    entry gets, per Django model field class. Half of a filterset's registered parameters come
    from `Meta.fields`, and the lookup suffixes they accept depend on this table."""
    path = os.path.join(ROOT, 'netbox', 'filtersets.py')
    tree = parse(path)
    if tree is None:
        sys.exit(f"!! {rel(path)}: unreadable")
    out = {}
    for n in ast.walk(tree):
        if not (isinstance(n, ast.Call) and ast.unparse(n.func).endswith('FILTER_DEFAULTS.update')):
            continue
        if not n.args or not isinstance(n.args[0], ast.Dict):
            note('filterset', at(path, n), "FILTER_DEFAULTS.update() argument is not a dict display")
            continue
        for k, v in zip(n.args[0].keys, n.args[0].values, strict=True):
            field_cls = ast.unparse(k).split('.')[-1]
            cls = None
            if isinstance(v, ast.Dict):
                for kk, vv in zip(v.keys, v.values, strict=True):
                    if lit(kk) == 'filter_class':
                        cls = ast.unparse(vv).split('.')[-1]
            if cls is None:
                note('filterset', at(path, n), f"FILTER_DEFAULTS[{field_cls}] names no filter_class")
                continue
            out[field_cls] = cls
    if not out:
        sys.exit(f"!! {rel(path)}: FILTER_DEFAULTS not found -- without it every Meta.fields "
                 f"parameter's lookup suffixes would be a guess")
    return out

def extract_filter_classes():
    """`utilities/filters.py`'s filter classes and their bases, so `MultiValueContentTypeFilter`
    can be recognised as the `MultiValueCharFilter` the isinstance chain names."""
    out = {}
    for f in files('utilities/filters.py', '*/filters.py'):
        tree = parse(f)
        if tree is None:
            continue
        for node in top_classes(tree):
            if node.name in out:
                continue
            out[node.name] = base_names(node)
    return out

def extract_model_field_bases():
    """NetBox's own model field classes and their bases (`class ASNField(models.BigIntegerField)`).

    A `Meta.fields` entry's filter class -- and therefore which lookup suffixes it registers --
    is chosen by the *Django* field class. `FILTER_DEFAULTS` is keyed on those, and a NetBox
    field class only matches after walking up to the Django one it subclasses."""
    out = {}
    for f in files('*/fields.py', '*/fields/*.py', '*/models/fields.py', 'utilities/fields.py'):
        tree = parse(f)
        if tree is None:
            continue
        for node in top_classes(tree):
            out.setdefault(node.name, base_names(node))
    return out

def extract_filtersets():
    """Every `*FilterSet` / filter mixin, its declared filters and its `Meta.fields`.

    django-filter inherits *declared filters* down the MRO but not `Meta` -- no NetBox
    filterset writes `class Meta(Base.Meta)` -- so the bases are recorded and merged, and
    `meta_fields` is per concrete class."""
    out = {}
    for f in files('*/filtersets.py', '*/base_filtersets.py', '*/filterset_mixins.py'):
        tree = parse(f)
        if tree is None:
            continue
        for node in top_classes(tree):
            if not (node.name.endswith('FilterSet') or node.name.endswith('FilterMixin')):
                continue
            entry = {'source': at(f, node), 'app': rel(f).split('/')[0],
                     'bases': base_names(node), 'declared': {}}
            meta = nested_meta(node)
            if meta is not None:
                m = dict(assigns(meta))
                if 'model' in m:
                    entry['model'] = ast.unparse(m['model']).split('.')[-1]
                if 'fields' in m:
                    v = lit(m['fields'])
                    if v is None and empty_container(m['fields']):
                        v = []
                    if v is None:
                        # Meta.fields is half the registered parameter set. A guess is a
                        # natural key built on a parameter NetBox drops in silence.
                        note('filterset', at(f, node), f"{node.name}.Meta.fields is not a literal "
                             f"({ast.unparse(m['fields'])}); half its parameters are unknown")
                        entry['meta_fields'] = None
                    elif isinstance(v, dict):
                        # django-filter also accepts {field: [lookups]}.
                        entry['meta_fields'] = sorted(v)
                        entry['meta_field_lookups'] = {k: list(vv) for k, vv in v.items()}
                    else:
                        entry['meta_fields'] = list(v)
            for name, v in assigns(node):
                if not isinstance(v, ast.Call):
                    continue
                cls = func_name(v)
                if not cls.endswith('Filter'):
                    continue
                kwn = kwargs_of(v)
                d = {'filter_class': cls}
                for k in FILTER_KWARGS:
                    if k not in kwn:
                        continue
                    v = const_eval(kwn[k], CONST_LITERALS)
                    d[k] = {'unresolved': ast.unparse(kwn[k])} if v is MISSING else v
                if 'choices' in kwn:
                    d['choices'] = ast.unparse(kwn['choices']).split('.')[-1]
                entry['declared'][name] = d
            if node.name in out:
                sys.exit(f"!! filterset {node.name} is declared twice: {out[node.name]['source']} "
                         f"and {at(f, node)}. One parameter list would silently replace the other.")
            out[node.name] = entry
    return out


def netbox_version():
    """The version stamped on the output, read from the checkout's release.yaml rather than
    passed in by hand: an IR labelled with the wrong NetBox version is worse than an unlabelled
    one, because a version bump then produces a diff nobody can attribute."""
    for cand in ('release.yaml', os.path.join('..', 'release.yaml')):
        path = os.path.join(ROOT, cand)
        if not os.path.exists(path):
            continue
        for line in open(path, encoding='utf-8'):
            if line.startswith('version:'):
                return line.split(':', 1)[1].strip().strip('"\'')
    note('version', 'release.yaml', 'not found: the output carries no NetBox version stamp')
    return None

_prescan_constants()
lookup_maps = extract_lookup_maps()
dispatch, standard_lookups = extract_lookup_dispatch()
out = {
    'netbox_version': netbox_version(),
    'lookup_maps': lookup_maps,
    'lookup_dispatch': dispatch,
    'standard_lookups': standard_lookups,
    'filter_classes': extract_filter_classes(),
    'filter_defaults': extract_filter_defaults(),
    'model_field_bases': extract_model_field_bases(),
    'choices': extract_choices(),
    'serializers': extract_serializers(),
    'filtersets': extract_filtersets(),
}
out['unresolved'] = UNRESOLVED
print(json.dumps(out, indent=1, sort_keys=True, default=str))
