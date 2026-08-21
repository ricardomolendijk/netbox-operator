"""Fixture that must FAIL the run. See ../../../netbox-models/README.md.

Half of a same-named pair: `dcim.Rack` is declared here and again in `racks_legacy.py`.
`if key in out and not fields: continue` kept whichever file the glob reached first and
dropped the other class's entire field list, silently, in either direction.
"""
from django.db import models

from netbox.models import PrimaryModel


class Rack(PrimaryModel):
    name = models.CharField(
        max_length=100,
    )
