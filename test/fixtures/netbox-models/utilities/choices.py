"""Fixture ChoiceSet base, shaped like netbox/utilities/choices.py. See ../README.md.

`CHOICES = list()` is a legitimately empty set on the abstract base, not a parse failure:
warning about it would be crying wolf on the one class that has nothing to declare.
"""


class ChoiceSetMeta(type):
    """A metaclass, not a choice set. Its only base is `type`."""


class ChoiceSet(metaclass=ChoiceSetMeta):
    CHOICES = list()
