import uuid
import pytest
from asgiref.sync import sync_to_async
from channels.testing import WebsocketCommunicator
from django.contrib.auth import get_user_model
from rest_framework_simplejwt.tokens import RefreshToken

from apps.app_runner import job_manager as jm_mod
from apps.app_runner.middleware import JwtWsAuthMiddleware
from apps.app_runner.routing import websocket_urlpatterns
from apps.app_runner.models import Job
from channels.routing import URLRouter

pytestmark = pytest.mark.django_db


async def _make_token(user):
    # for_user() does a sync DB save (UPDATE_LAST_LOGIN); run it off the loop.
    refresh = await sync_to_async(RefreshToken.for_user)(user)
    return str(refresh.access_token)


@pytest.mark.asyncio
async def test_connect_replays_snapshot_then_live_events(monkeypatch):
    monkeypatch.setattr(jm_mod, '_broadcast', lambda job_id, event: None)

    user = await get_user_model().objects.acreate(username='c1', password='x')
    job_id = str(uuid.uuid4())
    await Job.objects.acreate(id=job_id, app_slug='x', owner=user)

    handle = jm_mod.JobHandle(job_id)
    handle.record({'type': 'job.state', 'status': 'running'})
    handle.record({'type': 'log', 'level': 'info', 'msg': 'already running'})
    monkeypatch.setattr(jm_mod.job_manager, 'get_handle', lambda jid: handle)

    app = JwtWsAuthMiddleware(URLRouter(websocket_urlpatterns))
    token = await _make_token(user)
    comm = WebsocketCommunicator(app, f'/ws/runner/{job_id}/?token={token}')
    connected, _ = await comm.connect()
    assert connected

    snap = await comm.receive_json_from()
    assert snap['type'] == 'state.snapshot'
    assert snap['status'] == 'running'
    assert isinstance(snap['items'], list)
    assert any(l['msg'] == 'already running' for l in snap['logs'])

    await comm.disconnect()


@pytest.mark.asyncio
async def test_client_stop_calls_manager(monkeypatch):
    user = await get_user_model().objects.acreate(username='c2', password='x')
    job_id = str(uuid.uuid4())
    await Job.objects.acreate(id=job_id, app_slug='x', owner=user)
    handle = jm_mod.JobHandle(job_id)
    monkeypatch.setattr(jm_mod.job_manager, 'get_handle', lambda jid: handle)

    called = {}
    monkeypatch.setattr(jm_mod.job_manager, 'stop',
                        lambda jid: called.__setitem__('stopped', jid) or True)

    app = JwtWsAuthMiddleware(URLRouter(websocket_urlpatterns))
    token = await _make_token(user)
    comm = WebsocketCommunicator(app, f'/ws/runner/{job_id}/?token={token}')
    await comm.connect()
    await comm.receive_json_from()  # snapshot
    await comm.send_json_to({'action': 'stop'})
    import asyncio
    await asyncio.sleep(0.05)
    assert called.get('stopped') == job_id
    await comm.disconnect()
