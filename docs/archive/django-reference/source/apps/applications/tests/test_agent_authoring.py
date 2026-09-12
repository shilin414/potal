"""智能体市场 authoring API tests (apps.applications.v2_authoring).

Covers the three marketplace capabilities: creating an Aily custom agent
(Application + runtime binding), picking the workspace main agent (§38), and
replacing the avatar. Everything runs on the isolated SQLite test DB — no
TiDB, no Aily network calls.
"""
import base64

import pytest
from django.core.files.uploadedfile import SimpleUploadedFile
from rest_framework.test import APIClient

from apps.applications.models import Application, ApplicationCategory
from apps.applications.v2_authoring import resolve_category
from apps.catalog.models import ApplicationRuntimeBinding, Provider
from apps.catalog.providers import AILY_AGENT_CAPABILITIES
from apps.catalog.runtime import ProviderAuthContext
from apps.users.models import User

# Smallest valid PNG (1x1, transparent) — enough for extension/type checks.
PNG_BYTES = base64.b64decode(
    'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8A'
    'AAwAB/AH/9tKkAAAAAElFTkSuQmCC')


@pytest.fixture
def media_root(tmp_path, settings):
    """Keep avatar uploads out of the developer's MEDIA_ROOT."""
    settings.MEDIA_ROOT = tmp_path
    return tmp_path


@pytest.fixture
def env(db):
    user = User.objects.create_user(username='author', password='test')
    other = User.objects.create_user(username='bystander', password='test')
    Provider.objects.create(
        key='feishu_aily', name='Feishu Aily',
        supported_runtime_types=['agent', 'workflow'],
        capabilities=AILY_AGENT_CAPABILITIES)
    client = APIClient()
    client.force_authenticate(user)
    return client, user, other


def create_payload(**overrides):
    payload = {
        'name': '销售助手',
        'slug': 'sales-assistant',
        'description': '分析客户',
        'icon': '📈',
        'runtime': {
            'provider_key': 'feishu_aily',
            'runtime_type': 'agent',
            'external_resource_id': 'agent_4jz9pu9exyaws',
        },
    }
    payload.update(overrides)
    return payload


# ---------------------------------------------------------------------------
# 新建 aily 自定义智能体
# ---------------------------------------------------------------------------

@pytest.mark.django_db
def test_create_aily_agent_creates_application_and_binding(env):
    client, user, _other = env

    response = client.post('/api/v2/applications', create_payload(), format='json')

    assert response.status_code == 201, response.data
    application = Application.objects.get(slug='sales-assistant')
    assert application.created_by_id == user.pk
    assert application.kind == Application.Kind.CHAT
    assert application.renderer_key == 'chat'
    # The binding is what makes the agent runnable in the workspace.
    binding = application.runtime_bindings.get()
    assert binding.provider_key == 'feishu_aily'
    assert binding.runtime_type == 'agent'
    assert binding.external_resource_id == 'agent_4jz9pu9exyaws'
    assert binding.identity_mode == 'user'
    assert binding.execution_mode == 'interactive'
    assert binding.enabled is True
    # Capabilities are copied from the adapter, not from the request.
    assert binding.capabilities['streaming'] is True
    assert response.data['is_bound'] is True
    assert response.data['can_manage'] is True
    assert response.data['avatar_url'] == ''
    assert response.data['is_default_agent'] is False


@pytest.mark.django_db
def test_create_aily_agent_rejects_malformed_agent_id(env):
    client, _user, _other = env

    response = client.post('/api/v2/applications', create_payload(
        slug='bad-agent',
        runtime={
            'provider_key': 'feishu_aily',
            'runtime_type': 'agent',
            'external_resource_id': 'not-an-agent-id',
        },
    ), format='json')

    assert response.status_code == 400
    # DRF nests the runtime block's errors; the form maps them field-by-field.
    assert 'external_resource_id' in response.data['runtime']
    assert not Application.objects.filter(slug='bad-agent').exists()


@pytest.mark.django_db
def test_create_aily_agent_requires_a_resource_id(env):
    client, _user, _other = env

    response = client.post('/api/v2/applications', create_payload(
        slug='no-id',
        runtime={'provider_key': 'feishu_aily', 'runtime_type': 'agent'},
    ), format='json')

    assert response.status_code == 400
    assert 'external_resource_id' in response.data['runtime']


@pytest.mark.django_db
def test_create_agent_rejects_unknown_provider(env):
    client, _user, _other = env

    response = client.post('/api/v2/applications', create_payload(
        slug='ghost-provider',
        runtime={'provider_key': 'nope', 'runtime_type': 'agent'},
    ), format='json')

    assert response.status_code == 400
    assert 'provider_key' in response.data['runtime']


@pytest.mark.django_db
def test_create_agent_rejects_duplicate_slug(env):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')

    response = client.post('/api/v2/applications', create_payload(
        name='另一个销售助手'), format='json')

    assert response.status_code == 400
    assert 'slug' in response.data


@pytest.mark.django_db
def test_create_agent_generates_slug_when_omitted(env):
    client, _user, _other = env

    response = client.post(
        '/api/v2/applications', create_payload(slug=''), format='json')

    assert response.status_code == 201, response.data
    assert response.data['slug'].startswith('agent-')


@pytest.mark.django_db
def test_create_agent_uses_agent_category_slug(env):
    """The marketplace passes the agent-category slug so one rail filters both."""
    client, _user, _other = env

    response = client.post('/api/v2/applications', create_payload(
        slug='copy-agent', category_slug='copywriting',
        category_name='文案创作'), format='json')

    assert response.status_code == 201, response.data
    assert response.data['category_slug'] == 'copywriting'
    assert ApplicationCategory.objects.filter(slug='copywriting').exists()


@pytest.mark.django_db
def test_create_agent_without_category_falls_back_to_default(env):
    client, _user, _other = env

    client.post('/api/v2/applications', create_payload(slug='plain'), format='json')

    assert Application.objects.get(slug='plain').category.slug == 'agents'


@pytest.mark.django_db
def test_create_with_set_default_agent_promotes_it(env):
    client, _user, other = env
    previous = Application.objects.create(
        category=resolve_category('agents'), name='旧主智能体', slug='old-main',
        description='', created_by=other, kind=Application.Kind.CHAT,
        renderer_key='chat', is_default_agent=True)
    binding = ApplicationRuntimeBinding.objects.create(
        application=previous, provider_key='feishu_aily', runtime_type='agent',
        external_resource_id='agent_old', enabled=True)
    assert previous.default_agent_eligible

    response = client.post('/api/v2/applications', create_payload(
        set_default_agent=True), format='json')

    assert response.status_code == 201, response.data
    created = Application.objects.get(slug='sales-assistant')
    assert created.is_default_agent is True
    previous.refresh_from_db()
    assert previous.is_default_agent is False
    assert Application.objects.filter(is_default_agent=True).count() == 1
    assert binding.enabled is True


# ---------------------------------------------------------------------------
# 设置默认智能体
# ---------------------------------------------------------------------------

@pytest.mark.django_db
def test_set_default_agent_endpoint(env):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')

    response = client.post(
        f'/api/v2/applications/{application.pk}/default-agent')

    assert response.status_code == 200, response.data
    assert response.data['is_default_agent'] is True

    response = client.delete(
        f'/api/v2/applications/{application.pk}/default-agent')

    assert response.status_code == 200
    assert response.data['is_default_agent'] is False


@pytest.mark.django_db
def test_set_default_agent_requires_a_binding(env):
    client, user, _other = env
    unbound = Application.objects.create(
        category=resolve_category('agents'), name='未绑定', slug='unbound',
        description='', created_by=user, kind=Application.Kind.CHAT,
        renderer_key='chat')

    response = client.post(f'/api/v2/applications/{unbound.pk}/default-agent')

    assert response.status_code == 400
    assert 'detail' in response.data


@pytest.mark.django_db
def test_set_default_agent_rejects_non_owner(env):
    client, user, other = env
    application = Application.objects.create(
        category=resolve_category('agents'), name='别人的', slug='someone-else',
        description='', created_by=other, kind=Application.Kind.CHAT,
        renderer_key='chat')
    ApplicationRuntimeBinding.objects.create(
        application=application, provider_key='feishu_aily', runtime_type='agent',
        external_resource_id='agent_other', enabled=True)

    response = client.post(f'/api/v2/applications/{application.pk}/default-agent')

    assert response.status_code == 403
    application.refresh_from_db()
    assert application.is_default_agent is False


# ---------------------------------------------------------------------------
# 修改头像
# ---------------------------------------------------------------------------

def upload_png(client, application_id, name='avatar.png', payload=PNG_BYTES):
    return client.post(
        f'/api/v2/applications/{application_id}/avatar',
        {'file': SimpleUploadedFile(name, payload, content_type='image/png')},
        format='multipart')


@pytest.mark.django_db
def test_avatar_can_be_uploaded_read_and_cleared(env, media_root):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')

    response = upload_png(client, application.pk)

    assert response.status_code == 200, response.data
    assert response.data['avatar_url'].startswith(
        f'/api/v2/applications/{application.pk}/avatar')
    application.refresh_from_db()
    assert application.avatar
    assert list(media_root.glob('application-avatars/**/*.png'))

    response = client.get(response.data['avatar_url'])

    assert response.status_code == 200
    assert response['Content-Type'] == 'image/png'
    # Windows keeps the stream locked until the response is closed; the dev
    # server does this for us, the test client does not.
    response.close()

    response = client.delete(f'/api/v2/applications/{application.pk}/avatar')

    assert response.status_code == 200
    assert response.data['avatar_url'] == ''
    application.refresh_from_db()
    assert not application.avatar
    assert not list(media_root.glob('application-avatars/**/*.png'))


@pytest.mark.django_db
def test_avatar_rejects_unsupported_extension(env, media_root):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')

    response = upload_png(
        client, application.pk, name='payload.svg', payload=b'<svg/>')

    assert response.status_code == 400
    assert 'file' in response.data
    application.refresh_from_db()
    assert not application.avatar


@pytest.mark.django_db
def test_avatar_rejects_oversized_file(env, media_root):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')

    response = upload_png(
        client, application.pk, payload=b'\x00' * (2 * 1024 * 1024 + 1))

    assert response.status_code == 400
    assert 'file' in response.data


@pytest.mark.django_db
def test_avatar_requires_login(env, media_root):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')

    anonymous = APIClient()
    assert anonymous.get(
        f'/api/v2/applications/{application.pk}/avatar').status_code in (401, 403)
    assert upload_png(anonymous, application.pk).status_code in (401, 403)


@pytest.mark.django_db
def test_avatar_rejects_non_owner(env, media_root):
    client, user, other = env
    application = Application.objects.create(
        category=resolve_category('agents'), name='别人的', slug='not-mine',
        description='', created_by=other, kind=Application.Kind.CHAT,
        renderer_key='chat')
    other_client = APIClient()
    other_client.force_authenticate(other)

    assert upload_png(client, application.pk).status_code == 403
    assert upload_png(other_client, application.pk).status_code == 200


# ---------------------------------------------------------------------------
# 编辑 / 删除 / 列表 scope
# ---------------------------------------------------------------------------

@pytest.mark.django_db
def test_patch_updates_binding_resource_id_and_avatar_kept(env, media_root):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')
    upload_png(client, application.pk)

    response = client.patch(f'/api/v2/applications/{application.pk}', {
        'name': '销售助手 V2',
        'description': '更新后的描述',
        'runtime': {
            'provider_key': 'feishu_aily',
            'runtime_type': 'agent',
            'external_resource_id': 'agent_9newid',
        },
    }, format='json')

    assert response.status_code == 200, response.data
    application.refresh_from_db()
    assert application.name == '销售助手 V2'
    assert application.avatar  # untouched by a metadata edit
    binding = application.runtime_bindings.get()
    assert binding.external_resource_id == 'agent_9newid'
    assert application.runtime_bindings.count() == 1


@pytest.mark.django_db
def test_delete_agent_without_history(env, media_root):
    client, _user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')
    upload_png(client, application.pk)
    assert list(media_root.glob('application-avatars/**/*.png'))

    response = client.delete(f'/api/v2/applications/{application.pk}')

    assert response.status_code == 204
    assert not Application.objects.filter(slug='sales-assistant').exists()
    assert not ApplicationRuntimeBinding.objects.filter(
        application_id=application.pk).exists()
    # Deleting the agent must not leave its avatar behind in MEDIA_ROOT.
    assert not list(media_root.glob('application-avatars/**/*.png'))


@pytest.mark.django_db
def test_delete_agent_with_history_is_refused(env):
    from apps.conversations.models import Conversation

    client, user, _other = env
    client.post('/api/v2/applications', create_payload(), format='json')
    application = Application.objects.get(slug='sales-assistant')
    Conversation.objects.create(user=user, application=application, title='历史')

    response = client.delete(f'/api/v2/applications/{application.pk}')

    assert response.status_code == 409
    assert Application.objects.filter(slug='sales-assistant').exists()


@pytest.mark.django_db
def test_index_scope_manage_includes_private_and_unbound_agents(env):
    client, user, _other = env
    client.post('/api/v2/applications', create_payload(
        slug='private-agent', is_public=False), format='json')
    Application.objects.create(
        category=resolve_category('agents'), name='未绑定', slug='unbound-agent',
        description='', created_by=user, is_public=False,
        kind=Application.Kind.CHAT, renderer_key='chat')

    public = client.get('/api/v2/applications')
    assert public.status_code == 200
    assert public.data == []

    manage = client.get(
        '/api/v2/applications', {'scope': 'manage', 'include_unbound': 'true'})
    slugs = {entry['slug'] for entry in manage.data}
    # Seeded public-but-unbound chat apps (migrations) are in there too.
    assert {'private-agent', 'unbound-agent'} <= slugs
    unbound = next(e for e in manage.data if e['slug'] == 'unbound-agent')
    assert unbound['is_bound'] is False
    assert unbound['can_manage'] is True
    # The market card shows 「私有」 from this flag, so it must be in the listing.
    assert unbound['is_public'] is False
    private = next(e for e in manage.data if e['slug'] == 'private-agent')
    assert private['is_public'] is False
    assert private['avatar_url'] == ''
    assert private['external_resource_id'] == 'agent_4jz9pu9exyaws'
    assert private['category_name'] == '智能体'

    bound_only = client.get('/api/v2/applications', {'scope': 'manage'})
    bound_slugs = {entry['slug'] for entry in bound_only.data}
    assert 'private-agent' in bound_slugs
    assert 'unbound-agent' not in bound_slugs


@pytest.mark.django_db
def test_index_scope_mine_excludes_public_agents_of_others(env):
    client, user, other = env
    Application.objects.create(
        category=resolve_category('agents'), name='公开的', slug='public-other',
        description='', created_by=other, kind=Application.Kind.CHAT,
        renderer_key='chat')
    ApplicationRuntimeBinding.objects.create(
        application=Application.objects.get(slug='public-other'),
        provider_key='feishu_aily', runtime_type='agent',
        external_resource_id='agent_x', enabled=True)

    response = client.get('/api/v2/applications', {
        'scope': 'mine', 'include_unbound': 'true'})

    assert response.status_code == 200
    assert response.data == []


# ---------------------------------------------------------------------------
# 运行时目录 / 校验
# ---------------------------------------------------------------------------

@pytest.mark.django_db
def test_runtime_catalog_exposes_aily_descriptor(env):
    client, _user, _other = env

    response = client.get('/api/v2/runtimes')

    assert response.status_code == 200
    entry = next(e for e in response.data if e['key'] == 'feishu_aily:agent')
    assert entry['label'] == '飞书 Aily 自定义智能体'
    assert entry['resource_id_label'] == 'Agent ID'
    assert entry['resource_id_required'] is True
    assert 'agent_' in entry['resource_id_hint']
    assert entry['capabilities']['streaming'] is True


@pytest.mark.django_db
def test_runtime_validate_rejects_malformed_resource_id(env):
    client, _user, _other = env

    response = client.post('/api/v2/runtimes/validate', {
        'provider_key': 'feishu_aily',
        'runtime_type': 'agent',
        'external_resource_id': 'oops',
    }, format='json')

    assert response.status_code == 400
    assert 'external_resource_id' in response.data


@pytest.mark.django_db
def test_runtime_validate_without_feishu_identity_degrades(env, monkeypatch):
    """No Feishu identity → checked=False with guidance, never a 500.

    The adapter is stubbed rather than driving the real OAuth path: resolving
    a UAT needs Redis + a stored refresh token, neither of which exists in the
    isolated test settings.
    """
    client, _user, _other = env
    from integrations.aily.agent_adapter import AilyAgentAdapter

    async def no_identity(self, user, identity_mode='user'):
        raise LookupError(
            'user has no feishu identity; re-login through Feishu OAuth')

    monkeypatch.setattr(AilyAgentAdapter, 'build_auth', no_identity)

    response = client.post('/api/v2/runtimes/validate', {
        'provider_key': 'feishu_aily',
        'runtime_type': 'agent',
        'external_resource_id': 'agent_4jz9pu9exyaws',
    }, format='json')

    assert response.status_code == 200, response.data
    assert response.data['checked'] is False
    assert response.data['ok'] is True
    assert '飞书' in response.data['detail']


@pytest.mark.django_db
def test_runtime_validate_reports_visibility(env, monkeypatch):
    client, _user, _other = env
    from integrations.aily.agent_adapter import AilyAgentAdapter

    async def build_auth(self, user, identity_mode='user'):
        return ProviderAuthContext(
            provider='feishu_aily', identity_mode=identity_mode, token='uat')

    async def visible(self, auth, resource_id):
        assert resource_id == 'agent_4jz9pu9exyaws'
        assert auth.token == 'uat'
        return True

    monkeypatch.setattr(AilyAgentAdapter, 'build_auth', build_auth)
    monkeypatch.setattr(AilyAgentAdapter, 'check_visibility', visible)

    response = client.post('/api/v2/runtimes/validate', {
        'provider_key': 'feishu_aily',
        'runtime_type': 'agent',
        'external_resource_id': 'agent_4jz9pu9exyaws',
    }, format='json')

    assert response.status_code == 200, response.data
    assert response.data == {
        'ok': True, 'checked': True,
        'detail': '校验通过：当前身份对该智能体可见。'}

    async def invisible(self, auth, resource_id):
        return False

    monkeypatch.setattr(AilyAgentAdapter, 'check_visibility', invisible)

    response = client.post('/api/v2/runtimes/validate', {
        'provider_key': 'feishu_aily',
        'runtime_type': 'agent',
        'external_resource_id': 'agent_4jz9pu9exyaws',
    }, format='json')

    assert response.data['ok'] is False
    assert response.data['checked'] is True
    assert '不可见' in response.data['detail']


@pytest.mark.django_db
def test_runtime_catalog_skips_unregistered_runtime_types(env):
    """feishu_aily declares `workflow` but no adapter is registered yet."""
    client, _user, _other = env

    response = client.get('/api/v2/runtimes')

    keys = {entry['key'] for entry in response.data}
    assert 'feishu_aily:workflow' not in keys
