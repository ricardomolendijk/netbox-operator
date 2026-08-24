"""Fixture base serializers, shaped like netbox/netbox/api/serializers/. See ../../../README.md.

DRF accumulates *declared* fields down the MRO; `Meta.fields` it does not -- every NetBox
serializer writes its own -- so `display` reaches every kind and the field list never does.
"""
from rest_framework import serializers


class BaseModelSerializer(serializers.ModelSerializer):
    display = serializers.SerializerMethodField(read_only=True)


class PrimaryModelSerializer(BaseModelSerializer):
    pass
