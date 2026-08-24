"""Fixture filter classes, shaped like netbox/utilities/filters.py. See ../README.md.

Only the class *names and bases* matter here: which lookup map a filter gets is decided by an
isinstance chain in netbox/filtersets.py, so a subclass has to be recognised as the base the
chain names -- MultiValueContentTypeFilter is a MultiValueCharFilter and takes the char map.
"""
import django_filters


class MultiValueCharFilter(django_filters.MultipleChoiceFilter):
    pass


class MultiValueNumberFilter(django_filters.MultipleChoiceFilter):
    pass


class MultiValueContentTypeFilter(MultiValueCharFilter):
    pass


class TreeNodeMultipleChoiceFilter(django_filters.ModelMultipleChoiceFilter):
    pass
