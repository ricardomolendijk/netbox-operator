"""Fixture filter lookup maps, shaped like netbox/utilities/constants.py. See ../README.md.

These are the *parameter suffixes* NetBox registers, mapped to the ORM lookups they compile to.
The two are not interchangeable, and the operator emitting one where NetBox registers the other
is #206: `empty` is the suffix, `isnull` is what it means on a numeric filter -- and on a char
filter `empty` means string emptiness instead. Read out of the source by
hack/extract-netbox-api-schema.py rather than copied, because a stale copy is the bug.
"""

FILTER_CHAR_BASED_LOOKUP_MAP = dict(
    n='exact',
    ic='icontains',
    ie='iexact',
    nie='iexact',
    empty='empty',
)

FILTER_NUMERIC_BASED_LOOKUP_MAP = dict(
    n='exact',
    lte='lte',
    gte='gte',
    empty='isnull',
)

FILTER_NEGATION_LOOKUP_MAP = dict(
    n='exact'
)

FILTER_TAG_LOOKUP_MAP = dict(
    n='exact',
    any='exact',
)

FILTER_TREENODE_NEGATION_LOOKUP_MAP = dict(
    n='in'
)
