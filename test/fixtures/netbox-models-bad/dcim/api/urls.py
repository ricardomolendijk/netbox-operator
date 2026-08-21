"""Fixture that must FAIL the run. See ../../../netbox-models/README.md.

A `router.register` the endpoint extractor cannot parse: the prefix is a name, not a string
literal. The old single-quote-only regex skipped it without a word, and a missing endpoint row
means dcim.Rack never gets a CRD at all.
"""
from netbox.api.routers import NetBoxRouter

from . import views

RACKS = 'racks'

router = NetBoxRouter()
router.register(RACKS, views.RackViewSet)

urlpatterns = router.urls
