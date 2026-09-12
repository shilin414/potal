import pytest
from rest_framework.test import APIClient

from apps.applications.models import (
    Application,
    ApplicationCategory,
    ApplicationFavorite,
)
from apps.catalog.models import ApplicationRuntimeBinding, Provider
from apps.catalog.providers import AILY_AGENT_CAPABILITIES
from apps.conversations.models import Conversation
from apps.execution.models import Run
from apps.users.models import User


@pytest.fixture
def api_setup(db):
    user = User.objects.create_user(username='api-user', password='test')
    category = ApplicationCategory.objects.create(
        name='API Agents', slug='api-agents')
    app = Application.objects.create(
        category=category,
        name='API Assistant', slug='api-assistant',
        description='test', created_by=user,
        kind=Application.Kind.CHAT,
        renderer_key='chat')
    provider = Provider.objects.create(
        key='feishu_aily', name='Aily', capabilities=AILY_AGENT_CAPABILITIES)
    ApplicationRuntimeBinding.objects.create(
        application=app,
        provider=provider,
        provider_key='feishu_aily',
        runtime_type='agent',
        external_resource_id='agent_api',
        identity_mode='user',
        execution_mode='interactive')
    client = APIClient()
    client.force_authenticate(user)
    return client, user, app


@pytest.mark.django_db
def test_post_run_lazy_creates_conversation_and_queues(api_setup):
    client, user, app = api_setup

    response = client.post('/api/v2/runs', {
        'application_id': app.pk,
        'content': '分析这个客户',
    }, format='json')

    assert response.status_code == 201, response.data
    run = Run.objects.get(pk=response.data['id'])
    assert run.status == Run.Status.QUEUED
    assert run.provider == 'feishu_aily'
    assert run.conversation.application_id == app.pk
    assert run.conversation.user_id == user.pk
    assert run.conversation.messages.get().content == '分析这个客户'


@pytest.mark.django_db
def test_user_cannot_access_other_users_run(api_setup):
    client, _user, app = api_setup
    other = User.objects.create_user(username='other', password='test')
    conversation = Conversation.objects.create(
        user=other, application=app, title='private')
    binding = app.runtime_bindings.get()
    run = Run.objects.create(
        user=other, application=app, conversation=conversation,
        runtime_binding=binding,
        provider='feishu_aily', runtime_type='agent')

    response = client.get(f'/api/v2/runs/{run.pk}')

    assert response.status_code == 404


@pytest.mark.django_db
def test_applications_index_lists_bound_chat_apps_with_capabilities(api_setup):
    client, _user, app = api_setup

    response = client.get('/api/v2/applications')

    assert response.status_code == 200
    assert len(response.data) == 1
    entry = response.data[0]
    assert entry['id'] == app.pk
    assert entry['slug'] == 'api-assistant'
    assert entry['runtime_type'] == 'agent'
    assert entry['provider_key'] == 'feishu_aily'
    assert entry['identity_mode'] == 'user'
    # Capability flags drive the UI (attachment picker, cancel button...).
    assert entry['capabilities']['attachment'] is True
    assert entry['capabilities']['cancel'] is False


@pytest.mark.django_db
def test_applications_index_requires_authentication():
    client = APIClient()

    response = client.get('/api/v2/applications')

    # DRF denies anonymous access (401 with a WWW-Authenticate challenge,
    # 403 when no authenticator issues one) — either way: forbidden.
    assert response.status_code in (401, 403)


@pytest.fixture
def fixed_page_app(api_setup):
    """A fixed-page (non-chat) application: no runtime binding needed."""
    client, user, _chat_app = api_setup
    page_app = Application.objects.create(
        category=ApplicationCategory.objects.get(slug='api-agents'),
        name='修改OA密码', slug='change-oa-password',
        description='fixed page', created_by=user,
        kind=Application.Kind.CUSTOM, renderer_key='form')
    return client, user, page_app


@pytest.mark.django_db
def test_applications_index_defaults_to_chat_only(api_setup, fixed_page_app):
    """The chat composer's default-app resolver must not see fixed pages."""
    client, _user, _page_app = fixed_page_app

    response = client.get('/api/v2/applications')

    assert response.status_code == 200
    assert [entry['kind'] for entry in response.data] == ['chat']


@pytest.mark.django_db
def test_applications_index_kind_all_lists_fixed_pages(api_setup, fixed_page_app):
    client, _user, page_app = fixed_page_app

    response = client.get('/api/v2/applications', {'kind': 'all'})

    assert response.status_code == 200
    by_slug = {entry['slug']: entry for entry in response.data}
    assert set(by_slug) == {'api-assistant', 'change-oa-password'}
    # Fixed pages carry no binding/provider but expose their renderer key.
    assert by_slug['change-oa-password']['is_bound'] is False
    assert by_slug['change-oa-password']['renderer_key'] == 'form'
    assert by_slug['api-assistant']['is_bound'] is True


@pytest.mark.django_db
def test_unknown_kind_filter_falls_back_to_chat(api_setup):
    client, _user, _app = api_setup

    response = client.get('/api/v2/applications', {'kind': 'nonsense'})

    assert response.status_code == 200
    assert [entry['kind'] for entry in response.data] == ['chat']


@pytest.mark.django_db
def test_favorite_toggle_is_idempotent_and_reflected_in_index(api_setup):
    client, user, app = api_setup

    first = client.post(f'/api/v2/applications/{app.pk}/favorite')
    assert first.status_code == 200
    assert first.data == {'application_id': app.pk, 'is_favorite': True}
    # A double click must not violate the unique constraint.
    assert client.post(f'/api/v2/applications/{app.pk}/favorite').status_code == 200
    assert ApplicationFavorite.objects.filter(user=user).count() == 1

    entry = client.get('/api/v2/applications').data[0]
    assert entry['is_favorite'] is True

    removed = client.delete(f'/api/v2/applications/{app.pk}/favorite')
    assert removed.status_code == 200
    assert removed.data == {'application_id': app.pk, 'is_favorite': False}
    assert client.delete(f'/api/v2/applications/{app.pk}/favorite').status_code == 200
    assert ApplicationFavorite.objects.count() == 0
    assert client.get('/api/v2/applications').data[0]['is_favorite'] is False


@pytest.mark.django_db
def test_favorite_unknown_application_is_404(api_setup):
    client, _user, _app = api_setup

    assert client.post('/api/v2/applications/999999/favorite').status_code == 404
    assert client.delete('/api/v2/applications/999999/favorite').status_code == 404


@pytest.mark.django_db
def test_applications_index_reports_personal_usage(api_setup):
    client, user, app = api_setup
    conversation = Conversation.objects.create(
        user=user, application=app, title='t')
    binding = app.runtime_bindings.get()
    for _ in range(2):
        Run.objects.create(
            user=user, application=app, conversation=conversation,
            runtime_binding=binding, provider='feishu_aily', runtime_type='agent')
    # Another user's runs must not leak into my usage stats.
    other = User.objects.create_user(username='usage-other', password='test')
    Run.objects.create(
        user=other, application=app, conversation=conversation,
        runtime_binding=binding, provider='feishu_aily', runtime_type='agent')

    entry = client.get('/api/v2/applications').data[0]

    assert entry['usage_count'] == 2
    assert entry['last_used_at'] is not None


@pytest.mark.django_db
def test_applications_index_reports_zero_usage_for_unused_app(api_setup):
    client, _user, _app = api_setup

    entry = client.get('/api/v2/applications').data[0]

    assert entry['usage_count'] == 0
    assert entry['last_used_at'] is None


@pytest.mark.django_db
def test_applications_index_reports_the_main_agent(api_setup):
    """The workspace reads is_default_agent to pick the composer target (§38)."""
    client, _user, app = api_setup

    assert client.get('/api/v2/applications').data[0]['is_default_agent'] is False

    Application.set_default_agent(app)

    assert client.get('/api/v2/applications').data[0]['is_default_agent'] is True


@pytest.mark.django_db
def test_login_sets_studio_session_cookie(api_setup):
    client, user, _app = api_setup

    response = client.post('/api/auth/login/', {
        'username': 'api-user', 'password': 'test'}, format='json')

    assert response.status_code == 200
    # The cookie authenticates same-origin navigations such as
    # window.open('/api/v2/artifacts/{id}/open') without a bearer header.
    assert 'studio_session' in (response.cookies or {})
