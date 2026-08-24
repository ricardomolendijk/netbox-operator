"""Fixture choice sets, shaped like netbox/<app>/choices.py. See ../README.md.

The AST walk over the models records `choices=PrefixStatusChoices` and
`default=UNRESOLVED:PrefixStatusChoices.STATUS_ACTIVE` and stops there. The members live here,
and six agents have read them by hand out of the real files -- which is the gap NBO-041 closes.
"""
from django.utils.translation import gettext_lazy as _

from utilities.choices import ChoiceSet

# A module-level constant a member is written as, resolved before the class bodies are read.
STATUS_CODES = {'reserved': 'reserved'}


class PrefixStatusChoices(ChoiceSet):
    """`key` is the whole point: a deployment's FIELD_CHOICES can *replace or extend* this set
    (utilities/choices.py, ChoiceSetMeta.__new__), so the members are a default and not a closed
    one. A CRD that pins them as an enum rejects a value that deployment considers legal."""
    key = 'Prefix.status'

    STATUS_CONTAINER = 'container'
    STATUS_ACTIVE = 'active'
    STATUS_RESERVED = STATUS_CODES['reserved']

    CHOICES = [
        (STATUS_CONTAINER, _('Container'), 'gray'),
        (STATUS_ACTIVE, _('Active'), 'blue'),
        (STATUS_RESERVED, _('Reserved'), 'cyan'),
    ]


class VLANWidthChoices(ChoiceSet):
    """No `key`: no deployment can change these, so a closed CRD enum is safe. The values are
    integers built by arithmetic on class constants, which `ast.literal_eval` will not take, and
    one label is built at import time and so is not a literal at all."""

    WIDTH_HOUR = 60
    WIDTH_DAY = 60 * 24

    CHOICES = (
        (WIDTH_HOUR, _('One hour')),
        (WIDTH_HOUR * 12, _('Twelve hours')),
        (WIDTH_DAY, _('{n} minutes').format(n=1440)),
    )


class RoleKindChoices(ChoiceSet):
    """A grouped set: NetBox writes ('Group label', ((value, label), ...)). A CRD enum has no
    notion of an optgroup, so the members are flattened -- and the fact recorded."""

    CHOICES = (
        ('Physical', (
            ('copper', _('Copper')),
            ('fibre', _('Fibre')),
        )),
        ('Virtual', (
            ('virtual', _('Virtual')),
        )),
    )


class ExtendedRoleKindChoices(RoleKindChoices):
    """Splices another set in with a star-unpack, and the set it names may be declared either
    side of it in the file -- so the splice is resolved in a second pass."""

    KIND_BRIDGE = 'bridge'

    CHOICES = (
        *RoleKindChoices.CHOICES,
        (KIND_BRIDGE, _('Bridge')),
    )
