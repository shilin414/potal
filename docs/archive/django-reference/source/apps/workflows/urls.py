from rest_framework.routers import DefaultRouter

from .views import WorkflowRunViewSet, WorkflowViewSet


router = DefaultRouter()
router.register('runs', WorkflowRunViewSet, basename='workflow-run')
router.register('', WorkflowViewSet, basename='workflow')

urlpatterns = router.urls
