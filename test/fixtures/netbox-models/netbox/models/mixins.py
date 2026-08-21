"""Fixture scope mixin, shaped like netbox/netbox/models/mixins.py. See ../../README.md."""
from django.contrib.contenttypes.fields import GenericForeignKey
from django.db import models


class CachedScopeMixin(models.Model):
    """A generic-FK pair plus its accessor, all inherited: requiredness has to be derived
    across the merge, not just within one class body."""
    scope_type = models.ForeignKey(
        to='contenttypes.ContentType',
        on_delete=models.PROTECT,
        blank=True,
        null=True,
    )
    scope_id = models.PositiveBigIntegerField(
        blank=True,
        null=True,
    )
    scope = GenericForeignKey(
        ct_field='scope_type',
        fk_field='scope_id',
    )

    class Meta:
        abstract = True
