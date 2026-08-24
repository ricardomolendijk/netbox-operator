"""Fixture filtersets, shaped like netbox/<app>/filtersets.py. See ../README.md.

A natural key is a *query*, and django-filter silently ignores a query parameter it does not
register: the filter is dropped and the request returns the *unfiltered* result set, so the
engine adopts the wrong object (#206). Every declaration here pins one rule about which
parameters actually exist.
"""
import django_filters

from ipam.models.vlans import VLAN, VLANGroup, VRF, Prefix
from netbox.filtersets import OrganizationalModelFilterSet, PrimaryModelFilterSet
from utilities.filters import MultiValueCharFilter, MultiValueContentTypeFilter


class PrefixFilterSet(PrimaryModelFilterSet):
    # A declared filter with a `method=`: NetBox adds **no** lookup suffixes to it at all, so
    # `?prefix__ie=` and `?prefix__empty=` do not exist however char-shaped the column is.
    prefix = MultiValueCharFilter(
        method='filter_prefix',
    )
    # An FK filter. FILTER_NEGATION_LOOKUP_MAP registers `n` and nothing else -- so neither
    # `vrf_id__isnull` (what the operator emits) nor `vrf_id__empty` (#206's proposed fix)
    # is a parameter NetBox knows.
    vrf_id = django_filters.ModelMultipleChoiceFilter(
        queryset=VRF.objects.all(),
    )
    # A declared filter with a non-standard lookup_expr also gets no suffixes.
    mask_length = django_filters.NumberFilter(
        field_name='prefix',
        lookup_expr='net_mask_length',
    )

    class Meta:
        model = Prefix
        # `scope_id` is a numeric column, so `scope_id__empty` exists and means SQL NULL --
        # the one shape of null pin that is expressible.
        fields = ('id', 'scope_id')


class VRFFilterSet(PrimaryModelFilterSet):
    class Meta:
        model = VRF
        # Char columns: `rd__empty` exists but asks about *string emptiness*, not SQL NULL.
        # Not the same question, and the difference is invisible in the response.
        fields = ('id', 'name', 'rd', 'enforce_unique')


class VLANGroupFilterSet(OrganizationalModelFilterSet):
    # A generic-FK type filter takes `app_label.model` strings, not IDs (#35).
    scope_type = MultiValueContentTypeFilter()

    class Meta:
        model = VLANGroup
        fields = ('id', 'name', 'slug', 'scope_id')


class VLANFilterSet(PrimaryModelFilterSet):
    group_id = django_filters.ModelMultipleChoiceFilter(
        queryset=VLANGroup.objects.all(),
    )
    qinq_svlan_id = django_filters.ModelMultipleChoiceFilter(
        queryset=VLAN.objects.all(),
    )
    site_id = django_filters.ModelMultipleChoiceFilter(
        queryset=VLANGroup.objects.all(),
    )

    class Meta:
        model = VLAN
        fields = ('id', 'name', 'vid', 'description')
