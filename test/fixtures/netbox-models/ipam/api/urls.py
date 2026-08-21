"""Fixture router registrations, shaped like netbox/<app>/api/urls.py. See ../../README.md."""
from netbox.api.routers import NetBoxRouter

from . import views

router = NetBoxRouter()
router.register('vlans', views.VLANViewSet)
router.register('vlan-groups', views.VLANGroupViewSet)
router.register('vrfs', views.VRFViewSet)
router.register('route-targets', views.RouteTargetViewSet)
router.register('prefixes', views.PrefixViewSet)

urlpatterns = router.urls
