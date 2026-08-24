"""Fixture shared bases, shaped like netbox/netbox/models/__init__.py. See ../../README.md.

These are the abstract models nearly every NetBox kind inherits its columns from. The
extractor used to skip this package entirely, so those columns appeared nowhere (NBO-070).
"""
from django.db import models
from mptt.managers import TreeManager
from mptt.models import MPTTModel, TreeForeignKey

from netbox.models.features import CustomFieldsMixin, TagsMixin

# A constant local to the base module: a merged column must arrive with its length already
# resolved, not with the symbol.
BASE_NAME_MAX_LENGTH = 100


class ChangeLoggedModel(models.Model):
    created = models.DateTimeField(auto_now_add=True, blank=True, null=True)
    last_updated = models.DateTimeField(auto_now=True, blank=True, null=True)

    class Meta:
        abstract = True


class NetBoxModel(CustomFieldsMixin, TagsMixin, ChangeLoggedModel):
    """Declares nothing itself: it exists to prove attribution survives two hops."""

    class Meta:
        abstract = True


class PrimaryModel(NetBoxModel):
    description = models.CharField(
        max_length=200,
        blank=True,
    )
    comments = models.TextField(
        blank=True,
    )

    class Meta:
        abstract = True


class OrganizationalModel(NetBoxModel):
    name = models.CharField(
        max_length=BASE_NAME_MAX_LENGTH,
        unique=True,
    )
    slug = models.SlugField(
        max_length=BASE_NAME_MAX_LENGTH,
        unique=True,
    )
    description = models.CharField(
        max_length=200,
        blank=True,
    )

    class Meta:
        abstract = True


class NestedGroupModel(CustomFieldsMixin, TagsMixin, ChangeLoggedModel, MPTTModel):
    # mptt's TreeForeignKey, which is the only way `parent` is ever declared in NetBox.
    parent = TreeForeignKey(
        to='self',
        on_delete=models.CASCADE,
        related_name='children',
        blank=True,
        null=True,
        db_index=True,
    )
    name = models.CharField(
        max_length=BASE_NAME_MAX_LENGTH,
    )
    slug = models.SlugField(
        max_length=BASE_NAME_MAX_LENGTH,
    )
    description = models.CharField(
        max_length=200,
        blank=True,
    )

    # A manager, not a field: a queryset accessor is no part of the REST API, and must not be
    # emitted the way `tags` is just because it is a call assigned in a class body.
    objects = TreeManager()

    class Meta:
        abstract = True
