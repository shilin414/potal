import pytest
from rest_framework.test import APIRequestFactory

from apps.identity.authentication import StudioSessionAuthentication
from apps.identity.services import (
    load_session,
    load_state,
    make_state,
    sign_session,
    upsert_feishu_user,
)
from apps.users.models import User


@pytest.mark.django_db
def test_studio_session_is_separate_signed_identity_token():
    user = User.objects.create_user(username='feishu-user')
    token = sign_session(user.pk)

    assert load_session(token) == str(user.pk)
    assert 'uat' not in token.lower()

    request = APIRequestFactory().get('/api/v2/runs')
    request.COOKIES['studio_session'] = token
    authenticated, auth = StudioSessionAuthentication().authenticate(request)
    assert authenticated.pk == user.pk
    assert auth is None


def test_oauth_state_is_signed_and_rejects_tampering():
    state = make_state('/chat/sales')

    assert load_state(state) == {'return_to': '/chat/sales'}
    assert load_state(state + 'tampered') is None


@pytest.mark.django_db
def test_upsert_feishu_user_sets_auth_source_and_identity():
    user, identity = upsert_feishu_user({
        'open_id': 'ou_test',
        'user_id': 'user_test',
        'name': '飞书用户',
        'avatar_url': 'https://example.test/avatar.png',
    }, {
        'refresh_token': 'refresh-secret',
        'refresh_token_expires_in': 3600,
    })

    user.refresh_from_db()
    assert user.auth_source == User.AuthSource.FEISHU
    assert user.has_usable_password() is False
    assert user.avatar == 'https://example.test/avatar.png'
    assert identity.open_id == 'ou_test'
    assert identity.refresh_token == 'refresh-secret'
