"""Fixture router registrations, shaped like netbox/<app>/api/urls.py. See ../../README.md."""
from netbox.api.routers import NetBoxRouter

from . import views

router = NetBoxRouter()
router.register('sites', views.SiteViewSet)
router.register('regions', views.RegionViewSet)
router.register('manufacturers', views.ManufacturerViewSet)
router.register('rack-groups', views.RackGroupViewSet)
router.register('rack-roles', views.RackRoleViewSet)
router.register('device-types', views.DeviceTypeViewSet)
# Double-quoted, and a viewset with no `views.` prefix: both were skipped in silence, and a
# missing endpoint row is a Kind with no CRD at all.
router.register("racks", views.RackViewSet)
router.register("rack-reservations", RackReservationViewSet)

urlpatterns = router.urls
