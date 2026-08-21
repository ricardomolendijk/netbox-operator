"""Fixture models, shaped like netbox/ipam/models/*.py. See ../../README.md."""
from django.contrib.contenttypes.fields import GenericForeignKey, GenericRelation
from django.db import models
from django.db.models import Q
from django.utils.translation import gettext_lazy as _

from ipam.constants import AMBIGUOUS_MAX_LENGTH, VRF_RD_MAX_LENGTH
from netbox.models import PrimaryModel

# A constant defined in the model module rather than in constants.py.
LOCAL_NAME_MAX_LENGTH = 33


class VLAN(PrimaryModel):
    site = models.ForeignKey(
        to='dcim.Site',
        on_delete=models.PROTECT,
        related_name='vlans',
        blank=True,
        null=True,
    )
    group = models.ForeignKey(
        to='ipam.VLANGroup',
        on_delete=models.CASCADE,
        related_name='vlans',
        blank=True,
        null=True,
    )
    vid = models.PositiveSmallIntegerField(
        verbose_name=_('VLAN ID'),
    )
    name = models.CharField(
        max_length=LOCAL_NAME_MAX_LENGTH,
    )
    role = models.ForeignKey(
        to='ipam.Role',
        on_delete=models.SET_NULL,
        related_name='vlans',
        blank=True,
        null=True,
    )
    # A symbol the AST walk cannot resolve: same name, two different values.
    description = models.CharField(
        max_length=AMBIGUOUS_MAX_LENGTH,
        blank=True,
    )
    # Reverse generic relation. Not a column, takes no null=, and an unterminated VLAN is
    # entirely normal.
    l2vpn_terminations = GenericRelation(
        to='vpn.L2VPNTermination',
        content_type_field='assigned_object_type',
        object_id_field='assigned_object_id',
        related_query_name='vlan',
    )

    class Meta:
        ordering = ('site', 'group', 'vid', 'pk')
        constraints = (
            models.UniqueConstraint(
                fields=('group', 'vid'),
                name='%(app_label)s_%(class)s_unique_group_vid'
            ),
            models.UniqueConstraint(
                fields=('group', 'name'),
                name='%(app_label)s_%(class)s_unique_group_name'
            ),
            models.UniqueConstraint(
                fields=('qinq_svlan', 'vid'),
                name='%(app_label)s_%(class)s_unique_qinq_svlan_vid'
            ),
            models.UniqueConstraint(
                fields=('qinq_svlan', 'name'),
                name='%(app_label)s_%(class)s_unique_qinq_svlan_name'
            ),
            models.UniqueConstraint(
                fields=('site', 'vid'),
                name='%(app_label)s_%(class)s_unique_site_vid',
                condition=Q(group__isnull=True),
                violation_error_message=_('A VLAN with this VID already exists in the specified site.')
            ),
        )
        indexes = (models.Index(fields=('site', 'group', 'vid', 'id')),)


class VRF(PrimaryModel):
    name = models.CharField(
        max_length=LOCAL_NAME_MAX_LENGTH,
    )
    rd = models.CharField(
        max_length=VRF_RD_MAX_LENGTH,
        unique=True,
        blank=True,
        null=True,
        verbose_name=_('route distinguisher'),
    )
    enforce_unique = models.BooleanField(
        default=True,
    )


class RouteTarget(PrimaryModel):
    name = models.CharField(
        max_length=VRF_RD_MAX_LENGTH,
        unique=True,
    )
    # on_delete passed positionally, which Django accepts.
    tenant = models.ForeignKey('tenancy.Tenant', models.SET_DEFAULT, default=None, blank=True, null=True)


class Prefix(PrimaryModel):
    prefix = models.CharField(
        max_length=43,
    )
    vrf = models.ForeignKey(
        to='ipam.VRF',
        on_delete=models.PROTECT,
        related_name='prefixes',
        blank=True,
        null=True,
        verbose_name=_('VRF'),
    )
    # The scope half of a CachedScopeMixin-style generic FK: an unscoped (global) prefix is
    # legal, and that is visible on scope_type, not on scope.
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


class PrefixAttachment(PrimaryModel):
    """A generic FK whose content-type half *is* required — proves REQ is derived, not just suppressed."""
    object_type = models.ForeignKey(
        to='contenttypes.ContentType',
        on_delete=models.CASCADE,
    )
    object_id = models.PositiveBigIntegerField()
    parent = GenericForeignKey('object_type', 'object_id')
