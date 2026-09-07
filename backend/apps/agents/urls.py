from django.urls import path, include
from rest_framework.routers import DefaultRouter
from .views import AgentCategoryViewSet, AgentViewSet

router = DefaultRouter()
router.register(r'categories', AgentCategoryViewSet, basename='agent_category')
router.register(r'', AgentViewSet, basename='agent')

urlpatterns = [
    path('', include(router.urls)),
]