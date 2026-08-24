"""Fixture feature mixins, shaped like netbox/netbox/models/features.py. See ../../README.md."""
from django.db import models
from extras.managers import NetBoxTaggableManagerField
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
    """No column of its own: tags live in a through table, so no *column* must be merged —
    but `tags` is a writable REST field on every kind that inherits this, so the entry has to
    show it, marked as the M2M-through-a-through-table it is.

    Declared with the subclass real NetBox uses (extras.managers.NetBoxTaggableManagerField),
    not taggit's base class. The fixture used the base name for a while and so pinned nothing:
    the extractor matched on that name, no real model uses it, and `tags` was dropped from all
    92 taggable models with only a stderr warning to show for it."""
    tags = NetBoxTaggableManagerField(
        through='extras.TaggedItem',
        related_name='%(app_label)s_%(class)s_related',
    )

    class Meta:
        abstract = True


class LegacyTagsMixin(models.Model):
    """taggit's base TaggableManager, which no NetBox 4.6 model declares directly -- a plugin
    or an older release can. Here so the base-class entry in MANAGER_TARGETS is exercised by
    something; an unexercised entry is how the subclass came to be missing in the first place."""
    tags = TaggableManager(
        through='extras.TaggedItem',
    )

    class Meta:
        abstract = True
