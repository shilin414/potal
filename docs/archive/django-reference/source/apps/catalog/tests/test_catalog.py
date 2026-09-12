import pytest

from apps.applications.models import Application, ApplicationCategory
from apps.catalog.models import (
    ApplicationRuntimeBinding,
    ArtifactPolicy,
    ExecutionMode,
    IdentityMode,
    Provider,
    RuntimeType,
    SessionPolicy,
)
from apps.catalog.runtime import RuntimeAdapter, RuntimeRegistry
from apps.users.models import User


@pytest.fixture
def user(db):
    return User.objects.create_user(
        username='catalog-user', password='test-password')


@pytest.fixture
def application(user):
    category = ApplicationCategory.objects.create(
        name='AI 助手', slug='ai-assistants')
    return Application.objects.create(
        category=category,
        name='销售助手',
        slug='sales-assistant',
        description='销售分析',
        created_by=user,
        renderer_key='chat',
    )


@pytest.mark.django_db
def test_runtime_binding_snapshot_excludes_secrets(application):
    provider = Provider.objects.create(
        key='feishu_aily',
        name='Feishu Aily',
        capabilities={'streaming': True},
        secret_ref='FEISHU_APP_SECRET',
    )
    binding = ApplicationRuntimeBinding.objects.create(
        application=application,
        runtime_type=RuntimeType.AGENT,
        provider=provider,
        provider_key=provider.key,
        external_resource_id='agent_test',
        identity_mode=IdentityMode.USER,
        execution_mode=ExecutionMode.INTERACTIVE,
        session_policy=SessionPolicy.LAZY,
        artifact_policy=ArtifactPolicy.EXTERNAL_REFRESH,
        secret_ref='FEISHU_APP_SECRET',
    )

    snapshot = binding.snapshot()

    assert snapshot['provider_key'] == 'feishu_aily'
    assert snapshot['external_resource_id'] == 'agent_test'
    assert snapshot['identity_mode'] == 'user'
    assert 'secret_ref' not in snapshot
    assert 'token' not in snapshot


def test_runtime_registry_resolves_provider_and_runtime():
    class Adapter(RuntimeAdapter):
        runtime_key = 'example:agent'

    registry = RuntimeRegistry()
    adapter = Adapter()
    registry.register(adapter)

    assert registry.resolve('example', 'agent') is adapter
    assert registry.has('example', 'agent') is True
    assert registry.has('missing', 'agent') is False
