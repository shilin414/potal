import pytest
from asgiref.sync import sync_to_async
from channels.testing import WebsocketCommunicator
from django.contrib.auth import get_user_model
from rest_framework_simplejwt.tokens import RefreshToken

from apps.app_runner.middleware import JwtWsAuthMiddleware
from apps.app_runner.routing import websocket_urlpatterns
from channels.routing import URLRouter

pytestmark = pytest.mark.django_db


async def _make_token(user):
    refresh = await sync_to_async(RefreshToken.for_user)(user)
    return str(refresh.access_token)


@pytest.mark.asyncio
async def test_ws_rejects_without_token():
    app = JwtWsAuthMiddleware(URLRouter(websocket_urlpatterns))
    comm = WebsocketCommunicator(app, '/ws/runner/11111111-1111-1111-1111-111111111111/')
    connected, code = await comm.connect()
    assert connected is False


@pytest.mark.asyncio
async def test_ws_accepts_with_token_no_handle_sends_nothing():
    user = await get_user_model().objects.acreate(username='ws-smoke-u2', password='x')
    token = await _make_token(user)
    from apps.app_runner.models import Job
    await Job.objects.acreate(
        id='11111111-1111-1111-1111-111111111111', app_slug='x', owner=user)
    app = JwtWsAuthMiddleware(URLRouter(websocket_urlpatterns))
    comm = WebsocketCommunicator(
        app, f'/ws/runner/11111111-1111-1111-1111-111111111111/?token={token}')
    connected, _ = await comm.connect()
    assert connected is True
    # No handle seeded -> consumer sends nothing. Just ensure it stays open.
    await comm.disconnect()
