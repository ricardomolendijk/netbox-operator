"""Fixture models, shaped like netbox/dcim/models/*.py. See ../../README.md."""
from django.contrib.contenttypes.fields import GenericRelation
from django.db import models

from netbox.models import NestedGroupModel, OrganizationalModel


class Manufacturer(OrganizationalModel):
    """Column-less: name, slug and description all come from OrganizationalModel."""
    pass


class RackGroup(NestedGroupModel):
    """Column-less: name, slug, parent and description all come from NestedGroupModel."""
    pass


class Site(OrganizationalModel):
    region = models.ForeignKey(
        to='dcim.Region',
        on_delete=models.SET_NULL,
        related_name='sites',
        blank=True,
        null=True,
    )
    facility = models.CharField(
        max_length=50,
        blank=True,
    )
    prefixes = GenericRelation(
        to='ipam.Prefix',
        content_type_field='scope_type',
        object_id_field='scope_id',
        related_query_name='site',
    )
