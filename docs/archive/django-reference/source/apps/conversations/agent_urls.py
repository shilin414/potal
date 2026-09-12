"""Routes for the Codex-shaped Agent API v2."""
from rest_framework.routers import DefaultRouter

from .agent_views import (
    AgentServerRequestViewSet,
    AgentThreadViewSet,
    AgentTurnViewSet,
)

router = DefaultRouter()
router.register('threads', AgentThreadViewSet, basename='agent-thread')
router.register('turns', AgentTurnViewSet, basename='agent-turn')
router.register(
    'server-requests', AgentServerRequestViewSet, basename='agent-server-request'
)

urlpatterns = router.urls
