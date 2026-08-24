"""Fixture serializers, shaped like netbox/<app>/api/serializers.py. See ../README.md.

`Meta.fields` is the write path -- what the operator POSTs to. A column absent from it cannot be
set however NOT NULL it is in Postgres, and a `read_only=True` field written anyway is silently
dropped. Neither fact exists anywhere in the ORM.
"""
from django.contrib.contenttypes.models import ContentType
from rest_framework import serializers

from ipam.choices import PrefixStatusChoices
from ipam.constants import PREFIX_SCOPE_TYPES
from ipam.models.vlans import VLANGroup, VRF, Prefix
from netbox.api.fields import ChoiceField, ContentTypeField
from netbox.api.serializers import PrimaryModelSerializer


class VRFSerializer(PrimaryModelSerializer):
    class Meta:
        model = VRF
        fields = ['id', 'url', 'display', 'name', 'rd', 'enforce_unique', 'description', 'tags']
        brief_fields = ('id', 'url', 'display', 'name', 'rd')


class PrefixSerializer(PrimaryModelSerializer):
    # Read-only over REST, and a column all the same: writing it is accepted and does nothing.
    family = ChoiceField(choices=PrefixStatusChoices, read_only=True)
    # The only place the legal generic-FK targets are written down, and they are written as bare
    # model names -- the `app_label.model` spelling is resolved against models.json.
    scope_type = ContentTypeField(
        queryset=ContentType.objects.filter(
            model__in=PREFIX_SCOPE_TYPES
        ),
        allow_null=True,
        required=False,
        default=None,
    )
    scope = serializers.SerializerMethodField(read_only=True)
    status = ChoiceField(choices=PrefixStatusChoices, required=False)
    vrf = VRFSerializer(nested=True, required=False, allow_null=True)

    class Meta:
        model = Prefix
        # `scope_id` is deliberately absent: a NOT NULL-ish column that is not on the write path
        # is a conflict to record, not a field to generate.
        fields = ['id', 'url', 'display', 'prefix', 'vrf', 'scope_type', 'scope', 'status', 'tags']
        brief_fields = ('id', 'url', 'display', 'prefix')


class VLANGroupSerializer(PrimaryModelSerializer):
    scope_type = ContentTypeField(
        queryset=ContentType.objects.filter(
            model__in=PREFIX_SCOPE_TYPES
        ),
        allow_null=True,
        required=False,
    )

    class Meta:
        model = VLANGroup
        fields = ['id', 'url', 'display', 'name', 'slug', 'scope_type', 'scope_id', 'tags']
        read_only_fields = fields
