"""Fixture that must FAIL the run. See ../../../netbox-models/README.md.

The other half of the same-named pair: a different `dcim.Rack`, with a different field list.
Whichever of the two survived, the entry would describe a model that does not exist.
"""
from django.db import models

from netbox.models import PrimaryModel


class Rack(PrimaryModel):
    asset_tag = models.CharField(
        max_length=50,
        unique=True,
    )
