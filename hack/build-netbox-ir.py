"""One IR for the code generator: SQL truth, API truth and filterset truth in one file (NBO-041).

    python3 hack/build-netbox-ir.py /tmp/models.json /tmp/endpoints.txt /tmp/api-schema.json > ir.json

Consumes only checked-in JSON -- no NetBox source, no network -- so it can be re-run in CI over
committed inputs. The three inputs each hold a different third of the truth:

  models.json     SQL: nullability, FK targets, on_delete, Meta.constraints, decimal precision
  endpoints.txt   the endpoint path, which must never be derived by pluralising a model name
  api-schema.json REST: choice values, writable-vs-read-only, generic-FK target spellings, and
                  the query parameters each kind's filterset actually registers

Where the first two disagree the REST schema wins -- the operator talks to the API, not to
Postgres -- and the disagreement is *recorded* in `conflicts` rather than resolved in silence.
Anything that could not be resolved lands in `unresolved` and on stderr; a consumer that wants
a hard failure fails on that list rather than trusting a clean-looking file.
"""
import ast, hashlib, json, os, sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
from netbox_schema import GENERIC_FK, RELATIONS, sql_required  # noqa: E402

MODELS, ENDPOINTS, API = sys.argv[1], sys.argv[2], sys.argv[3]
UNRESOLVED, CONFLICTS = [], []

def note(kind, where, detail):
    UNRESOLVED.append({'kind': kind, 'where': where, 'detail': detail})
    print(f"!! {where}: {detail}", file=sys.stderr)

def conflict(kind, field, fact, sql, api, resolution):
    CONFLICTS.append({'kind': kind, 'field': field, 'fact': fact,
                      'models.json': sql, 'rest': api, 'resolved_to': resolution})

def sha(path):
    return hashlib.sha256(open(path, 'rb').read()).hexdigest()

models = json.load(open(MODELS, encoding='utf-8'))
api = json.load(open(API, encoding='utf-8'))
endpoints = {}
for line in open(ENDPOINTS, encoding='utf-8'):
    if not line.strip():
        continue
    ep, key = line.split()
    endpoints[key] = ep

# Django's model_name is the lowercased class name, which is the `app_label.model` spelling a
# generic FK and a ContentType filter both take. Built from models.json so a bare model name in
# a serializer's `ContentType.objects.filter(model__in=...)` can be qualified with its app --
# guessing the app is how classify-scope.py used to pick the wrong model (NBO-071).
BY_OBJECT_TYPE = {}
for key, v in models.items():
    BY_OBJECT_TYPE.setdefault(v['name'].lower(), []).append(key)


# ---------------------------------------------------------------------------------------
# Filterset -> the query parameters NetBox actually registers.
#
# django-filter silently ignores an unregistered query parameter, so a misspelt filter returns
# the *unfiltered* result set and the engine adopts the wrong object (#206). A natural key is a
# query, so the IR has to carry the real parameter names or every key is a guess.
# ---------------------------------------------------------------------------------------

LOOKUP_MAPS = api['lookup_maps']
DISPATCH = api['lookup_dispatch']
STANDARD_LOOKUPS = set(api['standard_lookups'])
FILTER_CLASS_BASES = api['filter_classes']
FILTER_DEFAULTS = api['filter_defaults']
MODEL_FIELD_BASES = api['model_field_bases']

# The one table here that is *not* read out of the NetBox source, because it belongs to
# django-filter (pinned at ==26.1 by NetBox's requirements.txt): FILTER_FOR_DBFIELD_DEFAULTS,
# for the Django field classes NetBox's own FILTER_DEFAULTS does not override. Anything absent
# from both tables is reported rather than guessed -- the parameter is still registered, we
# just do not know which suffixes it takes, and saying so is the difference between this file
# and #206.
# Django adds an implicit `id = AutoField(primary_key=True)` to every model, so it is never in
# the class body the AST walks -- but it is in almost every filterset's Meta.fields.
IMPLICIT_PK = {'name': 'id', 'type': 'AutoField'}

DJANGO_FILTER_DEFAULTS = {
    'BooleanField': 'BooleanFilter',
    'NullBooleanField': 'BooleanFilter',
    'TextField': 'CharFilter',
    'BigIntegerField': 'NumberFilter',
    'PositiveBigIntegerField': 'NumberFilter',
    'UUIDField': 'UUIDFilter',
    'DurationField': 'DurationFilter',
    'ForeignKey': 'ModelChoiceFilter',
    'OneToOneField': 'ModelChoiceFilter',
    'TreeForeignKey': 'ModelChoiceFilter',
    'ManyToManyField': 'ModelMultipleChoiceFilter',
    'IPAddressField': 'CharFilter',
    'GenericIPAddressField': 'CharFilter',
}

def filter_ancestry(cls, seen=()):
    """A filter class and the classes it subclasses, nearest first, so
    `MultiValueContentTypeFilter` is recognised as the `MultiValueCharFilter` the isinstance
    chain in `_get_filter_lookup_dict` actually names."""
    if cls in seen:
        return []
    out = [cls]
    for b in FILTER_CLASS_BASES.get(cls, []):
        out += filter_ancestry(b, seen + (cls,))
    return out

def lookup_map_for(filter_class):
    """The lookup map NetBox applies to this filter class, or None. The dispatch table is an
    ordered if-chain in the source: `MultiValueNumberFilter` matches the numeric arm before the
    char arm would ever see it, so order is load-bearing."""
    chain = filter_ancestry(filter_class)
    for arm in DISPATCH:
        if any(c in arm['classes'] for c in chain):
            return arm['map']
    return None

def django_field_ancestry(field_class, seen=()):
    if field_class in seen:
        return []
    out = [field_class]
    for b in MODEL_FIELD_BASES.get(field_class, []) or []:
        out += django_field_ancestry(b, seen + (field_class,))
    return out

def filter_class_for_column(field, where):
    """Which filter class a bare `Meta.fields` entry gets, from the column's Django field class."""
    for cls in django_field_ancestry(field['type']):
        if cls in FILTER_DEFAULTS:
            return FILTER_DEFAULTS[cls]
        if cls in DJANGO_FILTER_DEFAULTS:
            return DJANGO_FILTER_DEFAULTS[cls]
    note('filter', where, f"no filter class known for column type {field['type']}: the "
         f"parameter is registered but its lookup suffixes are unknown")
    return None

def filterset_chain(name, seen=()):
    """A filterset and its bases, *nearest last*, so merging in order lets a subclass's declared
    filter overwrite the base's. django-filter inherits declared filters down the MRO; `Meta` it
    does not, and no NetBox filterset writes `class Meta(Base.Meta)`."""
    if name in seen or name not in api['filtersets']:
        return []
    out = []
    for b in api['filtersets'][name]['bases']:
        out += filterset_chain(b, seen + (name,))
    return out + [name]

def registered_filters(fs_name, model_key):
    """param -> {filter_class, lookups, source}: every query parameter the filterset registers.

    Mirrors `BaseFilterSet.get_filters` / `get_additional_lookups` (`netbox/filtersets.py`):
    declared filters and `Meta.fields` entries, each augmented with the suffixes of the lookup
    map its filter class selects -- unless the filter has a `method=` or a non-standard
    `lookup_expr`, in which case NetBox adds no suffixes at all."""
    fields = models[model_key]['fields']
    by_name = {f['name']: f for f in fields}
    out, dynamic = {}, []
    declared = {}
    for name in filterset_chain(fs_name):
        fs = api['filtersets'][name]
        for p, d in fs['declared'].items():
            declared[p] = dict(d, declared_by=name, source=fs['source'])
        if 'AttributeFiltersMixin' in fs['bases']:
            dynamic.append('attr_<name>')
        if 'NetBoxModelFilterSet' in fs['bases'] or name == 'NetBoxModelFilterSet':
            dynamic.append('cf_<custom field name>')
    own = api['filtersets'][fs_name]

    base = {}
    for p, d in declared.items():
        base[p] = {'filter_class': d['filter_class'], 'column': d.get('field_name', p),
                   'method': d.get('method'), 'lookup_expr': d.get('lookup_expr', 'exact'),
                   'from': 'declared', 'declared_by': d['declared_by'], 'source': d['source']}
    meta_fields = own.get('meta_fields')
    if meta_fields is None:
        note('filter', own['source'], f"{fs_name}.Meta.fields did not parse: half of "
             f"{model_key}'s query parameters are unknown")
        meta_fields = []
    for p in meta_fields:
        if p in base:
            continue          # a declared filter of the same name wins
        # `id` is Django's implicit primary key and `<fk>_id` an FK's actual DB column; neither
        # is declared in the model body, so neither is in models.json.
        f = by_name.get(p) or (IMPLICIT_PK if p == 'id' else None) \
            or (by_name.get(p[:-3]) if p.endswith('_id') and by_name.get(p[:-3], {}).get('type') in RELATIONS else None)
        if f is None:
            note('filter', own['source'], f"{fs_name}.Meta.fields names {p!r}, which is not a "
                 f"column of {model_key} -- declared by a base outside the walked apps? "
                 f"({', '.join(models[model_key]['bases'])}): parameter recorded, lookups unknown")
            base[p] = {'filter_class': None, 'column': p, 'method': None,
                       'lookup_expr': 'exact', 'from': 'meta.fields', 'source': own['source']}
            continue
        base[p] = {'filter_class': filter_class_for_column(f, own['source']), 'column': p,
                   'method': None, 'lookup_expr': 'exact', 'from': 'meta.fields',
                   'source': own['source']}

    for p, d in sorted(base.items()):
        entry = dict(d)
        # get_additional_lookups(): a filter with a method or a non-standard lookup_expr gets no
        # suffixes. `mask_length` on PrefixFilterSet is the example -- `?mask_length__gte=` exists
        # only because the filterset declares it by hand.
        if d['method'] is not None:
            entry['lookups'] = {}
            entry['no_lookups_because'] = 'the filter has a method=, so NetBox adds no suffixes'
        elif d['lookup_expr'] not in STANDARD_LOOKUPS:
            entry['lookups'] = {}
            entry['no_lookups_because'] = f"lookup_expr={d['lookup_expr']!r} is not in STANDARD_LOOKUPS"
        elif d['filter_class'] is None:
            entry['lookups'] = None
            entry['no_lookups_because'] = 'the filter class could not be determined'
        else:
            m = lookup_map_for(d['filter_class'])
            if m is None:
                # _get_filter_lookup_dict returns None for filter types NetBox deliberately does
                # not augment (BooleanFilter, UUIDFilter, ...). Not a failure: an empty set.
                entry['lookups'] = {}
                entry['no_lookups_because'] = (f"{d['filter_class']} matches no arm of "
                                               f"_get_filter_lookup_dict, so it takes no suffixes")
            else:
                entry['lookups'] = dict(LOOKUP_MAPS[m])
                entry['lookup_map'] = m
        out[p] = entry
    return out, sorted(set(dynamic))

def registered(param, filters):
    """Whether NetBox registers this exact query parameter, and the ORM lookup behind it.

    `lookups` on the base parameter is the whole suffix set: `vrf_id` carries
    FILTER_NEGATION_LOOKUP_MAP, so `vrf_id__n` exists and `vrf_id__empty` does not. Composing
    here rather than materialising ten thousand `param__suffix` entries keeps the IR readable
    and leaves exactly one place that decides what is registered."""
    if param in filters:
        return True, None
    head, _, suffix = param.rpartition('__')
    base = filters.get(head)
    if base and base.get('lookups') and suffix in base['lookups']:
        return True, base['lookups'][suffix]
    return False, None


# ---------------------------------------------------------------------------------------
# Natural keys: Meta.constraints as data, with each column resolved to a real query parameter.
# ---------------------------------------------------------------------------------------

CASE_INSENSITIVE = {'Lower': 'ie', 'Upper': 'ie'}

def constraint_nodes(expr):
    """The UniqueConstraint calls in a `Meta.constraints` expression, verbatim source and all."""
    try:
        tree = ast.parse(expr, mode='eval')
    except SyntaxError as e:
        return None, str(e)
    out = []
    for n in ast.walk(tree):
        if isinstance(n, ast.Call) and ast.unparse(n.func).split('.')[-1] == 'UniqueConstraint':
            out.append(n)
    return out, None

def null_pins(cond_node):
    """`condition=Q(tenant__isnull=True)` -> ['tenant']. Anything else is returned as the second
    element so the caller can say the condition is not modelled rather than pretend it is."""
    pins, other = [], []
    for n in ast.walk(cond_node):
        if isinstance(n, ast.keyword) and n.arg:
            if n.arg.endswith('__isnull'):
                try:
                    val = ast.literal_eval(n.value)
                except Exception:
                    val = None
                (pins if val is True else other).append(n.arg)
            else:
                other.append(n.arg)
    return pins, other

def key_columns(call, where):
    """The ordered (column, lookup) pairs of one UniqueConstraint. `fields=(...)` and positional
    expressions both occur; `Lower('name')` is a case-insensitive column, which is the whole
    point of KeyField.Lookup -- a case-sensitive `name=` filter fails to find `DNS` for `dns`
    and the engine then creates a duplicate."""
    cols = []
    for a in call.args:
        if isinstance(a, ast.Constant) and isinstance(a.value, str):
            cols.append((a.value, ''))
        elif isinstance(a, ast.Call) and ast.unparse(a.func).split('.')[-1] in CASE_INSENSITIVE \
                and a.args and isinstance(a.args[0], ast.Constant):
            cols.append((a.args[0].value, CASE_INSENSITIVE[ast.unparse(a.func).split('.')[-1]]))
        else:
            note('naturalkey', where, f"unique constraint expression not understood: "
                 f"{ast.unparse(a)}; that column is missing from the key")
    for kw in call.keywords:
        if kw.arg != 'fields':
            continue
        try:
            cols += [(c, '') for c in ast.literal_eval(kw.value)]
        except Exception:
            note('naturalkey', where, f"fields= is not a literal: {ast.unparse(kw.value)}")
    return cols

def resolve_param(column, lookup, filters, fields_by_name):
    """A column's query parameter, or None with the reason. The API name of an FK column is
    conventionally `<column>_id`; a `_`-prefixed cached column (`virtualization.Cluster._site`)
    is filtered as `site_id`, which is why a natural key may legitimately name a read-only
    column. Every candidate is *checked against the registered set* -- an unregistered parameter
    is dropped by django-filter in silence and the query then matches everything (#206)."""
    f = fields_by_name.get(column)
    bare = column.lstrip('_')
    cands = []
    if f is not None and (f['type'] in RELATIONS or f['type'] == 'ManyToManyField'):
        cands = [f'{bare}_id', bare]
    else:
        cands = [bare, f'{bare}_id']
    for c in cands:
        param = f'{c}__{lookup}' if lookup else c
        if registered(param, filters)[0]:
            return param, None
    tried = ', '.join(f'{c}__{lookup}' if lookup else c for c in cands)
    return None, (f"no registered filter parameter for column {column!r}"
                  + (f" with lookup {lookup!r}" if lookup else '') + f" (tried: {tried})")

def natural_keys(key, meta, filters, fields_by_name):
    out = []
    for meta_key in ('constraints', 'unique_together'):
        expr = meta.get(meta_key)
        if not expr:
            continue
        if meta_key == 'unique_together':
            try:
                groups = ast.literal_eval(expr)
            except Exception:
                note('naturalkey', key, f"meta.unique_together is not a literal: {expr}")
                continue
            if groups and isinstance(groups[0], str):
                groups = (groups,)
            calls = [{'cols': [(c, '') for c in g], 'condition': None, 'name': None,
                      'source': f'unique_together{tuple(g)}'} for g in groups]
        else:
            nodes, err = constraint_nodes(expr)
            if nodes is None:
                note('naturalkey', key, f"meta.constraints does not parse ({err}); no natural "
                     f"key derived, and the engine cannot look this kind up before create")
                continue
            calls = []
            for n in nodes:
                kw = {k.arg: k.value for k in n.keywords if k.arg}
                calls.append({'cols': key_columns(n, key), 'condition': kw.get('condition'),
                              'name': ast.literal_eval(kw['name']) if 'name' in kw
                                      and isinstance(kw['name'], ast.Constant) else None,
                              'source': ast.unparse(n)})
        for c in calls:
            entry = {'constraint': c['name'], 'source': c['source'], 'fields': [],
                     'null_fields': [], 'unusable': None}
            reasons = []
            for column, lookup in c['cols']:
                param, why = resolve_param(column, lookup, filters, fields_by_name)
                entry['fields'].append({'column': column, 'lookup': lookup, 'filter': param,
                                        'read_only_column': column.startswith('_')})
                if why:
                    reasons.append(why)
            if c['condition'] is not None:
                pins, other = null_pins(c['condition'])
                entry['condition'] = ast.unparse(c['condition'])
                for pin in pins:
                    column = pin[:-len('__isnull')]
                    # The sharp end of #206. A null pin is a *query*, and its parameter is
                    # `<param>__empty` -- never `__isnull`, which is the ORM lookup `empty` maps
                    # to. For an FK the map is FILTER_NEGATION_LOOKUP_MAP, which has no `empty`
                    # at all, so there is no such parameter and the pin cannot be expressed.
                    param, _why = resolve_param(column, 'empty', filters, fields_by_name)
                    pf = {'column': column, 'filter': param}
                    if param is None:
                        base_param, _ = resolve_param(column, '', filters, fields_by_name)
                        b = filters.get(base_param) if base_param else None
                        pf['reason'] = (
                            f"{base_param or column} registers no `empty` suffix"
                            + (f" ({b['filter_class']} -> {b.get('lookup_map', 'no lookup map')})" if b else '')
                            + ": a null pin on it is not expressible as a query parameter (#206)")
                        reasons.append(pf['reason'])
                    else:
                        pf['orm_lookup'] = registered(param, filters)[1]
                        # `empty` maps to `isnull` for a numeric filter and to `empty` -- string
                        # emptiness -- for a char one. Not the same question.
                        pf['means_sql_null'] = pf['orm_lookup'] == 'isnull'
                        if not pf['means_sql_null']:
                            pf['warning'] = ("this is a char filter: `empty` asks about string "
                                             "emptiness, not SQL NULL")
                    entry['null_fields'].append(pf)
                if other:
                    entry['condition_not_modelled'] = sorted(set(other))
                    reasons.append(f"constraint condition is more than a null pin: {sorted(set(other))}")
            if reasons:
                entry['unusable'] = reasons[0]
                entry['unusable_reasons'] = reasons
            out.append(entry)
    return out


# ---------------------------------------------------------------------------------------
# Field merge: the source-of-truth table.
# ---------------------------------------------------------------------------------------

def serializer_chain(name, seen=()):
    if name in seen or name not in api['serializers']:
        return []
    out = []
    for b in api['serializers'][name]['bases']:
        out += serializer_chain(b, seen + (name,))
    return out + [name]

def api_fields(ser_name):
    """(write-path field list, declared field attributes). DRF accumulates declared fields down
    the MRO; `Meta.fields` it does not -- every NetBox serializer writes its own."""
    declared = {}
    for n in serializer_chain(ser_name):
        for f, d in api['serializers'][n]['declared'].items():
            declared[f] = dict(d, declared_by=n)
    own = api['serializers'][ser_name]
    return own.get('fields'), declared, own.get('read_only_fields') or []

def qualify_content_types(names, where):
    out = []
    for n in names:
        keys = BY_OBJECT_TYPE.get(n, [])
        if len(keys) == 1:
            out.append(f"{models[keys[0]]['app']}.{n}")
        else:
            note('objecttype', where, f"content type {n!r} matches "
                 f"{'no model' if not keys else 'models in more than one app: ' + ', '.join(keys)}"
                 f"; left unqualified rather than mislabelled")
            out.append({'unresolved': n})
    return out

def classify(f, target_ct):
    """The FieldClass NBO-042 emits from. `object_types` on extras.Tag is a ManyToMany onto
    ContentType whose API values are `app_label.model` strings, not references to CRs, so it is
    its own class: emitting []ContentTypeRef would send the resolver looking for CRs that
    cannot exist."""
    t = f['type']
    if t in GENERIC_FK:
        return 'GenericFK'
    if t == 'GenericRelation':
        return 'ReverseRelation'
    if t == 'ManyToManyField' or f.get('not_a_column'):
        return 'ObjectTypeList' if target_ct else 'M2M'
    if t in RELATIONS:
        return 'GenericFKType' if target_ct else 'Ref'
    if t == 'DecimalField':
        return 'Decimal'
    if t == 'ArrayField':
        return 'Array'
    if t == 'JSONField':
        return 'JSON'
    if f.get('choices'):
        return 'Enum'
    return 'Scalar'


kinds = {}
for key in sorted(endpoints):
    if key not in models:
        # An endpoint whose viewset name matches no model: recorded, never invented. The endpoint
        # map is derived from the viewset name, so this is either a model-less helper endpoint or
        # a Kind about to be missed.
        note('endpoint', endpoints[key], f"endpoint maps to {key}, which has no models.json entry")
        continue

for key, v in sorted(models.items()):
    endpoint = endpoints.get(key)
    if endpoint is None:
        continue     # abstract bases and through tables: excluded by the endpoint join, per spec
    app, model = v['app'], v['name']
    fields_by_name = {f['name']: f for f in v['fields']}

    sers = [n for n, s in api['serializers'].items()
            if s.get('model') == model and s['app'] == app and not n.startswith('Nested')]
    if not sers:
        note('serializer', key, "no REST serializer found: writable-vs-read-only, choice values "
             "and generic-FK spellings are unknown for this kind")
        ser_name, write_fields, declared, ro_fields = None, None, {}, []
    else:
        ser_name = min(sers, key=lambda n: (len(n), n))
        if len(sers) > 1:
            note('serializer', key, f"{len(sers)} serializers name this model "
                 f"({', '.join(sorted(sers))}); used {ser_name}")
        write_fields, declared, ro_fields = api_fields(ser_name)

    fss = [n for n, s in api['filtersets'].items() if s.get('model') == model and s['app'] == app]
    if not fss:
        note('filterset', key, "no filterset found: every natural-key query on this kind would "
             "be a guess, and django-filter drops an unknown parameter in silence (#206)")
        fs_name, filters, dynamic = None, {}, []
    else:
        fs_name = min(fss, key=lambda n: (len(n), n))
        if len(fss) > 1:
            note('filterset', key, f"{len(fss)} filtersets name this model "
                 f"({', '.join(sorted(fss))}); used {fs_name}")
        filters, dynamic = registered_filters(fs_name, key)

    out_fields = []
    for f in v['fields']:
        name = f['name']
        d = declared.get(name, {})
        in_write_path = write_fields is None or name in write_fields
        read_only = bool(d.get('read_only')) or name in ro_fields
        target_ct = f.get('to') == 'contenttypes.ContentType'
        sql_req = sql_required(f, fields_by_name)
        entry = {
            'name': name, 'type': f['type'], 'class': classify(f, target_ct),
            'declared_by': f.get('declared_by'),
            'sql': {k: f[k] for k in ('to', 'on_delete', 'null', 'blank', 'default', 'choices',
                                      'max_length', 'max_digits', 'decimal_places', 'unique',
                                      'through', 'ct_field', 'related_name')
                    if k in f},
            'sql_required': sql_req,
            'read_only': read_only,
            'in_write_path': in_write_path,
            'nullable': bool(f.get('null')),
            'origin': 'both' if (name in declared or (write_fields and name in write_fields)) else 'sql',
        }
        for flag in ('to_unresolved', 'default_unresolved', 'max_length_unresolved',
                     'max_digits_unresolved', 'decimal_places_unresolved', 'not_a_column'):
            if f.get(flag):
                entry[flag] = True
        if f.get('to') and isinstance(f['to'], str) and '.' in f['to']:
            entry['ref'] = {'target': f['to'], 'self': f['to'] == key,
                            'on_delete': f.get('on_delete')}
        if d:
            entry['api'] = {k: vv for k, vv in d.items() if k != 'declared_by'}
            if d.get('choices'):
                entry['enum'] = d['choices']
            if d.get('content_types'):
                entry['object_types'] = qualify_content_types(d['content_types'], key)
        # The AST records `choices=PrefixStatusChoices` and nothing else. The members come from
        # the serializer's ChoiceField where there is one, and from the column's own `choices=`
        # otherwise -- six agents have read those by hand out of <app>/choices.py.
        if 'enum' not in entry and f.get('choices'):
            entry['enum'] = str(f['choices']).split('.')[-1]
        if entry.get('enum') and entry['enum'] not in api['choices']:
            note('enum', f"{key}.{name}", f"choice set {entry['enum']!r} is not in the extracted "
                 f"set: its member values are unknown")
            entry['enum_unresolved'] = True
        # Requiredness is the intersection: NOT NULL with no DB default *and* required by the
        # request serializer. OpenAPI/DRF marks fields required that have DB defaults, and the
        # AST marks fields REQ that are not columns at all.
        api_req = d.get('required')
        entry['required'] = bool(sql_req and in_write_path and not read_only and api_req is not False)
        if sql_req and api_req is False:
            conflict(key, name, 'required-on-create', 'NOT NULL, no default',
                     'serializer declares required=False', 'not required')
        if sql_req and not in_write_path:
            conflict(key, name, 'exists on the write path', 'NOT NULL, no default',
                     'absent from the serializer Meta.fields', 'not writable')
        if entry['class'] == 'ReverseRelation':
            # A GenericRelation is a reverse accessor, never writable and never a column. Not a
            # disagreement between the sources -- one of them simply has no opinion.
            entry['read_only'] = True
            entry['reason'] = 'GenericRelation: a reverse accessor, never on the write path'
        elif not in_write_path and not read_only:
            conflict(key, name, 'column absent from the write path', 'a column',
                     'absent from the serializer Meta.fields', 'not writable')
        # Belt and braces on the cached columns: a `_`-prefixed column or a CounterCacheField the
        # serializer does not mark read-only would be written and silently no-op.
        if (name.startswith('_') or f['type'] == 'CounterCacheField') and not read_only and in_write_path:
            conflict(key, name, 'read-only cached column', f"{f['type']}, `_`-prefixed"
                     if name.startswith('_') else f['type'], 'not marked read_only', 'read-only')
            entry['read_only'] = True
        out_fields.append(entry)

    nks = natural_keys(key, v['meta'], filters, fields_by_name)
    if not nks:
        note('naturalkey', key, "no Meta.constraints and no unique_together: this kind has no "
             "derivable natural key and needs one declared by hand before it can be adopted")
    kinds[key] = {
        'app': app, 'model': model, 'endpoint': endpoint,
        'object_type': f"{app}.{model.lower()}", 'source_file': v['file'],
        'bases': v['bases'], 'shadowed': v.get('shadowed'),
        'serializer': ser_name, 'filterset': fs_name,
        'write_path': write_fields, 'brief_fields': api['serializers'][ser_name].get('brief_fields')
                                                    if ser_name else None,
        'fields': out_fields,
        'filters': filters,
        'dynamic_filters': dynamic,
        'natural_keys': nks,
        'meta': v['meta'],
    }

# Enums: only the ones some kind actually references, so the IR is reviewable rather than a copy
# of every ChoiceSet in NetBox. `extendable` is the one that matters for a CRD: a ChoiceSet with
# a `key` can be replaced or extended by a deployment's FIELD_CHOICES, so a closed enum in the
# CRD will reject a value that deployment considers legal.
used = {f['enum'] for k in kinds.values() for f in k['fields'] if f.get('enum')}
enums = {n: api['choices'][n] for n in sorted(used) if n in api['choices']}
for n in sorted(e for e in used if e not in api['choices']):
    note('enum', n, "referenced by a field but not found in <app>/choices.py")

# Abstract bases, through tables and mixins: real entries in models.json with no API endpoint,
# and therefore not kinds. Listed rather than filtered out by a name list, so a *model* that
# ought to have an endpoint and does not is visible instead of merely absent.
no_endpoint = sorted(k for k in models if k not in endpoints)
ir = {
    'netbox_version': api.get('netbox_version'),
    'inputs': {os.path.basename(p): sha(p) for p in (MODELS, ENDPOINTS, API)},
    'kinds': kinds,
    'enums': enums,
    'lookup_maps': LOOKUP_MAPS,
    'conflicts': CONFLICTS,
    'unresolved': UNRESOLVED + api.get('unresolved', []),
    'models_without_endpoint': no_endpoint,
    'endpoints_without_model': sorted(ep for k, ep in endpoints.items() if k not in models),
}
print(json.dumps(ir, indent=1, sort_keys=True, default=str))
print(f"ok: {len(kinds)} kinds, {len(enums)} enums, "
      f"{sum(len(k['filters']) for k in kinds.values())} filter parameters, "
      f"{sum(len(k['natural_keys']) for k in kinds.values())} natural-key candidates "
      f"({sum(1 for k in kinds.values() for n in k['natural_keys'] if n['unusable'])} unusable), "
      f"{len(CONFLICTS)} conflicts, {len(ir['unresolved'])} unresolved", file=sys.stderr)
