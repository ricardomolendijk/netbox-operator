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

urlpatterns = router.urls
