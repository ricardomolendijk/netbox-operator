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


class Region(NestedGroupModel):
    """Declares only a reverse relation, and its Meta.constraints cites `parent` and `name`
    — two columns it inherits. A constraint naming a column the same entry does not list is
    exactly the contradiction NBO-070 is about."""
    prefixes = GenericRelation(
        to='ipam.Prefix',
        content_type_field='scope_type',
        object_id_field='scope_id',
        related_query_name='region',
    )

    class Meta:
        ordering = ('name',)
        constraints = (
            models.UniqueConstraint(
                fields=('parent', 'name'),
                name='%(app_label)s_%(class)s_unique_parent_name'
            ),
        )


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
