"""Fixture app-local mixins, shaped like netbox/<app>/models/mixins.py. See ../../README.md."""
from django.db import models


class ComponentModel(models.Model):
    """The ipam half of the two-apps-one-base-name pair (see dcim/models/mixins.py).

    It declares a *different* column from dcim's, so a subclass that inherited the wrong app's
    version would be visibly wrong rather than merely incomplete.
    """
    component_kind = models.CharField(max_length=16)

    class Meta:
        abstract = True


class TenancyMixin(models.Model):
    """Declared here and in netbox/models/features.py, and in neither of dcim's apps."""
    ambiguous_tenant = models.CharField(max_length=8, blank=True)

    class Meta:
        abstract = True
