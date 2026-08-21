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
