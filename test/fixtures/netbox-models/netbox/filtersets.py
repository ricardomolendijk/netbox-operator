"""Fixture base filtersets, shaped like netbox/netbox/filtersets.py. See ../README.md.

Three things are read out of this file, and each of them decides which query parameters a Kind
accepts -- so a hardcoded copy of any of them drifts into #206:

  FILTER_DEFAULTS           which filter class a bare `Meta.fields` entry gets, per column type
  _get_filter_lookup_dict   which lookup map that filter class then takes -- an *ordered*
                            if-chain, so MultiValueNumberFilter matches numeric before char
  STANDARD_LOOKUPS          a filter with a non-standard lookup_expr gets no suffixes at all
"""
import django_filters
from django.db import models

from utilities import filters
from utilities.constants import (
    FILTER_CHAR_BASED_LOOKUP_MAP,
    FILTER_NEGATION_LOOKUP_MAP,
    FILTER_NUMERIC_BASED_LOOKUP_MAP,
    FILTER_TREENODE_NEGATION_LOOKUP_MAP,
)

STANDARD_LOOKUPS = (
    'exact',
    'iexact',
    'in',
    'contains',
)


class BaseFilterSet(django_filters.FilterSet):
    FILTER_DEFAULTS = {}
    FILTER_DEFAULTS.update({
        models.AutoField: {
            'filter_class': filters.MultiValueNumberFilter
        },
        models.CharField: {
            'filter_class': filters.MultiValueCharFilter
        },
        models.SlugField: {
            'filter_class': filters.MultiValueCharFilter
        },
        models.PositiveSmallIntegerField: {
            'filter_class': filters.MultiValueNumberFilter
        },
    })

    @staticmethod
    def _get_filter_lookup_dict(existing_filter):
        if isinstance(existing_filter, (
            django_filters.NumberFilter,
            filters.MultiValueNumberFilter,
        )):
            return FILTER_NUMERIC_BASED_LOOKUP_MAP

        if isinstance(existing_filter, (
            filters.TreeNodeMultipleChoiceFilter,
        )):
            return FILTER_TREENODE_NEGATION_LOOKUP_MAP

        if isinstance(existing_filter, (
            django_filters.ModelChoiceFilter,
            django_filters.ModelMultipleChoiceFilter,
        )):
            # A foreign-key filter supports negation and nothing else: no `empty`, so a null pin
            # on an FK is not expressible as a query parameter at all (#206).
            return FILTER_NEGATION_LOOKUP_MAP

        if isinstance(existing_filter, (
            django_filters.filters.CharFilter,
            filters.MultiValueCharFilter,
        )):
            return FILTER_CHAR_BASED_LOOKUP_MAP

        return None


class NetBoxModelFilterSet(BaseFilterSet):
    """Adds `q`, and -- at runtime, from the database -- a `cf_<name>` per custom field, which no
    static walk can enumerate. Recorded as a dynamic filter rather than pretended to be absent."""
    q = django_filters.CharFilter(
        method='search',
    )


class OrganizationalModelFilterSet(NetBoxModelFilterSet):
    pass


class PrimaryModelFilterSet(NetBoxModelFilterSet):
    pass
