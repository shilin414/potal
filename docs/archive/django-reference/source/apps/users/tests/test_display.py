"""聊天展示身份：姓名（user_id）。

前端把每条消息的发送方渲染成 ``姓名（user_id）``：
``display_name`` 来自飞书昵称/first_name/username，
``display_id`` 来自 User.display_id → FeishuIdentity.feishu_user_id → 本地主键。
"""
import pytest
from django.core.management import call_command
from django.core.management.base import CommandError

from apps.identity.models import FeishuIdentity
from apps.identity.services import upsert_feishu_user
from apps.users.display import display_identity, session_user_payload
from apps.users.models import User
from apps.users.serializers import UserDetailSerializer, UserSerializer


@pytest.mark.django_db
def test_local_user_falls_back_to_username_and_local_pk():
    user = User.objects.create_user(username='demo')

    assert display_identity(user) == {
        'display_name': 'demo',
        'display_id': str(user.pk),
    }


@pytest.mark.django_db
def test_local_user_prefers_first_name_when_present():
    user = User.objects.create_user(username='demo', first_name='吴志彬')

    assert display_identity(user)['display_name'] == '吴志彬'


@pytest.mark.django_db
def test_feishu_user_uses_identity_name_and_feishu_user_id():
    user = User.objects.create_user(username='吴志彬', auth_source=User.AuthSource.FEISHU)
    FeishuIdentity.objects.create(
        user=user, open_id='ou_test', feishu_user_id='19127920',
        display_name='吴志彬', avatar_url='https://example.test/a.png')

    assert display_identity(user) == {
        'display_name': '吴志彬',
        'display_id': '19127920',
    }


@pytest.mark.django_db
def test_feishu_user_without_user_id_falls_back_to_local_pk():
    """飞书 OAuth 未授予通讯录权限时 user_info 不返回 user_id。"""
    user = User.objects.create_user(username='feishu-user',
                                    auth_source=User.AuthSource.FEISHU)
    FeishuIdentity.objects.create(user=user, open_id='ou_no_id',
                                  display_name='飞书用户')

    assert display_identity(user)['display_id'] == str(user.pk)


@pytest.mark.django_db
def test_operator_set_display_id_wins_over_feishu_user_id():
    user = User.objects.create_user(username='吴志彬', display_id='19127920',
                                    auth_source=User.AuthSource.FEISHU)
    FeishuIdentity.objects.create(user=user, open_id='ou_test',
                                  display_name='吴志彬')

    assert display_identity(user)['display_id'] == '19127920'


@pytest.mark.django_db
def test_upsert_feishu_user_seeds_display_id_only_when_empty():
    """登录快照只在展示 ID 为空时写入，运维手工设置的值不会被清掉。"""
    user, _identity = upsert_feishu_user(
        {'open_id': 'ou_a', 'user_id': '19127920', 'name': '吴志彬'},
        {'refresh_token': 'r1'})
    user.refresh_from_db()
    assert user.display_id == '19127920'

    user.display_id = 'A-0001'
    user.save(update_fields=['display_id'])
    upsert_feishu_user(
        {'open_id': 'ou_a', 'user_id': '19127920', 'name': '吴志彬'},
        {'refresh_token': 'r2'})
    user.refresh_from_db()
    assert user.display_id == 'A-0001'


@pytest.mark.django_db
def test_upsert_feishu_user_accepts_employee_no_as_display_id():
    user, _identity = upsert_feishu_user(
        {'open_id': 'ou_b', 'employee_no': 'E10086', 'name': '张三'}, {})
    user.refresh_from_db()

    assert user.display_id == 'E10086'


@pytest.mark.django_db
def test_session_payload_has_one_shape_for_every_login_path():
    user = User.objects.create_user(
        username='吴志彬', first_name='吴志彬', role=User.Role.CREATOR)
    payload = session_user_payload(user)

    # Every login/exchange/session endpoint returns this exact key set.
    assert set(payload) == {
        'id', 'username', 'email', 'role', 'avatar', 'avatar_url',
        'auth_source', 'is_staff', 'display_name', 'display_id',
    }
    assert payload['display_name'] == '吴志彬'
    assert payload['display_id'] == str(user.pk)
    assert payload['avatar_url'] == ''


@pytest.mark.django_db
def test_session_payload_prefers_identity_avatar():
    user = User.objects.create_user(username='吴志彬',
                                    auth_source=User.AuthSource.FEISHU)
    FeishuIdentity.objects.create(
        user=user, open_id='ou_test', feishu_user_id='19127920',
        display_name='吴志彬', avatar_url='https://example.test/avatar.png')

    payload = session_user_payload(user)
    assert payload['avatar_url'] == 'https://example.test/avatar.png'
    assert payload['display_name'] == '吴志彬'
    assert payload['display_id'] == '19127920'


@pytest.mark.django_db
def test_user_serializers_expose_display_identity():
    user = User.objects.create_user(username='吴志彬', first_name='吴志彬',
                                    display_id='19127920')

    assert UserSerializer(user).data['display_id'] == '19127920'
    assert UserSerializer(user).data['display_name'] == '吴志彬'
    assert UserDetailSerializer(user).data['display_id'] == '19127920'


@pytest.mark.django_db
def test_login_and_session_endpoints_return_the_display_label():
    """接口层契约：登录态接口必须带上 姓名 + user_id（前端直接渲染）。"""
    from rest_framework.test import APIClient

    user = User.objects.create_user(username='吴志彬', password='Creator@2026',
                                    display_id='19127920')
    FeishuIdentity.objects.create(user=user, open_id='ou_test',
                                  display_name='吴志彬')

    client = APIClient()
    login = client.post('/api/auth/login/',
                        {'username': '吴志彬', 'password': 'Creator@2026'},
                        format='json')
    assert login.status_code == 200
    assert login.data['user']['display_name'] == '吴志彬'
    assert login.data['user']['display_id'] == '19127920'

    from apps.identity.services import sign_session
    session = APIClient()
    session.cookies['studio_session'] = sign_session(user.pk)
    info = session.get('/api/identity/session')
    assert info.status_code == 200
    assert info.json()['display_name'] == '吴志彬'
    assert info.json()['display_id'] == '19127920'


@pytest.mark.django_db
def test_set_display_id_command_sets_and_clears():
    user = User.objects.create_user(username='吴志彬',
                                    auth_source=User.AuthSource.FEISHU)

    call_command('set_display_id', '--user', '吴志彬', '--user-id', '19127920')
    user.refresh_from_db()
    assert display_identity(user)['display_id'] == '19127920'

    # Idempotent by design (display_id is not a unique key).
    call_command('set_display_id', '--user', str(user.pk), '--user-id', '19127920')

    call_command('set_display_id', '--user', '吴志彬', '--clear')
    user.refresh_from_db()
    assert display_identity(user)['display_id'] == str(user.pk)


@pytest.mark.django_db
def test_set_display_id_command_rejects_unknown_user_and_missing_value():
    User.objects.create_user(username='a')

    with pytest.raises(CommandError):
        call_command('set_display_id', '--user', 'nobody', '--user-id', '1')
    with pytest.raises(CommandError):
        call_command('set_display_id', '--user', 'a')
