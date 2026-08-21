"""Fixture feature mixins, shaped like netbox/netbox/models/features.py. See ../../README.md."""
from django.db import models
from taggit.managers import TaggableManager


class CustomFieldsMixin(models.Model):
    """One real column, reached only through NetBoxModel — two inheritance hops."""
    custom_field_data = models.JSONField(
        default=dict,
        blank=True,
    )

    class Meta:
        abstract = True


class TagsMixin(models.Model):
    """No column of its own: tags live in a through table, so nothing must be merged."""
    tags = TaggableManager(
        through='extras.TaggedItem',
        related_name='%(app_label)s_%(class)s_related',
    )

    class Meta:
        abstract = True
