"""Fixture app-local mixin, shaped like netbox/dcim/models/mixins.py. See ../../README.md.

This module was always inside the extractor's app globs: WeightMixin's columns were
extracted, just never merged into DeviceType.
"""
from django.db import models


class WeightMixin(models.Model):
    weight = models.DecimalField(
        max_digits=8,
        decimal_places=2,
        blank=True,
        null=True,
    )
    weight_unit = models.CharField(
        max_length=50,
        blank=True,
    )

    class Meta:
        abstract = True


class ComponentModel(models.Model):
    """NBO-041: a base class name declared in **two** apps (also ipam/models/mixins.py).

    Resolving a base by bare class name found the name twice, gave up, and dropped every
    column both versions declare -- which in real NetBox 4.6.8 cost eleven shipped component
    Kinds (`dcim.Interface`, `dcim.ConsolePort`, ...) their `name`, `label` and `description`.
    A base must resolve within the declaring model's own app first.
    """
    name = models.CharField(max_length=64)
    label = models.CharField(max_length=64, blank=True)

    class Meta:
        abstract = True
