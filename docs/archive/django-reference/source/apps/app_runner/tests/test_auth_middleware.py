import pytest
from asgiref.sync import sync_to_async
from django.contrib.auth import get_user_model
from rest_framework_simplejwt.tokens import RefreshToken

from apps.app_runner.middleware import JwtWsAuthMiddleware

pytestmark = pytest.mark.django_db


class _Inner:
    def __init__(self):
        self.received = None

    async def __call__(self, scope, receive, send):
        self.received = scope


# RefreshToken.for_user() does a synchronous User.save() (UPDATE_LAST_LOGIN),
# which is not allowed in an event loop — generate the token in a worker thread.
def _make_token(user):
    return str(RefreshToken.for_user(user).access_token)


@pytest.mark.asyncio
async def test_valid_token_sets_user():
    User = get_user_model()
    user = await User.objects.acreate(username='wsuser', password='x')
    token = await sync_to_async(_make_token)(user)

    inner = _Inner()
    mw = JwtWsAuthMiddleware(inner)
    scope = {'query_string': f'token={token}'.encode()}
    await mw(scope, lambda *_: None, lambda *_: None)

    assert inner.received['user'].id == user.id


@pytest.mark.asyncio
async def test_invalid_token_leaves_user_none():
    inner = _Inner()
    mw = JwtWsAuthMiddleware(inner)
    scope = {'query_string': b'token=not-a-jwt'}
    await mw(scope, lambda *_: None, lambda *_: None)
    assert inner.received['user'] is None
