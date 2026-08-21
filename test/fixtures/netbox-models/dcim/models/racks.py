"""Fixture models, shaped like netbox/dcim/models/*.py. See ../../README.md."""
from django.db import models

from dcim.models.mixins import WeightMixin
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
