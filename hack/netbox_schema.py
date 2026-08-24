"""The one definition of "required in SQL", shared by the digest and the IR builder.

It lived in `digest-netbox-schema.py` and is asserted there by `hack/test_digest.py`. The IR
builder needs the same rule to intersect with the REST serializer's `required`, and two copies
of a rule this fiddly is how the two halves of the pipeline start disagreeing without anyone
noticing -- the same failure mode as the hardcoded lookup map in #206.
"""
GENERIC_FK = ('GenericForeignKey', 'RestrictedGenericForeignKey')
# Not columns: nothing here has a NOT NULL to violate. A ManyToManyField is a through table,
# and Django ignores null= on it entirely; a GenericRelation is a reverse accessor.
NOT_A_COLUMN = ('GenericRelation', 'ManyToManyField')
# A CounterCacheField *is* a column, but its `default=0` and `editable=False` are set inside the
# field class rather than at the declaration, so the AST sees neither and all 35 came out REQ.
NEVER_REQ = ('CounterCacheField',)
# Columns that hold a reference and nothing else -- no empty value to fall back on, so
# `blank=True` (a form-level flag) does not make one optional.
RELATIONS = ('ForeignKey', 'OneToOneField', 'TreeForeignKey')


def sql_required(f, by_name, seen=()):
    """Whether the column is NOT NULL with no default -- the SQL half of required-on-create."""
    if f['type'] in NOT_A_COLUMN or f['type'] in NEVER_REQ or f.get('not_a_column'):
        return False
    if f['type'] in GENERIC_FK:
        # A GenericForeignKey is an accessor over a (ct_field, fk_field) pair, not a column:
        # its requiredness is the content-type half's.
        ct = by_name.get(f.get('ct_field'))
        if ct is None or ct['name'] in seen:
            return False
        return sql_required(ct, by_name, seen + (f['name'],))
    if f['type'] in RELATIONS:
        return not (f.get('null') or 'default' in f)
    return not (f.get('null') or f.get('blank') or 'default' in f)
