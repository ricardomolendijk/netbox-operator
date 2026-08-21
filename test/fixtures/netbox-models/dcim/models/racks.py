"""Fixture models, shaped like netbox/dcim/models/*.py. See ../../README.md."""
from django.contrib.auth.models import User
from django.db import models

from dcim.choices import RackAirflow, RackStatus
from dcim.fields import RackUnitField
from dcim.models.mixins import WeightMixin
from dcim.models.sites import Site
from netbox.models import OrganizationalModel, PrimaryModel
from utilities.fields import ColorField


class RackRole(OrganizationalModel):
    """Declares one column. name, slug and description are inherited — the shape of the
    entry that listed `color` and nothing else."""
    color = ColorField()


class DeviceType(PrimaryModel, WeightMixin):
    """Two bases: one abstract model, one app-local mixin whose columns were extracted but
    never merged."""
    manufacturer = models.ForeignKey(
        to='dcim.Manufacturer',
        on_delete=models.PROTECT,
        related_name='device_types',
    )
    model = models.CharField(
        max_length=100,
    )
    slug = models.SlugField(
        max_length=100,
    )

    class Meta:
        ordering = ('manufacturer', 'model')
        constraints = (
            models.UniqueConstraint(
                fields=('manufacturer', 'model'),
                name='%(app_label)s_%(class)s_unique_manufacturer_model'
            ),
        )


class Rack(PrimaryModel):
    """Six NBO-071 defects at once. Registered by a double-quoted `router.register`, which the
    single-quote-only regex skipped without a word."""
    # A bare class reference, passed positionally: 13 rows of the real schema read `-> Site`,
    # leaving the app to be guessed by anything mapping a target to a Kind.
    site = models.ForeignKey(
        Site,
        on_delete=models.PROTECT,
        related_name='racks',
    )
    # Bare again, and `blank` with no `null`: the column is NOT NULL, so this is required
    # however form-level-optional it is.
    role = models.ForeignKey(
        to=RackRole,
        on_delete=models.PROTECT,
        related_name='racks',
        blank=True,
    )
    # A ManyToManyField has no NOT NULL column, and Django ignores null= on it, so it can
    # never be REQ. `to` and `related_name` are both positional here: Django's M2M signature
    # has no on_delete, and reading args[1] as one invents a contract that does not exist.
    tagged_vlans = models.ManyToManyField('ipam.VLAN', 'tagged_racks')
    # A DecimalField's precision is the whole of its contract.
    position = models.DecimalField(
        max_digits=4,
        decimal_places=1,
        blank=True,
        null=True,
    )
    # A symbolic default: not the string 'RackStatus.ACTIVE', which is what quoting it says.
    status = models.CharField(
        max_length=50,
        default=RackStatus.ACTIVE,
    )
    # ...next to a real string literal, and a symbolic `choices` the digest already prints
    # unquoted.
    airflow = models.CharField(
        max_length=50,
        choices=RackAirflow,
        default='front-to-rear',
        blank=True,
    )
    # A field class the extractor's whitelist does not know. The column is still dropped --
    # but the run now says which one, instead of losing it in silence.
    outer_unit = RackUnitField(
        max_length=10,
        blank=True,
    )

    # A nested class whose base name contains "Model": `ast.walk` reached it and made a
    # phantom `dcim.Meta` entry out of it, which the second one below then collapsed into.
    class Meta(PrimaryModel.Meta):
        ordering = ('site', 'position')


class RackReservation(PrimaryModel):
    """Registered by a re-exported viewset -- `router.register("rack-reservations",
    RackReservationViewSet)`, with no `views.` prefix, which the old regex demanded."""
    rack = models.ForeignKey(
        Rack,
        on_delete=models.CASCADE,
        related_name='reservations',
    )
    # A bare reference to a class from outside the ten scanned apps (django.contrib.auth's
    # User is the real one). There is no app to qualify it with, so it is flagged rather than
    # mislabelled `dcim.User`.
    user = models.ForeignKey(
        User,
        on_delete=models.PROTECT,
        related_name='rack_reservations',
    )

    class Meta(PrimaryModel.Meta):
        ordering = ('created',)
